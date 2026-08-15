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

package controller

import (
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

func TestPlannerCompileMetricResultUsesPlannerDiagnostics(t *testing.T) {
	tests := []struct {
		name        string
		configHash  string
		diagnostics []planner.Diagnostic
		want        string
	}{
		{
			name:       "accepted clean plan",
			configHash: "plan-hash",
			want:       "Accepted",
		},
		{
			name:       "planner rejection names first error diagnostic",
			configHash: "",
			diagnostics: []planner.Diagnostic{
				{Severity: planner.DiagnosticWarning, Reason: "GroupsPreferred"},
				{Severity: planner.DiagnosticError, Reason: "DependencyCycle"},
			},
			want: "DependencyCycle",
		},
		{
			name:       "missing hash without diagnostic stays generic",
			configHash: "",
			want:       "PlannerFailed",
		},
		{
			name:       "accepted degraded plan names first warning diagnostic",
			configHash: "plan-hash",
			diagnostics: []planner.Diagnostic{
				{Severity: planner.DiagnosticWarning, Reason: "ShutdownTierInversion"},
			},
			want: "ShutdownTierInversion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := plannerCompileMetricResult(tt.configHash, tt.diagnostics); got != tt.want {
				t.Fatalf("plannerCompileMetricResult() = %q, want %q", got, tt.want)
			}
		})
	}
}
