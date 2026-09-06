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

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
)

// This file is OD-27's simulation harness (docs/tasks.md, Planning & Execution Logic): the 20%
// runtime reserve, the 10% minimum compression, the "plan does not fit" verdict, and re-execution
// across a second outage, driven through the actual planner/executor boundary rather than asserted
// against either side in isolation.
//
// The pure math for all four is already unit-tested where it lives -- internal/adaptive/timing_test.go
// covers Budget() directly, and internal/executor/adaptive_test.go covers evaluateWave against
// hand-built Wave literals. What none of that proves is that a plan which actually went through
// planner.Compile, and crossed the boundary through the same function production uses, behaves the
// same way. executorWavesFromFlow (shutdownflow_execution.go) is that function: it is what turns
// planner.Wave (by way of the CRD status shape, powerv1alpha1.CompiledShutdownWave, via
// shutdownflow.APICompiledWaves) into executor.Wave. Every test below sends a real compiled plan
// through exactly that chain before it ever reaches the executor.
//
// "Node-condition inputs" and "synthetic UPS runtime curves" are the same thing read two ways:
// adaptive.PowerObservation carries both the battery/mains state and the runtime figure at each wave
// boundary, and a "curve" is a sequence of those fed through Executor.Observer -- which nothing in
// the existing suite does; every existing test applies one static observation for a whole run.

// simulatedTieredPlan compiles a real three-tier plan through planner.CompileWithHistory and returns
// it alongside the executor.Group set an execution needs, built the same way
// executorGroupsFromFlow builds them in production: independently, from the same declared timeouts
// the groups were compiled with, since a compiled Wave carries only the aggregate wave duration and
// never a per-group timeout.
//
// declaredTimeouts lets each test choose durations without duplicating the whole plan shape, and
// history seeds HistoryInputs.GroupDurations so execution-history samples are actually exercised at
// the boundary rather than left at their zero value.
func simulatedTieredPlan(t *testing.T, declaredTimeouts map[string]time.Duration, history planner.HistoryInputs) (planner.Plan, []executorpkg.Wave, []executorpkg.Group) {
	t.Helper()

	structural := planner.StructuralInputs{
		Triggers: []planner.Trigger{
			{Type: "RuntimeBelow", RuntimeBelowSeconds: int64Ptr(300)},
		},
		Groups: []planner.Group{
			{
				Name: "applications", Action: "ScaleWorkload",
				ShutdownTier: int32Ptr(4),
				Timeout:      planner.Duration{Duration: declaredTimeouts["applications"]},
			},
			{
				Name: "databases", Action: "ScaleWorkload",
				ShutdownTier: int32Ptr(3),
				Timeout:      planner.Duration{Duration: declaredTimeouts["databases"]},
			},
			{
				Name: "storage", Action: "ScaleWorkload",
				ShutdownTier: int32Ptr(2),
				Timeout:      planner.Duration{Duration: declaredTimeouts["storage"]},
			},
		},
	}

	plan, diagnostics, err := planner.CompileWithHistory(structural, planner.TelemetryInputs{}, history)
	if err != nil {
		t.Fatalf("compile simulated plan: %v (diagnostics: %+v)", err, diagnostics)
	}
	if len(plan.Waves) != 3 {
		t.Fatalf("expected three waves, one per tier, got %d", len(plan.Waves))
	}

	// The real production chain: planner.Wave -> the CRD status shape -> executor.Wave. Neither
	// conversion is reimplemented here; both are the actual functions a live reconcile calls.
	compiledWaves := shutdownflow.APICompiledWaves(plan.Waves)
	waves := executorWavesFromFlow(compiledWaves, nil)

	groups := make([]executorpkg.Group, 0, len(structural.Groups))
	for _, group := range structural.Groups {
		groups = append(groups, executorpkg.Group{
			Name:    group.Name,
			Action:  group.Action,
			Timeout: declaredTimeouts[group.Name],
		})
	}

	return plan, waves, groups
}

func int32Ptr(v int32) *int32 { return &v }
func int64Ptr(v int64) *int64 { return &v }

func runtimeObservation(onBattery, lowBattery bool, runtimeSeconds int64) adaptive.PowerObservation {
	seconds := runtimeSeconds
	return adaptive.PowerObservation{
		OnBattery: onBattery, LowBattery: lowBattery,
		RuntimeSeconds: &seconds, RuntimeTrusted: true,
	}
}

