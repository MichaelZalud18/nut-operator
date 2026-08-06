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

package resolver

import (
	"errors"
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

func TestResolveStructuralBuildsTopologyAndCapabilityMatches(t *testing.T) {
	inputs := StructuralInputs{
		SourceID: "resolver-test",
		Inventory: inventory.Snapshot{
			Entities: []inventory.Entity{
				{
					ID:           "ups-a",
					Kind:         inventory.EntityKindUPSDevice,
					PowerDomains: []string{"rack-a"},
					Model:        "Vendor Model",
					Firmware:     "1.0.0",
					DriverFamily: "snmp-ups",
				},
				{ID: "node-a", Kind: inventory.EntityKindNode},
				{ID: "switch-a", Kind: inventory.EntityKindPowerInfrastructure},
			},
			Edges: []inventory.Edge{
				{From: "ups-a", To: "node-a", Relation: inventory.EdgeRelationFeeds, Input: "psu"},
				{From: "switch-a", To: "node-a", Relation: inventory.EdgeRelationCarries},
			},
		},
		Profiles: []capability.Profile{
			{
				ID:      "vendor-model",
				Version: "1.0.0",
				Source:  capability.ProfileSourceCRD,
				Selector: capability.ProfileSelector{
					Model:    "Vendor Model",
					Firmware: "1.0.0",
				},
				TelemetryVariables: []string{"battery.runtime", "ups.status"},
			},
			{
				ID:      "universal",
				Version: "1.0.0",
				Source:  capability.ProfileSourceBundled,
				Selector: capability.ProfileSelector{
					Universal: true,
				},
			},
		},
	}

	bundle, diagnostics, err := ResolveStructural(inputs)
	if err != nil {
		t.Fatalf("expected structural resolve to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if bundle.Hash == "" {
		t.Fatal("expected structural bundle hash to be set")
	}
	if len(bundle.Topology.Domains) != 1 {
		t.Fatalf("expected one derived power domain, got %#v", bundle.Topology.Domains)
	}
	if len(bundle.CapabilityMatches) != 1 {
		t.Fatalf("expected one capability match, got %#v", bundle.CapabilityMatches)
	}
	if got := bundle.CapabilityMatches[0].ProfileID; got != "vendor-model" {
		t.Fatalf("expected vendor-model profile match, got %q", got)
	}
}

func TestResolveStructuralPropagatesInventoryRejection(t *testing.T) {
	inputs := StructuralInputs{
		Inventory: inventory.Snapshot{
			Entities: []inventory.Entity{
				{ID: "ups-a", Kind: inventory.EntityKindUPSDevice, PowerDomains: []string{"rack-a"}},
				{ID: "node-a", Kind: inventory.EntityKindNode},
			},
		},
		Profiles: []capability.Profile{
			universalProfile(),
		},
	}

	_, diagnostics, err := ResolveStructural(inputs)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasResolverDiagnostic(diagnostics, DiagnosticSourceInventory, "PowerPlanningOrphan") {
		t.Fatalf("expected inventory orphan diagnostic, got %#v", diagnostics)
	}
}

func TestResolveStructuralPropagatesCapabilityWarnings(t *testing.T) {
	inputs := StructuralInputs{
		Inventory: inventory.Snapshot{
			Entities: []inventory.Entity{
				{
					ID:           "ups-a",
					Kind:         inventory.EntityKindUPSDevice,
					PowerDomains: []string{"rack-a"},
					Model:        "Unknown Model",
					DriverFamily: "snmp-ups",
				},
				{ID: "node-a", Kind: inventory.EntityKindNode, CommunicationPathExempt: true},
			},
			Edges: []inventory.Edge{
				{From: "ups-a", To: "node-a", Relation: inventory.EdgeRelationFeeds, Input: "psu"},
			},
		},
		Profiles: []capability.Profile{
			universalProfile(),
		},
	}

	bundle, diagnostics, err := ResolveStructural(inputs)
	if err != nil {
		t.Fatalf("expected structural resolve to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if len(bundle.CapabilityMatches) != 1 || !bundle.CapabilityMatches[0].Unidentified {
		t.Fatalf("expected an unidentified-device match, got %#v", bundle.CapabilityMatches)
	}
	if !hasResolverDiagnostic(diagnostics, DiagnosticSourceCapability, "DeviceUnidentified") {
		t.Fatalf("expected capability fallback diagnostic, got %#v", diagnostics)
	}
}

func TestResolveStructuralPropagatesInventoryWarnings(t *testing.T) {
	inputs := StructuralInputs{
		Inventory: inventory.Snapshot{
			Entities: []inventory.Entity{
				{
					ID:           "ups-a",
					Kind:         inventory.EntityKindUPSDevice,
					PowerDomains: []string{"rack-a"},
					Model:        "Vendor Model",
				},
				{ID: "node-a", Kind: inventory.EntityKindNode},
			},
			Edges: []inventory.Edge{
				{From: "ups-a", To: "node-a", Relation: inventory.EdgeRelationFeeds, Input: "psu"},
			},
		},
		Profiles: []capability.Profile{
			{
				ID:      "vendor-model",
				Version: "1.0.0",
				Source:  capability.ProfileSourceCRD,
				Selector: capability.ProfileSelector{
					Model: "Vendor Model",
				},
			},
			universalProfile(),
		},
	}

	_, diagnostics, err := ResolveStructural(inputs)
	if err != nil {
		t.Fatalf("expected structural resolve to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if !hasResolverDiagnostic(diagnostics, DiagnosticSourceInventory, "CommunicationPathUnmodeled") {
		t.Fatalf("expected communication path warning, got %#v", diagnostics)
	}
}

func TestAttachResolvedInputHashMarksPlannerInput(t *testing.T) {
	flow := planner.StructuralInputs{
		SourceID: "shutdown-flow",
		Triggers: []planner.Trigger{
			{Type: "OnBattery"},
		},
		Steps: []planner.Step{
			{ID: "notify", Action: "Notify"},
		},
	}
	bundle := StructuralBundle{
		Hash:       "resolver-hash",
		ObservedAt: "2026-08-01T00:00:00Z",
	}

	attached := AttachResolvedInputHash(flow, bundle)
	if attached.ResolvedInputHash != "resolver-hash" {
		t.Fatalf("expected resolved input hash to be attached, got %q", attached.ResolvedInputHash)
	}
	if attached.ObservedAt != "2026-08-01T00:00:00Z" {
		t.Fatalf("expected observedAt from bundle, got %q", attached.ObservedAt)
	}
	if flow.ResolvedInputHash != "" {
		t.Fatalf("expected original flow input to remain unchanged, got %q", flow.ResolvedInputHash)
	}
}

func universalProfile() capability.Profile {
	return capability.Profile{
		ID:      "universal",
		Version: "1.0.0",
		Source:  capability.ProfileSourceBundled,
		Selector: capability.ProfileSelector{
			Universal: true,
		},
	}
}

func hasResolverDiagnostic(diagnostics []Diagnostic, source, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Source == source && diagnostic.Reason == reason {
			return true
		}
	}
	return false
}
