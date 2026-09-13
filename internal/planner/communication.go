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
	"strings"
)

// collectCommunicationGraphEdges orders work on a dependent, including its own
// release, before release of the node carrying its control path. Non-actuated
// carriers stay topology entities; they do not become synthetic shutdown groups.
func collectCommunicationGraphEdges(groups []Group, membership []GroupNodeMembership, dependencies []CommunicationDependency) []GraphEdge {
	if len(groups) == 0 || len(membership) == 0 || len(dependencies) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, group := range groups {
		known[group.Name] = true
	}
	releasers := map[string][]string{}
	for _, entry := range membership {
		if !known[entry.Group] {
			continue
		}
		for _, node := range entry.Releases {
			releasers[node] = append(releasers[node], entry.Group)
		}
	}
	upstream := map[string][]CommunicationDependency{}
	for _, dependency := range normalizeCommunicationDependencies(dependencies) {
		upstream[dependency.Dependent] = append(upstream[dependency.Dependent], dependency)
	}
	pathsByNode := map[string]map[string][]CommunicationDependency{}
	type groupPair struct{ from, to string }
	edges := map[groupPair]GraphEdge{}
	for _, entry := range membership {
		if !known[entry.Group] {
			continue
		}
		for _, node := range slices.Concat(entry.Acts, entry.Releases) {
			paths, found := pathsByNode[node]
			if !found {
				paths = communicationPathsFrom(upstream, node)
				pathsByNode[node] = paths
			}
			for carrier, path := range paths {
				for _, releaser := range releasers[carrier] {
					key := groupPair{entry.Group, releaser}
					edge, exists := edges[key]
					if !exists {
						edge = GraphEdge{
							ID:   graphEdgeID(entry.Group, releaser, GraphEdgeRelationCommunicationPath),
							From: entry.Group, To: releaser,
							Relation: GraphEdgeRelationCommunicationPath, Provenance: GraphEdgeProvenanceDerived,
							Explanation: fmt.Sprintf("%s runs before %s because its dependent work needs a communication carrier released by %s.", entry.Group, releaser, releaser),
						}
					}
					sources := []GraphSourceRef{
						{Kind: "Node", Name: node},
						{Kind: "Node", Name: carrier},
					}
					for _, dependency := range path {
						sources = append(sources, GraphSourceRef{Kind: "PowerInventoryEdge", Name: dependency.Source, Field: "carries"})
					}
					for _, source := range sources {
						if !slices.Contains(edge.Sources, source) {
							edge.Sources = append(edge.Sources, source)
						}
					}
					edges[key] = edge
				}
			}
		}
	}
	result := make([]GraphEdge, 0, len(edges))
	for _, edge := range edges {
		sort.Slice(edge.Sources, func(i, j int) bool {
			a, b := edge.Sources[i], edge.Sources[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Field < b.Field
		})
		result = append(result, edge)
	}
	sortGraph(Graph{Edges: result})
	return result
}

// One deterministic shortest path proves each ordering constraint. Visiting each
// entity once bounds cycles and avoids enumerating exponentially many routes.
func communicationPathsFrom(upstream map[string][]CommunicationDependency, node string) map[string][]CommunicationDependency {
	paths := map[string][]CommunicationDependency{node: nil}
	pending := []string{node}
	for i := 0; i < len(pending); i++ {
		dependent := pending[i]
		for _, dependency := range upstream[dependent] {
			if _, seen := paths[dependency.Carrier]; seen {
				continue
			}
			paths[dependency.Carrier] = append(slices.Clone(paths[dependent]), dependency)
			pending = append(pending, dependency.Carrier)
		}
	}
	delete(paths, node)
	return paths
}

func normalizeCommunicationDependencies(dependencies []CommunicationDependency) []CommunicationDependency {
	if len(dependencies) == 0 {
		return nil
	}
	seen := map[CommunicationDependency]struct{}{}
	for _, dependency := range dependencies {
		seen[dependency] = struct{}{}
	}
	result := make([]CommunicationDependency, 0, len(seen))
	for dependency := range seen {
		result = append(result, dependency)
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Dependent != b.Dependent {
			return a.Dependent < b.Dependent
		}
		if a.Carrier != b.Carrier {
			return a.Carrier < b.Carrier
		}
		return a.Source < b.Source
	})
	return result
}

func domainEntities(domain PowerDomainMembership) []string {
	entities := append([]string(nil), domain.Members...)
	entities = append(entities, domain.Nodes...)
	entities = append(entities, domain.Infrastructure...)
	return append(entities, domain.UPSDevices...)
}