// curveObserver replays a fixed sequence of readings, one per call, and fails the test rather than
// looping or panicking if a run asks for more than the curve supplies -- a wave boundary the curve
// did not anticipate is a bug in the test, not a state to paper over.
func curveObserver(t *testing.T, curve []adaptive.PowerObservation) executorpkg.PowerObserver {
	t.Helper()
	i := 0
	return func(_ context.Context) (adaptive.PowerObservation, error) {
		if i >= len(curve) {
			t.Fatalf("power curve exhausted after %d readings; the run needed another wave boundary", i)
		}
		reading := curve[i]
		i++
		return reading, nil
	}
}

// TestAdaptiveBoundary_HistoryInformsEstimatesButNeverTheLiveWaveDuration is the execution-history
// half of OD-27, and it is written to find out what actually happens rather than assume it.
//
// The design is explicit that adaptation reads a plan's *declared* durations, and that history
// (EX-32) only ever moves Plan.GroupEstimates and Plan.ObservedDuration -- deliberately outside the
// plan hash, so an estimate that later moves cannot retroactively change plan identity. Since
// executorWavesFromFlow reads Wave.Duration, and Wave.Duration is part of the hash, the two claims
// together predict something specific and testable: a group whose execution history is wildly
// different from its declared timeout must still cross the boundary at the *declared* figure.
// "databases" is seeded with a single 300s observed sample against a 180s declared timeout --
// deliberately far apart, so a duration that leaked from the wrong source would be unmistakable
// rather than lost in rounding.
func TestAdaptiveBoundary_HistoryInformsEstimatesButNeverTheLiveWaveDuration(t *testing.T) {
	declared := map[string]time.Duration{
		"applications": 120 * time.Second,
		"databases":    180 * time.Second,
		"storage":      240 * time.Second,
	}
	history := planner.HistoryInputs{
		GroupDurations: map[string][]time.Duration{
			"databases": {300 * time.Second},
		},
	}

	plan, waves, _ := simulatedTieredPlan(t, declared, history)

	var databasesEstimate *planner.GroupEstimate
	for i := range plan.GroupEstimates {
		if plan.GroupEstimates[i].Group == "databases" {
			databasesEstimate = &plan.GroupEstimates[i]
		}
	}
	if databasesEstimate == nil {
		t.Fatal("expected a GroupEstimate for databases")
	}
	if databasesEstimate.Source != planner.EstimateObserved {
		t.Fatalf("databases estimate source = %q, want %q -- history did not inform the estimate at all",
			databasesEstimate.Source, planner.EstimateObserved)
	}
	if databasesEstimate.Duration.Duration != 300*time.Second {
		t.Fatalf("databases estimate = %s, want the observed 300s", databasesEstimate.Duration.Duration)
	}

	// The boundary itself: find the wave carrying "databases" and confirm what actually reaches the
	// executor is the declared 180s, not the 300s history informed the estimate with.
	var databasesWave *executorpkg.Wave
	for i := range waves {
		for _, name := range waves[i].Groups {
			if name == "databases" {
				databasesWave = &waves[i]
			}
		}
	}
	if databasesWave == nil {
		t.Fatal("expected a wave carrying the databases group")
	}
	if databasesWave.Duration != 180*time.Second {
		t.Fatalf("wave duration crossing the boundary = %s, want the declared 180s -- execution "+
			"history leaked into the live compression input, which PL-14/PL-27 forbid",
			databasesWave.Duration)
	}
}

