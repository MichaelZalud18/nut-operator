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

func TestCommunicationIndexPathsAndSupplies(t *testing.T) {
	input := StructuralInputs{
		CommunicationDependencies: []CommunicationDependency{
			{Dependent: "node", Carrier: "b", Source: "node-b"},
			{Dependent: "b", Carrier: "root", Source: "b-root"},
			{Dependent: "node", Carrier: "a", Source: "node-a-z"},
			{Dependent: "node", Carrier: "a", Source: "node-a"},
			{Dependent: "a", Carrier: "root", Source: "a-root"},
			{Dependent: "root", Carrier: "node", Source: "cycle"},
			{Dependent: "node", Carrier: "a", Source: "node-a"},
		},
		PowerDomains: []PowerDomainMembership{
			{Name: "z", Members: []string{"root", "root"}, UPSDevices: []string{"z-ups", ""}},
			{Name: "a", Infrastructure: []string{"root"}, Nodes: []string{"a"}, UPSDevices: []string{"a-ups"}},
			{Name: "empty-supply", Nodes: []string{"b"}},
		},
	}
	index := newCommunicationIndex(input)
	paths := communicationPathsFrom(index.upstream, "node")
	wantPath := []CommunicationDependency{{Dependent: "node", Carrier: "a", Source: "node-a"}, {Dependent: "a", Carrier: "root", Source: "a-root"}}
	if !reflect.DeepEqual(paths["root"], wantPath) || len(paths) != 3 {
		t.Fatalf("shortest path/provenance changed: %+v", paths)
	}
	wantSupplies := []CommunicationSupplyConstraint{
		{Carrier: "root", PowerDomains: []string{"a", "z"}, UPSDevices: []string{"a-ups", "z-ups"}},
		{Carrier: "b", PowerDomains: []string{"empty-supply"}, UPSDevices: []string{}, UnknownSupply: true},
		{Carrier: "unknown", PowerDomains: []string{}, UPSDevices: []string{}, UnknownSupply: true},
	}
	if got := index.supplyConstraints([]string{"root", "b", "unknown"}); !reflect.DeepEqual(got, wantSupplies) {
		t.Fatalf("supplies = %+v, want %+v", got, wantSupplies)
	}
	if got := sortedSetKeys(index.consumers([]string{"root", "root"})); !slices.Equal(got, []string{"a", "b", "node", "root"}) {
		t.Fatalf("cyclic consumers = %v", got)
	}
	service := CommunicationServicePath{Service: "NUT", Entities: []string{"node", "root"}}
	if got := index.serviceCarrierPaths(service); got["root"] != nil || !reflect.DeepEqual(got["a"], wantPath[:1]) {
		t.Fatalf("explicit service endpoint/provenance changed: %+v", got)
	}
}

