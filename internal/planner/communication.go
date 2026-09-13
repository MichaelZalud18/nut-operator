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
	"sort"
	"strings"
)

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
	dependents := map[string][]string{}
	for _, dependency := range input.CommunicationDependencies {
		dependents[dependency.Carrier] = append(dependents[dependency.Carrier], dependency.Dependent)
	}
	visited := map[string]struct{}{}
	consumers := map[string]struct{}{}
	var pending []string
	// Unknown carrier power cannot prove its dependents are outside this outage.
	for _, dependency := range input.CommunicationDependencies {
		if len(carrierPowerDomains(input, dependency.Carrier)) == 0 {
			if _, seen := visited[dependency.Carrier]; !seen {
				visited[dependency.Carrier] = struct{}{}
				pending = append(pending, dependency.Carrier)
			}
		}
	}
	for _, domain := range input.PowerDomains {
		if _, affected := affectedDomains[domain.Name]; !affected {
			continue
		}
		for _, entity := range domainEntities(domain) {
			if _, seen := visited[entity]; !seen {
				visited[entity] = struct{}{}
				pending = append(pending, entity)
			}
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
	return diagnostics
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
