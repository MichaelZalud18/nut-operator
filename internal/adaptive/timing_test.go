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

package adaptive

import (
	"testing"
	"time"
)

func TestIndicatedModeFollowsRemainingRuntime(t *testing.T) {
	parameters := DefaultParameters()

	for name, testCase := range map[string]struct {
		observation PowerObservation
		expected    TimingMode
	}{
		"mains present":           {onMains(60), ModeRelaxed},
		"deep reserve on battery": {onBattery(3600), ModeRelaxed},
		"inside the nominal band": {onBattery(600), ModeNominal},
		"exactly at nominal":      {onBattery(900), ModeNominal},
		"inside the urgent band":  {onBattery(120), ModeUrgent},
		"exactly at urgent":       {onBattery(300), ModeUrgent},
		// The device's own LB assertion outranks any runtime figure -- it is the judgement the
		// hardware is better placed to make.
		"low battery despite healthy runtime": {
			PowerObservation{OnBattery: true, LowBattery: true, RuntimeSeconds: runtimeOf(3600), RuntimeTrusted: true},
			ModeUrgent},
		// PL-32: missing data never yields the optimistic verdict.
		"unknown runtime on battery": {PowerObservation{OnBattery: true}, ModeUrgent},
		// CR-4: a static firmware estimate cannot drive timing decisions.
		"untrusted runtime on battery": {
			PowerObservation{OnBattery: true, RuntimeSeconds: runtimeOf(3600)}, ModeUrgent},
		// A low reading while charging is not a deadline.
		"low runtime on mains": {onMains(30), ModeRelaxed},
	} {
		t.Run(name, func(t *testing.T) {
			if got := IndicatedMode(testCase.observation, parameters); got != testCase.expected {
				t.Fatalf("IndicatedMode = %q, want %q", got, testCase.expected)
			}
		})
	}
}

// Escalation is immediate. Waiting for confirmation to get more urgent spends the very margin the
// escalation exists to protect.
func TestEscalationTakesEffectOnASingleObservation(t *testing.T) {
	state := TimingState{Mode: ModeRelaxed}

	state, transition := state.Observe(onBattery(120), DefaultParameters())

	if !transition.Changed || state.Mode != ModeUrgent {
		t.Fatalf("expected an immediate escalation to Urgent, got %#v %#v", state, transition)
	}
	if transition.Reason != "Escalated" {
		t.Fatalf("reason = %q, want Escalated", transition.Reason)
	}
}

// Relaxation is not immediate. A single good reading during an outage is as likely to be a
// measurement artifact as a recovery, and relaxing on it hands back time that may not exist.
func TestRelaxationRequiresSustainedImprovement(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeUrgent}

	// Comfortably clear of the nominal threshold plus its margin.
	improved := onBattery(2000)

	for i := 1; i < parameters.RelaxAfterObservations; i++ {
		var transition TimingTransition
		state, transition = state.Observe(improved, parameters)
		if transition.Changed {
			t.Fatalf("relaxed after %d observations, want %d", i, parameters.RelaxAfterObservations)
		}
		if transition.Reason != "RelaxedPending" {
			t.Fatalf("reason = %q, want RelaxedPending", transition.Reason)
		}
	}

	state, transition := state.Observe(improved, parameters)
	if !transition.Changed || state.Mode != ModeRelaxed {
		t.Fatalf("expected relaxation on the final observation, got %#v %#v", state, transition)
	}
}

// The streak has to be consecutive. An improving run interrupted by a bad reading starts over --
// otherwise "sustained" would mean "eventually accumulated", which is a different and weaker claim.
func TestRelaxationStreakResetsOnAnInterruption(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeUrgent}

	state, _ = state.Observe(onBattery(2000), parameters)
	state, _ = state.Observe(onBattery(2000), parameters)
	// One bad reading.
	state, _ = state.Observe(onBattery(100), parameters)
	if state.Mode != ModeUrgent {
		t.Fatalf("mode = %q, want it still Urgent", state.Mode)
	}

	state, transition := state.Observe(onBattery(2000), parameters)
	if transition.Changed {
		t.Fatalf("the streak must restart after an interruption, got %#v", transition)
	}
	if state.PendingCount != 1 {
		t.Fatalf("pending count = %d, want 1 after restarting", state.PendingCount)
	}
}

