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
)

func effectiveShutdownTiers(groups []Group, policy TierPolicy) map[string]int32 {
	active := policy.DefaultTier != nil || len(policy.Definitions) > 0
	for _, group := range groups {
		if group.ShutdownTier != nil {
			active = true
			break
		}
	}
	if !active {
		return nil
	}

	defaultTier, hasDefault := effectiveDefaultShutdownTier(groups, policy)
	tiers := make(map[string]int32, len(groups))
	for _, group := range groups {
		if group.ShutdownTier != nil {
			tiers[group.Name] = *group.ShutdownTier
			continue
		}
		if hasDefault {
			tiers[group.Name] = defaultTier
		}
	}
	if len(tiers) == 0 {
		return nil
	}
	return tiers
}

func effectiveDefaultShutdownTier(groups []Group, policy TierPolicy) (int32, bool) {
	if policy.DefaultTier != nil {
		return *policy.DefaultTier, true
	}
	var maxTier int32
	found := false
	for _, definition := range policy.Definitions {
		if definition.Tier > maxTier || !found {
			maxTier = definition.Tier
			found = true
		}
	}
	for _, group := range groups {
		if group.ShutdownTier == nil {
			continue
		}
		tier := *group.ShutdownTier
		if tier > maxTier || !found {
			maxTier = tier
			found = true
		}
	}
	if !found || maxTier == 0 {
		return 0, false
	}
	return maxTier, true
}

func collectShutdownTierGraphEdges(groups []Group, policy TierPolicy) []GraphEdge {
	tiers := effectiveShutdownTiers(groups, policy)
	if len(tiers) == 0 {
		return nil
	}

	groupsByTier := map[int32][]string{}
	for _, group := range groups {
		tier, found := tiers[group.Name]
		if !found {
			continue
		}
		groupsByTier[tier] = append(groupsByTier[tier], group.Name)
	}

	occupiedTiers := make([]int32, 0, len(groupsByTier))
	for tier := range groupsByTier {
		occupiedTiers = append(occupiedTiers, tier)
		sort.Strings(groupsByTier[tier])
	}
	sort.Slice(occupiedTiers, func(i, j int) bool {
		return occupiedTiers[i] > occupiedTiers[j]
	})

	var edges []GraphEdge
	for i := 0; i < len(occupiedTiers)-1; i++ {
		higherTier := occupiedTiers[i]
		lowerTier := occupiedTiers[i+1]
		for _, from := range groupsByTier[higherTier] {
			for _, to := range groupsByTier[lowerTier] {
				edges = append(edges, GraphEdge{
					ID:         graphEdgeID(from, to, GraphEdgeRelationShutdownTier),
					From:       from,
					To:         to,
					Relation:   GraphEdgeRelationShutdownTier,
					Provenance: GraphEdgeProvenanceDerived,
					Sources: []GraphSourceRef{{
						Kind:  "PowerManagementCluster",
						Field: "spec.shutdownTiers",
					}},
					Explanation: fmt.Sprintf("%s runs before %s because shutdown tier %d stops before tier %d.", from, to, higherTier, lowerTier),
				})
			}
		}
	}
	return edges
}

func shutdownTierPtr(groupName string, tiers map[string]int32) *int32 {
	if len(tiers) == 0 {
		return nil
	}
	tier, found := tiers[groupName]
	if !found {
		return nil
	}
	copied := tier
	return &copied
}

func sharedShutdownTier(groupNames []string, tiers map[string]int32) *int32 {
	if len(groupNames) == 0 || len(tiers) == 0 {
		return nil
	}
	first, found := tiers[groupNames[0]]
	if !found {
		return nil
	}
	for _, groupName := range groupNames[1:] {
		next, found := tiers[groupName]
		if !found || next != first {
			return nil
		}
	}
	copied := first
	return &copied
}

func graphShutdownTiers(graph Graph) map[string]int32 {
	tiers := map[string]int32{}
	for _, vertex := range graph.Vertices {
		if vertex.ShutdownTier == nil {
			continue
		}
		tiers[vertex.ID] = *vertex.ShutdownTier
	}
	if len(tiers) == 0 {
		return nil
	}
	return tiers
}

func normalizeTierPolicy(policy TierPolicy) TierPolicy {
	normalized := TierPolicy{
		LabelKey:      policy.LabelKey,
		DefaultTier:   copyInt32Ptr(policy.DefaultTier),
		Definitions:   append([]TierDefinition(nil), policy.Definitions...),
		SelectorRules: append([]TierSelector(nil), policy.SelectorRules...),
	}
	sort.SliceStable(normalized.Definitions, func(i, j int) bool {
		if normalized.Definitions[i].Tier != normalized.Definitions[j].Tier {
			return normalized.Definitions[i].Tier < normalized.Definitions[j].Tier
		}
		return normalized.Definitions[i].Name < normalized.Definitions[j].Name
	})
	sort.SliceStable(normalized.SelectorRules, func(i, j int) bool {
		return normalized.SelectorRules[i].Name < normalized.SelectorRules[j].Name
	})
	return normalized
}

func validateTierPolicy(policy TierPolicy) []Diagnostic {
	var diagnostics []Diagnostic
	if policy.DefaultTier != nil && *policy.DefaultTier < 2 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "ShutdownTierDefaultReserved",
			Message:  "shutdown tier default must be 2 or greater; tiers 0 and 1 are reserved",
		})
	}

	seenDefinitions := map[int32]struct{}{}
	for _, definition := range policy.Definitions {
		if definition.Tier < 0 {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ShutdownTierInvalid",
				Subject:  definition.Name,
				Message:  fmt.Sprintf("shutdown tier %d must be zero or greater", definition.Tier),
			})
		}
		if _, exists := seenDefinitions[definition.Tier]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateShutdownTier",
				Subject:  fmt.Sprintf("%d", definition.Tier),
				Message:  fmt.Sprintf("shutdown tier %d is defined more than once", definition.Tier),
			})
		}
		seenDefinitions[definition.Tier] = struct{}{}
	}

	seenRules := map[string]struct{}{}
	for _, rule := range policy.SelectorRules {
		if rule.Name == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ShutdownTierRuleNameRequired",
				Message:  "every shutdown tier selector rule requires a name",
			})
		}
		if _, exists := seenRules[rule.Name]; rule.Name != "" && exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateShutdownTierRule",
				Subject:  rule.Name,
				Message:  fmt.Sprintf("shutdown tier selector rule %q is duplicated", rule.Name),
			})
		}
		seenRules[rule.Name] = struct{}{}
		if rule.Tier < 0 {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ShutdownTierInvalid",
				Subject:  rule.Name,
				Message:  fmt.Sprintf("shutdown tier selector rule %q assigns invalid tier %d", rule.Name, rule.Tier),
			})
		}
	}

	return diagnostics
}

func validateGroupShutdownTiers(groups []Group) []Diagnostic {
	var diagnostics []Diagnostic
	for _, group := range groups {
		if group.ShutdownTier == nil {
			continue
		}
		switch tier := *group.ShutdownTier; {
		case tier < 0:
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ShutdownTierInvalid",
				Subject:  group.Name,
				Message:  fmt.Sprintf("shutdown group %q has invalid shutdown tier %d", group.Name, tier),
			})
		case tier == 0:
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ShutdownTierZeroTargeted",
				Subject:  group.Name,
				Message:  fmt.Sprintf("shutdown group %q targets tier 0, which is excluded from orchestrated flow actions", group.Name),
			})
		}
	}
	return diagnostics
}

func copyInt32Ptr(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
