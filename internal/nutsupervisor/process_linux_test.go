package nutsupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOwnedProcessExitIsReaped(t *testing.T) {
	p, err := startProcess(exec.Command("sh", "-c", "exit 7"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("child was not collected")
	}
	var exit *exec.ExitError
	if !errors.As(p.err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("exit result = %v", p.err)
	}
	if !errors.Is(syscall.Kill(p.cmd.Process.Pid, 0), syscall.ESRCH) {
		t.Fatal("completed child still exists")
	}
}

func TestOwnedProcessStartFailure(t *testing.T) {
	if _, err := startProcess(exec.Command(filepath.Join(t.TempDir(), "missing"))); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestOwnedProcessesShareTerminationGrace(t *testing.T) {
	var processes []*ownedProcess
	for range 3 {
		ready := filepath.Join(t.TempDir(), "ready")
		cmd := exec.Command("sh", "-c", `trap '' TERM; touch "$1"; exec sleep 60`, "fixture", ready)
		p, err := startProcess(cmd)
		if err != nil {
			t.Fatal(err)
		}
		processes = append(processes, p)
		t.Cleanup(func() { stopProcesses([]*ownedProcess{p}, 0) })
		eventually(t, time.Second, func() bool { _, err := os.Stat(ready); return err == nil })
	}
	start := time.Now()
	stopProcesses(processes, 300*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond || elapsed > 750*time.Millisecond {
		t.Fatalf("termination took %s; expected one shared grace period", elapsed)
	}
	for _, p := range processes {
		if !errors.Is(syscall.Kill(p.cmd.Process.Pid, 0), syscall.ESRCH) {
			t.Fatal("worker survived or was not reaped")
		}
	}
}

func TestOwnedCommandCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("sh", "-c", `trap '' TERM; touch "$1"; exec sleep 60`, "fixture", ready)
	done := make(chan error, 1)
	go func() { done <- runCommand(ctx, cmd) }()
	eventually(t, time.Second, func() bool { _, err := os.Stat(ready); return err == nil })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation result = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not terminate and join command")
	}
	if !errors.Is(syscall.Kill(cmd.Process.Pid, 0), syscall.ESRCH) {
		t.Fatal("canceled command was not reaped")
	}
}

func TestOwnedCommandDoesNotStartAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("sleep", "60")
	if err := runCommand(ctx, cmd); !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("canceled invocation started: process=%v error=%v", cmd.Process, err)
	}
}

func termIgnoringTestCommand(dir, name string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestTermIgnoringProcessHelper$")
	cmd.Env = append(os.Environ(), "NUT_TEST_WORKER="+filepath.Join(dir, name))
	return cmd
}

func TestTermIgnoringProcessHelper(t *testing.T) {
	path := os.Getenv("NUT_TEST_WORKER")
	if path == "" {
		return
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	if err := os.WriteFile(path+".ready", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range signals {
		if err := os.WriteFile(path+".term", nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOwnedProcessLeaderExitCleansDescendant(t *testing.T) {
	if os.Getenv("NUT_TEST_SUBREAPER") == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwnedProcessLeaderExitCleansDescendant$")
		cmd.Env = append(os.Environ(), "NUT_TEST_SUBREAPER=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated subreaper test: %v\n%s", err, output)
		}
		return
	}
	// Confine adoption/reaping to this helper, never the concurrent test runner.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	output, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	defer func() { _ = writer.Close() }()
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = release.Close() }()
	cmd := exec.Command("sh", "-c", `sleep 60 & printf '%s\n' "$!"; read release; exit 7`)
	cmd.Stdout, cmd.Stdin = writer, input
	p, err := startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer stopProcesses([]*ownedProcess{p}, 0)
	_ = writer.Close()
	var descendant int
	if _, err := fmt.Fscanln(output, &descendant); err != nil {
		t.Fatal(err)
	}
	reaped := false
	defer func() {
		if !reaped {
			// Clean up even when the behavior under test fails.
			_ = unix.Kill(descendant, unix.SIGKILL)
			stopProcesses([]*ownedProcess{p}, 0)
			for {
				_, err := unix.Wait4(descendant, nil, 0, nil)
				if err != unix.EINTR {
					break
				}
			}
		}
	}()
	_ = release.Close()
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("leader was not reaped")
	}
	var status unix.WaitStatus
	eventually(t, time.Second, func() bool {
		pid, err := unix.Wait4(descendant, &status, unix.WNOHANG, nil)
		if err != nil && err != unix.EINTR {
			t.Fatalf("reap adopted descendant: %v", err)
		}
		reaped = pid == descendant
		return reaped
	})
	if !status.Signaled() || status.Signal() != unix.SIGKILL {
		t.Fatalf("descendant exit status = %v; expected group cleanup SIGKILL", status)
	}
	if !errors.Is(unix.Kill(descendant, 0), unix.ESRCH) {
		t.Fatal("descendant survived or was not reaped")
	}
}
