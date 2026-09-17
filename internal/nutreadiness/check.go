// Package nutreadiness checks configured drivers through the patched NUT CLI.
package nutreadiness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timeout covers enumeration and all status commands, leaving headroom inside
// the operand's five-second exec-probe deadline.
const Timeout = 4 * time.Second

// MaxDrivers bounds process fanout. Oversized inventories fail closed instead of
// queueing probes, which could hide a healthy device behind stalled devices.
// The cap limits captured status output to 8 MiB across both streams and avoids
// unbounded subprocess growth in the resource-constrained upsd container.
const MaxDrivers = 64

const maxOutputBytes = 64 << 10

type commandFunc func(context.Context, ...string) *exec.Cmd

// Check requires the operand's upsdrvquery_prepare patch: PING timeout must be an
// error, not successful preparation. NUT owns config parsing and exact socket
// identity; no directory discovery or cached upsd values are used here.
func Check(ctx context.Context) error {
	return check(ctx, func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "upsdrvctl", args...)
	})
}

func check(ctx context.Context, command commandFunc) error {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	output, err := invoke(ctx, command, "--", "list")
	if err != nil {
		return fmt.Errorf("enumerate configured drivers: %w", err)
	}
	names, err := driverNames(output)
	if err != nil {
		return err
	}
	results := make(chan bool, len(names))
	var probes sync.WaitGroup
	defer func() {
		cancel()
		probes.Wait()
	}()
	for _, name := range names {
		probes.Go(func() {
			output, err := invoke(ctx, command, "--", "status", name)
			results <- err == nil && responsive(output, name)
		})
	}
	for range names {
		select {
		case ready := <-results:
			if ready {
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("no configured driver is responsive")
}

func driverNames(output []byte) ([]string, error) {
	var names []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.ContainsAny(name, "\x00\t\r") {
			return nil, errors.New("invalid or empty driver enumeration")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
		if len(names) > MaxDrivers {
			return nil, fmt.Errorf("driver inventory exceeds readiness limit of %d", MaxDrivers)
		}
	}
	return names, nil
}

func responsive(output []byte, name string) bool {
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 7 && strings.TrimSpace(fields[0]) == name && strings.TrimSpace(fields[4]) == "RESPONSIVE" {
			// Require a fresh GETPID response too; PID files and RUNNING are not evidence.
			pid, err := strconv.ParseInt(strings.TrimSpace(fields[5]), 10, 32)
			return err == nil && pid > 0
		}
	}
	return false
}

func invoke(ctx context.Context, command commandFunc, args ...string) ([]byte, error) {
	cmd := command(ctx, args...)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "NUT_QUIET_INIT_BANNER=true")
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Bound inherited output pipes even if a command leaves descendants behind.
	cmd.WaitDelay = 50 * time.Millisecond
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if stdout.overflow || stderr.overflow {
		return nil, errors.New("NUT command output exceeded limit")
	}
	return stdout.buffer.Bytes(), nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxOutputBytes - b.buffer.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
