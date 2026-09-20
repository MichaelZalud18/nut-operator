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

import "slices"

// communicationIndex owns only topology that survives action scoping. Construct
// it once from normalized inputs and treat it as read-only for that compilation.
// Group membership and service declarations stay on the current inputs, so a
// pre-scope index cannot reintroduce pruned actions. No input slices are retained.
type communicationIndex struct {
	upstream   map[string][]CommunicationDependency
	dependents map[string][]string
	supplies   map[string]CommunicationSupplyConstraint
}

func newCommunicationIndex(input StructuralInputs) *communicationIndex {
	index := &communicationIndex{
		upstream:   map[string][]CommunicationDependency{},
		dependents: map[string][]string{},
		supplies:   map[string]CommunicationSupplyConstraint{},
	}
	for _, dependency := range normalizeCommunicationDependencies(input.CommunicationDependencies) {
		index.upstream[dependency.Dependent] = append(index.upstream[dependency.Dependent], dependency)
		index.dependents[dependency.Carrier] = append(index.dependents[dependency.Carrier], dependency.Dependent)
	}
	domains := map[string]map[string]struct{}{}
	devices := map[string]map[string]struct{}{}
	for _, domain := range input.PowerDomains {
		for _, entity := range domainEntities(domain) {
			if domains[entity] == nil {
				domains[entity] = map[string]struct{}{}
				devices[entity] = map[string]struct{}{}
			}
			domains[entity][domain.Name] = struct{}{}
			for _, device := range domain.UPSDevices {
				if device != "" {
					devices[entity][device] = struct{}{}
				}
			}
		}
	}
	for entity, names := range domains {
		index.supplies[entity] = CommunicationSupplyConstraint{
			Carrier: entity, PowerDomains: sortedSetKeys(names),
			UPSDevices: sortedSetKeys(devices[entity]), UnknownSupply: len(devices[entity]) == 0,
		}
	}
	return index
}

func (index *communicationIndex) supplyConstraints(carriers []string) []CommunicationSupplyConstraint {
	var constraints []CommunicationSupplyConstraint
	for _, carrier := range carriers {
		supply, known := index.supplies[carrier]
		if !known {
			// Preserve empty (non-nil) domain/device lists in published artifacts.
			supply = CommunicationSupplyConstraint{Carrier: carrier, PowerDomains: []string{}, UPSDevices: []string{}, UnknownSupply: true}
		} else {
			supply.PowerDomains = slices.Clone(supply.PowerDomains)
			supply.UPSDevices = slices.Clone(supply.UPSDevices)
		}
		constraints = append(constraints, supply)
	}
	return constraints
}
