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

// collectNodeClearanceGraphEdges derives PL-20 node-clearance edges.
//
// Nodes are terminal vertices: a node cannot power off until the work assigned
// to it has cleared. So for every node, each group that acts on it is ordered
// before the group that releases it. These are real graph edges rather than
// prose the executor is trusted to honor, which matters because wave
// compilation reads the graph — an edge here changes execution order, it does
// not merely describe it.
//
// Without this, a flow that drains rack-a and powers off rack-a with no
// explicit `before` between them compiles both into the same wave: the drain
// races the poweroff on the very node being drained. Declaring the ordering by
// hand works, but it is exactly the kind of edge a topology model exists to
// derive.
//
// A group that both acts on and releases the same node produces no edge — it
// would order the group against itself. The ordering it implies is internal to
// that group's own action sequence, not something the planner arbitrates.
func collectNodeClearanceGraphEdges(groups []Group, membership []GroupNodeMembership) []GraphEdge {
	if len(membership) == 0 || len(groups) == 0 {
		return nil
	}

	known := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		known[group.Name] = struct{}{}
	}

	// releasersByNode answers "who powers this node off", which is the
	// destination every clearance edge points at.
	releasersByNode := map[string][]string{}
	for _, entry := range membership {
		if _, ok := known[entry.Group]; !ok {
			continue
		}
		for _, node := range entry.Releases {
			releasersByNode[node] = append(releasersByNode[node], entry.Group)
		}
	}
	if len(releasersByNode) == 0 {
		return nil
	}
	for node := range releasersByNode {
		sort.Strings(releasersByNode[node])
	}

	seen := map[string]struct{}{}
	var edges []GraphEdge
	for _, entry := range membership {
		if _, ok := known[entry.Group]; !ok {
			continue
		}
		for _, node := range entry.Acts {
			for _, releaser := range releasersByNode[node] {
				if releaser == entry.Group {
					continue
				}
				id := graphEdgeID(entry.Group, releaser, GraphEdgeRelationNodeClearance)
				if _, duplicate := seen[id]; duplicate {
					continue
				}
				seen[id] = struct{}{}
				edges = append(edges, GraphEdge{
					ID:         id,
					From:       entry.Group,
					To:         releaser,
					Relation:   GraphEdgeRelationNodeClearance,
					Provenance: GraphEdgeProvenanceDerived,
					Sources: []GraphSourceRef{{
						Kind: "Node",
						Name: node,
					}},
					Explanation: fmt.Sprintf(
						"%s runs before %s because node %s cannot power off until %s has cleared it.",
						entry.Group, releaser, node, entry.Group),
				})
			}
		}
	}

	sort.SliceStable(edges, func(left, right int) bool { return edges[left].ID < edges[right].ID })
	return edges
}

// reportDefaultedShutdownTiers names groups whose tier came from the cluster
// default rather than from anything the group itself declares.
//
// Falling back is legitimate configuration, and silent until now. That made a
// mistyped tier label indistinguishable from a deliberate default: the group
// simply became ordinary, and nothing said so. The tier compiler already
// diagnoses tiers that are malformed — invalid values, duplicates, a targeted
// tier 0 — but never one that is merely absent.
//
// Informational, because defaulting is not an error. The point is that it is
// visible before an outage rather than inferred from a wave order afterward.
func reportDefaultedShutdownTiers(input StructuralInputs) []Diagnostic {
	defaultTier, hasDefault := effectiveDefaultShutdownTier(input.Groups, input.TierPolicy)
	if !hasDefault {
		return nil
	}

	var diagnostics []Diagnostic
	for _, group := range input.Groups {
		if group.Name == "" || group.ShutdownTier != nil {
			continue
		}
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticInfo,
			Reason:   "ShutdownTierDefaulted",
			Subject:  group.Name,
			Message: fmt.Sprintf(
				"shutdown group %q declares no tier and was placed at the default tier %d",
				group.Name, defaultTier),
		})
	}
	return diagnostics
}

// validateTierInversion reports workloads scheduled to outlive the node running
// them (OD-18).
//
// Tiers count down: a higher tier stops earlier, so a node at tier 4 is meant to
// be gone while a group at tier 2 is still expected to be working. If that group
// runs on that node, the arrangement cannot hold — the node powers off with work
// still on it, or it never clears and the wave stalls. Either way the plan says
// something the cluster cannot do.
//
// This warns rather than rejects. The condition is real but the remedy is a
// choice the operator has to make — retier the workload, move it, or accept it —
// and OD-18 leaves opt-in migration and node blocking open. Stating the gap is
// what is required; deciding it for the user is not.
func validateTierInversion(input StructuralInputs) []Diagnostic {
	if len(input.GroupNodes) == 0 || len(input.NodeTiers) == 0 {
		return nil
	}
	groupTiers := effectiveShutdownTiers(input.Groups, input.TierPolicy)
	if len(groupTiers) == 0 {
		return nil
	}

	nodeTiers := make(map[string]int32, len(input.NodeTiers))
	for _, node := range input.NodeTiers {
		nodeTiers[node.Name] = node.Tier
	}

	var diagnostics []Diagnostic
	for _, entry := range input.GroupNodes {
		groupTier, hasGroupTier := groupTiers[entry.Group]
		if !hasGroupTier {
			continue
		}
		for _, node := range entry.Acts {
			nodeTier, hasNodeTier := nodeTiers[node]
			if !hasNodeTier || nodeTier <= groupTier {
				continue
			}
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticWarning,
				Reason:   "ShutdownTierInversion",
				Subject:  entry.Group,
				Message: fmt.Sprintf(
					"shutdown group %q is tier %d but runs on node %q at tier %d; the node is scheduled to power off before this group stops",
					entry.Group, groupTier, node, nodeTier),
			})
		}
	}
	sort.SliceStable(diagnostics, func(left, right int) bool {
		return diagnostics[left].Message < diagnostics[right].Message
	})
	return diagnostics
}
