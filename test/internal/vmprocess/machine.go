//go:build hadron || talos

// Package vmprocess retains verified QEMU ownership from startup through cleanup.
// It is only used by the disposable VM harnesses, never by the shipped operator.
package vmprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

type processHandle interface {
	exited() (bool, error)
	kill() error
	close() error
}

type ownedMachine struct {
	types.Machine
	mu        sync.Mutex
	attempted bool
	stopped   bool
	process   processHandle
}

// Wrap must be called before Create. An unverified or failed startup preserves
// state for diagnosis; cleanup never falls back to PEG's numeric-PID Stop.
func Wrap(m types.Machine) types.Machine { return &ownedMachine{Machine: m} }

func (m *ownedMachine) Create(ctx context.Context) (context.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempted {
		return ctx, fmt.Errorf("machine startup already attempted")
	}
	m.attempted = true
	next, startErr := m.Machine.Create(ctx)
	process, ownershipErr := capture(m.Config().StateDir)
	m.process = process
	return next, errors.Join(startErr, ownershipErr)
}

func capture(root string) (processHandle, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("absolute machine state directory required")
	}
	data, err := os.ReadFile(filepath.Join(root, "pid"))
	if err != nil {
		return nil, fmt.Errorf("reading startup PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return nil, fmt.Errorf("invalid startup PID; refusing cleanup ownership")
	}
	return captureProcess(pid, root)
}

// Stop terminates only the process captured during Create and keeps diagnostics.
func Stop(m types.Machine, timeout time.Duration) error {
	owned, ok := m.(*ownedMachine)
	if !ok {
		return fmt.Errorf("machine has no verified startup ownership; refusing stop")
	}
	return owned.stop(timeout)
}

func (m *ownedMachine) Stop() error { return m.stop(30 * time.Second) }

// Exited observes the verified startup identity without rereading a PID file.
// This proves only process exit, never guest-initiated shutdown or its cause.
func Exited(m types.Machine) (bool, error) {
	owned, ok := m.(*ownedMachine)
	if !ok {
		return false, fmt.Errorf("machine has no verified startup ownership")
	}
	owned.mu.Lock()
	defer owned.mu.Unlock()
	if owned.stopped {
		return true, nil
	}
	if owned.process == nil {
		return false, fmt.Errorf("machine has no verified startup ownership")
	}
	return owned.process.exited()
}

func (m *ownedMachine) stop(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("stop timeout must be positive")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return nil
	}
	if m.process == nil {
		return fmt.Errorf("machine has no verified startup ownership; preserving state")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := m.process.kill(); err != nil {
		return fmt.Errorf("stopping owned process: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exited, err := m.process.exited()
		if err != nil {
			return fmt.Errorf("checking owned process: %w", err)
		}
		if exited {
			m.stopped = true
			return m.process.close()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned process has not exited; preserving state: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (m *ownedMachine) Clean() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempted && !m.stopped {
		return fmt.Errorf("owned process exit not confirmed; preserving state")
	}
	return m.Machine.Clean()
}
