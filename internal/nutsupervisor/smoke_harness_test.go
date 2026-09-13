//go:build linux

package nutsupervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSmokeHarnessCancellationCleansOwnedContainer(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "container-tool")
	stub := "#!/bin/sh\ncase \"$1\" in\nrun) touch \"$FIXTURE/started\"; exec sleep 30;;\nrm) touch \"$FIXTURE/removed\";;\nesac\n"
	if err := os.WriteFile(tool, []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "../../hack/nut-supervisor-smoke.sh", tool, "unused-image")
	cmd.Env = append(os.Environ(), "FIXTURE="+dir, "NUT_READINESS_SAMPLES=0", "TMPDIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	eventually(t, time.Second, func() bool {
		_, err := os.Stat(filepath.Join(dir, "started"))
		return err == nil
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatal("cancellation waited for the container command instead of running cleanup")
	}
	if err == nil {
		t.Fatal("cancellation unexpectedly reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, "removed")); err != nil {
		t.Fatalf("owned container was not removed: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "tmp.*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary identity leaked: %v, %v", matches, err)
	}
}
