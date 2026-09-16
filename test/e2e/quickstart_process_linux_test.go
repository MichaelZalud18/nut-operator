//go:build e2e && linux

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestQuickstartCommandDescendants(t *testing.T) {
	if os.Getenv("NUT_QUICKSTART_SUBREAPER") == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQuickstartCommandDescendants$")
		cmd.Env = append(os.Environ(), "NUT_QUICKSTART_SUBREAPER=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.WaitDelay = time.Second
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated descendant test: %v\n%s", err, out)
		}
		return
	}
	// Adopt orphaned fixture children only in this isolated test process.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name   string
		script string
		signal syscall.Signal
	}{
		{"inherited-pipes", `sh -c 'printf "%s\n" "$$"; while :; do :; done' & wait`, 0},
		{"term-resistant", `sh -c 'trap "" TERM; printf "%s\n" "$$"; while :; do :; done' & wait`, 0},
		{"closed-pipes", `sh -c 'trap "" TERM; printf "%s\n" "$$"; exec >/dev/null 2>&1; while :; do :; done' & wait`, 0},
		{"parent-sigterm", `sh -c 'trap "" TERM; printf "%s\n" "$$"; kill -TERM -"$1"; while :; do :; done' sh "$PPID" & wait`, syscall.SIGTERM},
		{"parent-interrupt", `sh -c 'trap "" TERM INT; printf "%s\n" "$$"; kill -INT -"$1"; while :; do :; done' sh "$PPID" & wait`, syscall.SIGINT},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			stop := func() {}
			timeout := time.Second
			observed := make(chan os.Signal, 1)
			if fixture.signal != 0 {
				// Model Ginkgo's independent subscription. The fixture signals the
				// Go parent's group only, after its own TERM-resistant child is ready.
				signal.Notify(observed, fixture.signal)
				defer signal.Stop(observed)
				ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
				defer stop()
				timeout = 10 * time.Second
			}
			started := time.Now()
			out, err := quickstartCommand(ctx, timeout, nil, "sh", "-c", fixture.script)
			elapsed := time.Since(started)
			var child int
			if _, scanErr := fmt.Fscan(strings.NewReader(out), &child); scanErr != nil || child <= 0 {
				t.Fatalf("fixture did not report its child: %q: %v", out, scanErr)
			}
			reaped := false
			t.Cleanup(func() {
				if !reaped {
					_ = unix.Kill(child, unix.SIGKILL)
					for {
						if _, waitErr := unix.Wait4(child, nil, 0, nil); !errors.Is(waitErr, unix.EINTR) {
							break
						}
					}
				}
			})
			if err == nil {
				t.Fatal("command outlived its deadline without an error")
			}
			if elapsed > 4*time.Second {
				t.Fatalf("command cleanup exceeded its bound: %s", elapsed)
			}
			var status unix.WaitStatus
			// Allow the kernel to finish exit/reparenting after the group signal.
			deadline := time.Now().Add(time.Second)
			for {
				pid, waitErr := unix.Wait4(child, &status, unix.WNOHANG, nil)
				if waitErr != nil && !errors.Is(waitErr, unix.EINTR) {
					t.Fatalf("reap descendant: %v", waitErr)
				}
				if pid == child {
					reaped = true
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("descendant survived quickstartCommand return")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !status.Signaled() || status.Signal() != unix.SIGKILL {
				t.Fatalf("descendant status = %v; want SIGKILL", status)
			}
			if !errors.Is(unix.Kill(child, 0), unix.ESRCH) {
				t.Fatal("descendant still exists after reaping")
			}
			if fixture.signal != 0 {
				// Model deferred cleanup using the same spec context. Even a fresh
				// deadline must not permit another command to start after the signal.
				out, err := quickstartCommand(ctx, time.Second, nil, "sh", "-c", "printf started")
				if !errors.Is(err, context.Canceled) || out != "" {
					t.Fatalf("cleanup command started after cancellation: output=%q err=%v", out, err)
				}
				stop()
				assertQuickstartSignalObserver(t, observed, fixture.signal)
			}
		})
	}
}

func assertQuickstartSignalObserver(t *testing.T, observed <-chan os.Signal, sig syscall.Signal) {
	t.Helper()
	select {
	case <-observed:
	default:
		t.Fatal("independent signal subscriber missed cancellation")
	}
	// Stopping the command's subscription must preserve other subscribers.
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("command cleanup removed independent signal subscriber")
	}
}