// communicationDependents includes transitive consumers when a carrier loses power.
// A visited set bounds cyclic carries topology without inventing a shutdown cycle.
func communicationDependents(input StructuralInputs, affectedDomains map[string]struct{}) map[string]struct{} {
	var seeds []string
	// Unknown carrier power cannot prove its dependents are outside this outage.
	for _, dependency := range input.CommunicationDependencies {
		if len(carrierPowerDomains(input, dependency.Carrier)) == 0 {
			seeds = append(seeds, dependency.Carrier)
		}
	}
	for _, domain := range input.PowerDomains {
		if _, affected := affectedDomains[domain.Name]; !affected {
			continue
		}
		seeds = append(seeds, domainEntities(domain)...)
	}
	return communicationConsumers(input.CommunicationDependencies, seeds)
}

func communicationConsumers(dependencies []CommunicationDependency, seeds []string) map[string]struct{} {
	dependents := map[string][]string{}
	for _, dependency := range dependencies {
		dependents[dependency.Carrier] = append(dependents[dependency.Carrier], dependency.Dependent)
	}
	visited := map[string]struct{}{}
	consumers := map[string]struct{}{}
	var pending []string
	for _, seed := range seeds {
		if _, seen := visited[seed]; !seen {
			visited[seed] = struct{}{}
			pending = append(pending, seed)
		}
	}
	for i := 0; i < len(pending); i++ {
		for _, dependent := range dependents[pending[i]] {
			consumers[dependent] = struct{}{}
			if _, seen := visited[dependent]; seen {
				continue
			}
			visited[dependent] = struct{}{}
			pending = append(pending, dependent)
		}
	}
	return consumers
}

