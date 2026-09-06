/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// This file exercises driverSupervisorScript() as a running process tree rather than as text.
//
// Every existing test for this script (nutserver_watchdog_test.go, nutserver_reload_test.go) checks
// that a substring is present in the rendered script. None of them run it, so the property the
// per-device split exists to prove -- that one driver failing cannot take healthy ones with it -- has
// never actually been observed happening. That property, reload convergence, and the bound on
// restart frequency are what this file adds (component-test item, docs/tasks.md, NUT Server /
// upsd, [High]).
//
// The script hardcodes /etc/nut and /run/nut/driver-supervisor, which a non-root test cannot write
// to. Rather than adding a testability seam to the production script, this substitutes those two
// literal paths for a temp directory before executing it -- the same approach
// nodepoweragent_probes_test.go already uses for the upsmon probe script. Production behavior is
// unchanged because the production script is never modified; only this test's private copy of the
// string is.
//
// `upsdrvctl` and `upsd` are replaced on PATH with fake implementations whose behavior per UPS name
// is read from a fixture directory this harness controls, so a "driver" here is a real OS process
// with a real PID, started and reaped by the real script logic, just not real NUT code.

// driverSupervisorHarness runs one instance of driverSupervisorScript() against a fixture directory
// this test controls, and gives each test a way to change what a named UPS's fake driver does while
// the supervisor is running.
type driverSupervisorHarness struct {
	t *testing.T

	root      string // temp dir root
	etcDir    string // stands in for /etc/nut
	stateDir  string // stands in for /run/nut/driver-supervisor
	behaveDir string // per-UPS behavior files the fake upsdrvctl reads
	binDir    string // fake upsdrvctl / upsd
	upsConf   string // etcDir/ups.conf
	upsdUsers string // etcDir/upsd.users

	cmd    *exec.Cmd
	stdout *strings.Builder
	mu     sync.Mutex // guards stdout, which the child writes to concurrently with reads below
}

// newDriverSupervisorHarness lays out the fixture tree and fake binaries, but does not start the
// supervisor -- tests configure the initial UPS set first with setConfiguredUPS.
func newDriverSupervisorHarness(t *testing.T) *driverSupervisorHarness {
	t.Helper()
	root := t.TempDir()
	h := &driverSupervisorHarness{
		t:         t,
		root:      root,
		etcDir:    filepath.Join(root, "etc-nut"),
		stateDir:  filepath.Join(root, "run-driver-supervisor"),
		behaveDir: filepath.Join(root, "behavior"),
		binDir:    filepath.Join(root, "bin"),
	}
	h.upsConf = filepath.Join(h.etcDir, "ups.conf")
	h.upsdUsers = filepath.Join(h.etcDir, "upsd.users")

	for _, dir := range []string{h.etcDir, h.stateDir, h.behaveDir, h.binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(h.upsdUsers, []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("seed upsd.users: %v", err)
	}
	h.writeFakeBinaries()
	return h
}

// writeFakeBinaries installs upsdrvctl and upsd on a PATH-only bin dir.
//
// upsdrvctl's three subcommands are given exactly the behavior the supervisor script depends on:
// `list` reports the configured set from a file this harness writes, and fails with the literal
// "no UPS definitions found" text when told to, because configuredDrivers() in the real script
// greps for that exact phrase to tell an empty config apart from a broken one. `-FF start <ups>`
// reads a per-UPS behavior file and either blocks until killed (a healthy driver) or exits with a
// chosen code after a chosen delay (a crashing one). `stop` is a no-op that always succeeds, matching
// upsdrvctl's own best-effort cleanup call in stopDriver.
func (h *driverSupervisorHarness) writeFakeBinaries() {
	h.t.Helper()
	upsdrvctl := `#!/bin/sh
behave_dir="` + h.behaveDir + `"
case "$1" in
  list)
    configured_file="$behave_dir/.configured"
    if [ -f "$behave_dir/.list_fail" ]; then
      echo "simulated upsdrvctl list failure" >&2
      exit 1
    fi
    if [ ! -s "$configured_file" ]; then
      echo "Network UPS Tools - UPS driver controller 2.8.5" >&2
      echo "no UPS definitions found in ups.conf" >&2
      exit 1
    fi
    cat "$configured_file"
    exit 0
    ;;
  -FF)
    ups="$3"
    behavior_file="$behave_dir/$ups.behavior"
    behavior="run"
    if [ -f "$behavior_file" ]; then
      behavior="$(cat "$behavior_file")"
    fi
    case "$behavior" in
      run)
        trap 'exit 0' TERM INT
        while :; do sleep 3600 & wait $!; done
        ;;
      crash)
        exit 7
        ;;
      crash-after:*)
        delay="${behavior#crash-after:}"
        trap 'exit 0' TERM INT
        sleep "$delay"
        exit 7
        ;;
      *)
        echo "fake upsdrvctl: unknown behavior '$behavior' for $ups" >&2
        exit 2
        ;;
    esac
    ;;
  stop)
    exit 0
    ;;
  *)
    echo "fake upsdrvctl: unhandled args: $*" >&2
    exit 2
    ;;
esac
`
	upsd := `#!/bin/sh
# Only "-c reload" is exercised by driverSupervisorScript. A marker file lets tests assert it was
# actually invoked, and a present .reload_fail file lets a test make it fail on purpose.
if [ "$1" = "-c" ] && [ "$2" = "reload" ]; then
  echo "reload $(date +%s%N)" >> "` + filepath.Join(h.root, "upsd-reload.log") + `"
  if [ -f "` + filepath.Join(h.root, "upsd-reload-fail") + `" ]; then
    exit 1
  fi
  exit 0
fi
echo "fake upsd: unhandled args: $*" >&2
exit 2
`
	h.mustWriteExecutable(filepath.Join(h.binDir, "upsdrvctl"), upsdrvctl)
	h.mustWriteExecutable(filepath.Join(h.binDir, "upsd"), upsd)
}

func (h *driverSupervisorHarness) mustWriteExecutable(path, content string) {
	h.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		h.t.Fatalf("write %s: %v", path, err)
	}
}

