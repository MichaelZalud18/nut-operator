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
	"fmt"
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

func communicationReleaseInput() StructuralInputs {
	return StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "drain-consumer", Action: "DrainNodes", Target: Target{NodeSelector: true}},
			{Name: "halt-consumer", Action: "AgentShutdown", Target: Target{AgentRefCount: 1}},
			{Name: "halt-carrier", Action: "AgentShutdown", Target: Target{AgentRefCount: 1}},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "drain-consumer", Acts: []string{"consumer"}},
			{Group: "halt-consumer", Releases: []string{"consumer"}},
			{Group: "halt-carrier", Releases: []string{"carrier"}},
		},
		CommunicationDependencies: []CommunicationDependency{{Dependent: "consumer", Carrier: "carrier", Source: "inventory/path"}},
		PowerDomains:              []PowerDomainMembership{{Name: "rack", UPSDevices: []string{"ups"}, Nodes: []string{"consumer", "carrier"}}},
	}
}

func TestCommunicationCarrierReleaseFollowsDependentWork(t *testing.T) {
	plan, diagnostics, err := Compile(communicationReleaseInput(), TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	var waves [][]string
	for _, wave := range plan.Waves {
		waves = append(waves, wave.Groups)
	}
	want := [][]string{{"drain-consumer"}, {"halt-consumer"}, {"halt-carrier"}}
	if !reflect.DeepEqual(waves, want) {
		t.Fatalf("communication carrier released too early: waves=%v, want %v", waves, want)
	}
	for _, group := range []string{"drain-consumer", "halt-consumer"} {
		edge := findGraphEdge(plan.Graph.Edges, group, "halt-carrier", "CommunicationPath")
		if edge == nil || edge.Provenance != GraphEdgeProvenanceDerived {
			t.Fatalf("missing derived order for %s", group)
		}
		if !slices.ContainsFunc(edge.Sources, func(s GraphSourceRef) bool { return s.Name == "inventory/path" }) {
			t.Fatalf("missing authored path provenance: %+v", edge)
		}
	}
}

func TestCommunicationReleaseTraversesNonActuatedTransit(t *testing.T) {
	input := communicationReleaseInput()
	input.CommunicationDependencies = []CommunicationDependency{
		{Dependent: "consumer", Carrier: "switch", Source: "access-link"},
		{Dependent: "switch", Carrier: "carrier", Source: "upstream-link"},
		{Dependent: "carrier", Carrier: "switch", Source: "transit-cycle"},
	}
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	if len(plan.Waves) != 3 || !slices.Equal(plan.Waves[2].Groups, []string{"halt-carrier"}) {
		t.Fatalf("transit path lost: %+v", plan.Waves)
	}
	if len(plan.Graph.Vertices) != len(input.Groups) {
		t.Fatal("non-actuated switch became a shutdown target")
	}
	edge := findGraphEdge(plan.Graph.Edges, "halt-consumer", "halt-carrier", "CommunicationPath")
	if edge == nil {
		t.Fatal("missing transitive order")
	}
	for _, source := range []string{"access-link", "upstream-link"} {
		if !slices.ContainsFunc(edge.Sources, func(s GraphSourceRef) bool { return s.Name == source }) {
			t.Fatalf("missing %s provenance: %+v", source, edge.Sources)
		}
	}
}

func TestCommunicationReleaseRejectsUnsafeOrders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*StructuralInputs)
		reason string
	}{
		{name: "declared reverse order", reason: "DependencyCycle", change: func(input *StructuralInputs) { input.Groups[2].Before = []string{"drain-consumer"} }},
		{name: "tier inversion", reason: "DependencyCycle", change: func(input *StructuralInputs) {
			early, late := int32(4), int32(2)
			input.Groups[0].ShutdownTier = &late
			input.Groups[1].ShutdownTier = &late
			input.Groups[2].ShutdownTier = &early
		}},
		{name: "mutually dependent releases", reason: "DependencyCycle", change: func(input *StructuralInputs) {
			input.CommunicationDependencies = append(input.CommunicationDependencies, CommunicationDependency{Dependent: "carrier", Carrier: "consumer", Source: "reverse-path"})
		}},
		{name: "one release group for both nodes", reason: "CommunicationReleaseConflict", change: func(input *StructuralInputs) {
			input.Groups = input.Groups[:2]
			input.GroupNodes = input.GroupNodes[:2]
			input.GroupNodes[1].Releases = []string{"consumer", "carrier"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := communicationReleaseInput()
			tc.change(&input)
			plan, diagnostics, err := Compile(input, TelemetryInputs{})
			if err == nil || len(plan.Waves) != 0 {
				t.Fatalf("unsafe order compiled: %+v", plan.Waves)
			}
			if !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason == tc.reason && d.Severity == DiagnosticError }) {
				t.Fatalf("missing %s: %+v", tc.reason, diagnostics)
			}
		})
	}
}

