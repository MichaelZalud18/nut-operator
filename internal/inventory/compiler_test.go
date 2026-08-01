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

package inventory

import (
	"errors"
	"reflect"
	"testing"
)

func TestCompileDerivesPowerDomainsAndCommunicationOrders(t *testing.T) {
	snapshot := Snapshot{
		SourceID: "test-inventory",
		Version:  "2026-08-01",
		Entities: []Entity{
			{ID: "ups-a", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-a"}},
			{ID: "ups-b", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-b"}},
			{ID: "pdu-a", Kind: EntityKindPowerInfrastructure},
			{ID: "switch-a", Kind: EntityKindPowerInfrastructure},
			{ID: "node-a", Kind: EntityKindNode},
		},
		Edges: []Edge{
			{From: "ups-a", To: "pdu-a", Relation: EdgeRelationFeeds, Input: "line-a"},
			{From: "pdu-a", To: "node-a", Relation: EdgeRelationFeeds, Input: "psu-a"},
			{From: "ups-b", To: "node-a", Relation: EdgeRelationFeeds, Input: "psu-b"},
			{From: "ups-a", To: "switch-a", Relation: EdgeRelationFeeds, Input: "line-a"},
			{From: "switch-a", To: "node-a", Relation: EdgeRelationCarries, SourceID: "lan-path"},
		},
	}

	topology, diagnostics, err := Compile(snapshot)
	if err != nil {
		t.Fatalf("expected inventory compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if topology.Hash == "" {
		t.Fatal("expected topology hash to be set")
	}
	if got, want := domainNodes(topology.Domains), map[string][]string{
		"rack-a": {"node-a"},
		"rack-b": {"node-a"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected domain nodes %#v, got %#v", want, got)
	}
	if got, want := domainInfrastructure(topology.Domains), map[string][]string{
		"rack-a": {"pdu-a", "switch-a"},
		"rack-b": nil,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected domain infrastructure %#v, got %#v", want, got)
	}
	if got, want := topology.CommunicationOrders, []DerivedEdge{
		{From: "node-a", To: "switch-a", Reason: "CommunicationPath", Source: "lan-path"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected communication orders %#v, got %#v", want, got)
	}
}

func TestCompileIsDeterministicAndDoesNotMutateInput(t *testing.T) {
	snapshot := Snapshot{
		Entities: []Entity{
			{ID: "node-a", Kind: EntityKindNode},
			{ID: "ups-a", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-b", "rack-a"}},
			{ID: "switch-a", Kind: EntityKindPowerInfrastructure},
		},
		Edges: []Edge{
			{From: "switch-a", To: "node-a", Relation: EdgeRelationCarries},
			{From: "ups-a", To: "switch-a", Relation: EdgeRelationFeeds, Input: "line"},
			{From: "switch-a", To: "node-a", Relation: EdgeRelationFeeds, Input: "psu"},
		},
	}
	originalEntities := append([]Entity(nil), snapshot.Entities...)
	originalPowerDomains := append([]string(nil), snapshot.Entities[1].PowerDomains...)
	originalEdges := append([]Edge(nil), snapshot.Edges...)

	first, diagnostics, err := Compile(snapshot)
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	second, diagnostics, err := Compile(snapshot)
	if err != nil {
		t.Fatalf("expected second compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected deterministic topology\nfirst: %#v\nsecond: %#v", first, second)
	}
	if !reflect.DeepEqual(snapshot.Entities, originalEntities) {
		t.Fatalf("compile mutated entity order: got %#v, want %#v", snapshot.Entities, originalEntities)
	}
	if !reflect.DeepEqual(snapshot.Entities[1].PowerDomains, originalPowerDomains) {
		t.Fatalf("compile mutated power domain order: got %#v, want %#v", snapshot.Entities[1].PowerDomains, originalPowerDomains)
	}
	if !reflect.DeepEqual(snapshot.Edges, originalEdges) {
		t.Fatalf("compile mutated edge order: got %#v, want %#v", snapshot.Edges, originalEdges)
	}
}

func TestCompileRejectsPowerPlanningOrphan(t *testing.T) {
	snapshot := Snapshot{
		Entities: []Entity{
			{ID: "ups-a", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-a"}},
			{ID: "node-a", Kind: EntityKindNode, CommunicationPathExempt: true},
		},
	}

	_, diagnostics, err := Compile(snapshot)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "PowerPlanningOrphan") {
		t.Fatalf("expected PowerPlanningOrphan diagnostic, got %#v", diagnostics)
	}
}

func TestCompileAllowsExplicitPowerPlanningExemption(t *testing.T) {
	snapshot := Snapshot{
		Entities: []Entity{
			{ID: "ups-a", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-a"}},
			{ID: "node-a", Kind: EntityKindNode, PowerPlanningExempt: true, CommunicationPathExempt: true},
		},
	}

	_, diagnostics, err := Compile(snapshot)
	if err != nil {
		t.Fatalf("expected compile to succeed with explicit exemption, got %v with diagnostics %#v", err, diagnostics)
	}
}

func TestCompileWarnsForUnmodeledCommunicationPath(t *testing.T) {
	snapshot := Snapshot{
		Entities: []Entity{
			{ID: "ups-a", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-a"}},
			{ID: "node-a", Kind: EntityKindNode},
		},
		Edges: []Edge{
			{From: "ups-a", To: "node-a", Relation: EdgeRelationFeeds, Input: "psu"},
		},
	}

	_, diagnostics, err := Compile(snapshot)
	if err != nil {
		t.Fatalf("expected compile to succeed with warning, got %v with diagnostics %#v", err, diagnostics)
	}
	if !hasDiagnosticReason(diagnostics, "CommunicationPathUnmodeled") {
		t.Fatalf("expected CommunicationPathUnmodeled diagnostic, got %#v", diagnostics)
	}
}

func TestCompileRejectsInvalidEdges(t *testing.T) {
	snapshot := Snapshot{
		Entities: []Entity{
			{ID: "ups-a", Kind: EntityKindUPSDevice, PowerDomains: []string{"rack-a"}},
			{ID: "node-a", Kind: EntityKindNode},
		},
		Edges: []Edge{
			{From: "ups-a", To: "node-a", Relation: EdgeRelationFeeds},
			{From: "ups-a", To: "missing-node", Relation: EdgeRelationCarries},
		},
	}

	_, diagnostics, err := Compile(snapshot)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "FeedInputRequired") {
		t.Fatalf("expected FeedInputRequired diagnostic, got %#v", diagnostics)
	}
	if !hasDiagnosticReason(diagnostics, "UnknownEdgeEndpoint") {
		t.Fatalf("expected UnknownEdgeEndpoint diagnostic, got %#v", diagnostics)
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

func domainNodes(domains []PowerDomain) map[string][]string {
	nodes := map[string][]string{}
	for _, domain := range domains {
		nodes[domain.Name] = domain.Nodes
	}
	return nodes
}

func domainInfrastructure(domains []PowerDomain) map[string][]string {
	infrastructure := map[string][]string{}
	for _, domain := range domains {
		infrastructure[domain.Name] = domain.Infrastructure
	}
	return infrastructure
}
