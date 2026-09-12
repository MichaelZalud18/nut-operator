//go:build hadron

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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

// The subprocess only waits for stdin EOF; no VM or host shutdown is involved.
func TestHadronProcessHelper(t *testing.T) {
	if os.Getenv("HADRON_PROCESS_HELPER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startTeardownProcess(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHadronProcessHelper$")
	cmd.Env = append(os.Environ(), "HADRON_PROCESS_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd, exited
}

type pidDeletingMachine struct {
	types.Machine
	stateDir string
	cleaned  bool
	stopped  bool
	stop     func() error
}

func (m *pidDeletingMachine) Config() types.MachineConfig {
	return types.MachineConfig{StateDir: m.stateDir}
}

func (m *pidDeletingMachine) Stop() error {
	m.stopped = true
	if err := os.Remove(filepath.Join(m.stateDir, "pid")); err != nil {
		return err
	}
	if m.stop != nil {
		return m.stop()
	}
	return nil
}

func (m *pidDeletingMachine) Alive() bool {
	_, err := os.Stat(filepath.Join(m.stateDir, "pid"))
	return err == nil
}

func (m *pidDeletingMachine) Clean() error {
	m.cleaned = true
	return os.RemoveAll(m.stateDir)
}

func TestSafeTeardownDoesNotTreatMissingPIDFileAsExit(t *testing.T) {
	cmd, _ := startTeardownProcess(t)
	m := &pidDeletingMachine{stateDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(m.stateDir, "pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SafeTeardown(m, 30*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected timeout while original process remains alive, got %v", err)
	}
	if m.cleaned {
		t.Fatal("removed state while the original process was still running")
	}
}

func TestSafeTeardownRejectsMissingOrInvalidPID(t *testing.T) {
	for _, value := range []string{"missing", "", "not-a-pid", "0", "1", "-1", fmt.Sprint(os.Getpid())} {
		t.Run(value, func(t *testing.T) {
			m := &pidDeletingMachine{stateDir: t.TempDir()}
			if value != "missing" {
				if err := os.WriteFile(filepath.Join(m.stateDir, "pid"), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := SafeTeardown(m, time.Second); err == nil {
				t.Fatal("unverifiable machine accepted")
			}
			if m.stopped || m.cleaned {
				t.Fatal("unverifiable machine was mutated")
			}
		})
	}
}

func TestSafeTeardownWithRealPEGStop(t *testing.T) {
	cmd, exited := startTeardownProcess(t)
	m, _, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })
	if err := os.WriteFile(filepath.Join(m.Config().StateDir, "pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SafeTeardown(m, time.Second); err != nil {
		t.Fatal(err)
	}
	<-exited
	if _, err := os.Stat(m.Config().StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state still exists after real PEG Stop: %v", err)
	}
}

func TestSafeTeardownAlreadyExitedProcess(t *testing.T) {
	cmd, exited := startTeardownProcess(t)
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-exited
	m := &pidDeletingMachine{stateDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(m.stateDir, "pid"), []byte(fmt.Sprint(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SafeTeardown(m, time.Second); err != nil {
		t.Fatal(err)
	}
	if m.stopped || !m.cleaned {
		t.Fatal("already-exited machine should be cleaned without calling Stop")
	}
}

func TestSafeTeardownPreservesStopErrorAndLiveState(t *testing.T) {
	cmd, _ := startTeardownProcess(t)
	stopErr := errors.New("stop failed")
	m := &pidDeletingMachine{stateDir: t.TempDir(), stop: func() error { return stopErr }}
	if err := os.WriteFile(filepath.Join(m.stateDir, "pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	err := SafeTeardown(m, 20*time.Millisecond)
	if !errors.Is(err, stopErr) || !errors.Is(err, context.DeadlineExceeded) || m.cleaned {
		t.Fatalf("lost stop failure or deleted live state: cleaned=%v, err=%v", m.cleaned, err)
	}
}

func TestSafeTeardownWaitsForExitBeforeCleaning(t *testing.T) {
	cmd, exited := startTeardownProcess(t)
	m := &pidDeletingMachine{stateDir: t.TempDir(), stop: cmd.Process.Kill}
	if err := os.WriteFile(filepath.Join(m.stateDir, "pid"), []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SafeTeardown(m, time.Second); err != nil {
		t.Fatal(err)
	}
	<-exited
	if !m.cleaned {
		t.Fatal("state was not cleaned after process exit")
	}
}

func TestSafeStopPreservesEvidenceAndConfirmsExit(t *testing.T) {
	cmd, exited := startTeardownProcess(t)
	m := &pidDeletingMachine{stateDir: t.TempDir()}
	pidFile := filepath.Join(m.stateDir, "pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(cmd.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SafeStop(m, time.Second); err != nil {
		t.Fatal(err)
	}
	<-exited
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("lost diagnostic state: %v", err)
	}
	if m.stopped || m.cleaned {
		t.Fatal("called PEG's unbounded Stop or deleted state")
	}
	if err := SafeStop(m, time.Second); err != nil {
		t.Fatalf("repeat stop: %v", err)
	}
}
