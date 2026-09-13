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

const DiagnosticReasonPowerDomainScopeApplied = "PowerDomainScopeApplied"

// scopeStructuralInputs implements OD-14 as a compile-time graph reduction.
//
// A domain-scoped trigger should not shut down unrelated power domains, but the
// planner also cannot invent certainty it was not given. Only groups whose
// resolved node membership is wholly outside the affected domains are omitted.
// Groups with no membership, mixed membership, or a global trigger remain in the
// plan.
func scopeStructuralInputs(input StructuralInputs) (StructuralInputs, []Diagnostic) {
	affectedDomains, scopedTrigger := affectedPowerDomains(input.Triggers, input.PowerDomains)
	if input.ExecutionScope != nil {
		if !executionRootsMapped(input.ExecutionScope.UPSDevices, input.PowerDomains) {
			return input, []Diagnostic{{Severity: DiagnosticWarning, Reason: "ExecutionPowerDomainUnknown", Message: "an eligible UPS has no resolved power domain; work cannot safely be pruned"}}
		}
		affectedDomains, scopedTrigger = affectedPowerDomains([]Trigger{{UPSDevices: input.ExecutionScope.UPSDevices}}, input.PowerDomains)
	}
	if !scopedTrigger || len(affectedDomains) == 0 || len(input.GroupNodes) == 0 {
		return input, nil
	}
	work := input
	if len(work.Groups) == 0 {
		for _, step := range input.Steps {
			work.Groups = append(work.Groups, Group{Name: step.ID})
		}
	}
	if sharedCommunicationAffected(work, affectedDomains) {
		return input, []Diagnostic{{Severity: DiagnosticInfo, Reason: "CommunicationServiceScopeRetained", Message: "all actions remain in scope because a shared service path is affected or released"}}
	}

	affectedNodes := nodesForPowerDomains(input.PowerDomains, affectedDomains)
	// A healthy node can still lose its communication path with another UPS.
	// Follow carries edges from affected carriers, including transit-only devices.
	for entity := range communicationDependents(input, affectedDomains) {
		affectedNodes[entity] = struct{}{}
	}
	membership := groupNodeSets(input.GroupNodes)
	unresolved := unresolvedGroupMembership(input.GroupNodes)
	includeUnmappedNodes(input, membership, affectedNodes)
	retainReleasedCarrierConsumers(work, membership, affectedNodes)
	pruned := map[string]struct{}{}
	groups := make([]Group, 0, len(work.Groups))
	for _, group := range work.Groups {
		nodes, known := membership[group.Name]
		if known && !unresolved[group.Name] && len(nodes) > 0 && outsideNodeSet(nodes, affectedNodes) {
			pruned[group.Name] = struct{}{}
			continue
		}
		groups = append(groups, group)
	}
	if len(pruned) == 0 {
		return input, nil
	}

	for i := range groups {
		groups[i] = scrubPrunedDependencies(groups[i], pruned)
	}

	scoped := input
	if len(input.Groups) > 0 {
		scoped.Groups = groups
		// A fully pruned grouped plan must never activate its ignored linear fallback.
		scoped.Steps = nil
	} else {
		scoped.Steps = nil
		for _, step := range input.Steps {
			if _, omitted := pruned[step.ID]; !omitted {
				scoped.Steps = append(scoped.Steps, step)
			}
		}
	}
	scoped.GroupNodes = filterPrunedGroupNodes(input.GroupNodes, pruned)

	domainNames := sortedSetKeys(affectedDomains)
	diagnostic := Diagnostic{
		Severity: DiagnosticInfo,
		Reason:   DiagnosticReasonPowerDomainScopeApplied,
		Subject:  strings.Join(domainNames, ","),
		Message: fmt.Sprintf(
			"shutdown plan scoped to power domain(s) %s; %d action(s) wholly outside the affected node set were omitted",
			strings.Join(domainNames, ", "), len(pruned)),
	}
	return scoped, []Diagnostic{diagnostic}
}

