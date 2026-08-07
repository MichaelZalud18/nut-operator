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

const (
	GraphVertexKindGroup = "ShutdownGroup"
	GraphVertexKindStep  = "ShutdownStep"

	GraphEdgeRelationRequires      = "Requires"
	GraphEdgeRelationBefore        = "Before"
	GraphEdgeRelationAfter         = "After"
	GraphEdgeRelationLinearOrder   = "LinearOrder"
	GraphEdgeRelationShutdownTier  = "ShutdownTier"
	GraphEdgeRelationNodeClearance = "NodeClearance"

	GraphEdgeProvenanceDeclared = "Declared"
	GraphEdgeProvenanceDerived  = "Derived"
	GraphEdgeProvenancePolicy   = "Policy"
)

func buildGroupGraph(groups []Group, policy TierPolicy, membership []GroupNodeMembership) Graph {
	tiers := effectiveShutdownTiers(groups, policy)
	graph := Graph{
		Vertices: make([]GraphVertex, 0, len(groups)),
		Edges:    append(collectGroupGraphEdges(groups, policy), collectNodeClearanceGraphEdges(groups, membership)...),
	}
	for _, group := range groups {
		graph.Vertices = append(graph.Vertices, GraphVertex{
			ID:            group.Name,
			Kind:          GraphVertexKindGroup,
			Label:         group.Name,
			Action:        group.Action,
			Phase:         copyPhase(group.Phase),
			ShutdownTier:  shutdownTierPtr(group.Name, tiers),
			TargetSummary: summarizeTarget(group.Target),
		})
	}
	sortGraph(graph)
	return graph
}

func collectGroupGraphEdges(groups []Group, policy TierPolicy) []GraphEdge {
	var edges []GraphEdge
	for _, group := range groups {
		for _, dependency := range group.Requires {
			edges = append(edges, GraphEdge{
				ID:         graphEdgeID(group.Name, dependency, GraphEdgeRelationRequires),
				From:       group.Name,
				To:         dependency,
				Relation:   GraphEdgeRelationRequires,
				Provenance: GraphEdgeProvenanceDeclared,
				Sources: []GraphSourceRef{{
					Kind:  GraphVertexKindGroup,
					Name:  group.Name,
					Field: "spec.groups[].requires",
				}},
				Explanation: fmt.Sprintf("%s runs before %s because %s requires %s to remain available during shutdown.", group.Name, dependency, group.Name, dependency),
			})
		}
		for _, dependency := range group.Before {
			edges = append(edges, GraphEdge{
				ID:         graphEdgeID(group.Name, dependency, GraphEdgeRelationBefore),
				From:       group.Name,
				To:         dependency,
				Relation:   GraphEdgeRelationBefore,
				Provenance: GraphEdgeProvenanceDeclared,
				Sources: []GraphSourceRef{{
					Kind:  GraphVertexKindGroup,
					Name:  group.Name,
					Field: "spec.groups[].before",
				}},
				Explanation: fmt.Sprintf("%s runs before %s because %s declares before: %s.", group.Name, dependency, group.Name, dependency),
			})
		}
		for _, dependency := range group.After {
			edges = append(edges, GraphEdge{
				ID:         graphEdgeID(dependency, group.Name, GraphEdgeRelationAfter),
				From:       dependency,
				To:         group.Name,
				Relation:   GraphEdgeRelationAfter,
				Provenance: GraphEdgeProvenanceDeclared,
				Sources: []GraphSourceRef{{
					Kind:  GraphVertexKindGroup,
					Name:  group.Name,
					Field: "spec.groups[].after",
				}},
				Explanation: fmt.Sprintf("%s runs before %s because %s declares after: %s.", dependency, group.Name, group.Name, dependency),
			})
		}
	}
	edges = append(edges, collectShutdownTierGraphEdges(groups, policy)...)
	return dedupeGraphEdges(edges)
}

func buildStepGraph(steps []Step) Graph {
	graph := Graph{
		Vertices: make([]GraphVertex, 0, len(steps)),
		Edges:    make([]GraphEdge, 0, max(len(steps)-1, 0)),
	}
	for i, step := range steps {
		graph.Vertices = append(graph.Vertices, GraphVertex{
			ID:            step.ID,
			Kind:          GraphVertexKindStep,
			Label:         step.ID,
			Action:        step.Action,
			TargetSummary: summarizeTarget(step.Target),
		})
		if i == 0 {
			continue
		}
		previous := steps[i-1]
		graph.Edges = append(graph.Edges, GraphEdge{
			ID:         graphEdgeID(previous.ID, step.ID, GraphEdgeRelationLinearOrder),
			From:       previous.ID,
			To:         step.ID,
			Relation:   GraphEdgeRelationLinearOrder,
			Provenance: GraphEdgeProvenancePolicy,
			Sources: []GraphSourceRef{{
				Kind:  GraphVertexKindStep,
				Field: "spec.steps",
			}},
			Explanation: fmt.Sprintf("%s runs before %s because linear steps preserve declared order.", previous.ID, step.ID),
		})
	}
	sortGraph(graph)
	return graph
}

func advisoryStartupWaves(shutdownWaves []Wave) []Wave {
	if len(shutdownWaves) == 0 {
		return nil
	}
	startup := make([]Wave, 0, len(shutdownWaves))
	var cumulative Duration
	for i := len(shutdownWaves) - 1; i >= 0; i-- {
		source := shutdownWaves[i]
		cumulative.Duration += source.Duration.Duration
		groups := append([]string(nil), source.Groups...)
		sort.Strings(groups)
		wave := Wave{
			Index:              int32(len(startup)),
			Phase:              copyPhase(source.Phase),
			ShutdownTier:       copyInt32Ptr(source.ShutdownTier),
			Groups:             groups,
			Duration:           source.Duration,
			CumulativeDuration: cumulative,
		}
		startup = append(startup, wave)
	}
	return startup
}

