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

package trigger

import (
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

func TestEvaluateApprovesRuntimeBelowTrigger(t *testing.T) {
	observedAt := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	runtimeThreshold := int64(120)
	runtimeRemaining := int64(90)

	evaluation := Evaluate(Inputs{
		ObservedAt: observedAt,
		Triggers: []Trigger{
			{
				ID:                  "runtime-critical",
				Type:                TypeRuntimeBelow,
				UPSDevices:          []string{"rack-a"},
				RuntimeBelowSeconds: &runtimeThreshold,
			},
		},
		UPSStates: []UPSState{
			{
				UPSDevice:      "rack-a",
				Phase:          telemetry.PhaseOnBattery,
				RuntimeSeconds: &runtimeRemaining,
			},
		},
	})

	if !evaluation.Approved || evaluation.Reason != "TriggerEligible" {
		t.Fatalf("expected approved evaluation, got %#v", evaluation)
	}
	if len(evaluation.SelectedUPSDevices) != 1 || evaluation.SelectedUPSDevices[0] != "rack-a" {
		t.Fatalf("expected rack-a selected, got %#v", evaluation.SelectedUPSDevices)
	}
	decision := evaluation.Decisions[0]
	if !decision.Matched || !decision.Eligible || decision.Reason != "TriggerEligible" {
		t.Fatalf("expected eligible runtime decision, got %#v", decision)
	}
}

func TestEvaluateRequiresHoldDuration(t *testing.T) {
	startedAt := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	first := Evaluate(Inputs{
		ObservedAt: startedAt,
		Triggers: []Trigger{
			{
				ID:   "on-battery-held",
				Type: TypeOnBattery,
				For:  5 * time.Minute,
			},
		},
		UPSStates: []UPSState{{UPSDevice: "rack-a", OnBattery: true}},
	})

	if first.Approved || first.Reason != "TriggerHoldPending" {
		t.Fatalf("expected first evaluation to wait for hold duration, got %#v", first)
	}
	if len(first.Holds) != 1 || !first.Holds[0].StartedAt.Equal(startedAt) {
		t.Fatalf("expected hold to start at observation time, got %#v", first.Holds)
	}
	if first.Decisions[0].EligibleAt == nil || !first.Decisions[0].EligibleAt.Equal(startedAt.Add(5*time.Minute)) {
		t.Fatalf("expected eligibleAt after hold duration, got %#v", first.Decisions[0])
	}

	second := Evaluate(Inputs{
		ObservedAt: startedAt.Add(6 * time.Minute),
		Triggers: []Trigger{
			{
				ID:   "on-battery-held",
				Type: TypeOnBattery,
				For:  5 * time.Minute,
			},
		},
		UPSStates: []UPSState{{UPSDevice: "rack-a", OnBattery: true}},
		Holds:     first.Holds,
	})

	if !second.Approved || second.Decisions[0].Reason != "TriggerEligible" {
		t.Fatalf("expected second evaluation to approve after hold, got %#v", second)
	}
	if second.Decisions[0].HoldStartedAt == nil || !second.Decisions[0].HoldStartedAt.Equal(startedAt) {
		t.Fatalf("expected original hold start to be preserved, got %#v", second.Decisions[0])
	}
}

func TestEvaluateSelectsPowerDomainsAndStaleTelemetry(t *testing.T) {
	evaluation := Evaluate(Inputs{
		ObservedAt: time.Date(2026, 8, 2, 21, 0, 0, 0, time.UTC),
		Triggers: []Trigger{
			{
				ID:           "stale-core-domain",
				Type:         TypeTelemetryStale,
				PowerDomains: []string{"core"},
			},
		},
		UPSStates: []UPSState{
			{UPSDevice: "rack-b", PowerDomains: []string{"edge"}, Phase: telemetry.PhaseOnline},
			{UPSDevice: "rack-a", PowerDomains: []string{"core"}, Phase: telemetry.PhaseStale},
		},
	})

	if !evaluation.Approved {
		t.Fatalf("expected stale core domain to approve, got %#v", evaluation)
	}
	if len(evaluation.SelectedUPSDevices) != 1 || evaluation.SelectedUPSDevices[0] != "rack-a" {
		t.Fatalf("expected only rack-a selected, got %#v", evaluation.SelectedUPSDevices)
	}
}

func TestEvaluateReportsDiagnostics(t *testing.T) {
	chargeThreshold := int32(20)
	evaluation := Evaluate(Inputs{
		ObservedAt: time.Date(2026, 8, 2, 22, 0, 0, 0, time.UTC),
		Triggers: []Trigger{
			{
				ID:         "bad-runtime-trigger",
				Type:       TypeRuntimeBelow,
				UPSDevices: []string{"rack-a"},
			},
			{
				ID:                 "missing-charge",
				Type:               TypeChargeBelow,
				UPSDevices:         []string{"rack-a"},
				ChargeBelowPercent: &chargeThreshold,
			},
			{
				ID:         "empty-selection",
				Type:       TypeLowBattery,
				UPSDevices: []string{"missing"},
			},
		},
		UPSStates: []UPSState{{UPSDevice: "rack-a"}},
	})

	if evaluation.Approved {
		t.Fatalf("expected diagnostics-only evaluation not to approve, got %#v", evaluation)
	}
	for _, expected := range []string{"RuntimeThresholdMissing", "ChargeTelemetryMissing", "TriggerSelectionEmpty"} {
		if !hasDiagnostic(evaluation.Diagnostics, expected) {
			t.Fatalf("expected diagnostic %q, got %#v", expected, evaluation.Diagnostics)
		}
	}
}

func hasDiagnostic(diagnostics []Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			return true
		}
	}
	return false
}

