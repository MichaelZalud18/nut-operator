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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// This file is the `F-97` "probes in the first minutes after pod start" half of `docs/tasks.md`
// (NUT Server / upsd). The driver-supervisor's own crash isolation and recovery is covered by
// nutserver_driver_supervisor_component_test.go; this file covers the separate mechanism that
// actually decides whether Kubernetes routes traffic to the pod -- upsdReadinessProbeScript, which
// the kubelet execs directly against the upsd container on its own timer.
//
// TestUpsdReadinessProbeScriptClassifiesRealisticStatusOutput is the same gap this codebase has
// closed twice already for the driver-supervisor script: every existing test for this probe
// (nutserver_render_test.go, nutserver_controller_test.go) checks the rendered text for a
// substring. None of them have ever run it. The script's own comment warns about two specific
// substring traps -- a version banner line and the NOT_RESPONSIVE/RESPONSIVE overlap -- and until
// now nothing had actually fed it realistic upsdrvctl status output to confirm it avoids either.
//
// TestReadinessDoesNotFlapUnderTransientProbeFailures and
// TestReadinessFlipsNotReadyOnlyAfterSustainedFailure are the part of F-97 a component test can
// honestly answer. The still-open question -- why a driver upsd is still talking to fails a fresh
// upsdrvctl status connection -- is real driver/socket behavior no fixture can manufacture; that is
// Real-resource territory per the task's own testability note. What a fixture can prove is what the
// readiness gate actually does once that happens: whether an isolated miss (which is what most of
// the historical restarts were -- eight of ten invisible to upsd, per the 2026-08-24 correction in
// operator-maturity-benchmarks.md) costs the pod its readiness under the FailureThreshold this
// operator actually renders, or whether only a sustained run of misses does. This models the
// kubelet's own exec-probe counting rule rather than a running kubelet -- envtest or Kind is the
// tier that proves a real kubelet enforces it this way, and the task's Testability line already
// draws that boundary.

// readinessProbeHarness runs the real upsdReadinessProbeScript() under sh -c against a fake
// `upsdrvctl` whose `status` output this test controls, so a "driver" here is a real process
// producing real stdout through a real pipeline into a real awk, not a Go-level stand-in for one.
type readinessProbeHarness struct {
	t       *testing.T
	binDir  string
	outFile string
	rcFile  string
}

