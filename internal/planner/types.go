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

// Package planner compiles declarative shutdown structure into deterministic
// ordered waves. It performs no I/O and owns no Kubernetes client.
package planner

import "time"

// StructuralInputs are the inputs that define the shape and identity of a plan.
// Telemetry must not be added here; plan identity is computed from this bundle
// plus the emitted plan.
type StructuralInputs struct {
	SourceID          string     `json:"sourceID,omitempty"`
	ObservedAt        string     `json:"observedAt,omitempty"`
	ResolvedInputHash string     `json:"resolvedInputHash,omitempty"`
	TierPolicy        TierPolicy `json:"tierPolicy,omitempty"`
	Triggers          []Trigger  `json:"triggers,omitempty"`
	Groups            []Group    `json:"groups,omitempty"`
	Steps             []Step     `json:"steps,omitempty"`
	AbortBehavior     string     `json:"abortBehavior,omitempty"`
	// DeviceCapabilities carries the resolver's matched capability profiles,
	// one per UPS device, so trigger definitions can be validated against what
	// the devices actually report (PL-19). Empty means the caller supplied no
	// capability context and trigger-capability validation is skipped.
	DeviceCapabilities []DeviceCapability `json:"deviceCapabilities,omitempty"`
	// PowerDomains is the resolver's derived domain membership, used to decide
	// which devices a domain-scoped trigger is validated against.
	PowerDomains []PowerDomainMembership `json:"powerDomains,omitempty"`
	// GroupNodes is which cluster nodes each group touches, resolved before the
	// planner runs because expanding a selector requires reading the cluster.
	// Without it the planner cannot name a node, and PL-20 clearance edges
	// cannot be derived. Empty means the caller supplied no node context and
	// clearance derivation is skipped.
	GroupNodes []GroupNodeMembership `json:"groupNodes,omitempty"`
}

// GroupNodeMembership is one group's relationship to real cluster nodes.
//
// Acts and Releases are deliberately separate. A group that drains a node acts
// on it; a group that powers it off releases it. The distinction is the whole
// basis of PL-20: a node is a terminal vertex, so everything acting on it must
// finish before whatever releases it.
type GroupNodeMembership struct {
	Group string `json:"group"`
	// Acts are nodes this group does work on without powering them off.
	Acts []string `json:"acts,omitempty"`
	// Releases are nodes this group powers off.
	Releases []string `json:"releases,omitempty"`
}

// DeviceCapability is one UPS device's resolved telemetry capability, reduced
// to what the planner needs. The planner never performs profile matching.
type DeviceCapability struct {
	DeviceID           string   `json:"deviceID"`
	ProfileID          string   `json:"profileID,omitempty"`
	Unidentified       bool     `json:"unidentified,omitempty"`
	TelemetryVariables []string `json:"telemetryVariables,omitempty"`
}

// PowerDomainMembership names the UPS devices derived into one power domain.
type PowerDomainMembership struct {
	Name       string   `json:"name"`
	UPSDevices []string `json:"upsDevices,omitempty"`
}

// TierPolicy assigns default shutdown tiers for groups that do not carry an
// explicit tier. Higher tiers stop earlier. Tier 0 is last-ditch and cannot be
// targeted by a flow.
type TierPolicy struct {
	LabelKey      string           `json:"labelKey,omitempty"`
	DefaultTier   *int32           `json:"defaultTier,omitempty"`
	Definitions   []TierDefinition `json:"definitions,omitempty"`
	SelectorRules []TierSelector   `json:"selectorRules,omitempty"`
}

