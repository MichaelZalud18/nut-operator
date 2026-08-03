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