func newReadinessProbeHarness(t *testing.T) *readinessProbeHarness {
	t.Helper()
	dir := t.TempDir()
	h := &readinessProbeHarness{
		t:       t,
		binDir:  filepath.Join(dir, "bin"),
		outFile: filepath.Join(dir, "status.out"),
		rcFile:  filepath.Join(dir, "status.rc"),
	}
	if err := os.MkdirAll(h.binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	// upsdReadinessProbeScript only ever calls `upsdrvctl status`, so the fake needs only that one
	// subcommand -- unlike the fuller fake in nutserver_driver_supervisor_component_test.go, which
	// also has to answer `list`, `-FF start`, and `stop` for the supervisor loop it drives.
	fake := `#!/bin/sh
if [ "$1" = "status" ]; then
  cat "` + h.outFile + `" 2>/dev/null
  rc="$(cat "` + h.rcFile + `" 2>/dev/null || echo 0)"
  exit "$rc"
fi
echo "fake upsdrvctl: unhandled args: $*" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(h.binDir, "upsdrvctl"), []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake upsdrvctl: %v", err)
	}
	// Ready before the first setStatusOutput call, matching a driver socket that does not exist
	// yet: no output, exit 0. exit 0 rather than nonzero because a probe's own pipeline exit status
	// here comes from awk, not from upsdrvctl -- see the package doc comment above.
	h.setStatusOutput("", 0)
	return h
}

// setStatusOutput changes what the next probe invocation's `upsdrvctl status` reports, letting a
// test walk a driver through startup, a run of transient misses, or a sustained outage across a
// sequence of run() calls the way real polls would see it change over time.
func (h *readinessProbeHarness) setStatusOutput(output string, exitCode int) {
	h.t.Helper()
	if err := os.WriteFile(h.outFile, []byte(output), 0o644); err != nil {
		h.t.Fatalf("set status output: %v", err)
	}
	if err := os.WriteFile(h.rcFile, []byte(strconv.Itoa(exitCode)), 0o644); err != nil {
		h.t.Fatalf("set status exit code: %v", err)
	}
}

// run executes the real, unmodified upsdReadinessProbeScript() -- no path substitution is needed
// here, unlike the driver-supervisor script, because this probe never touches a hardcoded
// filesystem path; it only ever shells out to `upsdrvctl status` by name -- and reports whether the
// kubelet would have scored this single poll a Success.
func (h *readinessProbeHarness) run() bool {
	h.t.Helper()
	cmd := exec.Command("sh", "-c", upsdReadinessProbeScript())
	cmd.Env = append(os.Environ(), "PATH="+h.binDir+":"+os.Getenv("PATH"))
	return cmd.Run() == nil
}

// A realistic `upsdrvctl status` transcript: a banner line, a header row, then one TAB-separated
// row per device. Built from the exact shape driver_recovery_test.go greps for on a real cluster,
// not invented for this test.
const upsdrvctlStatusBanner = "Network UPS Tools upsdrvctl - UPS driver controller 2.8.5 release\n"
const upsdrvctlStatusHeader = "UPSNAME\tUPSDRV\tRUNNING\tPF_PID\tS_RESPONSIVE\tS_PID\tS_STATUS\n"

func upsdrvctlStatusRow(name string, responsive bool) string {
	if responsive {
		return name + "\tdummy-ups\tRUNNING\t123\tRESPONSIVE\t123\t\"OL\"\n"
	}
	return name + "\tdummy-ups\tN/A\t123\tNOT_RESPONSIVE\tN/A\t\"OL\"\n"
}

func TestUpsdReadinessProbeScriptClassifiesRealisticStatusOutput(t *testing.T) {
	cases := []struct {
		name      string
		output    string
		exitCode  int
		wantReady bool
	}{
		{
			name:      "single device responsive",
			output:    upsdrvctlStatusBanner + upsdrvctlStatusHeader + upsdrvctlStatusRow("a", true),
			wantReady: true,
		},
		{
			name:      "single device not responsive",
			output:    upsdrvctlStatusBanner + upsdrvctlStatusHeader + upsdrvctlStatusRow("a", false),
			wantReady: false,
		},
		{
			name: "one responsive device among several is enough",
			output: upsdrvctlStatusBanner + upsdrvctlStatusHeader +
				upsdrvctlStatusRow("a", false) + upsdrvctlStatusRow("b", false) + upsdrvctlStatusRow("c", true),
			wantReady: true,
		},
		{
			name:      "every configured device not responsive",
			output:    upsdrvctlStatusBanner + upsdrvctlStatusHeader + upsdrvctlStatusRow("a", false) + upsdrvctlStatusRow("b", false),
			wantReady: false,
		},
		{
			name: "header and banner alone, no device rows, must not read as responsive",
			// Regression for the exact trap upsdReadinessProbeScript's own doc comment names: the
			// header row's own token is S_RESPONSIVE, and matching on absence of RESPONSIVE would
			// read the banner's "controller" line as a device. This is the first time either has
			// actually been fed to the script rather than asserted about it in prose.
			output:    upsdrvctlStatusBanner + upsdrvctlStatusHeader,
			wantReady: false,
		},
		{
			name:      "no ups.conf devices at all -- upsdrvctl prints nothing",
			output:    "",
			wantReady: false,
		},
		{
			name: "upsdrvctl status itself exits nonzero -- driver socket does not exist yet",
			// The pipeline's exit status comes from awk, not from upsdrvctl (see run()'s doc
			// comment), so this must still correctly report not-ready from empty input rather than
			// from upsdrvctl's own exit code, which the script never inspects.
			output:    "",
			exitCode:  1,
			wantReady: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newReadinessProbeHarness(t)
			h.setStatusOutput(tc.output, tc.exitCode)
			if got := h.run(); got != tc.wantReady {
				t.Fatalf("expected ready=%v for output:\n%s\ngot ready=%v", tc.wantReady, tc.output, got)
			}
		})
	}
}

// simulateKubeletReadiness replays a sequence of single-poll results the way the kubelet's exec
// probe consumes them for a readiness probe: any Success marks the container Ready immediately, and
// it takes upsdReadinessFailureThreshold consecutive Failures in a row to mark it NotReady again. A
// Failure that does not extend an existing run of failures to the threshold changes nothing.
//
// This is the counting rule, not a running kubelet -- see the package doc comment for why that
// split is honest and where the boundary the task's own Testability note draws sits.
func simulateKubeletReadiness(pollResults []bool) (finalReady bool, history []bool) {
	ready := false
	consecutiveFailures := 0
	for _, ok := range pollResults {
		if ok {
			ready = true
			consecutiveFailures = 0
		} else {
			consecutiveFailures++
			if consecutiveFailures >= upsdReadinessFailureThreshold {
				ready = false
			}
		}
		history = append(history, ready)
	}
	return ready, history
}

// pollSequence drives the real probe script through a scripted sequence of driver states -- each
// entry either "responsive", "not-responsive", or "no-driver" (nothing listening yet) -- and
// returns the real per-poll Success/Failure sequence the script actually produced, for
// simulateKubeletReadiness to score.
func pollSequence(t *testing.T, states []string) []bool {
	t.Helper()
	h := newReadinessProbeHarness(t)
	results := make([]bool, len(states))
	for i, state := range states {
		switch state {
		case "responsive":
			h.setStatusOutput(upsdrvctlStatusBanner+upsdrvctlStatusHeader+upsdrvctlStatusRow("a", true), 0)
		case "not-responsive":
			h.setStatusOutput(upsdrvctlStatusBanner+upsdrvctlStatusHeader+upsdrvctlStatusRow("a", false), 0)
		case "no-driver":
			h.setStatusOutput("", 0)
		default:
			t.Fatalf("unknown simulated driver state %q", state)
		}
		results[i] = h.run()
	}
	return results
}

// TestReadinessToleratesADelayedDriverStart is the "delay readiness" scenario F-97 names: a
// driver-supervisor worker that has not bound its control socket yet answers every poll as
// no-driver, and the pod must correctly stay NotReady until it does -- not flap, and not report
// Ready on a guess.
func TestReadinessToleratesADelayedDriverStart(t *testing.T) {
	results := pollSequence(t, []string{"no-driver", "no-driver", "no-driver", "responsive", "responsive"})
	ready, history := simulateKubeletReadiness(results)

	for i := 0; i < 3; i++ {
		if history[i] {
			t.Fatalf("poll %d: expected NotReady while the driver has not started, got Ready (history=%v)", i, history)
		}
	}
	if !ready || !history[3] || !history[4] {
		t.Fatalf("expected Ready as soon as the driver responds, got history=%v", history)
	}
}

// TestReadinessDoesNotFlapUnderTransientProbeFailures reproduces the pattern the 2026-08-24
// correction actually measured: isolated single-poll misses that never run three in a row. Under
// the FailureThreshold this operator renders, that pattern must never cost the pod its readiness --
// which is the honest, testable half of "keep an existing upsd session alive while rejecting new
// probes": whether an isolated rejection matters to the readiness gate, even though why the
// rejection happens is not something this fixture can manufacture.
func TestReadinessDoesNotFlapUnderTransientProbeFailures(t *testing.T) {
	results := pollSequence(t, []string{
		"responsive", "responsive", "not-responsive", "responsive",
		"responsive", "not-responsive", "responsive", "responsive",
	})
	ready, history := simulateKubeletReadiness(results)

	if !ready {
		t.Fatalf("expected the pod to remain Ready throughout isolated single-poll misses, got history=%v", history)
	}
	for i, wasReady := range history {
		if !wasReady {
			t.Fatalf("poll %d flipped NotReady on an isolated miss that never reached "+
				"FailureThreshold=%d consecutive failures; history=%v", i, upsdReadinessFailureThreshold, history)
		}
	}
}

// TestReadinessFlipsNotReadyOnlyAfterSustainedFailure is the other half: a driver that stops
// answering for FailureThreshold consecutive polls must actually cost the pod its readiness, not
// merely log a line -- an operator whose whole purpose is acting on live UPS telemetry cannot route
// traffic to a NUT server whose driver has been down for three consecutive checks and call it Ready.
func TestReadinessFlipsNotReadyOnlyAfterSustainedFailure(t *testing.T) {
	// One initial success, then enough consecutive failures to cross the threshold with one to
	// spare -- so the assertions below distinguish "not yet at threshold" from "at or past it"
	// instead of relying on a single boundary sample.
	results := pollSequence(t, []string{"responsive", "not-responsive", "not-responsive", "not-responsive", "not-responsive"})
	ready, history := simulateKubeletReadiness(results)

	// Polls 1 and 2 (indices 1, 2) are the first and second consecutive failures -- below
	// FailureThreshold=3, so readiness must still hold.
	for i := 1; i < upsdReadinessFailureThreshold; i++ {
		if !history[i] {
			t.Fatalf("poll %d flipped NotReady before reaching FailureThreshold=%d consecutive "+
				"failures; history=%v", i, upsdReadinessFailureThreshold, history)
		}
	}
	// Poll at index upsdReadinessFailureThreshold is the threshold-th consecutive failure, and
	// every poll from there on must stay NotReady.
	for i := upsdReadinessFailureThreshold; i < len(history); i++ {
		if history[i] {
			t.Fatalf("poll %d should be NotReady at or past FailureThreshold=%d consecutive "+
				"failures; history=%v", i, upsdReadinessFailureThreshold, history)
		}
	}
	if ready {
		t.Fatalf("expected the pod to remain NotReady while failures continue, got Ready")
	}
}