// atomicWriteFile writes via a temp file plus rename rather than a direct os.WriteFile, so a
// supervisor process reading the target concurrently never observes it truncated-and-empty
// mid-write. This is required for any file the live supervisor reads while a test is still
// running (.configured, ups.conf, upsd.users); mustWriteExecutable above is fine as a direct
// write because it only ever runs before start(), before any reader exists (F-143).
func atomicWriteFile(t *testing.T, path string, content []byte, perm os.FileMode) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		t.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename %s to %s: %v", tmp, path, err)
	}
}

// setConfiguredUPS declares the set upsdrvctl list should report, and bumps ups.conf so the
// script's own driver-config digest changes -- the same signal a real UPSDevice add/remove produces.
// A test that wants a server-only reload (upsd.users changed, drivers untouched) should call
// touchServerConfig instead.
func (h *driverSupervisorHarness) setConfiguredUPS(names ...string) {
	h.t.Helper()
	// F-143: both files below are read live by the running supervisor (configuredDrivers's
	// upsdrvctl list, and deviceConfigDigest's awk over ups.conf), on its own 1s-interval
	// reconcile loop that this test does not otherwise synchronize with. A plain os.WriteFile
	// truncates before writing, so a reconcile pass unlucky enough to land in that window can
	// read the file empty -- observed as the fixture-race described in
	// fresh-review-2026-09-04.md's F-143, reproduced under -race at cycle 13 of the repeated
	// add/remove test. atomicWriteFile's rename makes every read see either the fully-old or
	// fully-new content, never a truncated one.
	configuredFile := filepath.Join(h.behaveDir, ".configured")
	content := strings.Join(names, "\n")
	if len(names) > 0 {
		content += "\n"
	}
	atomicWriteFile(h.t, configuredFile, []byte(content), 0o644)
	// Real [name] sections, in the shape renderUPSConf actually writes -- deviceConfigDigest reads
	// this file expecting exactly that, and a synthetic marker format would make every device's
	// per-device digest identical regardless of content, defeating the F-124 change detection this
	// harness exists to exercise (TestDriverSupervisorRestartsADriverWhoseOwnConfigurationChanges).
	var conf strings.Builder
	for _, name := range names {
		fmt.Fprintf(&conf, "[%s]\n  driver = dummy-ups\n", name)
	}
	atomicWriteFile(h.t, h.upsConf, []byte(conf.String()), 0o644)
}

