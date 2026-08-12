package planner

import (
	"testing"
	"time"
)

func historyGroups() []Group {
	return []Group{
		{
			Name: "applications", Action: "ScaleWorkload", Before: []string{"databases"},
			Timeout: Duration{Duration: 2 * time.Minute}, Target: Target{WorkloadSelector: true},
		},
		{
			Name: "databases", Action: "ScaleWorkload",
			Timeout: Duration{Duration: 2 * time.Minute}, Target: Target{WorkloadSelector: true},
		},
	}
}

func historyInputs() StructuralInputs {
	return StructuralInputs{
		SourceID: "shutdownflows.power.zalud.io/history",
		Triggers: []Trigger{{Type: "RuntimeBelow", PowerDomains: []string{"core"}}},
		Groups:   historyGroups(),
	}
}

// EX-32: the estimate is the slowest observation, not the mean. When a battery is draining the
// question is not how long this usually takes but how long it might, and an average is beaten half
// the time by construction.
func TestObservedEstimateTakesTheSlowestSample(t *testing.T) {
	estimate := estimateGroupDuration(historyGroups()[0], []time.Duration{
		90 * time.Second, 5 * time.Minute, 2 * time.Minute,
	})

	if estimate.Source != EstimateObserved {
		t.Fatalf("source = %q, want Observed", estimate.Source)
	}
	if estimate.Duration.Duration != 5*time.Minute {
		t.Fatalf("estimate = %s, want the slowest sample 5m", estimate.Duration.Duration)
	}
	if estimate.Samples != 3 {
		t.Fatalf("samples = %d, want 3", estimate.Samples)
	}
	// The declared number survives alongside it: a group that runs longer than its declared
	// timeout is the most useful thing this surfaces, and it is only visible as a comparison.
	if estimate.Declared.Duration != 2*time.Minute {
		t.Fatalf("declared = %s, want it preserved", estimate.Declared.Duration)
	}
}

// A cluster that has never run a flow is the normal starting state, not an error.
func TestNoHistoryFallsBackToTheDeclaredTimeout(t *testing.T) {
	estimate := estimateGroupDuration(historyGroups()[0], nil)

	if estimate.Source != EstimateDeclared || estimate.Duration.Duration != 2*time.Minute {
		t.Fatalf("estimate = %#v, want the declared 2m", estimate)
	}
	if estimate.Samples != 0 {
		t.Fatalf("samples = %d, want 0 for a declared estimate", estimate.Samples)
	}
}

// A broken record is not a fast run. Counting a zero or negative elapsed time would drag the
// estimate down in exactly the direction that is unsafe.
func TestNonPositiveSamplesAreIgnored(t *testing.T) {
	estimate := estimateGroupDuration(historyGroups()[0], []time.Duration{0, -time.Minute})

	if estimate.Source != EstimateDeclared {
		t.Fatalf("source = %q, want Declared; no usable sample was present", estimate.Source)
	}
}

// PL-27: identical inputs must produce a byte-identical plan, and a deployment with no history
// must not see its plan identity move just because the mechanism exists.
func TestNoHistoryLeavesThePlanUnchanged(t *testing.T) {
	structural := historyInputs()

	withoutHistory, _, err := Compile(structural, TelemetryInputs{})
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}
	withEmptyHistory, _, err := CompileWithHistory(structural, TelemetryInputs{}, HistoryInputs{})
	if err != nil {
		t.Fatalf("CompileWithHistory returned error: %v", err)
	}

	if withoutHistory.Hash != withEmptyHistory.Hash {
		t.Fatalf("plan hash moved without any history: %q vs %q", withoutHistory.Hash, withEmptyHistory.Hash)
	}
	if withoutHistory.EstimatedDuration.Duration != 4*time.Minute {
		t.Fatalf("estimated duration = %s, want the declared 4m", withoutHistory.EstimatedDuration.Duration)
	}
}

// The whole point: an observed plan total reflects what this cluster actually did, and the
// estimate says so per group rather than presenting one opaque number.
func TestObservedHistoryDrivesThePlanTotalAndIsAttributed(t *testing.T) {
	plan, _, err := CompileWithHistory(
		historyInputs(),
		TelemetryInputs{},
		HistoryInputs{GroupDurations: map[string][]time.Duration{
			"applications": {6 * time.Minute},
			// databases has never run, so it keeps its declared 2m.
		}},
	)
	if err != nil {
		t.Fatalf("CompileWithHistory returned error: %v", err)
	}

	// The declared total is untouched, because plan identity must not move when an estimate does --
	// history is looked up by plan hash, so a hash that tracked the estimate would chase itself.
	if plan.EstimatedDuration.Duration != 4*time.Minute {
		t.Fatalf("declared duration = %s, want the declared 4m left alone", plan.EstimatedDuration.Duration)
	}
	if plan.ObservedDuration.Duration != 8*time.Minute {
		t.Fatalf("observed duration = %s, want 6m observed + 2m declared", plan.ObservedDuration.Duration)
	}
	sources := map[string]string{}
	for _, estimate := range plan.GroupEstimates {
		sources[estimate.Group] = estimate.Source
	}
	if sources["applications"] != EstimateObserved || sources["databases"] != EstimateDeclared {
		t.Fatalf("sources = %#v, want applications observed and databases declared", sources)
	}
	// EX-33: a group with thin history is what a rehearsal would improve, so it is named.
	if plan.EstimateConfidence.ObservedGroups != 1 || plan.EstimateConfidence.DeclaredGroups != 1 {
		t.Fatalf("confidence = %#v, want one of each", plan.EstimateConfidence)
	}
	if len(plan.EstimateConfidence.ThinGroups) != 2 {
		t.Fatalf("thin groups = %v, want both named (one sample is not a trend)", plan.EstimateConfidence.ThinGroups)
	}
}

// The estimate must never move the plan hash. History is looked up by that hash, so a hash that
// tracked the estimate would change which history the next compile finds, which would move the
// estimate again.
func TestHistoryDoesNotMoveThePlanHash(t *testing.T) {
	structural := historyInputs()

	bare, _, err := CompileWithHistory(structural, TelemetryInputs{}, HistoryInputs{})
	if err != nil {
		t.Fatalf("CompileWithHistory returned error: %v", err)
	}
	withHistory, _, err := CompileWithHistory(structural, TelemetryInputs{}, HistoryInputs{
		GroupDurations: map[string][]time.Duration{"applications": {9 * time.Minute}},
	})
	if err != nil {
		t.Fatalf("CompileWithHistory returned error: %v", err)
	}

	if bare.Hash != withHistory.Hash {
		t.Fatalf("plan hash moved with history: %q vs %q", bare.Hash, withHistory.Hash)
	}
	if withHistory.ObservedDuration.Duration == 0 {
		t.Fatal("history was supplied but produced no observed duration")
	}
}
