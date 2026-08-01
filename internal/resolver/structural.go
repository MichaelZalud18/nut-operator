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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
)

const (
	DiagnosticError   = "Error"
	DiagnosticWarning = "Warning"

	DiagnosticSourceInventory  = "Inventory"
	DiagnosticSourceCapability = "Capability"
)

var ErrRejected = errors.New("resolver rejected structural inputs")

// ResolveStructural compiles inventory topology and matches UPS capability
// profiles. All external reads must happen before calling this function.
func ResolveStructural(inputs StructuralInputs) (StructuralBundle, []Diagnostic, error) {
	topology, inventoryDiagnostics, err := inventory.Compile(inputs.Inventory)
	diagnostics := resolverInventoryDiagnostics(inventoryDiagnostics)
	if err != nil {
		return StructuralBundle{}, diagnostics, ErrRejected
	}

	matches, capabilityDiagnostics, err := matchCapabilities(topology, inputs.Profiles)
	diagnostics = append(diagnostics, capabilityDiagnostics...)
	if err != nil {
		return StructuralBundle{}, diagnostics, ErrRejected
	}

	bundle := StructuralBundle{
		SourceID:          inputs.SourceID,
		ObservedAt:        inputs.ObservedAt,
		Topology:          topology,
		CapabilityMatches: matches,
	}
	bundle.Hash = stableHash(struct {
		SourceID          string                   `json:"sourceID,omitempty"`
		ObservedAt        string                   `json:"observedAt,omitempty"`
		TopologyHash      string                   `json:"topologyHash,omitempty"`
		CapabilityMatches []capability.MatchResult `json:"capabilityMatches,omitempty"`
	}{
		SourceID:          bundle.SourceID,
		ObservedAt:        bundle.ObservedAt,
		TopologyHash:      topology.Hash,
		CapabilityMatches: bundle.CapabilityMatches,
	})

	return bundle, diagnostics, nil
}

func matchCapabilities(topology inventory.Topology, profiles []capability.Profile) ([]capability.MatchResult, []Diagnostic, error) {
	var diagnostics []Diagnostic
	var matches []capability.MatchResult
	for _, entity := range topology.Entities {
		if entity.Kind != inventory.EntityKindUPSDevice {
			continue
		}
		result, profileDiagnostics, err := capability.Match(capability.Device{
			ID:           entity.ID,
			Model:        entity.Model,
			Firmware:     entity.Firmware,
			DriverFamily: entity.DriverFamily,
		}, profiles)
		diagnostics = append(diagnostics, resolverCapabilityDiagnostics(profileDiagnostics)...)
		if err != nil {
			return nil, diagnostics, err
		}
		matches = append(matches, result)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].DeviceID < matches[j].DeviceID
	})
	return matches, diagnostics, nil
}

func resolverInventoryDiagnostics(diagnostics []inventory.Diagnostic) []Diagnostic {
	converted := make([]Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		converted = append(converted, Diagnostic{
			Severity: diagnostic.Severity,
			Source:   DiagnosticSourceInventory,
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	return converted
}

func resolverCapabilityDiagnostics(diagnostics []capability.Diagnostic) []Diagnostic {
	converted := make([]Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		converted = append(converted, Diagnostic{
			Severity: diagnostic.Severity,
			Source:   DiagnosticSourceCapability,
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	return converted
}

func stableHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("resolver input could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