// TierDefinition names a numbered shutdown tier.
type TierDefinition struct {
	Tier        int32  `json:"tier"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// TierSelector is carried in structural identity for central tier policy. The
// Kubernetes adapter owns selector evaluation before planner input assembly.
type TierSelector struct {
	Name    string `json:"name"`
	Subject string `json:"subject,omitempty"`
	Tier    int32  `json:"tier"`
	Hash    string `json:"hash,omitempty"`
}

// TelemetryInputs are intentionally separate from StructuralInputs so changing
// live power readings does not change the structural plan hash.
type TelemetryInputs struct {
	PowerDomains []PowerDomainSnapshot `json:"powerDomains,omitempty"`
}

// PowerDomainSnapshot captures live power data used for advisory feasibility.
type PowerDomainSnapshot struct {
	Domain                  string `json:"domain"`
	ObservedAt              string `json:"observedAt,omitempty"`
	RuntimeRemainingSeconds *int64 `json:"runtimeRemainingSeconds,omitempty"`
	ChargePercent           *int32 `json:"chargePercent,omitempty"`
	OnBattery               *bool  `json:"onBattery,omitempty"`
	Stale                   bool   `json:"stale,omitempty"`
}

// Trigger describes a structurally valid trigger definition.
type Trigger struct {
	Type                string   `json:"type"`
	UPSDevices          []string `json:"upsDevices,omitempty"`
	PowerDomains        []string `json:"powerDomains,omitempty"`
	For                 Duration `json:"for,omitempty"`
	RuntimeBelowSeconds *int64   `json:"runtimeBelowSeconds,omitempty"`
	ChargeBelowPercent  *int32   `json:"chargeBelowPercent,omitempty"`
}

// Group is a graph vertex in a compiled shutdown flow.
type Group struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Action       string            `json:"action"`
	Target       Target            `json:"target,omitempty"`
	Requires     []string          `json:"requires,omitempty"`
	Before       []string          `json:"before,omitempty"`
	After        []string          `json:"after,omitempty"`
	Phase        *int32            `json:"phase,omitempty"`
	ShutdownTier *int32            `json:"shutdownTier,omitempty"`
	Timeout      Duration          `json:"timeout,omitempty"`
	Params       map[string]string `json:"params,omitempty"`
}

// Step is a linear fallback action for simple installs.
type Step struct {
	ID              string            `json:"id"`
	Action          string            `json:"action"`
	Target          Target            `json:"target,omitempty"`
	Duration        Duration          `json:"duration,omitempty"`
	Timeout         Duration          `json:"timeout,omitempty"`
	ContinueOnError bool              `json:"continueOnError,omitempty"`
	Params          map[string]string `json:"params,omitempty"`
}

// Target is a compact planner-side target summary input.
type Target struct {
	NodeSelector      bool `json:"nodeSelector,omitempty"`
	NamespaceSelector bool `json:"namespaceSelector,omitempty"`
	WorkloadSelector  bool `json:"workloadSelector,omitempty"`
	NamespaceCount    int  `json:"namespaceCount,omitempty"`
	WorkloadRefCount  int  `json:"workloadRefCount,omitempty"`
	AgentRefCount     int  `json:"agentRefCount,omitempty"`
}

// Duration wraps time.Duration with stable JSON encoding.
type Duration struct {
	time.Duration `json:"-"`
}

// Plan is the deterministic compiler output.
type Plan struct {
	Hash              string         `json:"hash,omitempty"`
	StructuralHash    string         `json:"structuralHash,omitempty"`
	Steps             []CompiledStep `json:"steps,omitempty"`
	Waves             []Wave         `json:"waves,omitempty"`
	StartupWaves      []Wave         `json:"startupWaves,omitempty"`
	Graph             Graph          `json:"graph,omitempty"`
	Explanations      []Explanation  `json:"explanations,omitempty"`
	Diagrams          DiagramExports `json:"diagrams,omitempty"`
	EstimatedDuration Duration       `json:"estimatedDuration,omitempty"`
	Feasibility       Feasibility    `json:"feasibility,omitempty"`
}

// CompiledStep is the flattened review view for status and audit surfaces.
type CompiledStep struct {
	ID                 string   `json:"id"`
	Index              int32    `json:"index"`
	Action             string   `json:"action"`
	ShutdownTier       *int32   `json:"shutdownTier,omitempty"`
	TargetSummary      string   `json:"targetSummary,omitempty"`
	CumulativeDuration Duration `json:"cumulativeDuration,omitempty"`
}

// Wave groups concurrently executable shutdown groups.
type Wave struct {
	Index              int32    `json:"index"`
	Phase              *int32   `json:"phase,omitempty"`
	ShutdownTier       *int32   `json:"shutdownTier,omitempty"`
	Groups             []string `json:"groups"`
	Duration           Duration `json:"duration,omitempty"`
	CumulativeDuration Duration `json:"cumulativeDuration,omitempty"`
}

// Graph is the normalized dependency graph artifact emitted by the planner.
type Graph struct {
	Vertices []GraphVertex `json:"vertices,omitempty"`
	Edges    []GraphEdge   `json:"edges,omitempty"`
}

// GraphVertex is a normalized plan node. It is intentionally small enough to
// publish through status and durable audit records.
type GraphVertex struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Label         string `json:"label,omitempty"`
	Action        string `json:"action,omitempty"`
	Phase         *int32 `json:"phase,omitempty"`
	ShutdownTier  *int32 `json:"shutdownTier,omitempty"`
	TargetSummary string `json:"targetSummary,omitempty"`
}

// GraphEdge is a normalized directed dependency from From to To.
type GraphEdge struct {
	ID          string           `json:"id"`
	From        string           `json:"from"`
	To          string           `json:"to"`
	Relation    string           `json:"relation"`
	Provenance  string           `json:"provenance"`
	Sources     []GraphSourceRef `json:"sources,omitempty"`
	Explanation string           `json:"explanation"`
}

// GraphSourceRef points to the declarative input field that produced an edge.
type GraphSourceRef struct {
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	Field string `json:"field,omitempty"`
}

// Explanation is a publishable "why" statement for planner decisions.
type Explanation struct {
	ID      string `json:"id"`
	Subject string `json:"subject,omitempty"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// DiagramExports are deterministic renderings generated from Graph.
type DiagramExports struct {
	Mermaid     string `json:"mermaid,omitempty"`
	GraphvizDOT string `json:"graphvizDOT,omitempty"`
	D2          string `json:"d2,omitempty"`
}

// Diagnostic is a machine-readable warning or rejection from compilation.
type Diagnostic struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

// Feasibility is advisory at compile time; execution performs authoritative
// checks against fresh telemetry.
type Feasibility struct {
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