func hasTriggerDiagnostic(diagnostics []Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			return true
		}
	}
	return false
}

// OD-9 end to end at evaluation time. Without this the fallback would be a field the API accepts,
// the planner validates, and nothing ever acts on -- the defect class F-25 and F-33 both were.
func TestEvaluateAppliesFallbackWhenTheDeviceCannotReportRuntime(t *testing.T) {
	observedAt := time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC)
	runtimeThreshold := int64(120)

	evaluation := Evaluate(Inputs{
		ObservedAt: observedAt,
		Triggers: []Trigger{
			{
				ID:                  "runtime-critical",
				Type:                TypeRuntimeBelow,
				FallbackType:        TypeLowBattery,
				UPSDevices:          []string{"rack-a"},
				RuntimeBelowSeconds: &runtimeThreshold,
			},
		},
		UPSStates: []UPSState{
			// Reports LB, reports no runtime. The whole case OD-9 exists for.
			{UPSDevice: "rack-a", Phase: telemetry.PhaseLowBattery, LowBattery: true},
		},
	})

	if !evaluation.Approved {
		t.Fatalf("the declared fallback should have matched, got %#v", evaluation)
	}
	if !hasTriggerDiagnostic(evaluation.Diagnostics, "TriggerFallbackApplied") {
		t.Fatalf("expected TriggerFallbackApplied, got %#v", evaluation.Diagnostics)
	}
	if hasTriggerDiagnostic(evaluation.Diagnostics, "RuntimeTelemetryMissing") {
		t.Fatalf("a handled substitution must not also report the telemetry as missing, got %#v", evaluation.Diagnostics)
	}
}

// The fallback substitutes the condition, it does not force a match. A device reporting no runtime
// and no LB flag is simply not in trouble yet.
func TestEvaluateFallbackStillRequiresItsOwnConditionToHold(t *testing.T) {
	runtimeThreshold := int64(120)

	evaluation := Evaluate(Inputs{
		ObservedAt: time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC),
		Triggers: []Trigger{
			{
				ID:                  "runtime-critical",
				Type:                TypeRuntimeBelow,
				FallbackType:        TypeLowBattery,
				UPSDevices:          []string{"rack-a"},
				RuntimeBelowSeconds: &runtimeThreshold,
			},
		},
		UPSStates: []UPSState{
			{UPSDevice: "rack-a", Phase: telemetry.PhaseOnBattery, OnBattery: true},
		},
	})

	if evaluation.Approved {
		t.Fatalf("LowBattery had not fired, so the fallback must not approve, got %#v", evaluation)
	}
}

