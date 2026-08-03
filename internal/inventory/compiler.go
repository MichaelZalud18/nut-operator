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

package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	DiagnosticError   = "Error"
	DiagnosticWarning = "Warning"
)

var ErrRejected = errors.New("inventory rejected structural snapshot")

// Compile validates a provider-neutral inventory snapshot and derives topology
// structure used by the resolver and planner. It performs no I/O.
func Compile(snapshot Snapshot) (Topology, []Diagnostic, error) {
	normalized := normalizeSnapshot(snapshot)
	diagnostics := validateSnapshot(normalized)
	if hasError(diagnostics) {
		return Topology{}, diagnostics, ErrRejected
	}

	topology := Topology{
		Hash:                stableHash(normalized),
		Domains:             derivePowerDomains(normalized),
		CommunicationOrders: deriveCommunicationOrders(normalized),
		Entities:            append([]Entity(nil), normalized.Entities...),
		Edges:               append([]Edge(nil), normalized.Edges...),
	}

	diagnostics = append(diagnostics, validateDerivedTopology(topology)...)
	if hasError(diagnostics) {
		return Topology{}, diagnostics, ErrRejected
	}

	return topology, diagnostics, nil
}

func validateSnapshot(snapshot Snapshot) []Diagnostic {
	var diagnostics []Diagnostic
	entities := map[string]Entity{}
	for _, entity := range snapshot.Entities {
		if entity.ID == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "EntityIDRequired",
				Message:  "every inventory entity requires an id",
			})
			continue
		}
		if _, exists := entities[entity.ID]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateEntity",
				Subject:  entity.ID,
				Message:  fmt.Sprintf("inventory entity %q is duplicated", entity.ID),
			})
		}
		if !validEntityKind(entity.Kind) {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "UnsupportedEntityKind",
				Subject:  entity.ID,
				Message:  fmt.Sprintf("inventory entity %q has unsupported kind %q", entity.ID, entity.Kind),
			})
		}
		if entity.Kind == EntityKindUPSDevice && len(entity.PowerDomains) == 0 {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "PowerDomainRequired",
				Subject:  entity.ID,
				Message:  fmt.Sprintf("UPS entity %q requires at least one power domain label", entity.ID),
			})
		}
		if entity.Kind != EntityKindUPSDevice && len(entity.PowerDomains) > 0 {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "PowerDomainDeclaredOnNonRoot",
				Subject:  entity.ID,
				Message:  fmt.Sprintf("inventory entity %q declares power domains, but domain membership is derived from UPS roots", entity.ID),
			})
		}
		if entity.ShutdownTier != nil {
			switch tier := *entity.ShutdownTier; {
			case tier < 0:
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "ShutdownTierInvalid",
					Subject:  entity.ID,
					Message:  fmt.Sprintf("inventory entity %q declares invalid shutdown tier %d", entity.ID, tier),
				})
			case entity.Kind == EntityKindNode && tier == 0:
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "ShutdownTierZeroNode",
					Subject:  entity.ID,
					Message:  fmt.Sprintf("node %q cannot be assigned shutdown tier 0", entity.ID),
				})
			}
		}
		entities[entity.ID] = entity
	}

	seenEdges := map[string]struct{}{}
	for _, edge := range snapshot.Edges {
		if edge.From == "" || edge.To == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "EdgeEndpointRequired",
				Message:  "every inventory edge requires from and to endpoints",
			})
			continue
		}
		if edge.From == edge.To {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "SelfEdge",
				Subject:  edge.From,
				Message:  fmt.Sprintf("inventory edge %q -> %q cannot point at itself", edge.From, edge.To),
			})
		}
		if _, exists := entities[edge.From]; !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "UnknownEdgeEndpoint",
				Subject:  edge.From,
				Message:  fmt.Sprintf("inventory edge references unknown source entity %q", edge.From),
			})
		}
		if _, exists := entities[edge.To]; !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "UnknownEdgeEndpoint",
				Subject:  edge.To,
				Message:  fmt.Sprintf("inventory edge references unknown target entity %q", edge.To),
			})
		}
		if !validEdgeRelation(edge.Relation) {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "UnsupportedEdgeRelation",
				Subject:  edge.From,
				Message:  fmt.Sprintf("inventory edge %q -> %q has unsupported relation %q", edge.From, edge.To, edge.Relation),
			})
		}
		if edge.Relation == EdgeRelationFeeds && edge.Input == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "FeedInputRequired",
				Subject:  edge.To,
				Message:  fmt.Sprintf("feeds edge %q -> %q requires an input qualifier", edge.From, edge.To),
			})
		}
		key := stableHash(struct {
			From     string       `json:"from"`
			To       string       `json:"to"`
			Relation EdgeRelation `json:"relation"`
			Input    string       `json:"input,omitempty"`
		}{
			From:     edge.From,
			To:       edge.To,
			Relation: edge.Relation,
			Input:    edge.Input,
		})
		if _, exists := seenEdges[key]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateEdge",
				Subject:  edge.From,
				Message:  fmt.Sprintf("inventory edge %q -> %q relation %q is duplicated", edge.From, edge.To, edge.Relation),
			})
		}
		seenEdges[key] = struct{}{}
	}

	return diagnostics
}

