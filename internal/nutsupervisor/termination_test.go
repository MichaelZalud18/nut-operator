package nutsupervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func childPID(t *testing.T, h *driverSupervisorHarness, name string) int {
	t.Helper()
	var pid int
	eventually(t, time.Second, func() bool {
		data, err := os.ReadFile(filepath.Join(h.behaveDir, name+".child"))
		if err != nil {
			return false
		}
		_, err = fmt.Sscanf(string(data), "%d", &pid)
		return err == nil && alive(pid)
	})
	return pid
}

func TestSupervisorBoundsUncooperativeWorkerRemoval(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("bad", "ignore-term")
	h.setBehavior("good", "run")
	h.setConfiguredUPS("bad", "good")
	h.start()
	bad := childPID(t, h, "bad")
	good := childPID(t, h, "good")
	h.setConfiguredUPS("good")
	eventually(t, 9*time.Second, func() bool {
		_, present := h.pid("bad")
		return !present && !alive(bad)
	})
	if !alive(good) {
		t.Fatal("removing a stuck worker stopped the healthy driver")
	}
}

func TestSupervisorStopsUncooperativeWorkersConcurrently(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	for _, name := range []string{"a", "b", "c"} {
		h.setBehavior(name, "ignore-term")
	}
	h.setConfiguredUPS("a", "b", "c")
	h.start()
	pids := []int{childPID(t, h, "a"), childPID(t, h, "b"), childPID(t, h, "c")}
	cmd := h.cmd
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		h.cmd = nil
		if err != nil {
			t.Fatalf("supervisor exit: %v", err)
		}
	case <-time.After(9 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		h.cmd = nil
		t.Fatal("shutdown exceeded the shared worker termination budget")
	}
	for _, pid := range pids {
		if alive(pid) {
			t.Errorf("worker %d survived supervisor shutdown", pid)
		}
	}
}

func TestSupervisorBoundsStalledNamedStop(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setConfiguredUPS("old", "good")
	h.start()
	_ = childPID(t, h, "old")
	good := childPID(t, h, "good")
	if err := os.WriteFile(filepath.Join(h.behaveDir, ".stop_hang"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.setConfiguredUPS("good", "new")
	eventually(t, 9*time.Second, func() bool {
		pid, present := h.pid("new")
		return present && alive(pid)
	})
	if !alive(good) {
		t.Fatal("stalled named stop interrupted the unrelated driver")
	}
}