// TestAdaptiveBoundary_ReserveAppliesToTheActualCompiledPlan proves the 20% reserve against
// durations planner.Compile actually produced, not a Wave{Duration: ...} literal typed into a test.
func TestAdaptiveBoundary_ReserveAppliesToTheActualCompiledPlan(t *testing.T) {
	declared := map[string]time.Duration{
		"applications": 60 * time.Second,
		"databases":    90 * time.Second,
		"storage":      120 * time.Second,
	}
	plan, waves, groups := simulatedTieredPlan(t, declared, planner.HistoryInputs{})

	var totalDeclared time.Duration
	for _, wave := range waves {
		totalDeclared += wave.Duration
	}
	if totalDeclared != 270*time.Second {
		t.Fatalf("total declared plan duration = %s, want 270s (60+90+120)", totalDeclared)
	}

	// 1000s remaining, 20% reserved -> 800s available against a 270s plan: comfortably fits, so
	// compression should clamp to 1 (never grants more than declared) rather than expand anything.
	writer := &fakeAuditStore{}
	input := executorpkg.Input{
		ExecutionID: "sim-reserve", ShutdownFlow: "sim", PlanConfigHash: plan.Hash, Mode: executorpkg.ModeDryRun,
		Waves: waves, Groups: groups,
		Adaptive: executorpkg.AdaptiveInput{Observation: runtimeObservation(true, false, 1000), FinalTier: 1},
	}
	result, err := executorpkg.Executor{Writer: writer, Clock: time.Now, NewID: func() string { return "sim" }}.
		Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != executorpkg.PhaseCompleted {
		t.Fatalf("phase = %q, want Completed", result.Phase)
	}
	for _, group := range writer.executionGroups {
		if group.Details["planFits"] != true {
			t.Fatalf("group %s: planFits = %v, want true with 800s available against a 270s plan",
				group.GroupName, group.Details["planFits"])
		}
		if group.Details["compression"] != 1.0 {
			t.Fatalf("group %s: compression = %v, want 1.0 -- plentiful runtime must not expand a "+
				"declared duration", group.GroupName, group.Details["compression"])
		}
	}

	// Tight enough to force real compression, still comfortably above the 10% minimum: 300s
	// remaining -> 240s available (the 20% reserve visible directly) against 270s declared ->
	// compression = 240/270 ≈ 0.889.
	//
	// That figure only holds for the first wave. remainingPlanDuration sums the waves not yet run,
	// so it shrinks as the flow proceeds -- by the second wave only 210s (databases + storage) is
	// left, and 240s comfortably covers that on its own, clamping compression back to 1. Checking
	// only "applications" is therefore not a simplification, it is the only group for which "the
	// reserve applied to the full compiled plan" is actually the claim being tested.
	tight := &fakeAuditStore{}
	input.Adaptive.Observation = runtimeObservation(true, false, 300)
	if _, err := (executorpkg.Executor{Writer: tight, Clock: time.Now, NewID: func() string { return "sim" }}).
		Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	wantCompression := float64(240*time.Second) / float64(270*time.Second)
	var firstGroup *executorpkg.Group
	for i := range groups {
		if groups[i].Name == "applications" {
			firstGroup = &groups[i]
		}
	}
	if firstGroup == nil {
		t.Fatal("expected an applications group in the compiled plan")
	}
	var recorded bool
	for _, group := range tight.executionGroups {
		if group.GroupName != "applications" {
			continue
		}
		recorded = true
		got, _ := group.Details["compression"].(float64)
		if diff := got - wantCompression; diff > 1e-6 || diff < -1e-6 {
			t.Fatalf("applications compression = %v, want %.6f (the 20%% reserve applied against "+
				"the full 270s compiled plan, measured on the wave where nothing has run yet)",
				group.Details["compression"], wantCompression)
		}
	}
	if !recorded {
		t.Fatal("expected an audit record for the applications group")
	}
}