func graphExplanations(graph Graph, shutdownWaveCount, startupWaveCount int) []Explanation {
	explanations := make([]Explanation, 0, len(graph.Edges)+2)
	for _, edge := range graph.Edges {
		explanations = append(explanations, Explanation{
			ID:      "edge-" + edge.ID,
			Subject: edge.ID,
			Reason:  edge.Relation,
			Message: edge.Explanation,
		})
	}
	explanations = append(explanations, Explanation{
		ID:      "plan-compiled",
		Subject: "plan",
		Reason:  "PlanCompiled",
		Message: fmt.Sprintf("compiled %d graph vertices and %d graph edges into %d shutdown wave(s).", len(graph.Vertices), len(graph.Edges), shutdownWaveCount),
	})
	if startupWaveCount > 0 {
		explanations = append(explanations, Explanation{
			ID:      "startup-projection",
			Subject: "startupWaves",
			Reason:  "AdvisoryStartupProjection",
			Message: fmt.Sprintf("projected %d advisory startup wave(s) by reversing the shutdown dependency order.", startupWaveCount),
		})
	}
	return explanations
}

func renderDiagramExports(graph Graph) DiagramExports {
	if len(graph.Vertices) == 0 {
		return DiagramExports{}
	}
	return DiagramExports{
		Mermaid:     renderMermaid(graph),
		GraphvizDOT: renderGraphvizDOT(graph),
		D2:          renderD2(graph),
	}
}

func graphSuccessors(graph Graph) map[string][]string {
	successors := map[string][]string{}
	for _, vertex := range graph.Vertices {
		successors[vertex.ID] = nil
	}
	for _, edge := range graph.Edges {
		successors[edge.From] = append(successors[edge.From], edge.To)
	}
	for key := range successors {
		sort.Strings(successors[key])
	}
	return successors
}

func sortGraph(graph Graph) {
	sort.SliceStable(graph.Vertices, func(i, j int) bool {
		return graph.Vertices[i].ID < graph.Vertices[j].ID
	})
	sort.SliceStable(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].From != graph.Edges[j].From {
			return graph.Edges[i].From < graph.Edges[j].From
		}
		if graph.Edges[i].To != graph.Edges[j].To {
			return graph.Edges[i].To < graph.Edges[j].To
		}
		return graph.Edges[i].Relation < graph.Edges[j].Relation
	})
}

func dedupeGraphEdges(edges []GraphEdge) []GraphEdge {
	byID := make(map[string]GraphEdge, len(edges))
	for _, edge := range edges {
		byID[edge.ID] = edge
	}
	deduped := make([]GraphEdge, 0, len(byID))
	for _, edge := range byID {
		deduped = append(deduped, edge)
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		if deduped[i].From != deduped[j].From {
			return deduped[i].From < deduped[j].From
		}
		if deduped[i].To != deduped[j].To {
			return deduped[i].To < deduped[j].To
		}
		return deduped[i].Relation < deduped[j].Relation
	})
	return deduped
}

func graphEdgeID(from, to, relation string) string {
	return safeGraphID(from) + "-" + strings.ToLower(relation) + "-" + safeGraphID(to)
}

func safeGraphID(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return "node"
	}
	return builder.String()
}

func copyPhase(phase *int32) *int32 {
	if phase == nil {
		return nil
	}
	copied := *phase
	return &copied
}

func renderMermaid(graph Graph) string {
	nodeIDs := diagramNodeIDs(graph.Vertices)
	var builder strings.Builder
	builder.WriteString("flowchart TD\n")
	for _, vertex := range graph.Vertices {
		fmt.Fprintf(&builder, "  %s[%q]\n", nodeIDs[vertex.ID], diagramLabel(vertex))
	}
	for _, edge := range graph.Edges {
		fmt.Fprintf(&builder, "  %s -->|%s| %s\n", nodeIDs[edge.From], edge.Relation, nodeIDs[edge.To])
	}
	return builder.String()
}

func renderGraphvizDOT(graph Graph) string {
	var builder strings.Builder
	builder.WriteString("digraph shutdown_plan {\n")
	builder.WriteString("  rankdir=LR;\n")
	for _, vertex := range graph.Vertices {
		fmt.Fprintf(&builder, "  %q [label=%q];\n", vertex.ID, diagramLabel(vertex))
	}
	for _, edge := range graph.Edges {
		fmt.Fprintf(&builder, "  %q -> %q [label=%q];\n", edge.From, edge.To, edge.Relation)
	}
	builder.WriteString("}\n")
	return builder.String()
}

func renderD2(graph Graph) string {
	var builder strings.Builder
	for _, vertex := range graph.Vertices {
		fmt.Fprintf(&builder, "%s: %q\n", safeGraphID(vertex.ID), diagramLabel(vertex))
	}
	for _, edge := range graph.Edges {
		fmt.Fprintf(&builder, "%s -> %s: %s\n", safeGraphID(edge.From), safeGraphID(edge.To), edge.Relation)
	}
	return builder.String()
}

func diagramNodeIDs(vertices []GraphVertex) map[string]string {
	ids := make(map[string]string, len(vertices))
	used := map[string]int{}
	for _, vertex := range vertices {
		base := safeGraphID(vertex.ID)
		id := base
		if count := used[base]; count > 0 {
			id = fmt.Sprintf("%s_%d", base, count)
		}
		used[base]++
		ids[vertex.ID] = id
	}
	return ids
}

func diagramLabel(vertex GraphVertex) string {
	if vertex.Action == "" {
		return vertex.Label
	}
	return vertex.Label + "\\n" + vertex.Action
}
