package nutsupervisor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runtimeFixture(t *testing.T) (*driverSupervisorHarness, *runtimeSupervisor) {
	t.Helper()
	h := newDriverSupervisorHarness(t)
	s := newRuntime(Options{ConfigDir: h.etcDir, Interval: 50 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	s.command = func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(filepath.Join(h.binDir, name), args...)
		cmd.Env = append(os.Environ(), "NUT_CONFPATH="+h.etcDir, "NUT_QUIET_INIT_BANNER=true")
		return cmd
	}
	s.grace = 100 * time.Millisecond
	s.timeout = 100 * time.Millisecond
	t.Cleanup(func() { s.stop() })
	return h, s
}

func TestRuntimeRetainsWorkersWhenListingFails(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	p := s.workers["good"].process
	h.failListing(true)
	h.setConfiguredUPS("other")
	s.reconcile(context.Background())
	if s.workers["good"].process != p || len(s.workers) != 1 {
		t.Fatal("failed enumeration changed workers")
	}
}

func TestRuntimePIDStabilityAcrossRepeatedMembershipChanges(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("stable")
	s.reconcile(context.Background())
	stable := s.workers["stable"].process
	for cycle := range 15 {
		h.setConfiguredUPS("stable", "churn")
		s.reconcile(context.Background())
		if len(s.workers) != 2 {
			t.Fatalf("cycle %d: driver was not added", cycle)
		}
		churn := s.workers["churn"].process
		h.setConfiguredUPS("stable")
		s.reconcile(context.Background())
		if len(s.workers) != 1 || s.workers["stable"].process != stable {
			t.Fatalf("cycle %d: unchanged worker was replaced", cycle)
		}
		select {
		case <-churn.done:
		default:
			t.Fatalf("cycle %d: removed worker was not reaped", cycle)
		}
		select {
		case <-stable.done:
			t.Fatalf("cycle %d: unchanged worker exited", cycle)
		default:
		}
	}
}

func TestRuntimeFailedReloadRetainsUnappliedConfig(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	p := s.workers["good"].process
	old := s.applied
	marker := filepath.Join(h.root, "upsd-reload-fail")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.setConfiguredUPS("good", "new")
	s.reconcile(context.Background())
	s.reconcile(context.Background())
	if s.applied != old || len(s.workers) != 1 || s.workers["good"].process != p {
		t.Fatal("failed server reload adopted configuration")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	s.reconcile(context.Background())
	if s.applied == old || len(s.workers) != 2 || s.workers["good"].process != p {
		t.Fatal("server reload did not recover without disturbing healthy worker")
	}
}

func TestRuntimeEmptyConfigConvergesWithoutEnumerating(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("old")
	s.reconcile(context.Background())
	p := s.workers["old"].process
	h.setConfiguredUPS()
	h.failListing(true)
	s.reconcile(context.Background())
	if len(s.workers) != 0 {
		t.Fatal("zero-byte desired configuration retained worker")
	}
	select {
	case <-p.done:
	default:
		t.Fatal("removed child was not joined")
	}
}

func TestRuntimeUsersChangeDoesNotReloadDrivers(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	p := s.workers["good"].process
	h.touchServerConfig("changed")
	s.reconcile(context.Background())
	if s.workers["good"].process != p {
		t.Fatal("users-only change restarted driver")
	}
	if _, err := os.Stat(filepath.Join(h.root, "driver-reload.log")); !os.IsNotExist(err) {
		t.Fatal("users-only change invoked driver reload")
	}
}

func TestRuntimeUnchangedConfigurationDoesNotReload(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	log := filepath.Join(h.root, "upsd-reload.log")
	before, err := os.ReadFile(log)
	if err != nil || string(before) != "reload\n" {
		t.Fatalf("initial reload = %q, %v", before, err)
	}
	s.reconcile(context.Background())
	after, err := os.ReadFile(log)
	if err != nil || string(after) != string(before) {
		t.Fatalf("unchanged configuration reloaded upsd: %q, %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "driver-reload.log")); !os.IsNotExist(err) {
		t.Fatal("unchanged configuration reloaded drivers")
	}
}

func TestRuntimeStalledEnumerationRetainsWorkers(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	w := s.workers["good"]
	_ = childPID(t, h, "good")
	h.mustWriteExecutable(filepath.Join(h.binDir, "upsdrvctl"), "#!/bin/sh\ntrap '' TERM\nexec sleep 60\n")
	start := time.Now()
	s.reconcile(context.Background())
	if time.Since(start) > time.Second || s.workers["good"] != w {
		t.Fatal("stalled enumeration exceeded its deadline or changed workers")
	}
	select {
	case <-w.process.done:
		t.Fatal("stalled enumeration stopped healthy driver")
	default:
	}
}

func TestRuntimeDriverReloadUsesNUTAndRetriesFailure(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	w := s.workers["good"]
	_ = childPID(t, h, "good")
	old := w.config
	atomicWriteFile(t, filepath.Join(h.binDir, "upsdrvctl"), []byte("#!/bin/sh\ncase \"$1\" in\nlist) echo good;;\n-c) exit 1;;\nesac\n"), 0o755)
	atomicWriteFile(t, h.upsConf, []byte("[good]\n driver = dummy-ups\n pollinterval = 3\n"), 0o600)
	s.reconcile(context.Background())
	if w.config != old {
		t.Fatal("failed driver reload was marked applied")
	}
	log := filepath.Join(h.root, "driver-reload.log")
	atomicWriteFile(t, filepath.Join(h.binDir, "upsdrvctl"), []byte("#!/bin/sh\ncase \"$1\" in\nlist) echo good;;\n-c) printf '%s\\n' \"$*\" >> '"+log+"';;\nesac\n"), 0o755)
	s.reconcile(context.Background())
	data, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(data)) != "-c reload-or-exit good" {
		t.Fatalf("NUT reload invocation = %q, %v", data, err)
	}
	if w.config == old || s.workers["good"] != w {
		t.Fatal("successful live reload did not preserve worker")
	}
}

func TestRuntimeCrashRecoveryHasFixedMinimumInterval(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setBehavior("bad", "crash")
	h.setConfiguredUPS("bad", "good")
	s.reconcile(context.Background())
	p := s.workers["bad"].process
	good := s.workers["good"].process
	<-p.done
	s.reconcile(context.Background())
	if s.workers["bad"] != nil {
		t.Fatal("crashed worker restarted without interval")
	}
	time.Sleep(2 * s.opts.Interval)
	s.reconcile(context.Background())
	if s.workers["bad"] == nil || s.workers["bad"].process == p || s.workers["good"].process != good {
		t.Fatal("crash recovery failed or disturbed healthy worker")
	}
}

func TestRuntimeDoesNotAdoptConfigChangedDuringEnumeration(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	old := s.applied
	_ = childPID(t, h, "good")
	atomicWriteFile(t, filepath.Join(h.binDir, "upsdrvctl"), []byte("#!/bin/sh\necho changed >> '"+h.upsConf+"'\necho good\n"), 0o755)
	s.reconcile(context.Background())
	if s.applied != old {
		t.Fatal("configuration changed during validation was adopted")
	}
}

func TestRuntimeCancellationJoinsDrivers(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	p := s.workers["good"].process
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.run(ctx)
	select {
	case <-p.done:
	default:
		t.Fatal("run returned before joining child")
	}
}

func TestRuntimeReloadFallbackTargetsOwnedLeader(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("good")
	s.reconcile(context.Background())
	w := s.workers["good"]
	_ = childPID(t, h, "good")
	if err := w.process.reload(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.process.done:
		t.Fatal("reload signal must not terminate a reloadable worker")
	case <-time.After(30 * time.Millisecond):
	}
	s.stop()
	if err := w.process.reload(); err == nil {
		t.Fatal("reaped worker accepted a reload signal")
	}
}

func TestRuntimeServerReloadRollbackRetries(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit-%d", exitCode), func(t *testing.T) {
			h, s := runtimeFixture(t)
			h.setConfiguredUPS("good")
			s.reconcile(context.Background())
			p := s.workers["good"].process
			old := s.applied
			backup := filepath.Join(h.root, "users-a")
			atomicWriteFile(t, backup, []byte("initial\n"), 0o600)
			adopted := filepath.Join(h.root, "server-adopted")
			atomicWriteFile(t, filepath.Join(h.binDir, "upsd"), []byte(fmt.Sprintf(
				"#!/bin/sh\ncp '%s' '%s'\ncp '%s' '%s'\nexit %d\n", h.upsdUsers, adopted, backup, h.upsdUsers, exitCode)), 0o755)
			h.touchServerConfig("B")
			s.reconcile(context.Background())
			if s.hasApplied || s.applied != old {
				t.Error("uncertain server reload retained confirmed applied state")
			}
			atomicWriteFile(t, filepath.Join(h.binDir, "upsd"), []byte(fmt.Sprintf(
				"#!/bin/sh\ncp '%s' '%s'\n", h.upsdUsers, adopted)), 0o755)
			s.reconcile(context.Background())
			data, err := os.ReadFile(adopted)
			if err != nil || string(data) != "initial\n" {
				t.Fatalf("server did not reload rolled-back A: %q, %v", data, err)
			}
			if !s.hasApplied || s.applied != old || s.workers["good"].process != p {
				t.Fatal("rollback did not converge while preserving worker")
			}
		})
	}
}

