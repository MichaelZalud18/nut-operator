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
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCommunicationOutageScope(t *testing.T) {
	for _, tc := range []struct {
		name           string
		dependencies   []CommunicationDependency
		healthyCarrier bool
		wantRetained   bool
		wantUnknown    bool
	}{
		{name: "switch only outage", dependencies: []CommunicationDependency{{Dependent: "node-b", Carrier: "switch-a"}}, wantRetained: true},
		{name: "transitive path", dependencies: []CommunicationDependency{{Dependent: "switch-b", Carrier: "switch-a"}, {Dependent: "node-b", Carrier: "switch-b"}}, wantRetained: true},
		{name: "cyclic transit", dependencies: []CommunicationDependency{{Dependent: "switch-b", Carrier: "switch-a"}, {Dependent: "switch-a", Carrier: "switch-b"}, {Dependent: "node-b", Carrier: "switch-b"}}, wantRetained: true},
		{name: "unaffected carrier", dependencies: []CommunicationDependency{{Dependent: "node-b", Carrier: "switch-b"}}, healthyCarrier: true},
		{name: "unknown supply", dependencies: []CommunicationDependency{{Dependent: "node-b", Carrier: "unknown-switch"}}, wantRetained: true, wantUnknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := partialDomainInput(Trigger{Type: "OnBattery", PowerDomains: []string{"rack-a"}})
			input.PowerDomains[0].Nodes = nil
			if tc.healthyCarrier {
				input.PowerDomains[0].Nodes = []string{"other-affected-node"}
			}
			input.PowerDomains[0].Infrastructure = []string{"switch-a"}
			input.PowerDomains[1].Infrastructure = []string{"switch-b"}
			input.CommunicationDependencies = tc.dependencies
			plan, diagnostics, err := Compile(input, TelemetryInputs{})
			if err != nil {
				t.Fatalf("compile: %v: %+v", err, diagnostics)
			}
			scoped, _ := scopeStructuralInputs(input)
			retained := slices.ContainsFunc(scoped.Groups, func(g Group) bool { return g.Name == "drain-rack-b" })
			if retained != tc.wantRetained {
				t.Fatalf("retained=%v, want %v: %+v", retained, tc.wantRetained, scoped.Groups)
			}
			if slices.ContainsFunc(scoped.Groups, func(g Group) bool { return g.Name == "drain-rack-a" }) {
				t.Fatal("unrelated node retained")
			}
			unknown := slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason == "CommunicationPowerDomainUnknown" })
			if unknown != tc.wantUnknown {
				t.Fatalf("unknown=%v: %+v", unknown, diagnostics)
			}
			if !hasExplanationReason(plan.Explanations, "CommunicationPowerDependency") {
				t.Fatal("missing communication provenance")
			}
		})
	}
}

func TestCommunicationIdentityAndProvenance(t *testing.T) {
	input := partialDomainInput(Trigger{Type: "OnBattery"})
	input.PowerDomains[0].Infrastructure = []string{"switch-a"}
	input.PowerDomains[1].Infrastructure = []string{"switch-a"}
	input.CommunicationDependencies = []CommunicationDependency{
		{Dependent: "node-b", Carrier: "switch-a", Source: "inventory/carries-b"},
		{Dependent: "node-a", Carrier: "switch-a", Source: "inventory/carries-a"},
	}
	original := slices.Clone(input.CommunicationDependencies)
	first, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	if !reflect.DeepEqual(input.CommunicationDependencies, original) {
		t.Fatal("compile mutated input")
	}
	slices.Reverse(input.CommunicationDependencies)
	input.CommunicationDependencies = append(input.CommunicationDependencies, input.CommunicationDependencies[0])
	second, _, err := Compile(input, TelemetryInputs{})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("ordering or duplicates changed plan: %v", err)
	}
	if !slices.ContainsFunc(first.Explanations, func(e Explanation) bool {
		return e.Subject == "node-b" && strings.Contains(e.Message, "inventory/carries-b") && strings.Contains(e.Message, "rack-a, rack-b")
	}) {
		t.Fatal("missing edge and supply provenance")
	}
	input.CommunicationDependencies = nil
	third, _, err := Compile(input, TelemetryInputs{})
	if err != nil || first.StructuralHash == third.StructuralHash {
		t.Fatalf("dependency change must change structural identity: %v", err)
	}
}

func TestInvalidCommunicationDependencies(t *testing.T) {
	for _, dep := range []CommunicationDependency{{Carrier: "switch"}, {Dependent: "node"}, {Dependent: "same", Carrier: "same"}} {
		input := partialDomainInput(Trigger{Type: "OnBattery"})
		input.CommunicationDependencies = []CommunicationDependency{dep}
		_, diagnostics, err := Compile(input, TelemetryInputs{})
		if err == nil || !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool {
			return d.Reason == "CommunicationDependencyInvalid" && d.Severity == DiagnosticError
		}) {
			t.Fatalf("invalid edge accepted: %+v: %+v", dep, diagnostics)
		}
	}
}

func TestCommunicationMultipleSuppliesRetainConsumers(t *testing.T) {
	input := partialDomainInput(Trigger{Type: "OnBattery", PowerDomains: []string{"rack-a"}})
	input.PowerDomains[0].Nodes = nil
	input.PowerDomains[0].Infrastructure = []string{"switch"}
	input.PowerDomains[1].Infrastructure = []string{"switch"}
	input.CommunicationDependencies = []CommunicationDependency{{Dependent: "node-b", Carrier: "switch"}}
	scoped, _ := scopeStructuralInputs(input)
	if !slices.ContainsFunc(scoped.Groups, func(g Group) bool { return g.Name == "drain-rack-b" }) {
		t.Fatal("multiple supplying domains cannot establish redundant power guarantees")
	}
}

func TestCommunicationAbsentPreservesScopeFallback(t *testing.T) {
	input := partialDomainInput(Trigger{Type: "OnBattery", PowerDomains: []string{"rack-a"}})
	input.PowerDomains[0].Nodes = nil
	input.PowerDomains[0].Infrastructure = []string{"switch"}
	scoped, _ := scopeStructuralInputs(input)
	if !reflect.DeepEqual(scoped, input) {
		t.Fatal("without known affected nodes or communication consumers, retain existing scope fallback")
	}
}
