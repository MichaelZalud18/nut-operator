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
	"slices"
	"sort"
)

func sortedUnique(values []string) []string {
	if values == nil {
		return nil
	}
	result := slices.Clone(values)
	sort.Strings(result)
	return slices.Compact(result)
}

func normalizeCommunicationServices(paths []CommunicationServicePath) []CommunicationServicePath {
	result := slices.Clone(paths)
	for i := range result {
		result[i].Entities = sortedUnique(result[i].Entities)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Service < result[j].Service })
	return result
}

func communicationServiceDiagnostics(input StructuralInputs) []Diagnostic {
	var diagnostics []Diagnostic
	seen := map[string]bool{}
	for _, path := range input.CommunicationServices {
		if seen[path.Service] || (path.Service != "OperatorAPI" && path.Service != "NUT") || path.Exempt == (len(path.Entities) > 0) {
			diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "CommunicationServiceInvalid", Subject: path.Service, Message: "declare each OperatorAPI or NUT service once, with required inventory entities or an explicit exemption"})
		}
		seen[path.Service] = true
		for _, entity := range path.Entities {
			if entity == "" || (input.InventoryEntities != nil && !slices.Contains(input.InventoryEntities, entity)) {
				diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "CommunicationServiceEntityUnknown", Subject: entity, Message: fmt.Sprintf("%s service path references an unknown inventory entity %q", path.Service, entity)})
			}
		}
	}
	return diagnostics
}

// serviceCarrierPaths includes explicitly required entities themselves, as well
// as upstream carries paths. A service endpoint's own UPS is also a constraint.
func (index *communicationIndex) serviceCarrierPaths(service CommunicationServicePath) map[string][]CommunicationDependency {
	paths := map[string][]CommunicationDependency{}
	if service.Exempt {
		return paths
	}
	for _, entity := range sortedUnique(service.Entities) {
		paths[entity] = nil
		for carrier, path := range communicationPathsFrom(index.upstream, entity) {
			if _, found := paths[carrier]; !found {
				paths[carrier] = path
			}
		}
	}
	return paths
}

func sharedCommunicationCarriersWithIndex(input StructuralInputs, index *communicationIndex) []string {
	carriers := map[string]struct{}{}
	for _, service := range input.CommunicationServices {
		for carrier := range index.serviceCarrierPaths(service) {
			carriers[carrier] = struct{}{}
		}
	}
	return sortedSetKeys(carriers)
}

func collectCommunicationGraphEdgesWithIndex(groups []Group, membership []GroupNodeMembership, index *communicationIndex, services ...CommunicationServicePath) []GraphEdge {
	edges := collectNodeCommunicationGraphEdgesWithIndex(groups, membership, index)
	byID := map[string]int{}
	for i, edge := range edges {
		byID[edge.ID] = i
	}
	known := map[string]bool{}
	for _, group := range groups {
		known[group.Name] = true
	}
	for _, service := range normalizeCommunicationServices(services) {
		paths := index.serviceCarrierPaths(service)
		for _, release := range membership {
			if !known[release.Group] {
				continue
			}
			for _, carrier := range release.Releases {
				path, required := paths[carrier]
				if !required {
					continue
				}
				for _, group := range groups {
					// Shared services may be released only by the terminal action.
					// Distinct service-releasing groups therefore form a rejected cycle.
					if group.Name == release.Group {
						continue
					}
					id := graphEdgeID(group.Name, release.Group, GraphEdgeRelationCommunicationPath)
					index, found := byID[id]
					if !found {
						index = len(edges)
						byID[id] = index
						edges = append(edges, GraphEdge{ID: id, From: group.Name, To: release.Group, Relation: GraphEdgeRelationCommunicationPath, Provenance: GraphEdgeProvenanceDerived, Explanation: fmt.Sprintf("%s runs before %s because the shared communication path is required throughout the flow.", group.Name, release.Group)})
					}
					sources := []GraphSourceRef{{Kind: "ShutdownFlow", Name: service.Service, Field: "spec.communicationPaths"}, {Kind: "InventoryEntity", Name: carrier}}
					for _, dependency := range path {
						sources = append(sources, GraphSourceRef{Kind: "PowerInventoryEdge", Name: dependency.Source, Field: "carries"})
					}
					for _, source := range sources {
						if !slices.Contains(edges[index].Sources, source) {
							edges[index].Sources = append(edges[index].Sources, source)
						}
					}
				}
			}
		}
	}
	for i := range edges {
		sort.Slice(edges[i].Sources, func(a, b int) bool {
			x, y := edges[i].Sources[a], edges[i].Sources[b]
			if x.Kind != y.Kind {
				return x.Kind < y.Kind
			}
			if x.Name != y.Name {
				return x.Name < y.Name
			}
			return x.Field < y.Field
		})
	}
	sortGraph(Graph{Edges: edges})
	return edges
}

func communicationCoverageWithIndex(input StructuralInputs, consumers []string, index *communicationIndex) []CommunicationCoverage {
	var coverage []CommunicationCoverage
	for _, node := range consumers {
		state := "Unmodeled"
		if slices.Contains(input.CommunicationExemptNodes, node) {
			state = "Exempt"
		}
		if len(index.upstream[node]) > 0 {
			state = "Modeled"
		}
		coverage = append(coverage, CommunicationCoverage{Kind: "Node", Name: node, State: state})
	}
	for _, name := range []string{"NUT", "OperatorAPI"} {
		item := CommunicationCoverage{Kind: "Service", Name: name, State: "Unmodeled"}
		for _, service := range input.CommunicationServices {
			if service.Service != name {
				continue
			}
			item.Entities = sortedUnique(service.Entities)
			if service.Exempt {
				item.State = "Exempt"
			} else if len(service.Entities) > 0 {
				item.State = "Modeled"
			}
		}
		coverage = append(coverage, item)
	}
	return coverage
}

func completeServiceCoverage(coverage []CommunicationCoverage) bool {
	for _, item := range coverage {
		if item.Kind == "Service" && item.State == "Unmodeled" {
			return false
		}
	}
	return true
}

// A shared path is required by every action, including work outside the UPS
// domain and actions with no node selector. Do not prune that work on an outage
// of the shared path, or while a retained action releases one of its carriers.
func sharedCommunicationAffectedWithIndex(input StructuralInputs, affected map[string]struct{}, index *communicationIndex) bool {
	carriers := sharedCommunicationCarriersWithIndex(input, index)
	for _, supply := range index.supplyConstraints(carriers) {
		if supply.UnknownSupply {
			return true
		}
		for _, domain := range supply.PowerDomains {
			if _, ok := affected[domain]; ok {
				return true
			}
		}
	}
	membership := groupNodeSets(input.GroupNodes)
	unresolved := unresolvedGroupMembership(input.GroupNodes)
	affectedNodes := nodesForPowerDomains(input.PowerDomains, affected)
	includeUnmappedNodesWithIndex(membership, affectedNodes, index)
	for entity := range communicationDependentsWithIndex(input, affected, index) {
		affectedNodes[entity] = struct{}{}
	}
	retainReleasedCarrierConsumersWithIndex(input, membership, affectedNodes, index)
	for _, entry := range input.GroupNodes {
		if !unresolved[entry.Group] && outsideNodeSet(membership[entry.Group], affectedNodes) {
			continue
		}
		for _, node := range entry.Releases {
			if slices.Contains(carriers, node) {
				return true
			}
		}
	}
	return false
}