// Retaining one mixed-domain release group can bring another communication
// consumer into scope. Grow to a fixed point before pruning any groups.
func retainReleasedCarrierConsumers(input StructuralInputs, membership map[string]map[string]struct{}, affected map[string]struct{}) {
	if len(input.CommunicationDependencies) == 0 {
		return
	}
	for {
		retained := map[string]bool{}
		for _, group := range input.Groups {
			nodes, known := membership[group.Name]
			retained[group.Name] = !known || len(nodes) == 0 || !outsideNodeSet(nodes, affected)
		}
		var released []string
		for _, entry := range input.GroupNodes {
			if retained[entry.Group] {
				released = append(released, entry.Releases...)
			}
		}
		changed := false
		for consumer := range communicationConsumers(input.CommunicationDependencies, released) {
			if _, included := affected[consumer]; !included {
				affected[consumer] = struct{}{}
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func carrierPowerDomains(input StructuralInputs, carrier string) []string {
	domains := map[string]struct{}{}
	for _, domain := range input.PowerDomains {
		for _, entity := range domainEntities(domain) {
			if entity == carrier {
				domains[domain.Name] = struct{}{}
				break
			}
		}
	}
	return sortedSetKeys(domains)
}

// CommunicationSupplyDevices resolves UPS supplies of all upstream carriers for
// the given consumers. Unknown supply remains explicit rather than disappearing
// from a minimum-runtime calculation.
func CommunicationSupplyDevices(input StructuralInputs, consumers []string) ([]string, bool) {
	return communicationSupplySummary(communicationSupplyConstraints(input, consumers))
}

func communicationSupplyConstraints(input StructuralInputs, consumers []string) []CommunicationSupplyConstraint {
	upstream := map[string][]CommunicationDependency{}
	for _, dependency := range normalizeCommunicationDependencies(input.CommunicationDependencies) {
		upstream[dependency.Dependent] = append(upstream[dependency.Dependent], dependency)
	}
	carriers := map[string]struct{}{}
	for _, consumer := range consumers {
		for carrier := range communicationPathsFrom(upstream, consumer) {
			carriers[carrier] = struct{}{}
		}
	}
	var constraints []CommunicationSupplyConstraint
	for _, carrier := range sortedSetKeys(carriers) {
		devices := map[string]struct{}{}
		domains := map[string]struct{}{}
		for _, domain := range input.PowerDomains {
			if !slices.Contains(domainEntities(domain), carrier) {
				continue
			}
			domains[domain.Name] = struct{}{}
			for _, device := range domain.UPSDevices {
				if device != "" {
					devices[device] = struct{}{}
				}
			}
		}
		constraints = append(constraints, CommunicationSupplyConstraint{
			Carrier: carrier, PowerDomains: sortedSetKeys(domains), UPSDevices: sortedSetKeys(devices), UnknownSupply: len(devices) == 0,
		})
	}
	return constraints
}

func communicationSupplySummary(constraints []CommunicationSupplyConstraint) ([]string, bool) {
	devices := map[string]struct{}{}
	unknown := false
	for _, constraint := range constraints {
		unknown = unknown || constraint.UnknownSupply
		for _, device := range constraint.UPSDevices {
			devices[device] = struct{}{}
		}
	}
	return sortedSetKeys(devices), unknown
}

// CommunicationBudgetForInputs is shared by publication and execution. Pass
// only the compiled actions so pruned work cannot expand the runtime envelope.
func CommunicationBudgetForInputs(input StructuralInputs) *CommunicationBudget {
	if len(input.CommunicationDependencies) == 0 {
		return nil
	}
	var actions []string
	for _, group := range input.Groups {
		actions = append(actions, group.Name)
	}
	if len(input.Groups) == 0 {
		for _, step := range input.Steps {
			actions = append(actions, step.ID)
		}
	}
	membership := groupNodeSets(input.GroupNodes)
	consumers := map[string]struct{}{}
	unresolved := map[string]struct{}{}
	for _, action := range actions {
		nodes := membership[action]
		if len(nodes) == 0 {
			unresolved[action] = struct{}{}
		}
		for node := range nodes {
			consumers[node] = struct{}{}
		}
	}
	if len(unresolved) > 0 {
		for _, dependency := range input.CommunicationDependencies {
			consumers[dependency.Dependent] = struct{}{}
		}
	}
	constraints := communicationSupplyConstraints(input, sortedSetKeys(consumers))
	names, _ := communicationSupplySummary(constraints)
	return &CommunicationBudget{Scope: "WholePlan", UPSDevices: names, UnresolvedActions: sortedSetKeys(unresolved), Supplies: constraints}
}

func communicationDiagnostics(input StructuralInputs) []Diagnostic {
	var diagnostics []Diagnostic
	unknown := map[string]struct{}{}
	for _, dependency := range input.CommunicationDependencies {
		if dependency.Dependent == "" || dependency.Carrier == "" || dependency.Dependent == dependency.Carrier {
			diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "CommunicationDependencyInvalid", Message: "communication dependencies require distinct, non-empty dependent and carrier identities"})
			continue
		}
		if len(carrierPowerDomains(input, dependency.Carrier)) == 0 {
			unknown[dependency.Carrier] = struct{}{}
		}
	}
	for _, carrier := range sortedSetKeys(unknown) {
		diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticWarning, Reason: "CommunicationPowerDomainUnknown", Subject: carrier, Message: fmt.Sprintf("communication carrier %q has no resolved supplying power domain; its power-loss constraints are unknown", carrier)})
	}
	orderEdges := collectCommunicationGraphEdges(input.Groups, input.GroupNodes, input.CommunicationDependencies)
	stepIndex := map[string]int{}
	if len(input.Groups) == 0 {
		orderEdges = collectLinearCommunicationGraphEdges(input)
		for i, step := range input.Steps {
			stepIndex[step.ID] = i
		}
	}
	for _, edge := range orderEdges {
		if edge.From == edge.To {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError, Reason: "CommunicationReleaseConflict", Subject: edge.From,
				Message: fmt.Sprintf("shutdown action %q releases a communication carrier while also targeting its dependent; split carrier release into a separate group or step so dependent work can finish first", edge.From),
			})
		} else if len(input.Groups) == 0 && stepIndex[edge.From] > stepIndex[edge.To] {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError, Reason: "CommunicationReleaseOrderInvalid", Subject: edge.To,
				Message: fmt.Sprintf("linear step %q must precede %q because the latter releases its communication carrier; reorder the declared steps", edge.From, edge.To),
			})
		}
	}
	return diagnostics
}

func collectLinearCommunicationGraphEdges(input StructuralInputs) []GraphEdge {
	if len(input.CommunicationDependencies) == 0 {
		return nil
	}
	groups := make([]Group, 0, len(input.Steps))
	for _, step := range input.Steps {
		groups = append(groups, Group{Name: step.ID})
	}
	return collectCommunicationGraphEdges(groups, input.GroupNodes, input.CommunicationDependencies)
}

func communicationExplanations(input StructuralInputs) []Explanation {
	var explanations []Explanation
	for i, dependency := range input.CommunicationDependencies {
		domains := carrierPowerDomains(input, dependency.Carrier)
		supply := strings.Join(domains, ", ")
		if supply == "" {
			supply = "unknown"
		}
		explanations = append(explanations, Explanation{
			ID: fmt.Sprintf("communication-%d", i), Subject: dependency.Dependent, Reason: "CommunicationPowerDependency",
			Message: fmt.Sprintf("%q depends on communication carrier %q (source %q), supplied by power domain(s) %s; dependent work remains in outage scope if the carrier's supply is affected", dependency.Dependent, dependency.Carrier, dependency.Source, supply),
		})
	}
	return explanations
}
