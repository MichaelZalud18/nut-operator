package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state.json")
}

func readyConfig(path string, policy string) actuatorConfig {
	return actuatorConfig{Policy: policy, StatePath: path, Interval: 5 * time.Second}
}

// F-64: the point of the whole change. The old probe ran `--version`, which cannot fail, so these
// are the states that used to report Ready and must not.
func TestReadinessFailsOnEveryStateTheVersionProbeUsedToPass(t *testing.T) {
	now := time.Now().UTC()

	for _, testCase := range []struct {
		name  string
		state *actuatorState
		want  string
	}{
		{
			// A process parked forever in block() under a policy that should be looping never
			// writes the file at all. This is the case the audit named.
			name:  "the watch loop never ran",
			state: nil,
			want:  "has not recorded a pass",
		},
		{
			name: "the watch loop stopped looping",
			state: &actuatorState{
				Timestamp:       now.Add(-10 * time.Minute).Format(time.RFC3339),
				Policy:          policyStub,
				IntervalSeconds: 5,
			},
			want: "last completed a pass",
		},
		{
			name: "the signal directory never appeared",
			state: &actuatorState{
				Timestamp:            now.Format(time.RFC3339),
				Policy:               policyStub,
				IntervalSeconds:      5,
				UnreadableSignalDirs: []string{"/var/lib/power-agent/signals"},
			},
			want: "signal directories unreadable",
		},
		{
			name: "the record is corrupt",
			state: &actuatorState{
				Timestamp:       "not a timestamp",
				Policy:          policyStub,
				IntervalSeconds: 5,
			},
			want: "unparseable timestamp",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := statePath(t)
			if testCase.state != nil {
				if err := writeActuatorState(path, *testCase.state); err != nil {
					t.Fatalf("write state: %v", err)
				}
			}

			err := checkActuatorReady(readyConfig(path, policyStub), now)
			if err == nil {
				t.Fatal("readiness must fail here; this is exactly what --version could not detect")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("the message must say %q, got: %v", testCase.want, err)
			}
		})
	}
}

func TestReadinessPassesWhileTheLoopIsRunning(t *testing.T) {
	path := statePath(t)
	now := time.Now().UTC()
	if err := writeActuatorState(path, actuatorState{
		Timestamp:       now.Add(-2 * time.Second).Format(time.RFC3339),
		Policy:          policyStub,
		IntervalSeconds: 5,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if err := checkActuatorReady(readyConfig(path, policyStub), now); err != nil {
		t.Fatalf("a loop that just completed a pass must be ready: %v", err)
	}
}

// A Disabled actuator's configured job is to block, so there is no loop to report on and readiness
// must not demand one. Getting this wrong would leave every Disabled agent permanently NotReady.
func TestReadinessDoesNotDemandALoopUnderTheDisabledPolicy(t *testing.T) {
	config := readyConfig(filepath.Join(t.TempDir(), "absent.json"), policyDisabled)

	if err := checkActuatorReady(config, time.Now().UTC()); err != nil {
		t.Fatalf("a Disabled actuator has no watch loop and must still be ready: %v", err)
	}
}

// The bound is derived from the loop's own interval rather than fixed, so slowing the loop down
// does not make a correctly-running actuator fail readiness for obeying its configuration.
func TestStalenessBoundFollowsTheConfiguredInterval(t *testing.T) {
	if bound := actuatorStaleAfter(60 * time.Second); bound != 3*time.Minute {
		t.Errorf("a 60s interval must allow 3m, got %s", bound)
	}
	// The floor stops a fast loop from making the check hair-triggered on scheduling latency.
	if bound := actuatorStaleAfter(time.Second); bound != 30*time.Second {
		t.Errorf("a 1s interval must still allow the 30s floor, got %s", bound)
	}
}

// The probe reads this file while the loop rewrites it. A partially written record would fail the
// timestamp parse and report NotReady on a perfectly healthy actuator, so the write is atomic.
func TestStateIsReplacedAtomically(t *testing.T) {
	path := statePath(t)
	if err := writeActuatorState(path, actuatorState{Timestamp: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read state directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("the temporary file must be renamed, not left behind: %s", entry.Name())
		}
	}
}

func TestUnreadableSignalDirsNamesOnlyMissingDirectories(t *testing.T) {
	present := t.TempDir()

	unreadable := unreadableSignalDirs([]string{
		filepath.Join(present, "shutdown.json"),
		"/definitely/not/here/shutdown.json",
	})

	// A missing signal *file* in a directory that exists is the normal case -- no shutdown is in
	// progress -- and must never be reported.
	if len(unreadable) != 1 || unreadable[0] != "/definitely/not/here" {
		t.Fatalf("expected only the missing directory, got: %v", unreadable)
	}
}
