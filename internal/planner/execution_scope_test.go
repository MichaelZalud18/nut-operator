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

func TestExecutionScopeUsesOnlyEligibleUPSRoots(t *testing.T) {
	for _, linear := range []bool{false, true} {
		input := StructuralInputs{
			Triggers:       []Trigger{{Type: "OnBattery", UPSDevices: []string{"ups-a"}}, {Type: "OnBattery", UPSDevices: []string{"ups-b"}}},
			Groups:         []Group{{Name: "a"}, {Name: "b"}, {Name: "mixed"}, {Name: "unknown"}, {Name: "no-target"}},
			GroupNodes:     []GroupNodeMembership{{Group: "a", Acts: []string{"a"}}, {Group: "b", Acts: []string{"b"}}, {Group: "mixed", Acts: []string{"a", "b"}}, {Group: "unknown", Acts: []string{"unmapped"}}},
			PowerDomains:   []PowerDomainMembership{{Name: "rack-a", UPSDevices: []string{"ups-a"}, Nodes: []string{"a"}}, {Name: "rack-b", UPSDevices: []string{"ups-b"}, Nodes: []string{"b"}}},
			ExecutionScope: &ExecutionScope{UPSDevices: []string{"ups-a"}},
		}
		if linear {
			for _, g := range input.Groups {
				input.Steps = append(input.Steps, Step{ID: g.Name})
			}
			input.Groups = nil
		}
		plan, diagnostics, err := Compile(input, TelemetryInputs{})
		if err != nil {
			t.Fatalf("compile: %v %+v", err, diagnostics)
		}
		if slices.Contains(compiledStepIDs(plan.Steps), "b") {
			t.Fatalf("healthy rack executes (linear=%v): %v", linear, compiledStepIDs(plan.Steps))
		}
		for _, retained := range []string{"a", "mixed", "unknown", "no-target"} {
			if !slices.Contains(compiledStepIDs(plan.Steps), retained) {
				t.Errorf("lost conservative target %s", retained)
			}
		}
		input.ExecutionScope.UPSDevices = []string{"ups-b", "ups-a", "ups-a"}
		both, _, err := Compile(input, TelemetryInputs{})
		if err != nil || len(both.Steps) != 5 {
			t.Fatalf("both domains: %+v %v", both.Steps, err)
		}
		input.ExecutionScope.UPSDevices = []string{"ups-a", "ups-b"}
		again, _, err := Compile(input, TelemetryInputs{})
		if err != nil || !reflect.DeepEqual(both, again) {
			t.Fatal("selection order changed plan")
		}
		if both.Hash == plan.Hash {
			t.Fatal("execution scope omitted from plan identity")
		}
	}
}

func TestExecutionScopeEmptyRejectedUnknownRootConservative(t *testing.T) {
	input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, Groups: []Group{{Name: "work"}}, ExecutionScope: &ExecutionScope{}}
	if _, _, err := Compile(input, TelemetryInputs{}); err == nil {
		t.Fatal("empty execution scope accepted")
	}
	input.ExecutionScope.UPSDevices = []string{"unmapped-ups"}
	plan, _, err := Compile(input, TelemetryInputs{})
	if err != nil || len(plan.Steps) != 1 {
		t.Fatalf("unmapped selection pruned work: %v %+v", err, plan.Steps)
	}
}

func TestFullyPrunedGroupsNeverActivateLinearFallback(t *testing.T) {
	input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, Groups: []Group{{Name: "healthy"}}, Steps: []Step{{ID: "fallback"}},
		GroupNodes:     []GroupNodeMembership{{Group: "healthy", Acts: []string{"node-b"}}},
		PowerDomains:   []PowerDomainMembership{{Name: "a", UPSDevices: []string{"ups-a"}, Nodes: []string{"node-a"}}, {Name: "b", UPSDevices: []string{"ups-b"}, Nodes: []string{"node-b"}}},
		ExecutionScope: &ExecutionScope{UPSDevices: []string{"ups-a"}},
	}
	if plan, diagnostics, err := Compile(input, TelemetryInputs{}); err == nil || len(plan.Steps) != 0 {
		t.Fatalf("empty grouped scope activated fallback: %+v %v %+v", plan.Steps, err, diagnostics)
	}
}

func TestAffectedDomainWithoutNodesDoesNotRunHealthyActions(t *testing.T) {
	for _, linear := range []bool{false, true} {
		input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, Groups: []Group{{Name: "healthy"}, {Name: "notify"}},
			GroupNodes:     []GroupNodeMembership{{Group: "healthy", Acts: []string{"node-b"}}},
			PowerDomains:   []PowerDomainMembership{{Name: "a", UPSDevices: []string{"ups-a"}}, {Name: "b", UPSDevices: []string{"ups-b"}, Nodes: []string{"node-b"}}},
			ExecutionScope: &ExecutionScope{UPSDevices: []string{"ups-a"}},
		}
		if linear {
			input.Groups = nil
			input.Steps = []Step{{ID: "healthy"}, {ID: "notify"}}
		}
		plan, diagnostics, err := Compile(input, TelemetryInputs{})
		if err != nil || !slices.Equal(compiledStepIDs(plan.Steps), []string{"notify"}) {
			t.Fatalf("empty affected domain linear=%v: %v %+v %+v", linear, err, plan.Steps, diagnostics)
		}
	}
}

func TestUnresolvedCarrierReleaseRetainsDependentWork(t *testing.T) {
	for _, linear := range []bool{false, true} {
		for _, shared := range []bool{false, true} {
			input := StructuralInputs{
				Triggers: []Trigger{{Type: "OnBattery"}}, ExecutionScope: &ExecutionScope{UPSDevices: []string{"ups-a"}},
				Groups: []Group{{Name: "consumer"}, {Name: "unrelated"}, {Name: "release"}},
				GroupNodes: []GroupNodeMembership{
					{Group: "consumer", Acts: []string{"consumer"}},
					{Group: "unrelated", Acts: []string{"other"}},
					{Group: "release", Releases: []string{"carrier"}, Unresolved: true},
				},
				PowerDomains: []PowerDomainMembership{
					{Name: "a", UPSDevices: []string{"ups-a"}},
					{Name: "b", UPSDevices: []string{"ups-b"}, Nodes: []string{"consumer", "carrier", "other"}, Infrastructure: []string{"transit"}},
				},
				CommunicationDependencies: []CommunicationDependency{{Dependent: "consumer", Carrier: "transit"}, {Dependent: "transit", Carrier: "carrier"}},
			}
			if shared {
				input.CommunicationServices = []CommunicationServicePath{{Service: "OperatorAPI", Entities: []string{"carrier"}}, {Service: "NUT", Exempt: true}}
			}
			if linear {
				for _, group := range input.Groups {
					input.Steps = append(input.Steps, Step{ID: group.Name})
				}
				input.Groups = nil
			}
			plan, diagnostics, err := Compile(input, TelemetryInputs{})
			want := []string{"consumer", "release"}
			if shared {
				want = []string{"consumer", "unrelated", "release"}
			}
			got := compiledStepIDs(plan.Steps)
			slices.Sort(want)
			slices.Sort(got)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("linear=%v shared=%v: %v got=%v want=%v %+v", linear, shared, err, got, want, diagnostics)
			}
		}
	}
}