func TestRuntimeDriverReloadRotationRetries(t *testing.T) {
	for _, exitCode := range []int{0, 1} {
		t.Run(fmt.Sprintf("exit-%d", exitCode), func(t *testing.T) {
			h, s := runtimeFixture(t)
			h.setConfiguredUPS("good")
			s.reconcile(context.Background())
			w := s.workers["good"]
			_ = childPID(t, h, "good")
			old := w.config
			a, err := os.ReadFile(h.upsConf)
			if err != nil {
				t.Fatal(err)
			}
			backup := filepath.Join(h.root, "ups-a")
			atomicWriteFile(t, backup, a, 0o600)
			adopted := filepath.Join(h.root, "driver-adopted")
			atomicWriteFile(t, filepath.Join(h.binDir, "upsdrvctl"), []byte(fmt.Sprintf(
				"#!/bin/sh\ncase \"$1\" in\nlist) echo good;;\n-c) cp '%s' '%s'; cp '%s' '%s'; exit %d;;\nesac\n",
				h.upsConf, adopted, backup, h.upsConf, exitCode)), 0o755)
			atomicWriteFile(t, h.upsConf, append(append([]byte{}, a...), []byte("  pollinterval = 3\n")...), 0o600)
			s.reconcile(context.Background())
			if w.config != old {
				t.Error("driver adopted digest despite rotation during reload")
			}
			atomicWriteFile(t, filepath.Join(h.binDir, "upsdrvctl"), []byte(fmt.Sprintf(
				"#!/bin/sh\ncase \"$1\" in\nlist) echo good;;\n-c) cp '%s' '%s';;\nesac\n", h.upsConf, adopted)), 0o755)
			s.reconcile(context.Background())
			data, err := os.ReadFile(adopted)
			if err != nil || string(data) != string(a) {
				t.Fatalf("driver did not reload rolled-back A: %q, %v", data, err)
			}
			if s.workers["good"] != w || w.config != old {
				t.Fatal("rollback did not converge while preserving worker")
			}
		})
	}
}

