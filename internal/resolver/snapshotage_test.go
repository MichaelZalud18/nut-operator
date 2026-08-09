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

package resolver

import (
	"strings"
	"testing"
	"time"
)

var snapshotNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func stampedAgo(age time.Duration) string {
	return snapshotNow.Add(-age).Format(time.RFC3339)
}

// A fresh snapshot is not worth a word. Reporting on every resolve would make the
// signal that matters — a snapshot that has stopped being refreshed — invisible.
func TestEvaluateSnapshotAgeStaysSilentWhenFresh(t *testing.T) {
	if got := EvaluateSnapshotAge(stampedAgo(time.Minute), snapshotNow, nil); got != nil {
		t.Fatalf("expected no diagnostic for a one-minute-old snapshot, got %#v", got)
	}
}

// IN-16 escalates through severity rather than through a cutoff.
func TestEvaluateSnapshotAgeEscalatesBySeverity(t *testing.T) {
	levels := []SnapshotAgeLevel{
		{Severity: DiagnosticInfo, After: time.Hour},
		{Severity: DiagnosticWarning, After: 6 * time.Hour},
	}

	info := EvaluateSnapshotAge(stampedAgo(2*time.Hour), snapshotNow, levels)
	if info == nil || info.Severity != DiagnosticInfo {
		t.Fatalf("expected an Info diagnostic two hours in, got %#v", info)
	}
	if info.Reason != "InventorySnapshotAging" {
		t.Fatalf("expected the aging reason, got %q", info.Reason)
	}

	warning := EvaluateSnapshotAge(stampedAgo(7*time.Hour), snapshotNow, levels)
	if warning == nil || warning.Severity != DiagnosticWarning {
		t.Fatalf("expected a Warning diagnostic seven hours in, got %#v", warning)
	}
}

// The oldest matching threshold wins regardless of the order it was written in,
// so a hand-edited spec cannot silently downgrade its own worst level.
func TestEvaluateSnapshotAgePicksTheHighestMatchingLevel(t *testing.T) {
	levels := []SnapshotAgeLevel{
		{Severity: DiagnosticWarning, After: 6 * time.Hour},
		{Severity: DiagnosticInfo, After: time.Hour},
	}

	got := EvaluateSnapshotAge(stampedAgo(48*time.Hour), snapshotNow, levels)
	if got == nil || got.Severity != DiagnosticWarning {
		t.Fatalf("expected the six-hour level to win at 48 hours, got %#v", got)
	}
}

// No level rejects. A provider outage during a power event must degrade planning,
// never stop it (IN-15), so no age can produce an Error.
func TestEvaluateSnapshotAgeNeverRejects(t *testing.T) {
	for _, age := range []time.Duration{time.Hour, 24 * time.Hour, 240 * 24 * time.Hour} {
		got := EvaluateSnapshotAge(stampedAgo(age), snapshotNow, nil)
		if got != nil && got.Severity == DiagnosticError {
			t.Fatalf("age %s produced a rejecting diagnostic: %#v", age, got)
		}
	}
}

// The eight-months-ago case the rule exists to prevent must be loud and must name
// the age, because "degraded" alone does not tell an operator what to fix.
func TestEvaluateSnapshotAgeReportsAVeryOldSnapshot(t *testing.T) {
	got := EvaluateSnapshotAge(stampedAgo(240*24*time.Hour), snapshotNow, nil)
	if got == nil || got.Severity != DiagnosticWarning {
		t.Fatalf("expected an eight-month-old snapshot to warn, got %#v", got)
	}
	if !strings.Contains(got.Message, "5760h0m0s") {
		t.Fatalf("expected the message to state the age, got %q", got.Message)
	}
}

// An unstamped snapshot is the declarative CRD provider, which is rebuilt every
// resolve. Inventing an age for it would report its metadata as staleness.
func TestEvaluateSnapshotAgeIgnoresAnUnstampedSnapshot(t *testing.T) {
	if got := EvaluateSnapshotAge("", snapshotNow, nil); got != nil {
		t.Fatalf("expected no diagnostic without a timestamp, got %#v", got)
	}
}

// A timestamp that cannot be parsed is a provider defect, reported as itself
// rather than silently treated as fresh.
func TestEvaluateSnapshotAgeReportsAnUnparseableTimestamp(t *testing.T) {
	got := EvaluateSnapshotAge("last tuesday", snapshotNow, nil)
	if got == nil || got.Reason != "InventorySnapshotTimestampInvalid" {
		t.Fatalf("expected an invalid-timestamp diagnostic, got %#v", got)
	}
	if got.Severity != DiagnosticWarning {
		t.Fatalf("expected a warning, got %q", got.Severity)
	}
}

// Clock skew is not staleness. A snapshot stamped in the future has not aged past
// anything, and saying otherwise would report the wrong fault.
func TestEvaluateSnapshotAgeIgnoresFutureTimestamps(t *testing.T) {
	future := snapshotNow.Add(time.Hour).Format(time.RFC3339)
	if got := EvaluateSnapshotAge(future, snapshotNow, nil); got != nil {
		t.Fatalf("expected no diagnostic for a future timestamp, got %#v", got)
	}
}

// An install that configured nothing is the one most likely to miss a stale
// snapshot, so the defaults have to apply rather than nothing applying.
func TestEvaluateSnapshotAgeAppliesDefaultsWhenUnconfigured(t *testing.T) {
	got := EvaluateSnapshotAge(stampedAgo(90*time.Minute), snapshotNow, nil)
	if got == nil || got.Severity != DiagnosticInfo {
		t.Fatalf("expected the default one-hour Info level to apply, got %#v", got)
	}
}