func TestCommunicationOrderProvenanceIsCanonical(t *testing.T) {
	input := communicationReleaseInput()
	input.GroupNodes[0].Acts = append(input.GroupNodes[0].Acts, "second-consumer")
	input.CommunicationDependencies = append(input.CommunicationDependencies,
		CommunicationDependency{Dependent: "second-consumer", Carrier: "carrier", Source: "second-path"},
		CommunicationDependency{Dependent: "consumer", Carrier: "carrier", Source: "alternate-path"},
	)
	first, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	slices.Reverse(input.Groups)
	slices.Reverse(input.GroupNodes)
	slices.Reverse(input.CommunicationDependencies)
	input.CommunicationDependencies = append(input.CommunicationDependencies, input.CommunicationDependencies[0])
	second, _, err := Compile(input, TelemetryInputs{})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("input order changed plan: %v", err)
	}
	edge := findGraphEdge(first.Graph.Edges, "drain-consumer", "halt-carrier", "CommunicationPath")
	if edge == nil {
		t.Fatal("missing derived order")
	}
	for _, node := range []string{"consumer", "second-consumer", "carrier"} {
		if !slices.Contains(edge.Sources, GraphSourceRef{Kind: "Node", Name: node}) {
			t.Fatalf("lost contributor %s: %+v", node, edge.Sources)
		}
	}
	if !strings.Contains(first.Diagrams.Mermaid, "CommunicationPath") || !hasExplanationReason(first.Explanations, "CommunicationPath") {
		t.Fatal("derived ordering absent from published graph explanations/diagrams")
	}
}

func TestCommunicationOrderIgnoresUnscheduledCarrierRelease(t *testing.T) {
	input := communicationReleaseInput()
	input.Groups = input.Groups[:2]
	// Membership can include stale or pruned groups. They must not become vertices.
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	if len(plan.Waves) != 2 || len(plan.Graph.Vertices) != 2 {
		t.Fatalf("unscheduled carrier became a target: %+v", plan)
	}
	if slices.ContainsFunc(plan.Graph.Edges, func(e GraphEdge) bool { return e.Relation == "CommunicationPath" }) {
		t.Fatal("derived edge points to an unscheduled group")
	}
}

func TestCommunicationScopeRetainsConsumersOfMixedReleaseGroup(t *testing.T) {
	input := communicationReleaseInput()
	input.Triggers = []Trigger{{Type: "OnBattery", PowerDomains: []string{"affected"}}}
	input.PowerDomains = append(input.PowerDomains, PowerDomainMembership{Name: "affected", UPSDevices: []string{"other-ups"}, Nodes: []string{"affected-node"}})
	// This retained group powers off a healthy-domain carrier alongside a node
	// in the affected domain. Its consumers still need to finish before it runs.
	input.GroupNodes[2].Releases = []string{"affected-node", "carrier"}
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	if len(plan.Waves) != 3 || !slices.Equal(plan.Waves[0].Groups, []string{"drain-consumer"}) || !slices.Equal(plan.Waves[2].Groups, []string{"halt-carrier"}) {
		t.Fatalf("consumer work pruned ahead of retained carrier release: %+v", plan.Waves)
	}
}

func TestCommunicationReleaseLinearOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			input := communicationReleaseInput()
			for _, group := range input.Groups {
				input.Steps = append(input.Steps, Step{ID: group.Name, Action: group.Action, Target: group.Target})
			}
			input.Groups = nil
			if reverse {
				slices.Reverse(input.Steps)
			}
			plan, diagnostics, err := Compile(input, TelemetryInputs{})
			if reverse {
				if err == nil || !slices.ContainsFunc(diagnostics, func(d Diagnostic) bool { return d.Reason == "CommunicationReleaseOrderInvalid" }) {
					t.Fatalf("unsafe linear order accepted: %+v", diagnostics)
				}
				return
			}
			if err != nil {
				t.Fatalf("compile: %v: %+v", err, diagnostics)
			}
			if len(plan.Steps) != 3 || plan.Steps[0].ID != "drain-consumer" || plan.Steps[2].ID != "halt-carrier" {
				t.Fatalf("linear order changed: %+v", plan.Steps)
			}
			if findGraphEdge(plan.Graph.Edges, "halt-consumer", "halt-carrier", "CommunicationPath") == nil {
				t.Fatal("linear plan lost communication constraint")
			}
		})
	}
}

func TestCommunicationScopeClosesOverNewlyRetainedReleases(t *testing.T) {
	input := communicationReleaseInput()
	input.Triggers = []Trigger{{Type: "OnBattery", PowerDomains: []string{"affected"}}}
	input.PowerDomains[0].Nodes = append(input.PowerDomains[0].Nodes, "second-carrier", "second-consumer")
	input.PowerDomains = append(input.PowerDomains, PowerDomainMembership{Name: "affected", Nodes: []string{"affected-node"}})
	input.GroupNodes[2].Releases = append(input.GroupNodes[2].Releases, "affected-node")
	input.GroupNodes[1].Releases = append(input.GroupNodes[1].Releases, "second-carrier")
	input.Groups = append(input.Groups, Group{Name: "drain-second", Action: "DrainNodes", Target: Target{NodeSelector: true}})
	input.GroupNodes = append(input.GroupNodes, GroupNodeMembership{Group: "drain-second", Acts: []string{"second-consumer"}})
	input.CommunicationDependencies = append(input.CommunicationDependencies, CommunicationDependency{Dependent: "second-consumer", Carrier: "second-carrier", Source: "second-path"})
	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("compile: %v: %+v", err, diagnostics)
	}
	if len(plan.Waves) != 3 || !slices.Equal(plan.Waves[0].Groups, []string{"drain-consumer", "drain-second"}) {
		t.Fatalf("dependent of newly retained mixed release was pruned: %+v", plan.Waves)
	}
}

func TestCommunicationSupplyDevices(t *testing.T) {
	input := StructuralInputs{
		CommunicationDependencies: []CommunicationDependency{
			{Dependent: "consumer", Carrier: "access"}, {Dependent: "access", Carrier: "uplink"},
			{Dependent: "uplink", Carrier: "access"}, {Dependent: "uplink", Carrier: "router"},
		},
		PowerDomains: []PowerDomainMembership{
			{Name: "compute", Nodes: []string{"consumer"}, UPSDevices: []string{"compute-ups"}},
			{Name: "access", Infrastructure: []string{"access"}, UPSDevices: []string{"access-ups", "shared-ups"}},
			{Name: "uplink", Infrastructure: []string{"uplink"}, UPSDevices: []string{"shared-ups"}},
		},
	}
	names, unknown := CommunicationSupplyDevices(input, []string{"consumer", "consumer"})
	if !unknown || !slices.Equal(names, []string{"access-ups", "shared-ups"}) {
		t.Fatalf("wrong partial supply closure: %v unknown=%v", names, unknown)
	}
	input.PowerDomains = append(input.PowerDomains, PowerDomainMembership{Name: "router", Nodes: []string{"router"}, UPSDevices: []string{"router-ups"}})
	names, unknown = CommunicationSupplyDevices(input, []string{"consumer"})
	if unknown || !slices.Equal(names, []string{"access-ups", "router-ups", "shared-ups"}) {
		t.Fatalf("wrong complete supply closure: %v unknown=%v", names, unknown)
	}
	names, unknown = CommunicationSupplyDevices(input, []string{"unmodeled"})
	if unknown || len(names) != 0 {
		t.Fatalf("invented supply for unmodeled path: %v unknown=%v", names, unknown)
	}
}