// touchServerConfig changes only upsd.users, so the driver digest is untouched and the supervisor
// should reload upsd without restarting any driver. Atomic for the same reason as
// setConfiguredUPS above: the supervisor reads upsd.users live while this test is running.
func (h *driverSupervisorHarness) touchServerConfig(marker string) {
	h.t.Helper()
	atomicWriteFile(h.t, h.upsdUsers, []byte(marker+"\n"), 0o644)
}

func (h *driverSupervisorHarness) setBehavior(ups, behavior string) {
	h.t.Helper()
	path := filepath.Join(h.behaveDir, ups+".behavior")
	if err := os.WriteFile(path, []byte(behavior), 0o644); err != nil {
		h.t.Fatalf("set behavior for %s: %v", ups, err)
	}
}

func (h *driverSupervisorHarness) failListing(fail bool) {
	h.t.Helper()
	marker := filepath.Join(h.behaveDir, ".list_fail")
	if !fail {
		_ = os.Remove(marker)
		return
	}
	if err := os.WriteFile(marker, []byte("1"), 0o644); err != nil {
		h.t.Fatalf("mark list failing: %v", err)
	}
}

// script returns driverSupervisorScript() with its hardcoded paths pointed at this harness's temp
// directory, and its reconcile interval shortened so a test does not have to wait multiples of a
// real 5s just to observe a second pass. Both substitutions are exact-string, so if the production
// script is ever edited to spell either literal differently, this fails to match rather than
// silently testing nothing -- verified below in TestFakeUpsdrvctlPathSubstitutionMatchesTheRealScript.
func (h *driverSupervisorHarness) script(intervalSeconds int) string {
	h.t.Helper()
	script := driverSupervisorScript()
	replacements := []struct{ from, to string }{
		{"state_dir=/run/nut/driver-supervisor", "state_dir=" + h.stateDir},
		{"/etc/nut/ups.conf", h.upsConf},
		{"/etc/nut/upsd.users", h.upsdUsers},
		{"\n  sleep 5\n", fmt.Sprintf("\n  sleep %d\n", intervalSeconds)},
	}
	for _, r := range replacements {
		if !strings.Contains(script, r.from) {
			h.t.Fatalf("path substitution target %q not found in driverSupervisorScript(); "+
				"the production script changed shape and this harness needs updating", r.from)
		}
		script = strings.ReplaceAll(script, r.from, r.to)
	}
	return script
}

// start launches the supervisor with a 1s reconcile interval, fast enough to keep these tests in
// the sub-minute range a unit-test suite needs to stay in, and stops it automatically at test
// cleanup by sending the same TERM the script's own trap handles.
func (h *driverSupervisorHarness) start() {
	h.t.Helper()
	h.startWithInterval(1)
}

func (h *driverSupervisorHarness) startWithInterval(intervalSeconds int) {
	h.t.Helper()
	cmd := exec.Command("sh", "-c", h.script(intervalSeconds))
	cmd.Env = append(os.Environ(), "PATH="+h.binDir+":"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	h.stdout = &strings.Builder{}
	cmd.Stdout = &syncWriter{mu: &h.mu, w: h.stdout}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("start supervisor: %v", err)
	}
	h.cmd = cmd
	h.t.Cleanup(h.stop)
}

// stop signals the whole process group, not just the shell PID: the supervisor backgrounds a
// subshell per driver, and a plain kill on cmd.Process would leave orphaned fake upsdrvctl
// processes running past the test.
func (h *driverSupervisorHarness) stop() {
	h.t.Helper()
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func (h *driverSupervisorHarness) log() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stdout.String()
}

// pid reads the pid file the real startDriver writes, in the substituted state dir. It returns
// false rather than failing the test when the file is absent, because "not started yet" is a normal
// intermediate state every polling helper below has to tolerate.
func (h *driverSupervisorHarness) pid(ups string) (int, bool) {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.stateDir, ups+".pid"))
	if err != nil {
		return 0, false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil {
		return 0, false
	}
	return pid, true
}