func executionRootsMapped(roots []string, domains []PowerDomainMembership) bool {
	known := map[string]bool{}
	for _, domain := range domains {
		if domain.Name == "" {
			continue
		}
		for _, root := range domain.UPSDevices {
			known[root] = true
		}
	}
	for _, root := range roots {
		if root == "" || !known[root] {
			return false
		}
	}
	return true
}

// Membership without a resolved supply is not evidence of an unaffected target.
func includeUnmappedNodes(input StructuralInputs, membership map[string]map[string]struct{}, retained map[string]struct{}) {
	known := map[string]bool{}
	for _, domain := range input.PowerDomains {
		for _, entity := range domainEntities(domain) {
			known[entity] = true
		}
	}
	for _, nodes := range membership {
		for node := range nodes {
			if !known[node] {
				retained[node] = struct{}{}
			}
		}
	}
}

func affectedPowerDomains(triggers []Trigger, domains []PowerDomainMembership) (map[string]struct{}, bool) {
	if len(triggers) == 0 {
		return nil, false
	}

	knownDomains := make(map[string]struct{}, len(domains))
	domainsByUPS := map[string][]string{}
	for _, domain := range domains {
		if domain.Name == "" {
			continue
		}
		knownDomains[domain.Name] = struct{}{}
		for _, device := range domain.UPSDevices {
			domainsByUPS[device] = append(domainsByUPS[device], domain.Name)
		}
	}

	affected := map[string]struct{}{}
	for _, trigger := range triggers {
		if len(trigger.PowerDomains) == 0 && len(trigger.UPSDevices) == 0 {
			return nil, false
		}
		for _, domain := range trigger.PowerDomains {
			if _, known := knownDomains[domain]; known {
				affected[domain] = struct{}{}
			}
		}
		for _, device := range trigger.UPSDevices {
			for _, domain := range domainsByUPS[device] {
				affected[domain] = struct{}{}
			}
		}
	}

	return affected, true
}

func nodesForPowerDomains(domains []PowerDomainMembership, affectedDomains map[string]struct{}) map[string]struct{} {
	nodes := map[string]struct{}{}
	for _, domain := range domains {
		if _, affected := affectedDomains[domain.Name]; !affected {
			continue
		}
		for _, node := range domain.Nodes {
			nodes[node] = struct{}{}
		}
	}
	return nodes
}

func groupNodeSets(memberships []GroupNodeMembership) map[string]map[string]struct{} {
	groups := map[string]map[string]struct{}{}
	for _, membership := range memberships {
		if membership.Group == "" {
			continue
		}
		nodes := groups[membership.Group]
		if nodes == nil {
			nodes = map[string]struct{}{}
			groups[membership.Group] = nodes
		}
		for _, node := range membership.Acts {
			nodes[node] = struct{}{}
		}
		for _, node := range membership.Releases {
			nodes[node] = struct{}{}
		}
	}
	return groups
}

func unresolvedGroupMembership(memberships []GroupNodeMembership) map[string]bool {
	groups := map[string]bool{}
	for _, membership := range memberships {
		if membership.Unresolved {
			groups[membership.Group] = true
		}
	}
	return groups
}

func outsideNodeSet(nodes, affected map[string]struct{}) bool {
	for node := range nodes {
		if _, ok := affected[node]; ok {
			return false
		}
	}
	return true
}

func scrubPrunedDependencies(group Group, pruned map[string]struct{}) Group {
	group.Requires = omitStrings(group.Requires, pruned)
	group.Before = omitStrings(group.Before, pruned)
	group.After = omitStrings(group.After, pruned)
	return group
}

func omitStrings(values []string, omitted map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if _, skip := omitted[value]; skip {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func filterPrunedGroupNodes(memberships []GroupNodeMembership, pruned map[string]struct{}) []GroupNodeMembership {
	if len(memberships) == 0 {
		return nil
	}
	filtered := make([]GroupNodeMembership, 0, len(memberships))
	for _, membership := range memberships {
		if _, skip := pruned[membership.Group]; skip {
			continue
		}
		filtered = append(filtered, membership)
	}
	return filtered
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for value := range values {
		keys = append(keys, value)
	}
	sort.Strings(keys)
	return keys
}
