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
}

// StructuralBundle is the deterministic structure emitted toward the planner.
type StructuralBundle struct {
	Hash              string                   `json:"hash,omitempty"`
	SourceID          string                   `json:"sourceID,omitempty"`
	ObservedAt        string                   `json:"observedAt,omitempty"`
	Topology          inventory.Topology       `json:"topology,omitempty"`
	CapabilityMatches []capability.MatchResult `json:"capabilityMatches,omitempty"`
}

// Diagnostic attributes a resolver warning or rejection to the source stage.
type Diagnostic struct {
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}
