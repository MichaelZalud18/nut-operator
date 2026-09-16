package nutsupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Options configures the foreground NUT driver supervisor.
type Options struct {
	ConfigDir string
	Interval  time.Duration
	Logger    *slog.Logger
}

// Run owns foreground NUT workers until cancellation, then terminates and joins
// them. It does not listen on a socket or access the Kubernetes API.
func Run(ctx context.Context, opts Options) error {
	if opts.ConfigDir == "" || opts.Interval <= 0 {
		return fmt.Errorf("configuration directory and positive reconciliation interval are required")
	}
	newRuntime(opts).run(ctx)
	return nil
}

type configDigest struct {
	ups   [sha256.Size]byte
	users [sha256.Size]byte
}

type driverWorker struct {
	process    *ownedProcess
	config     [sha256.Size]byte
	hasApplied bool
}

type runtimeSupervisor struct {
	opts       Options
	command    func(string, ...string) *exec.Cmd
	workers    map[string]*driverWorker
	retryAfter map[string]time.Time
	applied    configDigest
	hasApplied bool
	grace      time.Duration
	timeout    time.Duration
}

func newRuntime(opts Options) *runtimeSupervisor {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &runtimeSupervisor{
		opts: opts,
		command: func(name string, args ...string) *exec.Cmd {
			cmd := exec.Command(name, args...)
			cmd.Env = append(os.Environ(), "NUT_CONFPATH="+opts.ConfigDir, "NUT_QUIET_INIT_BANNER=true")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			return cmd
		},
		workers: make(map[string]*driverWorker), retryAfter: make(map[string]time.Time),
		grace: 5 * time.Second, timeout: 5 * time.Second,
	}
}

func (s *runtimeSupervisor) run(ctx context.Context) {
	defer s.stop()
	for ctx.Err() == nil {
		s.reconcile(ctx)
		timer := time.NewTimer(s.opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *runtimeSupervisor) stop() {
	processes := make([]*ownedProcess, 0, len(s.workers))
	for _, w := range s.workers {
		processes = append(processes, w.process)
	}
	stopProcesses(processes, s.grace)
	clear(s.workers)
}

func (s *runtimeSupervisor) snapshot() (configDigest, bool, error) {
	ups, err := os.ReadFile(filepath.Join(s.opts.ConfigDir, "ups.conf"))
	if err != nil {
		return configDigest{}, false, err
	}
	users, err := os.ReadFile(filepath.Join(s.opts.ConfigDir, "upsd.users"))
	if err != nil {
		return configDigest{}, false, err
	}
	return configDigest{sha256.Sum256(ups), sha256.Sum256(users)}, len(ups) == 0, nil
}

func (s *runtimeSupervisor) invoke(ctx context.Context, cmd *exec.Cmd) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return runCommand(ctx, cmd)
}

func (s *runtimeSupervisor) enumerate(ctx context.Context, empty bool) ([]string, error) {
	if empty {
		return nil, nil
	}
	cmd := s.command("upsdrvctl", "list")
	var output limitedOutput
	cmd.Stdout = &output
	if err := s.invoke(ctx, cmd); err != nil {
		return nil, err
	}
	if output.overflow {
		return nil, fmt.Errorf("driver listing exceeded output limit")
	}
	names := strings.Fields(output.String())
	if len(names) == 0 {
		return nil, fmt.Errorf("nonempty configuration enumerated no devices")
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// Keep command output bounded without blocking the child's stdout pipe.
type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (1 << 20) - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

func (s *runtimeSupervisor) unchanged(want configDigest) bool {
	current, _, err := s.snapshot()
	return err == nil && current == want
}

func (s *runtimeSupervisor) collectExits() {
	for name, w := range s.workers {
		select {
		case <-w.process.done:
			s.opts.Logger.Info("Driver exited", "ups", name, "result", w.process.err)
			s.retryAfter[name] = time.Now().Add(s.opts.Interval)
			delete(s.workers, name)
		default:
		}
	}
}

func (s *runtimeSupervisor) reconcile(ctx context.Context) {
	s.collectExits()
	digest, empty, err := s.snapshot()
	if err != nil {
		s.opts.Logger.Error("Cannot read NUT configuration", "error", err)
		return
	}
	names, err := s.enumerate(ctx, empty)
	if err != nil {
		s.opts.Logger.Error("Cannot enumerate drivers, keeping existing workers", "error", err)
		return
	}
	if !s.unchanged(digest) || ctx.Err() != nil {
		return
	}
	if !s.hasApplied || s.applied != digest {
		// The command may apply a different revision even if it fails or disk rolls back.
		s.hasApplied = false
		if err := s.invoke(ctx, s.command("upsd", "-c", "reload")); err != nil {
			s.opts.Logger.Error("Upsd reload failed, will retry", "error", err)
			return
		}
		if !s.unchanged(digest) || ctx.Err() != nil {
			return
		}
		s.applied, s.hasApplied = digest, true
	}
	s.removeAbsent(ctx, names)
	for _, name := range names {
		if ctx.Err() != nil || !s.unchanged(digest) {
			return
		}
		s.reconcileDriver(ctx, name, digest.ups)
	}
}

func (s *runtimeSupervisor) removeAbsent(ctx context.Context, names []string) {
	var removed []*ownedProcess
	for name, w := range s.workers {
		if !slices.Contains(names, name) {
			removed = append(removed, w.process)
		}
	}
	for name := range s.retryAfter {
		if !slices.Contains(names, name) {
			delete(s.retryAfter, name)
		}
	}
	if !stopProcessesContext(ctx, removed, s.grace) {
		// Shutdown still owns these workers and signals survivors without another wait.
		return
	}
	for name := range s.workers {
		if !slices.Contains(names, name) {
			delete(s.workers, name)
		}
	}
}

func (s *runtimeSupervisor) reconcileDriver(ctx context.Context, name string, digest [sha256.Size]byte) {
	if w := s.workers[name]; w != nil {
		if !w.hasApplied || w.config != digest {
			w.hasApplied = false
			err := s.invoke(ctx, s.command("upsdrvctl", "-c", "reload-or-exit", name))
			if err != nil {
				s.opts.Logger.Error("Driver reload failed, will retry", "ups", name, "error", err)
				// A changed driver= selects a new PID-file name in upsdrvctl.
				// Reach the old owned driver with NUT's same reload-or-exit signal;
				// NUT still decides whether to exit. Keep adoption unconfirmed.
				if ctx.Err() == nil {
					if signalErr := w.process.reload(); signalErr != nil {
						s.opts.Logger.Error("Owned driver reload signal failed", "ups", name, "error", signalErr)
					}
				}
				return
			}
			current, _, err := s.snapshot()
			if err != nil || current.ups != digest || ctx.Err() != nil {
				return
			}
			w.config, w.hasApplied = digest, true
		}
		return
	}
	if time.Now().Before(s.retryAfter[name]) || ctx.Err() != nil {
		return
	}
	p, err := startProcess(s.command("upsdrvctl", "-FF", "start", name))
	if err != nil {
		s.opts.Logger.Error("Cannot start driver", "ups", name, "error", err)
		s.retryAfter[name] = time.Now().Add(s.opts.Interval)
		return
	}
	s.workers[name] = &driverWorker{process: p, config: digest, hasApplied: true}
	s.opts.Logger.Info("Started driver", "ups", name, "pid", p.cmd.Process.Pid)
}
