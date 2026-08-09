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

// Package resolver assembles unreliable upstream inputs into deterministic
// bundles for planning. The package contains pure assembly helpers only; the
// controller/provider layer owns Kubernetes, NUT, database, and filesystem I/O.
package resolver

import (
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
)

// StructuralInputs are the resolver's already-read structural sources.
type StructuralInputs struct {
	SourceID   string               `json:"sourceID,omitempty"`
	ObservedAt string               `json:"observedAt,omitempty"`
	Inventory  inventory.Snapshot   `json:"inventory,omitempty"`
	Profiles   []capability.Profile `json:"profiles,omitempty"`
	// ClusterNodes are the real Kubernetes nodes, with the labels a shutdown
	// group's node selector matches against. Declared inventory names nodes;
	// this is what the cluster actually has.
	ClusterNodes []ClusterNode `json:"clusterNodes,omitempty"`
	// AgentCoverage maps each NodePowerAgent to the nodes it has selected, which
	// is how a group learns which nodes it would actually power off.
	AgentCoverage []AgentCoverage `json:"agentCoverage,omitempty"`
}

// ClusterNode is one Kubernetes node as the planner needs to see it: an
// identity and the labels a selector can match.
type ClusterNode struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentCoverage records which nodes a NodePowerAgent has selected. The agent
// controller already resolves this into status, so shutdown planning reads it
// rather than re-deriving node membership from a DaemonSet selector.
type AgentCoverage struct {
	Name  string   `json:"name"`
	Nodes []string `json:"nodes,omitempty"`
}

// StructuralBundle is the deterministic structure emitted toward the planner.
type StructuralBundle struct {
	Hash              string                   `json:"hash,omitempty"`
	SourceID          string                   `json:"sourceID,omitempty"`
	ObservedAt        string                   `json:"observedAt,omitempty"`
	Topology          inventory.Topology       `json:"topology,omitempty"`
	CapabilityMatches []capability.MatchResult `json:"capabilityMatches,omitempty"`
	// SnapshotObservedAt is when the inventory snapshot itself was taken, which
	// is what IN-16 age escalation measures. It is distinct from ObservedAt, the
	// time this bundle was assembled: a provider outage means those two diverge,
	// and the gap between them is exactly the thing worth reporting. It stays out
	// of Hash because the topology hash already covers the snapshot it came from.
	SnapshotObservedAt string `json:"snapshotObservedAt,omitempty"`
	// ClusterNodes and AgentCoverage carry through unchanged for group-to-node
	// expansion. They are deliberately absent from Hash: node labels churn for
	// reasons unrelated to shutdown planning, and only the membership actually
	// derived from them belongs in plan identity, which it reaches through the
	// planner's own structural hash.
	ClusterNodes  []ClusterNode   `json:"clusterNodes,omitempty"`
	AgentCoverage []AgentCoverage `json:"agentCoverage,omitempty"`
}

// Diagnostic attributes a resolver warning or rejection to the source stage.
type Diagnostic struct {
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}