// TestAdaptiveBoundary_SyntheticCurveTightensCompressionAndReportsNotFitting is the "10% minimum
// compression" and "plan does not fit" half, driven by a genuine multi-reading power curve across
// the real plan's three wave boundaries -- comfortable, then tight, then infeasible -- rather than
// one static observation applied to the whole run, which is what every existing executor test does.
func TestAdaptiveBoundary_SyntheticCurveTightensCompressionAndReportsNotFitting(t *testing.T) {
	declared := map[string]time.Duration{
		"applications": 120 * time.Second,
		"databases":    120 * time.Second,
		"storage":      120 * time.Second,
	}
	plan, waves, groups := simulatedTieredPlan(t, declared, planner.HistoryInputs{})

	// A declining runtime curve: comfortable at wave 0, tight but still feasible at wave 1, and
	// deliberately below what even the 10% floor can rescue at wave 2 -- 10s available against 120s
	// declared is a compression of ~0.083, under DefaultParameters().MinimumCompression (0.10).
	curve := []adaptive.PowerObservation{
		runtimeObservation(true, false, 900),
		runtimeObservation(true, false, 200),
		runtimeObservation(true, true, 13),
	}

	writer := &fakeAuditStore{}
	input := executorpkg.Input{
		ExecutionID: "sim-curve", ShutdownFlow: "sim", PlanConfigHash: plan.Hash, Mode: executorpkg.ModeDryRun,
		Waves: waves, Groups: groups,
		Adaptive: executorpkg.AdaptiveInput{Observation: curve[0], FinalTier: 1},
	}
	executor := executorpkg.Executor{
		Writer: writer, Clock: time.Now, NewID: func() string { return "sim" },
		Observer: curveObserver(t, curve),
	}
	result, err := executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	// PL-31: an infeasible plan is reported, not refused. The flow completes regardless.
	if result.Phase != executorpkg.PhaseCompleted {
		t.Fatalf("phase = %q, want Completed -- a plan that does not fit still runs (PL-31)", result.Phase)
	}
	if len(writer.executionGroups) != 3 {
		t.Fatalf("expected one group recorded per wave, got %d", len(writer.executionGroups))
	}

	firstFits, _ := writer.executionGroups[0].Details["planFits"].(bool)
	if !firstFits {
		t.Fatalf("wave 0 (900s available): planFits = %v, want true", firstFits)
	}

	lastGroup := writer.executionGroups[len(writer.executionGroups)-1]
	if fits, _ := lastGroup.Details["planFits"].(bool); fits {
		t.Fatalf("final wave (13s runtime against a 120s declared duration): planFits = true, "+
			"want false -- this is the case the 10%% minimum exists to report, group=%s",
			lastGroup.GroupName)
	}
	gotCompression, _ := lastGroup.Details["compression"].(float64)
	wantMinimum := adaptive.DefaultParameters().MinimumCompression
	if diff := gotCompression - wantMinimum; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("final wave compression = %v, want it held at the minimum %.2f rather than "+
			"compressed further -- past the minimum every action just fails its deadline",
			gotCompression, wantMinimum)
	}

	// The event log states the verdict in words too (Budget.Describe), which is what a subscriber
	// actually reads. Each wave is recorded twice -- a Running-phase row carrying the events, and a
	// later Completed-phase row that does not -- so this checks every row for the final wave's
	// index rather than assuming the last append is the one with events in it.
	finalWaveIndex := waves[len(waves)-1].Index
	found := false
	for _, wave := range writer.executionWaves {
		if wave.WaveIndex != finalWaveIndex {
			continue
		}
		events, _ := wave.Details["events"].([]string)
		for _, event := range events {
			if strings.Contains(event, "plan does not fit") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected a 'plan does not fit' event recorded for wave %d, found none across %d "+
			"wave records", finalWaveIndex, len(writer.executionWaves))
	}
}

// TestAdaptiveBoundary_SecondOutageReplansFromThePersistedDepth is "replan behavior" as this
// codebase actually implements it. There is no recompilation mid-flow -- the design forbids it
// (PL-14, PL-27) -- so re-planning here means what EX-25/EX-26 describe: a second Execute() against
// the same compiled plan, whose pointer resumes from wherever the first outage's ascent left it, and
// descends again through tiers it already visited, reported as re-execution rather than new work.
//
// The existing coverage of this (TestASecondDipResumesFromThePersistedDepth in
// internal/executor/adaptive_test.go) hand-builds the resume PointerState as a literal. This drives
// it from a real first Execute() on the real compiled plan, through PointerState.Ascend -- the
// same method the executor itself calls on recovery -- so the "second outage" state is produced by
// the model, not typed in by the test.
func TestAdaptiveBoundary_SecondOutageReplansFromThePersistedDepth(t *testing.T) {
	declared := map[string]time.Duration{
		"applications": 60 * time.Second,
		"databases":    60 * time.Second,
		"storage":      60 * time.Second,
	}
	plan, waves, groups := simulatedTieredPlan(t, declared, planner.HistoryInputs{})

	// First outage: on battery throughout, descends all the way to tier 2 (storage).
	firstWriter := &fakeAuditStore{}
	firstInput := executorpkg.Input{
		ExecutionID: "sim-outage-1", ShutdownFlow: "sim", PlanConfigHash: plan.Hash, Mode: executorpkg.ModeDryRun,
		Waves: waves, Groups: groups,
		Adaptive: executorpkg.AdaptiveInput{Observation: runtimeObservation(true, false, 600), FinalTier: 1},
	}
	firstResult, err := (executorpkg.Executor{Writer: firstWriter, Clock: time.Now, NewID: func() string { return "sim" }}).
		Execute(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if firstResult.Adaptive.Pointer.Tier != 2 || firstResult.Adaptive.Pointer.Deepest != 2 {
		t.Fatalf("first outage pointer = %+v, want Tier=2 Deepest=2 after descending the whole plan",
			firstResult.Adaptive.Pointer)
	}

	// Power recovers: the model's own Ascend, not a hand-built struct, produces the resume state --
	// exactly what a real reconcile persists and reloads.
	ascended, ascentMovement := firstResult.Adaptive.Pointer.Ascend(4)
	if ascentMovement.Direction != adaptive.DirectionAscend {
		t.Fatalf("expected an ascent movement, got %+v", ascentMovement)
	}
	if ascended.Deepest != 2 {
		t.Fatalf("ascent must not lose how deep the first outage actually reached: deepest = %d, want 2",
			ascended.Deepest)
	}

	// Second outage against the same compiled plan: re-descends through tier 4, 3, and 2, and the
	// deepest tiers are re-execution, not new work.
	secondWriter := &fakeAuditStore{}
	secondInput := executorpkg.Input{
		ExecutionID: "sim-outage-2", ShutdownFlow: "sim", PlanConfigHash: plan.Hash, Mode: executorpkg.ModeDryRun,
		Waves: waves, Groups: groups,
		Adaptive: executorpkg.AdaptiveInput{
			Observation: runtimeObservation(true, false, 600),
			Pointer:     ascended,
			StartTier:   4,
			FinalTier:   1,
		},
	}
	secondResult, err := (executorpkg.Executor{Writer: secondWriter, Clock: time.Now, NewID: func() string { return "sim" }}).
		Execute(context.Background(), secondInput)
	if err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}
	if secondResult.Phase != executorpkg.PhaseCompleted {
		t.Fatalf("second outage phase = %q, want Completed", secondResult.Phase)
	}
	if secondResult.Adaptive.Pointer.Deepest != 2 {
		t.Fatalf("second outage deepest = %d, want it to hold at 2 rather than reset",
			secondResult.Adaptive.Pointer.Deepest)
	}
	reexecuted := false
	for _, event := range secondResult.Adaptive.Events {
		if strings.Contains(event, "re-entered tier") {
			reexecuted = true
		}
	}
	if !reexecuted {
		t.Fatalf("expected a re-execution event when the second outage re-descended through "+
			"already-visited tiers, got %#v", secondResult.Adaptive.Events)
	}
}