func TestRuntimeCancellationDuringRemovalSignalsSurvivors(t *testing.T) {
	h, s := runtimeFixture(t)
	s.grace = time.Second
	originalCommand := s.command
	s.command = func(name string, args ...string) *exec.Cmd {
		if name == "upsdrvctl" && args[0] == "-FF" {
			return termIgnoringTestCommand(h.root, args[2])
		}
		return originalCommand(name, args...)
	}
	names := []string{"removed-a", "removed-b", "survivor-a", "survivor-b"}
	h.setConfiguredUPS(names...)
	s.reconcile(context.Background())
	var processes []*ownedProcess
	for _, name := range names {
		processes = append(processes, s.workers[name].process)
		eventually(t, 3*time.Second, func() bool {
			_, err := os.Stat(filepath.Join(h.root, name+".ready"))
			return err == nil
		})
	}
	h.setConfiguredUPS("survivor-a", "survivor-b")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	eventually(t, time.Second, func() bool {
		_, err := os.Stat(filepath.Join(h.root, "removed-a.term"))
		return err == nil
	})
	start := time.Now()
	cancel()
	eventually(t, s.grace/2, func() bool {
		for _, name := range names[2:] {
			if _, err := os.Stat(filepath.Join(h.root, name+".term")); err != nil {
				return false
			}
		}
		return true
	})
	select {
	case <-done:
	case <-time.After(3 * s.grace):
		t.Fatal("shutdown did not join workers")
	}
	if elapsed := time.Since(start); elapsed > s.grace*3/2 {
		t.Fatalf("cancellation spent serial removal and shutdown grace periods: %s", elapsed)
	}
	for _, p := range processes {
		select {
		case <-p.done:
		default:
			t.Error("shutdown lost ownership of an unjoined worker")
		}
	}
}

func TestRuntimeNUTRequestedRestartIsIsolated(t *testing.T) {
	h, s := runtimeFixture(t)
	h.setConfiguredUPS("changed", "good")
	s.reconcile(context.Background())
	changed, good := s.workers["changed"].process, s.workers["good"].process
	_ = childPID(t, h, "changed")
	_ = childPID(t, h, "good")
	originalCommand := s.command
	s.command = func(name string, args ...string) *exec.Cmd {
		if name == "upsdrvctl" && args[0] == "-c" {
			if strings.Join(args, " ") != "-c reload-or-exit "+args[2] {
				t.Fatalf("unexpected NUT control arguments: %v", args)
			}
			if args[2] == "changed" {
				return exec.Command("sh", "-c", `kill -TERM "$1"`, "fake-nut", fmt.Sprint(changed.cmd.Process.Pid))
			}
			return exec.Command("true")
		}
		return originalCommand(name, args...)
	}
	atomicWriteFile(t, h.upsConf, []byte("[changed]\n driver = dummy-ups\n port = new\n[good]\n driver = dummy-ups\n"), 0o600)
	s.reconcile(context.Background())
	select {
	case <-changed.done:
	case <-time.After(time.Second):
		t.Fatal("NUT-requested exit was not collected")
	}
	s.reconcile(context.Background())
	if s.workers["changed"] != nil {
		t.Fatal("NUT-requested exit skipped restart interval")
	}
	time.Sleep(2 * s.opts.Interval)
	s.reconcile(context.Background())
	if s.workers["changed"] == nil || s.workers["changed"].process == changed || s.workers["good"].process != good {
		t.Fatal("NUT-requested restart disturbed the unaffected worker or failed to restart")
	}
	select {
	case <-good.done:
		t.Fatal("unaffected worker exited")
	default:
	}
}
