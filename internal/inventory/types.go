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

// Package inventory normalizes authored topology snapshots into deterministic
// planner-ready structure. It performs no I/O and has no Kubernetes imports.
package inventory

// Snapshot is the provider-neutral inventory contract consumed by the resolver.
type Snapshot struct {
	SourceID   string   `json:"sourceID,omitempty"`
	Version    string   `json:"version,omitempty"`
	ObservedAt string   `json:"observedAt,omitempty"`
	Entities   []Entity `json:"entities,omitempty"`
	Edges      []Edge   `json:"edges,omitempty"`
}

// EntityKind identifies the small set of inventory entities the planner rules
// understand.
type EntityKind string

const (
	EntityKindUPSDevice           EntityKind = "UPSDevice"
	EntityKindNode                EntityKind = "Node"
	EntityKindPowerInfrastructure EntityKind = "PowerInfrastructure"
)

// Entity is a UPS root, Kubernetes node, or power/communication path component.
type Entity struct {
	ID                       string     `json:"id"`
	Kind                     EntityKind `json:"kind"`
	PowerDomains             []string   `json:"powerDomains,omitempty"`
	PowerPlanningExempt      bool       `json:"powerPlanningExempt,omitempty"`
	CommunicationPathExempt  bool       `json:"communicationPathExempt,omitempty"`
	Model                    string     `json:"model,omitempty"`
	Firmware                 string     `json:"firmware,omitempty"`
	DriverFamily             string     `json:"driverFamily,omitempty"`
	LastDitchRole            string     `json:"lastDitchRole,omitempty"`
	ControlPlane             bool       `json:"controlPlane,omitempty"`
	ControlPlaneQuorumMember bool       `json:"controlPlaneQuorumMember,omitempty"`
}

// EdgeRelation identifies a structural relation between inventory entities.
type EdgeRelation string

const (
	EdgeRelationFeeds   EdgeRelation = "feeds"
	EdgeRelationCarries EdgeRelation = "carries"
)

// Edge relates two entities. A feeds edge requires Input so multi-feed targets
// keep distinct power inputs.
type Edge struct {
	From     string       `json:"from"`
	To       string       `json:"to"`
	Relation EdgeRelation `json:"relation"`
	Input    string       `json:"input,omitempty"`
	SourceID string       `json:"sourceID,omitempty"`
}

// Topology is the deterministic result of compiling a Snapshot.
type Topology struct {
	Hash                string        `json:"hash,omitempty"`
	Domains             []PowerDomain `json:"domains,omitempty"`
	CommunicationOrders []DerivedEdge `json:"communicationOrders,omitempty"`
	Entities            []Entity      `json:"entities,omitempty"`
	Edges               []Edge        `json:"edges,omitempty"`
}

// PowerDomain is derived from transitive feeds closure starting at UPS roots.
type PowerDomain struct {
	Name           string   `json:"name"`
	UPSDevices     []string `json:"upsDevices,omitempty"`
	Members        []string `json:"members,omitempty"`
	Nodes          []string `json:"nodes,omitempty"`
	Infrastructure []string `json:"infrastructure,omitempty"`
}

// DerivedEdge is a deterministic edge produced from a provider relation.
type DerivedEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
	Source string `json:"source,omitempty"`
}

// Diagnostic is a machine-readable warning or rejection from inventory compile.
type Diagnostic struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}
