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
	"testing"
)

func TestSharedCommunicationPathsOrderNodeLessWork(t *testing.T) {
	tier := int32(2)
	input := StructuralInputs{
		Triggers:              []Trigger{{Type: "OnBattery"}},
		Groups:                []Group{{Name: "notify", ShutdownTier: &tier}, {Name: "release", ShutdownTier: &tier}},
		GroupNodes:            []GroupNodeMembership{{Group: "release", Releases: []string{"switch"}}},
		CommunicationServices: []CommunicationServicePath{{Service: "OperatorAPI", Entities: []string{"switch"}}, {Service: "NUT", Exempt: true}},
		PowerDomains:          []PowerDomainMembership{{Name: "network", UPSDevices: []string{"network-ups"}, Members: []string{"switch"}}},
	}
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v %v", err, diagnostics)
	}
	if len(plan.Waves) != 2 || !reflect.DeepEqual(plan.Waves[0].Groups, []string{"notify"}) {
		t.Fatalf("waves: %+v", plan.Waves)
	}
	if !reflect.DeepEqual(plan.CommunicationBudget.UPSDevices, []string{"network-ups"}) {
		t.Fatalf("budget: %+v", plan.CommunicationBudget)
	}
	input.Groups[0].After = []string{"release"}
	if _, _, err := Compile(input, TelemetryInputs{}); err == nil {
		t.Fatal("accepted early shared-path release")
	}
}

func TestCommunicationCoverageWithoutCarries(t *testing.T) {
	input := StructuralInputs{Groups: []Group{{Name: "work"}}, GroupNodes: []GroupNodeMembership{{Group: "work", Acts: []string{"missing", "exempt"}}}, CommunicationExemptNodes: []string{"exempt"}}
	budget := CommunicationBudgetForInputs(input)
	if budget == nil {
		t.Fatal("missing coverage artifact")
	}
	for _, want := range []CommunicationCoverage{{Kind: "Node", Name: "missing", State: "Unmodeled"}, {Kind: "Node", Name: "exempt", State: "Exempt"}, {Kind: "Service", Name: "OperatorAPI", State: "Unmodeled"}, {Kind: "Service", Name: "NUT", State: "Unmodeled"}} {
		if !slices.ContainsFunc(budget.Coverage, func(got CommunicationCoverage) bool { return reflect.DeepEqual(got, want) }) {
			t.Errorf("missing %+v in %+v", want, budget.Coverage)
		}
	}
}

func TestServicePathsRetainWorkAcrossDomains(t *testing.T) {
	for _, mode := range []string{"affected", "unknown", "healthy", "released"} {
		t.Run(mode, func(t *testing.T) {
			input := StructuralInputs{
				Triggers:              []Trigger{{Type: "OnBattery", PowerDomains: []string{"network"}}},
				Groups:                []Group{{Name: "work"}, {Name: "release"}},
				GroupNodes:            []GroupNodeMembership{{Group: "work", Acts: []string{"healthy"}}, {Group: "release", Releases: []string{"affected"}}},
				PowerDomains:          []PowerDomainMembership{{Name: "network", UPSDevices: []string{"ups"}, Nodes: []string{"affected"}, Infrastructure: []string{"switch"}}, {Name: "compute", UPSDevices: []string{"compute-ups"}, Nodes: []string{"healthy"}}},
				CommunicationServices: []CommunicationServicePath{{Service: "OperatorAPI", Entities: []string{"switch"}}},
			}
			if mode == "unknown" {
				input.PowerDomains[0].Infrastructure = nil
			}
			if mode == "healthy" || mode == "released" {
				input.PowerDomains[0].Infrastructure = nil
				input.PowerDomains[1].Infrastructure = []string{"switch"}
			}
			if mode == "released" {
				input.GroupNodes[1].Releases = append(input.GroupNodes[1].Releases, "switch")
			}
			plan, diagnostics, err := Compile(input, TelemetryInputs{})
			if err != nil {
				t.Fatalf("compile: %v %+v", err, diagnostics)
			}
			want := 2
			if mode == "healthy" {
				want = 1
			}
			if len(plan.Steps) != want {
				t.Fatalf("steps: %+v diagnostics:%+v", plan.Steps, diagnostics)
			}
		})
	}
}

func TestServicePathValidation(t *testing.T) {
	for _, paths := range [][]CommunicationServicePath{
		{{Service: "invalid", Exempt: true}},
		{{Service: "NUT"}},
		{{Service: "NUT", Exempt: true, Entities: []string{"switch"}}},
		{{Service: "NUT", Exempt: true}, {Service: "NUT", Exempt: true}},
		{{Service: "OperatorAPI", Entities: []string{"typo"}}},
		{{Service: "OperatorAPI", Entities: []string{""}}},
	} {
		input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, Groups: []Group{{Name: "work"}}, InventoryEntities: []string{"switch"}, CommunicationServices: paths}
		if _, diagnostics, err := Compile(input, TelemetryInputs{}); err == nil {
			t.Fatalf("accepted %+v: %+v", paths, diagnostics)
		}
	}
}