// TestAdaptiveBoundary_ReserveCoversARepresentativeHaltDurationSample is calibration evidence, not
// a behavioral test -- there is no function in this codebase that takes a measured halt duration as
// an input, because the reserve is a fixed fraction (OD-27), not a value derived from any single
// measurement. What this checks is whether that fixed fraction, applied at the runtime level where
// it matters most (the Urgent threshold, where the least margin exists to be wrong), comfortably
// covers a representative set of the tail latencies it exists to absorb.
//
// The sample set is synthetic, standing in for docs/contributing/audits/operator-maturity-benchmarks.md's
// nutoperator_halt_duration_seconds reconstruction pending real outage data -- exactly the role OD-27
// itself assigns real measurement: useful calibration evidence, not the v1 closure path. A future
// pass with real samples should replace the synthetic set here, not add a parallel test.
func TestAdaptiveBoundary_ReserveCoversARepresentativeHaltDurationSample(t *testing.T) {
	// A plausible spread for sync(2) plus the actuator's own checks plus the node-agent handoff --
	// the tail the reserve exists to cover, per parameters.go's own doc comment on ReserveFraction.
	syntheticHaltDurationSamples := []time.Duration{
		8 * time.Second, 12 * time.Second, 15 * time.Second, 22 * time.Second, 35 * time.Second,
	}

	params := adaptive.DefaultParameters()
	if params.ReserveFraction != 0.20 {
		t.Fatalf("ReserveFraction = %v, want 0.20 -- this test's margin claim is calibrated "+
			"against that specific value and needs revisiting if it changes", params.ReserveFraction)
	}

	// Evaluated at the Urgent threshold deliberately: that is the runtime level with the least
	// margin to spend, so it is where a reserve that is too small would show up first.
	runtimeAtUrgent := time.Duration(params.UrgentRuntimeSeconds) * time.Second
	reservedMargin := time.Duration(float64(runtimeAtUrgent) * params.ReserveFraction)

	var worstSample time.Duration
	for _, sample := range syntheticHaltDurationSamples {
		if sample > worstSample {
			worstSample = sample
		}
	}
	if reservedMargin <= worstSample {
		t.Fatalf("20%% of the Urgent threshold reserves %s, which does not comfortably exceed the "+
			"worst synthetic halt-duration sample (%s) -- the reserve would need to be raised, or "+
			"this sample set is not representative", reservedMargin, worstSample)
	}
}