func validateDerivedTopology(topology Topology) []Diagnostic {
	var diagnostics []Diagnostic
	domainByNode := map[string][]string{}
	for _, domain := range topology.Domains {
		for _, node := range domain.Nodes {
			domainByNode[node] = append(domainByNode[node], domain.Name)
		}
	}
	hasCarriesPath := map[string]bool{}
	for _, order := range topology.CommunicationOrders {
		hasCarriesPath[order.From] = true
	}

	for _, entity := range topology.Entities {
		if entity.Kind != EntityKindNode {
			continue
		}
		if len(domainByNode[entity.ID]) == 0 && !entity.PowerPlanningExempt {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "PowerPlanningOrphan",
				Subject:  entity.ID,
				Message:  fmt.Sprintf("node %q is not reachable from any UPS feeds path and is not power-planning exempt", entity.ID),
			})
		}
		if !hasCarriesPath[entity.ID] && !entity.CommunicationPathExempt {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticWarning,
				Reason:   "CommunicationPathUnmodeled",
				Subject:  entity.ID,
				Message:  fmt.Sprintf("node %q has no modeled carries path", entity.ID),
			})
		}
	}

	return diagnostics
}

func derivePowerDomains(snapshot Snapshot) []PowerDomain {
	entities := entitiesByID(snapshot.Entities)
	feeds := edgesByFrom(snapshot.Edges, EdgeRelationFeeds)
	domainsByName := map[string]*PowerDomain{}

	for _, root := range snapshot.Entities {
		if root.Kind != EntityKindUPSDevice {
			continue
		}
		for _, domainName := range root.PowerDomains {
			domain := domainsByName[domainName]
			if domain == nil {
				domain = &PowerDomain{Name: domainName}
				domainsByName[domainName] = domain
			}
			domain.UPSDevices = append(domain.UPSDevices, root.ID)
			for _, memberID := range feedsClosure(root.ID, feeds) {
				domain.Members = append(domain.Members, memberID)
				switch entities[memberID].Kind {
				case EntityKindNode:
					domain.Nodes = append(domain.Nodes, memberID)
				case EntityKindPowerInfrastructure:
					domain.Infrastructure = append(domain.Infrastructure, memberID)
				}
			}
		}
	}

	names := make([]string, 0, len(domainsByName))
	for name := range domainsByName {
		names = append(names, name)
	}
	sort.Strings(names)

	domains := make([]PowerDomain, 0, len(names))
	for _, name := range names {
		domain := *domainsByName[name]
		domain.UPSDevices = sortedUnique(domain.UPSDevices)
		domain.Members = sortedUnique(domain.Members)
		domain.Nodes = sortedUnique(domain.Nodes)
		domain.Infrastructure = sortedUnique(domain.Infrastructure)
		domains = append(domains, domain)
	}
	return domains
}

