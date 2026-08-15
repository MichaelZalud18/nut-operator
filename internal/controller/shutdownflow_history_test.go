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
	"context"
	"strings"
	"testing"
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

func historyRuntime(seconds int64) planner.HistoryObservation {
	return planner.HistoryObservation{RuntimeSeconds: &seconds}
}

// OD-12: the plan is never blocked or truncated. A plan that does not fit still runs, and the
// warning has to say so plainly rather than reading like a refusal.
func TestPlanFeasibilityWarnsWithoutBlocking(t *testing.T) {
	estimated := 20 * time.Minute
	status := planFeasibilityStatus(&estimated, historyRuntime(480),
		planner.EstimateConfidence{ObservedGroups: 2, DeclaredGroups: 1})

	if status == nil {
		t.Fatal("a plan with an estimate must publish a feasibility comparison")
	}
	if status.Fits {
		t.Fatalf("status = %#v, want fits false for 20m against 8m", status)
	}
	if status.PlanSeconds != 1200 || status.RuntimeSeconds == nil || *status.RuntimeSeconds != 480 {
		t.Fatalf("status = %#v, want both numbers published", status)
	}
	if !strings.Contains(status.Message, "will still run") {
		t.Fatalf("message = %q, must say the flow still runs", status.Message)
	}
	if status.ObservedGroups != 2 || status.DeclaredGroups != 1 {
		t.Fatalf("status = %#v, want the estimate provenance carried through", status)
	}
}

// PL-32: missing data never yields an optimistic verdict. Unknown runtime must not report a fit.
func TestUnknownRuntimeNeverReportsAFit(t *testing.T) {
	estimated := 2 * time.Minute
	status := planFeasibilityStatus(&estimated, planner.HistoryObservation{}, planner.EstimateConfidence{})

	if status.Fits {
		t.Fatal("unknown runtime must not be read as headroom")
	}
	if status.RuntimeSeconds != nil {
		t.Fatalf("runtime = %d, want it left absent", *status.RuntimeSeconds)
	}
	if !strings.Contains(status.Message, "unknown") {
		t.Fatalf("message = %q, should say the runtime is unknown rather than imply the plan is too long", status.Message)
	}
}

// A plan that fits still publishes both numbers: the comparison is the point, not just the verdict.
func TestAFittingPlanStillPublishesTheComparison(t *testing.T) {
	estimated := 4 * time.Minute
	status := planFeasibilityStatus(&estimated, historyRuntime(1200), planner.EstimateConfidence{})

	if !status.Fits {
		t.Fatalf("status = %#v, want fits true for 4m against 20m", status)
	}
	if status.PlanSeconds != 240 || *status.RuntimeSeconds != 1200 {
		t.Fatalf("status = %#v, want both numbers published", status)
	}
}

// EX-33: the groups a rehearsal would improve are named, so the recommendation is actionable.
func TestThinGroupsAreNamedForRehearsal(t *testing.T) {
	estimated := time.Minute
	status := planFeasibilityStatus(&estimated, historyRuntime(600),
		planner.EstimateConfidence{ThinGroups: []string{"databases", "storage"}})

	if len(status.ThinGroups) != 2 {
		t.Fatalf("thin groups = %v, want both named", status.ThinGroups)
	}
}

func TestRehearsalHistoryIsIncludedByDefaultAndExcludable(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{}
	flow.Name = "history-flow"
	store := &fakeAuditStore{groupDurationSamples: []audit.GroupDurationSample{
		{GroupName: "applications", Observed: time.Minute, Rehearsal: true},
		{GroupName: "databases", Observed: 2 * time.Minute},
	}}

	history := resolveFlowHistory(context.Background(), store, flow, "plan-hash")
	if got := len(history.GroupDurations["applications"]); got != 1 {
		t.Fatalf("default rehearsal history samples = %d, want 1", got)
	}
	if got := len(history.GroupDurations["databases"]); got != 1 {
		t.Fatalf("real history samples = %d, want 1", got)
	}

	include := false
	flow.Spec.Rehearsal.IncludeInEstimates = &include
	history = resolveFlowHistory(context.Background(), store, flow, "plan-hash")
	if got := len(history.GroupDurations["applications"]); got != 0 {
		t.Fatalf("excluded rehearsal history samples = %d, want 0", got)
	}
	if got := len(history.GroupDurations["databases"]); got != 1 {
		t.Fatalf("real history samples after rehearsal opt-out = %d, want 1", got)
	}
}

// A plan with no estimate publishes nothing rather than a zero comparison that reads as a fit.
func TestNoEstimatePublishesNoComparison(t *testing.T) {
	if status := planFeasibilityStatus(nil, historyRuntime(600), planner.EstimateConfidence{}); status != nil {
		t.Fatalf("status = %#v, want nil when there is no estimate to compare", status)
	}
	zero := time.Duration(0)
	if status := planFeasibilityStatus(&zero, historyRuntime(600), planner.EstimateConfidence{}); status != nil {
		t.Fatalf("status = %#v, want nil for a zero estimate", status)
	}
}
