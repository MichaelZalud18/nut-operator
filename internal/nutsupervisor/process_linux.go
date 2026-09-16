package nutsupervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ownedProcess exclusively owns Wait and all signals for one foreground process
// group. err is published by closing done; callers must receive done before reading it.
type ownedProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	mu   sync.Mutex
}

func startProcess(cmd *exec.Cmd) (*ownedProcess, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Bound pipe copying if an unexpected descendant escapes the process group.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &ownedProcess{cmd: cmd, done: make(chan struct{})}
	go p.wait()
	return p, nil
}

func (p *ownedProcess) wait() {
	// Leave the exited leader waitable until its group is cleaned up. Reaping
	// first would allow PID/PGID reuse before the final group signal.
	var info unix.Siginfo
	var waitErr error
	for {
		waitErr = unix.Waitid(unix.P_PID, p.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(waitErr, unix.EINTR) {
			break
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if waitErr == nil {
		_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
	}
	p.err = errors.Join(waitErr, p.cmd.Wait())
	close(p.done)
}

func (p *ownedProcess) signal(signal unix.Signal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return
	default:
		_ = unix.Kill(-p.cmd.Process.Pid, signal)
	}
}

// NUT uses SIGUSR1 for reload-or-exit. Single-device -FF execs the driver, so
// signal only the owned leader, never a PID inferred from a replacement config.
func (p *ownedProcess) reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return os.ErrProcessDone
	default:
		return p.cmd.Process.Signal(unix.SIGUSR1)
	}
}

// stopProcesses starts every grace period together and joins every owned child.
// As with container termination, kernel uninterruptible I/O can prevent reaping.
func stopProcesses(processes []*ownedProcess, grace time.Duration) {
	stopProcessesContext(context.Background(), processes, grace)
}

// Cancellation interrupts the grace wait; the caller retains ownership and must
// finish termination/joining, including any other workers needed for shutdown.
func stopProcessesContext(ctx context.Context, processes []*ownedProcess, grace time.Duration) bool {
	if len(processes) == 0 {
		return true
	}
	for _, p := range processes {
		p.signal(unix.SIGTERM)
	}
	done := make(chan struct{})
	go func() {
		for _, p := range processes {
			<-p.done
		}
		close(done)
	}()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-done:
		return true
	case <-timer.C:
		for _, p := range processes {
			p.signal(unix.SIGKILL)
		}
		select {
		case <-ctx.Done():
			return false
		case <-done:
			return true
		}
	}
}

// One-shot NUT commands have a hard context deadline rather than a driver grace
// period. Cancellation kills the entire command group and joins it before return.
func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := startProcess(cmd)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		p.signal(unix.SIGKILL)
		<-p.done
		return ctx.Err()
	case <-p.done:
		return p.err
	}
}