// A reading sitting just the wrong side of a boundary must not start a relaxation streak at all.
// Without the margin the mode oscillates on ordinary telemetry jitter, and every flip is an event.
func TestRelaxationRequiresClearingTheThresholdByTheMargin(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeUrgent}

	// Just above the nominal threshold, but inside the 10% margin.
	barely := onBattery(parameters.NominalRuntimeSeconds + 1)
	for range parameters.RelaxAfterObservations + 2 {
		var transition TimingTransition
		state, transition = state.Observe(barely, parameters)
		if transition.Changed {
			t.Fatal("a reading inside the improvement margin must never relax the mode")
		}
		if transition.Reason != "ImprovementWithinMargin" {
			t.Fatalf("reason = %q, want ImprovementWithinMargin", transition.Reason)
		}
	}
}

// Mains returning needs no margin: there is no noisy reading to guard against.
func TestMainsRestoredRelaxesWithoutAMarginCheck(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeUrgent}

	// A runtime figure that would fail the margin test on battery.
	restored := onMains(1)
	for range parameters.RelaxAfterObservations - 1 {
		state, _ = state.Observe(restored, parameters)
	}
	state, transition := state.Observe(restored, parameters)

	if !transition.Changed || state.Mode != ModeRelaxed {
		t.Fatalf("expected relaxation once mains is back, got %#v %#v", state, transition)
	}
}

// Escalating out of a pending relaxation must discard the accumulated evidence, or a later
// improving reading would inherit a streak built under conditions that no longer apply.
func TestEscalationDiscardsPendingRelaxationEvidence(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeNominal}

	state, _ = state.Observe(onBattery(2000), parameters)
	if state.PendingCount == 0 {
		t.Fatal("expected relaxation evidence to accumulate")
	}

	state, _ = state.Observe(onBattery(60), parameters)

	if state.Mode != ModeUrgent {
		t.Fatalf("mode = %q, want Urgent", state.Mode)
	}
	if state.PendingCount != 0 || state.PendingMode != "" {
		t.Fatalf("escalation must clear pending evidence, got %#v", state)
	}
}

// An observation agreeing with the mode in force clears evidence too: a relaxation streak
// interrupted by a reading at the current mode is not a streak.
func TestAgreementWithTheCurrentModeClearsEvidence(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeNominal}

	state, _ = state.Observe(onBattery(2000), parameters)
	state, transition := state.Observe(onBattery(600), parameters)

	if transition.Changed {
		t.Fatalf("no change expected, got %#v", transition)
	}
	if state.PendingCount != 0 {
		t.Fatalf("pending count = %d, want it cleared", state.PendingCount)
	}
}

// The mode must not drift on a steady reading no matter how many times it is observed.
func TestASteadyObservationIsStable(t *testing.T) {
	parameters := DefaultParameters()
	state := TimingState{Mode: ModeRelaxed}
	steady := onBattery(600)

	state, _ = state.Observe(steady, parameters)
	if state.Mode != ModeNominal {
		t.Fatalf("mode = %q, want Nominal", state.Mode)
	}
	for range 20 {
		var transition TimingTransition
		state, transition = state.Observe(steady, parameters)
		if transition.Changed {
			t.Fatalf("a steady reading must not change the mode, got %#v", transition)
		}
	}
}

// An empty mode is Relaxed rather than the zero string, so a fresh flow starts somewhere valid.
func TestZeroTimingStateStartsRelaxed(t *testing.T) {
	state, transition := TimingState{}.Observe(onMains(3600), DefaultParameters())

	if state.Mode != ModeRelaxed || transition.From != ModeRelaxed {
		t.Fatalf("state = %#v transition = %#v, want Relaxed", state, transition)
	}
}

// The declared duration is a ceiling. Compression exists to take less time when the battery is
// draining, never to grant more than the flow author wrote down.
func TestCompressionNeverExceedsOne(t *testing.T) {
	parameters := DefaultParameters()

	for name, observation := range map[string]PowerObservation{
		"mains present":     onMains(60),
		"deep reserve":      onBattery(36000),
		"unknown runtime":   {OnBattery: true},
		"untrusted runtime": {OnBattery: true, RuntimeSeconds: runtimeOf(36000)},
	} {
		t.Run(name, func(t *testing.T) {
			budget := parameters.Budget(observation, time.Minute)
			if budget.Compression > 1 {
				t.Fatalf("compression = %v, want at most 1", budget.Compression)
			}
		})
	}
}

