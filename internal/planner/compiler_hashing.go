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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func hashPlan(plan *Plan, scoped StructuralInputs) []Diagnostic {
	var err error
	var diagnostics []Diagnostic
	plan.StructuralHash, err = stableHash(scoped)
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "PlanHashEncodingFailed",
			Message:  fmt.Sprintf("plan structural hash could not be computed: %v", err),
		})
		return diagnostics
	}
	plan.Hash, err = stableHash(struct {
		StructuralHash string         `json:"structuralHash"`
		Steps          []CompiledStep `json:"steps,omitempty"`
		Waves          []Wave         `json:"waves,omitempty"`
		StartupWaves   []Wave         `json:"startupWaves,omitempty"`
		Graph          Graph          `json:"graph,omitempty"`
		Duration       Duration       `json:"estimatedDuration,omitempty"`
	}{
		StructuralHash: plan.StructuralHash,
		Steps:          plan.Steps,
		Waves:          plan.Waves,
		StartupWaves:   plan.StartupWaves,
		Graph:          plan.Graph,
		Duration:       plan.EstimatedDuration,
	})
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "PlanHashEncodingFailed",
			Message:  fmt.Sprintf("plan hash could not be computed: %v", err),
		})
		return diagnostics
	}

	return nil
}

// stableHash returns encoding failures to Compile so power-event planning can report
// a diagnosable rejection without panicking in reconciliation.
func stableHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode value for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
