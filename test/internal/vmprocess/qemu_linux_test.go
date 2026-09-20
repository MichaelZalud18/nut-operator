//go:build (hadron || talos) && linux

package vmprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Exercise the packaged executable, not the stand-in used by ownership unit tests.
// No guest OS, disk, network, KVM or image download is involved.
func TestInstalledQEMUOwnership(t *testing.T) {
	binary, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		if os.Getenv("VM_PROCESS_REQUIRE_QEMU") == "1" {
			t.Fatal(err)
		}
		t.Skip("packaged QEMU is exercised by CI")
	}
	for _, delay := range []time.Duration{0, 100 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			root := t.TempDir()
			monitor := "unix:" + filepath.Join(root, "qemu-monitor.sock") + ",server,nowait"
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-machine", "none,accel=tcg", "-nodefaults", "-display", "none", "-S", "-monitor", monitor)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			time.Sleep(delay)
			h, err := captureProcess(cmd.Process.Pid, root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = h.close() }()
			if err := h.kill(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
