//go:build (hadron || talos) && linux

package vmprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spectrocloud/peg/pkg/machine"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

func TestMain(m *testing.M) {
	if os.Getenv("VM_PROCESS_PEG_HELPER") == "1" {
		// PEG supplies QEMU flags and no stdin. Stay alive for the parent's stop
		// assertion, with a hard upper bound if the parent unexpectedly disappears.
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProcessHelper(t *testing.T) {
	if os.Getenv("VM_PROCESS_HELPER") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func startProcess(t *testing.T, root string, qemu bool) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if qemu {
		alias := filepath.Join(t.TempDir(), "qemu-system-x86_64")
		if err := os.Link(exe, alias); err != nil {
			t.Fatal(err)
		}
		exe = alias
	}
	cmd := exec.Command(exe, "-test.run=^TestProcessHelper$", "--", "-monitor",
		"unix:"+filepath.Join(root, "qemu-monitor.sock")+",server,nowait")
	cmd.Env = append(os.Environ(), "VM_PROCESS_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

type fixtureMachine struct {
	types.Machine
	root     string
	pid      int
	cleaned  bool
	startErr error
}

func (m *fixtureMachine) Config() types.MachineConfig {
	return types.MachineConfig{StateDir: m.root}
}

func (m *fixtureMachine) Create(ctx context.Context) (context.Context, error) {
	err := os.WriteFile(filepath.Join(m.root, "pid"), []byte(fmt.Sprint(m.pid)), 0600)
	return ctx, errors.Join(err, m.startErr)
}

func (m *fixtureMachine) Stop() error { panic("unchecked PEG Stop must never be called") }

func (m *fixtureMachine) Clean() error {
	m.cleaned = true
	return os.RemoveAll(m.root)
}

func TestRejectForeignStartupProcess(t *testing.T) {
	for _, qemu := range []bool{false, true} {
		t.Run(fmt.Sprintf("qemu=%v", qemu), func(t *testing.T) {
			root := t.TempDir()
			processRoot := root
			if qemu {
				processRoot = root + "-other"
			}
			cmd := startProcess(t, processRoot, qemu)
			base := &fixtureMachine{root: root, pid: cmd.Process.Pid}
			m := Wrap(base)
			if _, err := m.Create(context.Background()); err == nil {
				t.Fatal("foreign process accepted")
			}
			if err := Stop(m, time.Second); err == nil {
				t.Fatal("unverified stop accepted")
			}
			if err := m.Clean(); err == nil || base.cleaned {
				t.Fatal("unverified state deleted")
			}
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("foreign process did not survive: %v", err)
			}
		})
	}
}

func TestRetainedOwnershipSurvivesPIDFileChanges(t *testing.T) {
	for _, mutation := range []string{"replace", "remove", "already-exited", "partial-start"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			cmd := startProcess(t, root, true)
			foreign := startProcess(t, t.TempDir(), true)
			base := &fixtureMachine{root: root, pid: cmd.Process.Pid}
			if mutation == "partial-start" {
				base.startErr = errors.New("startup failed after launch")
			}
			m := Wrap(base)
			_, err := m.Create(context.Background())
			if !errors.Is(err, base.startErr) {
				t.Fatal(err)
			}
			if err := m.Clean(); err == nil {
				t.Fatal("cleaned before confirmed exit")
			}
			if exited, err := Exited(m); err != nil || exited {
				t.Fatalf("live process observation: exited=%v err=%v", exited, err)
			}
			pidFile := filepath.Join(root, "pid")
			switch mutation {
			case "replace":
				err = os.WriteFile(pidFile, []byte(fmt.Sprint(foreign.Process.Pid)), 0600)
			case "remove":
				err = os.Remove(pidFile)
			case "already-exited":
				err = cmd.Process.Kill()
				_ = cmd.Wait()
			}
			if err != nil && mutation != "partial-start" {
				t.Fatal(err)
			}
			if err := Stop(m, time.Second); err != nil {
				t.Fatal(err)
			}
			if exited, err := Exited(m); err != nil || !exited {
				t.Fatalf("stopped process observation: exited=%v err=%v", exited, err)
			}
			if _, err := os.Stat(root); err != nil {
				t.Fatalf("Stop removed diagnostic state: %v", err)
			}
			if err := Stop(m, time.Second); err != nil {
				t.Fatalf("repeat stop: %v", err)
			}
			if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("foreign process did not survive: %v", err)
			}
			if err := m.Clean(); err != nil || !base.cleaned {
				t.Fatalf("cleanup: %v", err)
			}
		})
	}
}

type stalledProcess struct{ probeErr, killErr error }

func (p stalledProcess) exited() (bool, error) { return false, p.probeErr }
func (p stalledProcess) kill() error           { return p.killErr }
func (p stalledProcess) close() error          { panic("live handle closed") }

func TestFailedStopPreservesState(t *testing.T) {
	probeErr := errors.New("probe failed")
	killErr := errors.New("signal failed")
	for _, tc := range []struct {
		name    string
		process stalledProcess
		want    error
	}{
		{"timeout", stalledProcess{}, context.DeadlineExceeded},
		{"probe", stalledProcess{probeErr: probeErr}, probeErr},
		{"signal", stalledProcess{killErr: killErr}, killErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := &fixtureMachine{root: t.TempDir()}
			m := &ownedMachine{Machine: base, attempted: true, process: tc.process}
			if err := Stop(m, 20*time.Millisecond); !errors.Is(err, tc.want) {
				t.Fatalf("stop: %v", err)
			}
			if err := m.Clean(); err == nil || base.cleaned {
				t.Fatal("unconfirmed process state deleted")
			}
		})
	}
}

func TestRejectMissingInvalidAndDeadStartupPID(t *testing.T) {
	cmd := startProcess(t, t.TempDir(), true)
	pid := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	for _, value := range []string{"missing", "", "invalid", "0", "1", "-1", fmt.Sprint(os.Getpid()), fmt.Sprint(pid)} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			if value != "missing" {
				if err := os.WriteFile(filepath.Join(root, "pid"), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if handle, err := capture(root); err == nil {
				_ = handle.close()
				t.Fatal("unverifiable startup accepted")
			}
		})
	}
}

func TestPinnedPEGStartupAndCleanup(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "qemu-system-x86_64")
	if err := os.Link(exe, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VM_PROCESS_PEG_HELPER", "1")
	root := t.TempDir()
	base, err := machine.New(types.QEMUEngine, types.WithStateDir(root), types.WithProcessName(alias),
		types.DisableDefaultNetworking, func(cfg *types.MachineConfig) error {
			cfg.AutoDriveSetup = false
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	m := Wrap(base)
	t.Cleanup(func() { _ = Stop(m, time.Second) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := m.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Stop(m, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := m.Clean(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state remains after verified teardown: %v", err)
	}
}
