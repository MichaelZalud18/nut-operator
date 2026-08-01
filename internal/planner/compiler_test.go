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

package planner

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCompileProducesDeterministicGraphPlan(t *testing.T) {
	appPhase := int32(10)
	dbPhase := int32(20)
	input := StructuralInputs{
		SourceID: "shutdownflows.power.zalud.io/test",
		Triggers: []Trigger{
			{
				Type:         "RuntimeBelow",
				PowerDomains: []string{"core"},
			},
		},
		Groups: []Group{
			{
				Name:    "databases",
				Action:  "ScaleWorkload",
				Phase:   &dbPhase,
				Timeout: Duration{Duration: 10 * time.Minute},
				Target:  Target{WorkloadSelector: true},
			},
			{
				Name:    "applications",
				Action:  "ScaleWorkload",
				Before:  []string{"databases"},
				Phase:   &appPhase,
				Timeout: Duration{Duration: 5 * time.Minute},
				Target:  Target{NamespaceSelector: true},
			},
		},
	}

	first, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	second, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected second compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected byte-equivalent plans from identical input\nfirst: %#v\nsecond: %#v", first, second)
	}
	if first.Hash == "" {
		t.Fatal("expected plan hash to be set")
	}
	if first.StructuralHash == "" {
		t.Fatal("expected structural hash to be set")
	}
	if first.EstimatedDuration.Duration != 15*time.Minute {
		t.Fatalf("expected 15m estimated duration, got %s", first.EstimatedDuration.Duration)
	}
	if got, want := waveGroups(first.Waves), [][]string{{"applications"}, {"databases"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected waves %#v, got %#v", want, got)
	}
}

func TestCompileDoesNotMutateStructuralInput(t *testing.T) {
	appPhase := int32(10)
	dbPhase := int32(20)
	input := StructuralInputs{
		Triggers: []Trigger{
			{
				Type:         "RuntimeBelow",
				UPSDevices:   []string{"ups-b", "ups-a"},
				PowerDomains: []string{"domain-b", "domain-a"},
			},
		},
		Groups: []Group{
			{
				Name:   "databases",
				Action: "ScaleWorkload",
				Before: []string{
					"network",
				},
				Phase: &dbPhase,
				Params: map[string]string{
					"shutdownMode": "ordered",
				},
			},
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Before: []string{
					"network",
					"databases",
				},
				Phase: &appPhase,
			},
			{
				Name:   "network",
				Action: "AgentShutdown",
				After:  []string{"databases"},
			},
		},
	}
	originalTriggerUPSDevices := append([]string(nil), input.Triggers[0].UPSDevices...)
	originalTriggerPowerDomains := append([]string(nil), input.Triggers[0].PowerDomains...)
	originalGroupNames := []string{input.Groups[0].Name, input.Groups[1].Name, input.Groups[2].Name}
	originalBefore := append([]string(nil), input.Groups[1].Before...)

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if !reflect.DeepEqual(input.Triggers[0].UPSDevices, originalTriggerUPSDevices) {
		t.Fatalf("compile mutated trigger ups devices: got %#v, want %#v", input.Triggers[0].UPSDevices, originalTriggerUPSDevices)
	}
	if !reflect.DeepEqual(input.Triggers[0].PowerDomains, originalTriggerPowerDomains) {
		t.Fatalf("compile mutated trigger power domains: got %#v, want %#v", input.Triggers[0].PowerDomains, originalTriggerPowerDomains)
	}
	if got := []string{input.Groups[0].Name, input.Groups[1].Name, input.Groups[2].Name}; !reflect.DeepEqual(got, originalGroupNames) {
		t.Fatalf("compile mutated group order: got %#v, want %#v", got, originalGroupNames)
	}
	if !reflect.DeepEqual(input.Groups[1].Before, originalBefore) {
		t.Fatalf("compile mutated group dependencies: got %#v, want %#v", input.Groups[1].Before, originalBefore)
	}
}

func TestCompileExcludesTelemetryFromPlanIdentity(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Steps: []Step{
			{
				ID:       "wait-for-drain",
				Action:   "Wait",
				Duration: Duration{Duration: 30 * time.Second},
			},
		},
	}
	runtimeSeconds := int64(1200)
	chargePercent := int32(96)
	onBattery := false

	noTelemetryPlan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile without telemetry to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	telemetryPlan, diagnostics, err := Compile(input, TelemetryInputs{
		PowerDomains: []PowerDomainSnapshot{
			{
				Domain:                  "core",
				RuntimeRemainingSeconds: &runtimeSeconds,
				ChargePercent:           &chargePercent,
				OnBattery:               &onBattery,
			},
		},
	})
	if err != nil {
		t.Fatalf("expected compile with telemetry to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if noTelemetryPlan.Hash != telemetryPlan.Hash {
		t.Fatalf("expected telemetry to be excluded from plan hash, got %q and %q", noTelemetryPlan.Hash, telemetryPlan.Hash)
	}
	if noTelemetryPlan.StructuralHash != telemetryPlan.StructuralHash {
		t.Fatalf("expected telemetry to be excluded from structural hash, got %q and %q", noTelemetryPlan.StructuralHash, telemetryPlan.StructuralHash)
	}
	if noTelemetryPlan.Feasibility.Verdict == telemetryPlan.Feasibility.Verdict {
		t.Fatalf("expected telemetry to change advisory feasibility, both were %q", noTelemetryPlan.Feasibility.Verdict)
	}
}

func TestCompileIncludesResolvedInputHashInPlanIdentity(t *testing.T) {
	input := StructuralInputs{
		ResolvedInputHash: "inventory-a",
		Triggers:          []Trigger{{Type: "OnBattery"}},
		Steps: []Step{
			{
				ID:     "notify",
				Action: "Notify",
			},
		},
	}

	first, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	input.ResolvedInputHash = "inventory-b"
	second, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected second compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if first.Hash == second.Hash {
		t.Fatalf("expected resolved input hash to affect plan hash, both were %q", first.Hash)
	}
	if first.StructuralHash == second.StructuralHash {
		t.Fatalf("expected resolved input hash to affect structural hash, both were %q", first.StructuralHash)
	}
}

func TestCompileRejectsUnknownDependency(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Before: []string{
					"databases",
				},
			},
		},
	}

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "UnknownDependency") {
		t.Fatalf("expected UnknownDependency diagnostic, got %#v", diagnostics)
	}
}

func TestCompileRejectsDependencyCycle(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Before: []string{
					"databases",
				},
			},
			{
				Name:   "databases",
				Action: "ScaleWorkload",
				Before: []string{
					"applications",
				},
			},
		},
	}

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "DependencyCycle") {
		t.Fatalf("expected DependencyCycle diagnostic, got %#v", diagnostics)
	}
}

func hasDiagnosticReason(diagnostics []Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			return true
		}
	}
	return false
}

func waveGroups(waves []Wave) [][]string {
	groups := make([][]string, 0, len(waves))
	for _, wave := range waves {
		groups = append(groups, wave.Groups)
	}
	return groups
}