// The whole reason the ratio is measured: less runtime against the same plan must compress more.
// A fixed per-mode multiplier cannot express this, because it does not know the plan.
func TestCompressionTightensAsRuntimeShortens(t *testing.T) {
	parameters := DefaultParameters()
	plan := 20 * time.Minute

	roomy := parameters.Budget(onBattery(1800), plan)
	tight := parameters.Budget(onBattery(600), plan)

	if !(tight.Compression < roomy.Compression) {
		t.Fatalf("expected tighter compression on less runtime, got %v then %v",
			roomy.Compression, tight.Compression)
	}
}

// And the same runtime against a longer plan must compress more, for the same reason.
func TestCompressionTightensAsThePlanGrows(t *testing.T) {
	parameters := DefaultParameters()
	observation := onBattery(600)

	short := parameters.Budget(observation, 5*time.Minute)
	long := parameters.Budget(observation, 30*time.Minute)

	if !(long.Compression < short.Compression) {
		t.Fatalf("expected tighter compression on a longer plan, got %v then %v",
			short.Compression, long.Compression)
	}
}

// The reserve covers the handoff and actuation tail the plan's own durations never measured.
// Compressing to fill exactly the measured runtime would spend it.
func TestTheReserveIsHeldBackFromThePlan(t *testing.T) {
	parameters := DefaultParameters()

	budget := parameters.Budget(onBattery(1000), time.Hour)

	expected := time.Duration(float64(1000*time.Second) * (1 - parameters.ReserveFraction))
	if budget.Available != expected {
		t.Fatalf("available = %s, want %s", budget.Available, expected)
	}
}

// PL-32 and CR-4: with no trustworthy runtime the budget is assumed rather than optimistic, and
// the record says which it was so nobody reads an assumption as a measurement.
func TestAnUnmeasuredRuntimeUsesTheAssumedBudget(t *testing.T) {
	parameters := DefaultParameters()

	for name, observation := range map[string]PowerObservation{
		"unknown":   {OnBattery: true},
		"untrusted": {OnBattery: true, RuntimeSeconds: runtimeOf(36000)},
	} {
		t.Run(name, func(t *testing.T) {
			budget := parameters.Budget(observation, time.Hour)
			if budget.RuntimeMeasured {
				t.Fatal("an unread or untrusted runtime is not a measurement")
			}
			assumed := time.Duration(float64(parameters.AssumedRuntimeUnknown) * (1 - parameters.ReserveFraction))
			if budget.Available != assumed {
				t.Fatalf("available = %s, want the assumed %s", budget.Available, assumed)
			}
		})
	}

	if !parameters.Budget(onBattery(600), time.Hour).RuntimeMeasured {
		t.Fatal("a trusted runtime figure is a measurement")
	}
}

// A plan that cannot be compressed enough is reported rather than compressed into nonsense.
// Past this point the shutdown does not finish faster; every action just fails its deadline.
func TestAPlanThatCannotFitIsReportedAndHeldAtTheMinimum(t *testing.T) {
	parameters := DefaultParameters()

	budget := parameters.Budget(onBattery(30), 10*time.Hour)

	if budget.Fits {
		t.Fatalf("budget = %#v, want it reported as not fitting", budget)
	}
	if budget.Compression != parameters.MinimumCompression {
		t.Fatalf("compression = %v, want it held at the %v minimum",
			budget.Compression, parameters.MinimumCompression)
	}
}

// While mains is present there is no deadline to fit inside, so the plan runs as written.
func TestMainsPresentRunsThePlanAsWritten(t *testing.T) {
	budget := DefaultParameters().Budget(onMains(30), time.Hour)

	if budget.Compression != 1 || !budget.Fits {
		t.Fatalf("budget = %#v, want the declared plan unchanged", budget)
	}
}

// Nothing declared a duration, so there is nothing to compress and no basis to claim the plan
// does not fit. Treating an undeclared plan as zero-length would make every plan look feasible.
func TestAnUndeclaredPlanNeitherCompressesNorFails(t *testing.T) {
	budget := DefaultParameters().Budget(onBattery(30), 0)

	if budget.Compression != 1 || !budget.Fits {
		t.Fatalf("budget = %#v, want no compression and no verdict", budget)
	}
}

