//go:build (hadron || talos) && linux

package vmprocess

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type pidHandle int

func captureProcess(pid int, root string) (processHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("opening startup pidfd: %w", err)
	}
	h := pidHandle(fd)
	verified := false
	defer func() {
		if !verified {
			_ = h.close()
		}
	}()
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return nil, fmt.Errorf("reading QEMU executable identity: %w", err)
	}
	if filepath.Base(exe) != "qemu-system-x86_64" {
		return nil, fmt.Errorf("startup PID does not identify the expected QEMU executable")
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, fmt.Errorf("reading QEMU command identity: %w", err)
	}
	// PEG supplies this exact monitor argument for each private state directory.
	// Match an actual option pair, not an arbitrary path prefix in a guest argument.
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	monitor := "unix:" + filepath.Join(root, "qemu-monitor.sock") + ",server,nowait"
	matched := false
	var observed []string
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "-monitor" {
			observed = append(observed, args[i+1])
			matched = matched || args[i+1] == monitor
		}
	}
	if !matched {
		exited, probeErr := h.exited()
		return nil, errors.Join(fmt.Errorf("QEMU does not reference the owned state directory: cmdline bytes=%d, monitor=%q, expected=%q, exited=%t", len(cmdline), observed, monitor, exited), probeErr)
	}
	// A PID could be reused while /proc was read. The original pidfd must still
	// identify a live process after validation; all subsequent signals use it.
	if exited, err := h.exited(); err != nil || exited {
		return nil, errors.Join(fmt.Errorf("startup process exited during ownership validation"), err)
	}
	verified = true
	return h, nil
}

func (h pidHandle) exited() (bool, error) {
	fds := []unix.PollFd{{Fd: int32(h), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, 0)
	if err != nil {
		return false, err
	}
	if fds[0].Revents&(unix.POLLNVAL|unix.POLLERR) != 0 {
		return false, fmt.Errorf("invalid pidfd poll result: %d", fds[0].Revents)
	}
	return fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
}

func (h pidHandle) kill() error {
	err := unix.PidfdSendSignal(int(h), unix.SIGKILL, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil // The retained process may already have powered itself off.
	}
	return err
}

func (h pidHandle) close() error { return unix.Close(int(h)) }
