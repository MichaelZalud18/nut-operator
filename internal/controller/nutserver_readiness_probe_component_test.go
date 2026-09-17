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

// These component tests exercise the rendered shell invocation and model kubelet readiness
// transitions. Driver socket semantics belong to the Go checker and real-image tests.
type readinessProbeHarness struct {
	t      *testing.T
	binDir string
	rcFile string
}

func newReadinessProbeHarness(t *testing.T) *readinessProbeHarness {
	t.Helper()
	dir := t.TempDir()
	h := &readinessProbeHarness{t: t, binDir: dir, rcFile: filepath.Join(dir, "ready.rc")}
	fake := `#!/bin/sh
[ "$#" -eq 0 ] || exit 64
printf 'diagnostic output does not determine readiness\n'
exit "$(cat "` + h.rcFile + `")"
`
	if err := os.WriteFile(filepath.Join(h.binDir, "nut-driver-ready"), []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake nut-driver-ready: %v", err)
	}
	h.setExitCode(1)
	return h
}

func (h *readinessProbeHarness) setExitCode(exitCode int) {
	h.t.Helper()
	if err := os.WriteFile(h.rcFile, []byte(strconv.Itoa(exitCode)), 0o644); err != nil {
		h.t.Fatalf("set checker exit code: %v", err)
	}
}

func (h *readinessProbeHarness) runExitCode() int {
	h.t.Helper()
	cmd := exec.Command("sh", "-c", upsdReadinessProbeScript())
	cmd.Env = append(os.Environ(), "PATH="+h.binDir+":"+os.Getenv("PATH"))
	err := cmd.Run()
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	h.t.Fatalf("run readiness probe: %v", err)
	return -1
}

func TestUpsdReadinessProbePropagatesCheckerExitCode(t *testing.T) {
	for _, exitCode := range []int{0, 1, 2, 42, 124} {
		t.Run(strconv.Itoa(exitCode), func(t *testing.T) {
			h := newReadinessProbeHarness(t)
			h.setExitCode(exitCode)
			if got := h.runExitCode(); got != exitCode {
				t.Fatalf("probe exit code = %d, want checker exit code %d", got, exitCode)
			}
		})
	}
}

func TestUpsdReadinessProbeFailsWhenCheckerIsMissing(t *testing.T) {
	h := newReadinessProbeHarness(t)
	if err := os.Remove(filepath.Join(h.binDir, "nut-driver-ready")); err != nil {
		t.Fatal(err)
	}
	// Resolve sh first, then isolate PATH so a host-installed checker cannot satisfy the probe.
	cmd := exec.Command("sh", "-c", upsdReadinessProbeScript())
	cmd.Env = append(os.Environ(), "PATH="+h.binDir)
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 127 {
		t.Fatalf("missing checker should exit 127, got %v", err)
	}
}

// simulateKubeletReadiness replays a sequence of single-poll results the way the kubelet's exec
// probe consumes them for a readiness probe: any Success marks the container Ready immediately, and
// it takes upsdReadinessFailureThreshold consecutive Failures in a row to mark it NotReady again. A
// Failure that does not extend an existing run of failures to the threshold changes nothing.
//
// This models the counting rule; it does not run a kubelet or establish socket behavior.
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
			h.setExitCode(0)
		case "not-responsive":
			h.setExitCode(1)
		case "no-driver":
			h.setExitCode(1)
		default:
			t.Fatalf("unknown simulated driver state %q", state)
		}
		results[i] = h.runExitCode() == 0
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