// alive checks a real PID with signal 0, which delivers nothing and only asks whether the process
// exists -- the same check driverRunning performs inside the script itself.
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// eventually polls a condition rather than sleeping a guessed duration, because every property this
// file checks is about a background loop converging, not about a fixed clock.
func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition was not met within %s", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// syncWriter serializes writes from the child process's own goroutine-driven stdout/stderr copying
// against reads from the test goroutine, which strings.Builder does not do on its own.
type syncWriter struct {
	mu *sync.Mutex
	w  *strings.Builder
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// TestFakeUpsdrvctlPathSubstitutionMatchesTheRealScript fails loudly, on its own, if the production
// script is ever edited so the literal strings this harness substitutes no longer appear -- rather
// than every other test in this file quietly exercising an empty no-op script because none of its
// path rewrites landed. script() already asserts this per call; this test exists so the failure has
// one clear name instead of surfacing as an unrelated timeout somewhere else in the suite.
func TestFakeUpsdrvctlPathSubstitutionMatchesTheRealScript(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	script := h.script(1)
	if strings.Contains(script, "/etc/nut") || strings.Contains(script, "/run/nut/driver-supervisor") {
		t.Fatalf("substituted script still references a real system path:\n%s", script)
	}
}

// TestDriverSupervisorIsolatesACrashingDriverFromHealthyOnes is the property the per-device split
// exists for (see driverSupervisorScript's own doc comment): a bundled `upsdrvctl -FF start` for
// every device took every driver down when one failed. This runs a healthy and a permanently
// crashing driver side by side and asserts the healthy one's process is never disturbed while the
// broken one is repeatedly restarted -- the first time this has been observed happening rather than
// inferred from the script's shape.
func TestDriverSupervisorIsolatesACrashingDriverFromHealthyOnes(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("healthy", "run")
	h.setBehavior("broken", "crash")
	h.setConfiguredUPS("healthy", "broken")
	h.start()

	var healthyPID int
	eventually(t, 5*time.Second, func() bool {
		pid, ok := h.pid("healthy")
		if !ok || !alive(pid) {
			return false
		}
		healthyPID = pid
		return true
	})

	// Long enough, at a 1s interval, for several restart attempts of "broken" -- observed directly
	// below rather than assumed from the wait alone.
	restarts := 0
	deadline := time.Now().Add(4 * time.Second)
	lastBrokenPID := 0
	for time.Now().Before(deadline) {
		if pid, ok := h.pid("broken"); ok && pid != lastBrokenPID {
			restarts++
			lastBrokenPID = pid
		}
		if p, ok := h.pid("healthy"); !ok || p != healthyPID {
			t.Fatalf("healthy driver's PID changed (was %d, now pid=%d ok=%v) while an unrelated "+
				"driver was crashing -- the per-device split is not isolating failures", healthyPID, p, ok)
		}
		if !alive(healthyPID) {
			t.Fatalf("healthy driver process %d died while an unrelated driver was crashing", healthyPID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if restarts < 2 {
		t.Fatalf("expected the crashing driver to be restarted repeatedly, saw %d restart(s); "+
			"the fixture may not be exercising the crash path", restarts)
	}
}

// TestDriverSupervisorRecoversAnExitedDriver proves the other half of the same mechanism: a driver
// that fails is not just isolated, it is actually brought back, without a manual reconcile.
func TestDriverSupervisorRecoversAnExitedDriver(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("flaky", "crash-after:1")
	h.setConfiguredUPS("flaky")
	h.start()

	var firstPID int
	eventually(t, 5*time.Second, func() bool {
		pid, ok := h.pid("flaky")
		if ok {
			firstPID = pid
		}
		return ok
	})

	eventually(t, 8*time.Second, func() bool {
		pid, ok := h.pid("flaky")
		return ok && pid != firstPID && alive(pid)
	})
}

// TestDriverSupervisorServerOnlyReloadDoesNotTouchDrivers checks the half of "reloads preserve the
// intended workers" that holds unconditionally: an upsd.users-only change reloads upsd and leaves
// every running driver's PID untouched, because the driver digest it is compared against never
// changed.
func TestDriverSupervisorServerOnlyReloadDoesNotTouchDrivers(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("a", "run")
	h.setBehavior("b", "run")
	h.setConfiguredUPS("a", "b")
	h.start()

	var pidA, pidB int
	eventually(t, 5*time.Second, func() bool {
		var okA, okB bool
		pidA, okA = h.pid("a")
		pidB, okB = h.pid("b")
		return okA && okB && alive(pidA) && alive(pidB)
	})

	h.touchServerConfig("auth changed")

	eventually(t, 5*time.Second, func() bool {
		return strings.Contains(h.log(), "reloading upsd")
	})
	// A moment past the log line for the reconcile pass that follows it to have actually run and
	// had the chance to disturb something, not merely to have printed the line.
	time.Sleep(300 * time.Millisecond)

	if got, ok := h.pid("a"); !ok || got != pidA {
		t.Fatalf("driver a was restarted by a server-only (upsd.users) reload: pid was %d, now %d (ok=%v)",
			pidA, got, ok)
	}
	if got, ok := h.pid("b"); !ok || got != pidB {
		t.Fatalf("driver b was restarted by a server-only (upsd.users) reload: pid was %d, now %d (ok=%v)",
			pidB, got, ok)
	}
}

// TestDriverSupervisorKeepsRunningDriversWhenListingFails is the behavioral form of the existing
// text-only assertion (nutserver_watchdog_test.go) that a temporarily unreadable ups.conf must not
// stop currently running workers. This proves it by actually breaking `upsdrvctl list` mid-run and
// watching a live driver survive the failure.
func TestDriverSupervisorKeepsRunningDriversWhenListingFails(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("a", "run")
	h.setConfiguredUPS("a")
	h.start()

	var pidA int
	eventually(t, 5*time.Second, func() bool {
		var ok bool
		pidA, ok = h.pid("a")
		return ok && alive(pidA)
	})

	h.failListing(true)
	// Several reconcile passes at the 1s test interval, so a supervisor that panics on the first
	// failure and one that only mishandles a later pass are both caught.
	time.Sleep(2500 * time.Millisecond)
	h.failListing(false)

	if got, ok := h.pid("a"); !ok || got != pidA || !alive(got) {
		t.Fatalf("driver a did not survive a failing `upsdrvctl list`: pid was %d, now %d (ok=%v)",
			pidA, got, ok)
	}
	if !strings.Contains(h.log(), "keeping existing workers") {
		t.Fatalf("expected the supervisor to log that it is keeping existing workers, got:\n%s", h.log())
	}
}

// TestDriverSupervisorRestartCadenceIsBoundedByTheReconcileInterval is the investigation the task
// description's "backoff stays bounded" sent this to: there is no backoff mechanism anywhere in
// driverSupervisorScript or the node-agent-operand design docs, no attempt counter, no growing
// delay. What actually exists is a fixed reconcile interval, and restart attempts for a
// permanently-crashing driver are bound to at most one per interval by construction -- startDriver
// backgrounds the run-and-record-exit step, but the *next* attempt only happens on the next
// reconcileDrivers call, which only happens once per sleep.
//
// That fixed bound is very likely the intended design and not a gap to be closed with a growing
// delay: this is a subprocess restart inside an already-running sidecar, not a Kubernetes container
// restart, so it costs one fork/exec of a small binary per interval and does not touch the
// container's restart count or trigger CrashLoopBackOff -- the exact visibility the per-device split
// was built to avoid pushing back up to Kubernetes. And unlike a container backoff, growing the
// delay here would directly fight the module's own purpose: UPS telemetry needs to recover fast
// after a transient fault during a power event, not slower each time one occurs. So this test
// verifies the bound that exists (one attempt per interval, not a tight spin) rather than a growing
// one, and says why in this comment rather than silently agreeing with the task's wording.
func TestDriverSupervisorRestartCadenceIsBoundedByTheReconcileInterval(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("broken", "crash")
	h.setConfiguredUPS("broken")
	const intervalSeconds = 1
	h.startWithInterval(intervalSeconds)

	window := 5 * time.Second
	restarts := 0
	last := 0
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if pid, ok := h.pid("broken"); ok && pid != last {
			restarts++
			last = pid
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Ceiling, not a tight equality: process scheduling jitter can shift a restart across an
	// interval boundary by a beat in either direction. What this must catch is a supervisor that
	// lost the sleep gate entirely and respawns as fast as fork/exec allows, which would produce
	// restart counts an order of magnitude higher than the interval permits.
	maxPlausible := int(window/(intervalSeconds*time.Second)) + 2
	if restarts > maxPlausible {
		t.Fatalf("crashing driver restarted %d times in %s at a %ds interval, which exceeds the "+
			"expected fixed-interval bound of %d -- the reconcile loop's sleep gate is not "+
			"actually limiting restart frequency", restarts, window, intervalSeconds, maxPlausible)
	}
	if restarts < 2 {
		t.Fatalf("expected multiple restart attempts within %s at a %ds interval, saw %d",
			window, intervalSeconds, restarts)
	}
}

// TestF124UnrelatedDriverKeepsItsPIDOnUPSConfChange is F-124's acceptance criterion
// (docs/contributing/audits/nutserver-pod-audit.md, 2026-09-03 pass), un-skipped now that
// reconcileDrivers restarts a driver only when its own ups.conf section actually changed rather than
// stopping every tracked driver on any config edit. This replaces the characterization subtest that
// used to stand here proving the defect: with the fix in place there is nothing left for it to show.
func TestF124UnrelatedDriverKeepsItsPIDOnUPSConfChange(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("a", "run")
	h.setBehavior("b", "run")
	h.setBehavior("c", "run")
	h.setConfiguredUPS("a", "b")
	h.start()

	var pidA int
	eventually(t, 5*time.Second, func() bool {
		var okA, okB bool
		pidA, okA = h.pid("a")
		_, okB = h.pid("b")
		return okA && okB && alive(pidA)
	})

	// b removed, c added; a is unchanged in the declared set and never stops being configured.
	h.setConfiguredUPS("a", "c")

	eventually(t, 5*time.Second, func() bool {
		pidC, okC := h.pid("c")
		return okC && alive(pidC)
	})
	eventually(t, 5*time.Second, func() bool {
		_, ok := h.pid("b")
		return !ok
	})

	// The acceptance criterion: a, which was never removed from the configured set, is never
	// touched by an add/remove that has nothing to do with it. Held for a beat past convergence
	// rather than checked once, so a delayed restart does not read as "no restart happened."
	time.Sleep(300 * time.Millisecond)
	if got, ok := h.pid("a"); !ok || got != pidA || !alive(got) {
		t.Fatalf("driver a was restarted by an unrelated UPSDevice add/remove: pid was %d, "+
			"now %d (ok=%v)", pidA, got, ok)
	}
}

// TestDriverSupervisorRestartsADriverWhoseOwnConfigurationChanges guards the half of the F-124 fix
// that a naive "restart only added/removed names" patch would have silently broken: a device that
// stays configured under the same name but whose own section content changes (a different port, a
// different community string) must still be restarted, or it serves stale configuration forever.
// Membership (is this name still configured) and content (did this name's own section change) are
// deliberately two separate checks in reconcileDrivers, and this is what proves the second one.
func TestDriverSupervisorRestartsADriverWhoseOwnConfigurationChanges(t *testing.T) {
	h := newDriverSupervisorHarness(t)
	h.setBehavior("a", "run")
	h.setConfiguredUPS("a")
	h.start()

	var pidA int
	eventually(t, 5*time.Second, func() bool {
		var ok bool
		pidA, ok = h.pid("a")
		return ok && alive(pidA)
	})

	// Same device name, same membership, different section content -- setConfiguredUPS's own
	// ups.conf write is a deterministic function of the name list, so calling it again with the
	// same single name would not change the file. Writing directly is what actually models "the
	// same device's own options changed." Atomic for the same reason as setConfiguredUPS (F-143):
	// the supervisor is already running and reads this file live.
	atomicWriteFile(t, h.upsConf, []byte("[a]\n  driver = dummy-ups\n  port = changed\n"), 0o644)

	eventually(t, 5*time.Second, func() bool {
		pid, ok := h.pid("a")
		return ok && pid != pidA && alive(pid)
	})
}