// Without a fallback the device still reports the gap. Substitution is opt-in, so the default
// behavior must be unchanged.
func TestEvaluateWithoutFallbackStillReportsMissingTelemetry(t *testing.T) {
	runtimeThreshold := int64(120)

	evaluation := Evaluate(Inputs{
		ObservedAt: time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC),
		Triggers: []Trigger{
			{
				ID:                  "runtime-critical",
				Type:                TypeRuntimeBelow,
				UPSDevices:          []string{"rack-a"},
				RuntimeBelowSeconds: &runtimeThreshold,
			},
		},
		UPSStates: []UPSState{
			{UPSDevice: "rack-a", Phase: telemetry.PhaseLowBattery, LowBattery: true},
		},
	})

	if evaluation.Approved {
		t.Fatalf("no fallback was declared, so a runtime trigger must not fire, got %#v", evaluation)
	}
	if !hasTriggerDiagnostic(evaluation.Diagnostics, "RuntimeTelemetryMissing") {
		t.Fatalf("expected RuntimeTelemetryMissing, got %#v", evaluation.Diagnostics)
	}
}

// A missing threshold is an authoring mistake, not a device that cannot answer. Substituting here
// would hide a broken flow behind a coarser trigger that happens to fire.
func TestEvaluateDoesNotSubstituteForAMissingThreshold(t *testing.T) {
	evaluation := Evaluate(Inputs{
		ObservedAt: time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC),
		Triggers: []Trigger{
			{
				ID:           "runtime-critical",
				Type:         TypeRuntimeBelow,
				FallbackType: TypeLowBattery,
				UPSDevices:   []string{"rack-a"},
			},
		},
		UPSStates: []UPSState{
			{UPSDevice: "rack-a", Phase: telemetry.PhaseLowBattery, LowBattery: true},
		},
	})

	if evaluation.Approved {
		t.Fatalf("a misconfigured trigger must not be rescued by its fallback, got %#v", evaluation)
	}
	if !hasTriggerDiagnostic(evaluation.Diagnostics, "RuntimeThresholdMissing") {
		t.Fatalf("expected RuntimeThresholdMissing, got %#v", evaluation.Diagnostics)
	}
	if hasTriggerDiagnostic(evaluation.Diagnostics, "TriggerFallbackApplied") {
		t.Fatalf("the fallback must not apply to an authoring mistake, got %#v", evaluation.Diagnostics)
	}
}

// A device that reports runtime uses the primary. The fallback must not preempt the finer trigger.
func TestEvaluatePrefersThePrimaryWhenTelemetryIsPresent(t *testing.T) {
	runtimeThreshold := int64(120)
	runtimeRemaining := int64(600)

	evaluation := Evaluate(Inputs{
		ObservedAt: time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC),
		Triggers: []Trigger{
			{
				ID:                  "runtime-critical",
				Type:                TypeRuntimeBelow,
				FallbackType:        TypeLowBattery,
				UPSDevices:          []string{"rack-a"},
				RuntimeBelowSeconds: &runtimeThreshold,
			},
		},
		UPSStates: []UPSState{
			// LB is set but 600s remain: the primary says no, and the primary is what governs.
			{UPSDevice: "rack-a", Phase: telemetry.PhaseLowBattery, LowBattery: true, RuntimeSeconds: &runtimeRemaining},
		},
	})

	if evaluation.Approved {
		t.Fatalf("runtime was above the threshold, so the trigger must not fire, got %#v", evaluation)
	}
	if hasTriggerDiagnostic(evaluation.Diagnostics, "TriggerFallbackApplied") {
		t.Fatalf("the fallback must not apply when the primary telemetry is present, got %#v", evaluation.Diagnostics)
	}
}
