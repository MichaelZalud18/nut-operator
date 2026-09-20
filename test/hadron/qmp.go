//go:build hadron
// +build hadron

/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hadron

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

// qmpSocketFile is the fixed, StateDir-relative path NewSafeMachineContext passes to QEMU's own
// -qmp flag. Deriving it from types.Machine.Config().StateDir -- the same pattern machineProcess
// uses for the PID file -- means no caller needs a new Credentials field to find it.
const qmpSocketFile = "qemu-qmp.sock"

func qmpSocketPath(m types.Machine) (string, error) {
	if m == nil || m.Config().StateDir == "" {
		return "", fmt.Errorf("machine state directory is required")
	}
	return filepath.Join(m.Config().StateDir, qmpSocketFile), nil
}

// waitForQMPShutdown blocks until QEMU's own QMP socket reports a real SHUTDOWN event, or ctx is
// done. QEMU only ever emits this event when the guest itself requests power-off (e.g. ACPI) --
// external termination (SIGKILL, a host-side kill, a crash) closes the socket without ever
// producing one. This is what lets a halt-acceptance test distinguish "the guest genuinely powered
// itself off" from "the QEMU process is merely gone," which process-exit alone cannot.
//
// The guest's QEMU process must be launched with "-no-shutdown" (NewSafeMachineContext does this
// unconditionally): without it, QEMU exits immediately after emitting SHUTDOWN, racing this read
// against the socket's own teardown. With it, the process (and its QMP socket) stays alive after
// the guest has stopped, so the event is never missed.
func waitForQMPShutdown(ctx context.Context, socketPath string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return fmt.Errorf("dialing QEMU QMP socket: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// net.Conn has no context-aware Read; closing it from a watcher goroutine is the standard way
	// to make a blocked Read return promptly when ctx is done instead of hanging until whatever
	// (if any) deadline the caller separately set.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatch:
		}
	}()

	scanner := bufio.NewScanner(conn)

	if !scanner.Scan() {
		return fmt.Errorf("QMP socket closed before greeting: %w", firstNonNil(scanner.Err(), ctx.Err()))
	}
	var greeting struct {
		QMP *struct{} `json:"QMP"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &greeting); err != nil || greeting.QMP == nil {
		return fmt.Errorf("unexpected QMP greeting: %s", scanner.Text())
	}

	if _, err := conn.Write([]byte(`{"execute":"qmp_capabilities"}` + "\n")); err != nil {
		return fmt.Errorf("negotiating QMP capabilities: %w", err)
	}
	if !scanner.Scan() {
		return fmt.Errorf("QMP socket closed before capabilities ack: %w", firstNonNil(scanner.Err(), ctx.Err()))
	}
	var ack struct {
		Error *struct {
			Desc string `json:"desc"`
		} `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		return fmt.Errorf("parsing QMP capabilities ack: %s", scanner.Text())
	}
	if ack.Error != nil {
		return fmt.Errorf("QMP capabilities negotiation rejected: %s", ack.Error.Desc)
	}

	for scanner.Scan() {
		var msg struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue // Other async event shapes are not this test's concern.
		}
		if msg.Event == "SHUTDOWN" {
			return nil
		}
	}
	return fmt.Errorf("QMP socket closed without a SHUTDOWN event: %w", firstNonNil(scanner.Err(), ctx.Err()))
}

func firstNonNil(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("no underlying error")
}