func TestServiceBudgetCompleteCoverageAndDeterminism(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}}, Groups: []Group{{Name: "notify"}},
		CommunicationServices:     []CommunicationServicePath{{Service: "OperatorAPI", Entities: []string{"api", "api"}}, {Service: "NUT", Exempt: true}},
		CommunicationDependencies: []CommunicationDependency{{Dependent: "api", Carrier: "switch", Source: "api-path"}, {Dependent: "other", Carrier: "unrelated"}},
		PowerDomains:              []PowerDomainMembership{{Name: "network", UPSDevices: []string{"network-ups"}, Members: []string{"switch"}}, {Name: "compute", UPSDevices: []string{"compute-ups"}, Members: []string{"api"}}},
	}
	before := slices.Clone(input.CommunicationServices[0].Entities)
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v %+v", err, diagnostics)
	}
	if !slices.Equal(plan.CommunicationBudget.UPSDevices, []string{"compute-ups", "network-ups"}) || len(plan.CommunicationBudget.Supplies) != 2 {
		t.Fatalf("budget: %+v", plan.CommunicationBudget)
	}
	if !slices.Equal(input.CommunicationServices[0].Entities, before) {
		t.Fatal("mutated caller")
	}
	input.CommunicationServices[0].Entities = []string{"api"}
	slices.Reverse(input.CommunicationServices)
	again, _, err := Compile(input, TelemetryInputs{})
	if err != nil || !reflect.DeepEqual(plan, again) {
		t.Fatalf("normalization changed plan: %v", err)
	}
	input.CommunicationServices[1].Entities = []string{"switch"}
	changed, _, err := Compile(input, TelemetryInputs{})
	if err != nil || changed.Hash == plan.Hash {
		t.Fatal("service path change omitted from identity")
	}
}

func TestServicePathLinearOrderAndConflictingReleases(t *testing.T) {
	input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}},
		Steps:                 []Step{{ID: "work"}, {ID: "release"}},
		GroupNodes:            []GroupNodeMembership{{Group: "release", Releases: []string{"switch"}}},
		CommunicationServices: []CommunicationServicePath{{Service: "NUT", Entities: []string{"switch"}}},
	}
	if _, diagnostics, err := Compile(input, TelemetryInputs{}); err != nil {
		t.Fatalf("valid linear: %v %+v", err, diagnostics)
	}
	slices.Reverse(input.Steps)
	if _, _, err := Compile(input, TelemetryInputs{}); err == nil {
		t.Fatal("accepted reversed linear service release")
	}
	input.Steps = nil
	input.Groups = []Group{{Name: "release"}, {Name: "other-release"}}
	input.GroupNodes = append(input.GroupNodes, GroupNodeMembership{Group: "other-release", Releases: []string{"other-switch"}})
	input.CommunicationServices[0].Entities = append(input.CommunicationServices[0].Entities, "other-switch")
	if _, _, err := Compile(input, TelemetryInputs{}); err == nil {
		t.Fatal("accepted mutually dependent shared path release groups")
	}
}

func TestTerminalGroupReleasesIndependentSharedCarriers(t *testing.T) {
	input := StructuralInputs{
		Triggers:   []Trigger{{Type: "OnBattery"}},
		Groups:     []Group{{Name: "work"}, {Name: "terminal"}},
		GroupNodes: []GroupNodeMembership{{Group: "terminal", Releases: []string{"api-switch", "nut-switch"}}},
		CommunicationServices: []CommunicationServicePath{
			{Service: "OperatorAPI", Entities: []string{"api-switch"}},
			{Service: "NUT", Entities: []string{"nut-switch"}},
		},
	}
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil || len(plan.Waves) != 2 || !slices.Equal(plan.Waves[1].Groups, []string{"terminal"}) {
		t.Fatalf("independent terminal carriers: %+v %v %+v", plan.Waves, err, diagnostics)
	}
	input.CommunicationDependencies = []CommunicationDependency{{Dependent: "nut-switch", Carrier: "api-switch", Source: "shared-uplink"}}
	_, diagnostics, err = Compile(input, TelemetryInputs{})
	if err == nil || !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason == "CommunicationReleaseConflict" }) {
		t.Fatalf("accepted co-release of dependent and carrier: %v %+v", err, diagnostics)
	}
}