func TestCommunicationIndexReuseAfterScoping(t *testing.T) {
	for _, linear := range []bool{false, true} {
		for _, mode := range []string{"pruned", "shared-service", "unknown-carrier", "unresolved", "mixed-release", "empty"} {
			name := mode + "/groups"
			if linear {
				name = mode + "/linear"
			}
			t.Run(name, func(t *testing.T) {
				input := communicationReleaseInput()
				input.Triggers[0].PowerDomains = []string{"rack"}
				input.Groups = append(input.Groups, Group{Name: "healthy", Action: "DrainNodes"})
				input.GroupNodes = append(input.GroupNodes, GroupNodeMembership{Group: "healthy", Acts: []string{"outside"}})
				input.PowerDomains = append(input.PowerDomains, PowerDomainMembership{Name: "healthy", Nodes: []string{"outside"}, Infrastructure: []string{"outside-switch"}, UPSDevices: []string{"outside-ups"}})
				input.CommunicationDependencies = append(input.CommunicationDependencies, CommunicationDependency{Dependent: "outside", Carrier: "outside-switch", Source: "outside-path"})
				switch mode {
				case "shared-service":
					input.CommunicationServices = []CommunicationServicePath{{Service: "OperatorAPI", Entities: []string{"carrier"}}, {Service: "NUT", Exempt: true}}
				case "unknown-carrier":
					input.CommunicationDependencies[1].Carrier = "unknown"
				case "unresolved":
					input.GroupNodes[3].Unresolved = true
				case "mixed-release":
					input.GroupNodes[2].Releases = append(input.GroupNodes[2].Releases, "outside-switch")
				case "empty":
					input.PowerDomains = append(input.PowerDomains, PowerDomainMembership{Name: "empty", UPSDevices: []string{"empty-ups"}})
					input.Triggers[0].PowerDomains = []string{"empty"}
				}
				if linear {
					for _, group := range input.Groups {
						input.Steps = append(input.Steps, Step{ID: group.Name, Action: group.Action})
					}
					input.Groups = nil
				}
				input, err := normalizeStructuralInputs(input)
				if err != nil {
					t.Fatal(err)
				}
				index := newCommunicationIndex(input)
				before := newCommunicationIndex(input)
				scoped, diagnostics := scopeStructuralInputsWithIndex(input, index)
				freshScoped, freshDiagnostics := scopeStructuralInputs(input)
				if !reflect.DeepEqual(scoped, freshScoped) || !reflect.DeepEqual(diagnostics, freshDiagnostics) {
					t.Fatal("reused index changed scoping")
				}
				if mode == "pruned" && len(scoped.GroupNodes) != 3 {
					t.Fatalf("healthy action was not pruned: %+v", scoped.GroupNodes)
				}
				if mode == "empty" && (len(scoped.Groups) != 0 || len(scoped.Steps) != 0) {
					t.Fatal("unrelated actions survived empty scope")
				}
				got := communicationIndexArtifacts(scoped, index)
				want := communicationIndexArtifacts(scoped, newCommunicationIndex(scoped))
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("pre-scope index changed artifacts: got %+v, want %+v", got, want)
				}
				gotHash, err := stableHash(got)
				if err != nil {
					t.Fatal(err)
				}
				wantHash, err := stableHash(want)
				if err != nil || gotHash != wantHash {
					t.Fatalf("artifact hash changed: %s != %s: %v", gotHash, wantHash, err)
				}
				budget := communicationBudgetWithIndex(scoped, index)
				if mode == "pruned" && slices.Contains(budget.UPSDevices, "outside-ups") {
					t.Fatal("pruned group leaked into budget")
				}
				if mode == "empty" && budget != nil {
					t.Fatal("empty scoped plan has a budget")
				}
				if !reflect.DeepEqual(index, before) {
					t.Fatal("scoping/artifact reads mutated index")
				}
			})
		}
	}
}

func communicationIndexArtifacts(input StructuralInputs, index *communicationIndex) []any {
	return []any{
		communicationDiagnosticsWithIndex(input, index),
		communicationBudgetWithIndex(input, index),
		communicationExplanationsWithIndex(input, index),
		collectLinearCommunicationGraphEdgesWithIndex(input, index),
		buildGroupGraphForInputs(input, index),
	}
}

func TestCommunicationIndexDoesNotAliasInputsOrOutputs(t *testing.T) {
	input := communicationReleaseInput()
	input.PowerDomains[0].Members = []string{"member"}
	input.PowerDomains[0].Infrastructure = []string{"switch"}
	index := newCommunicationIndex(input)
	want := newCommunicationIndex(input)
	input.CommunicationDependencies[0].Carrier = "changed"
	input.PowerDomains[0].Name = "changed"
	input.PowerDomains[0].UPSDevices[0] = "changed"
	input.PowerDomains[0].Nodes[0] = "changed"
	input.PowerDomains[0].Members[0] = "changed"
	input.PowerDomains[0].Infrastructure[0] = "changed"
	if !reflect.DeepEqual(index, want) {
		t.Fatal("index aliases input topology")
	}
	paths := communicationPathsFrom(index.upstream, "consumer")
	paths["carrier"][0].Source = "changed"
	delete(index.consumers([]string{"carrier"}), "consumer")
	supplies := index.consumerSupplyConstraints([]string{"consumer"})
	supplies[0].PowerDomains[0] = "changed"
	supplies[0].UPSDevices[0] = "changed"
	budget := communicationBudgetWithIndex(communicationReleaseInput(), index)
	budget.Supplies[0].PowerDomains[0] = "changed"
	budget.Supplies[0].UPSDevices[0] = "changed"
	graph := buildGroupGraphForInputs(communicationReleaseInput(), index)
	for i := range graph.Edges {
		if len(graph.Edges[i].Sources) > 0 {
			graph.Edges[i].Sources[0].Name = "changed"
		}
	}
	if !reflect.DeepEqual(index, want) {
		t.Fatal("index aliases returned artifacts")
	}
}
