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
	SourceID          string    `json:"sourceID,omitempty"`
	ObservedAt        string    `json:"observedAt,omitempty"`
	ResolvedInputHash string    `json:"resolvedInputHash,omitempty"`
	Triggers          []Trigger `json:"triggers,omitempty"`
	Groups            []Group   `json:"groups,omitempty"`
	Steps             []Step    `json:"steps,omitempty"`
	AbortBehavior     string    `json:"abortBehavior,omitempty"`
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
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Action      string            `json:"action"`
	Target      Target            `json:"target,omitempty"`
	Requires    []string          `json:"requires,omitempty"`
	Before      []string          `json:"before,omitempty"`
	After       []string          `json:"after,omitempty"`
	Phase       *int32            `json:"phase,omitempty"`
	Timeout     Duration          `json:"timeout,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
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
	EstimatedDuration Duration       `json:"estimatedDuration,omitempty"`
	Feasibility       Feasibility    `json:"feasibility,omitempty"`
}

// CompiledStep is the flattened review view for status and audit surfaces.
type CompiledStep struct {
	ID                 string   `json:"id"`
	Index              int32    `json:"index"`
	Action             string   `json:"action"`
	TargetSummary      string   `json:"targetSummary,omitempty"`
	CumulativeDuration Duration `json:"cumulativeDuration,omitempty"`
}

// Wave groups concurrently executable shutdown groups.
type Wave struct {
	Index              int32    `json:"index"`
	Phase              *int32   `json:"phase,omitempty"`
	Groups             []string `json:"groups"`
	Duration           Duration `json:"duration,omitempty"`
	CumulativeDuration Duration `json:"cumulativeDuration,omitempty"`
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