// An undeclared duration means no limit. Compressing "unlimited" must not invent a deadline the
// author never wrote.
func TestScalingLeavesAnUndeclaredDurationUnset(t *testing.T) {
	budget := DefaultParameters().Budget(onBattery(60), time.Hour)

	for _, declared := range []time.Duration{0, -time.Second} {
		if scaled := ScaleDuration(declared, budget); scaled != 0 {
			t.Fatalf("ScaleDuration(%s) = %s, want 0", declared, scaled)
		}
	}
}

// ScaleDuration is pure arithmetic over the budget: half the budget is half the duration.
func TestScaleDurationAppliesTheCompression(t *testing.T) {
	if got := ScaleDuration(time.Hour, TimingBudget{Compression: 0.5}); got != 30*time.Minute {
		t.Fatalf("ScaleDuration = %s, want 30m", got)
	}
	if got := ScaleDuration(time.Hour, TimingBudget{Compression: 1}); got != time.Hour {
		t.Fatalf("ScaleDuration = %s, want the declared hour", got)
	}
}

func TestCadenceIsFasterDuringAFlow(t *testing.T) {
	parameters := DefaultParameters()

	if parameters.Cadence(true) >= parameters.Cadence(false) {
		t.Fatalf("active cadence %s must be faster than idle %s",
			parameters.Cadence(true), parameters.Cadence(false))
	}
}

func TestWithDefaultsFillsEveryUnsetField(t *testing.T) {
	filled := Parameters{}.WithDefaults()

	if filled != DefaultParameters() {
		t.Fatalf("WithDefaults on a zero value = %#v, want the defaults", filled)
	}

	custom := Parameters{UrgentRuntimeSeconds: 120}.WithDefaults()
	if custom.UrgentRuntimeSeconds != 120 {
		t.Fatalf("WithDefaults overwrote a set field: %#v", custom)
	}
	if custom.IdleCadence != DefaultParameters().IdleCadence {
		t.Fatalf("WithDefaults left an unset field empty: %#v", custom)
	}
}

func TestDefaultParametersAreInternallyConsistent(t *testing.T) {
	if diagnostics := DefaultParameters().Validate(); len(diagnostics) != 0 {
		t.Fatalf("the shipped defaults must validate cleanly, got %#v", diagnostics)
	}
}

func TestValidateCatchesParametersThatCannotBehave(t *testing.T) {
	for name, testCase := range map[string]struct {
		parameters Parameters
		reason     string
	}{
		"inverted thresholds": {
			Parameters{UrgentRuntimeSeconds: 900, NominalRuntimeSeconds: 300}, "TimingThresholdsInverted"},
		"symmetric hysteresis": {
			Parameters{EscalateAfterObservations: 3, RelaxAfterObservations: 3}, "HysteresisNotAsymmetric"},
		"unreachable margin": {
			Parameters{RelaxImprovementMargin: 1.5}, "ImprovementMarginUnreachable"},
		"cadence inverted": {
			Parameters{IdleCadence: time.Second, ActiveCadence: time.Minute}, "CadenceSlowerDuringFlow"},
		"reserve consumes everything": {
			Parameters{ReserveFraction: 1.5}, "ReserveConsumesAllRuntime"},
		"minimum compression rejects everything": {
			Parameters{MinimumCompression: 1.5}, "MinimumCompressionRejectsEveryPlan"},
	} {
		t.Run(name, func(t *testing.T) {
			parameters := testCase.parameters.WithDefaults()
			// Re-apply the field under test: WithDefaults only fills zero values, but the cadence
			// case needs both fields kept as given.
			if testCase.parameters.IdleCadence > 0 {
				parameters.IdleCadence = testCase.parameters.IdleCadence
			}
			if testCase.parameters.ActiveCadence > 0 {
				parameters.ActiveCadence = testCase.parameters.ActiveCadence
			}
			found := false
			for _, diagnostic := range parameters.Validate() {
				if diagnostic.Reason == testCase.reason {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s, got %#v", testCase.reason, parameters.Validate())
			}
		})
	}
}