func deriveCommunicationOrders(snapshot Snapshot) []DerivedEdge {
	ordersByKey := map[string]DerivedEdge{}
	for _, edge := range snapshot.Edges {
		if edge.Relation != EdgeRelationCarries {
			continue
		}
		order := DerivedEdge{
			From:   edge.To,
			To:     edge.From,
			Reason: "CommunicationPath",
			Source: edge.SourceID,
		}
		ordersByKey[stableHash(order)] = order
	}

	keys := make([]string, 0, len(ordersByKey))
	for key := range ordersByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	orders := make([]DerivedEdge, 0, len(keys))
	for _, key := range keys {
		orders = append(orders, ordersByKey[key])
	}
	return orders
}

func feedsClosure(root string, feeds map[string][]Edge) []string {
	visited := map[string]bool{}
	var members []string

	var walk func(string)
	walk = func(id string) {
		if visited[id] {
			return
		}
		visited[id] = true
		members = append(members, id)
		for _, edge := range feeds[id] {
			walk(edge.To)
		}
	}
	walk(root)
	sort.Strings(members)
	return members
}

func normalizeSnapshot(snapshot Snapshot) Snapshot {
	normalized := Snapshot{
		SourceID:   snapshot.SourceID,
		Version:    snapshot.Version,
		ObservedAt: snapshot.ObservedAt,
		Entities:   append([]Entity(nil), snapshot.Entities...),
		Edges:      append([]Edge(nil), snapshot.Edges...),
	}
	for i := range normalized.Entities {
		normalized.Entities[i].PowerDomains = append([]string(nil), normalized.Entities[i].PowerDomains...)
		sort.Strings(normalized.Entities[i].PowerDomains)
	}
	sort.SliceStable(normalized.Entities, func(i, j int) bool {
		if normalized.Entities[i].ID != normalized.Entities[j].ID {
			return normalized.Entities[i].ID < normalized.Entities[j].ID
		}
		return normalized.Entities[i].Kind < normalized.Entities[j].Kind
	})
	sort.SliceStable(normalized.Edges, func(i, j int) bool {
		left, right := normalized.Edges[i], normalized.Edges[j]
		switch {
		case left.Relation != right.Relation:
			return left.Relation < right.Relation
		case left.From != right.From:
			return left.From < right.From
		case left.To != right.To:
			return left.To < right.To
		case left.Input != right.Input:
			return left.Input < right.Input
		default:
			return left.SourceID < right.SourceID
		}
	})
	return normalized
}

func validEntityKind(kind EntityKind) bool {
	switch kind {
	case EntityKindUPSDevice, EntityKindNode, EntityKindPowerInfrastructure:
		return true
	default:
		return false
	}
}

func validEdgeRelation(relation EdgeRelation) bool {
	switch relation {
	case EdgeRelationFeeds, EdgeRelationCarries:
		return true
	default:
		return false
	}
}

func entitiesByID(entities []Entity) map[string]Entity {
	byID := make(map[string]Entity, len(entities))
	for _, entity := range entities {
		byID[entity.ID] = entity
	}
	return byID
}

func edgesByFrom(edges []Edge, relation EdgeRelation) map[string][]Edge {
	byFrom := map[string][]Edge{}
	for _, edge := range edges {
		if edge.Relation == relation {
			byFrom[edge.From] = append(byFrom[edge.From], edge)
		}
	}
	for key := range byFrom {
		sort.SliceStable(byFrom[key], func(i, j int) bool {
			return byFrom[key][i].To < byFrom[key][j].To
		})
	}
	return byFrom
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	unique := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	return unique
}

func hasError(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == DiagnosticError {
			return true
		}
	}
	return false
}

func stableHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("inventory input could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
