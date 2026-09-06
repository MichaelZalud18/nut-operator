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

package shutdownflow

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// F-120: the planner's own ShutdownTierZeroTargeted diagnostic (internal/planner/tiers.go) only
// ever sees the group's resolved *int32 tier, so it cannot say whether a human wrote
// spec.shutdownTier: 0 or whether central tier policy assigned it. TierPolicyDiagnostics is the
// adapter-owned fix: it has the rule/label match already, at the exact point PlannerShutdownTier
// resolves it, and this file proves it attributes correctly rather than assuming the resolution
// logic and the diagnostic logic can't drift apart.

func tierDiagnosticsInt32Ptr(v int32) *int32 { return &v }

func TestTierPolicyDiagnosticsNamesTheSelectorRuleThatAssignedTierZero(t *testing.T) {
	groups := []powerv1alpha1.ShutdownGroup{{
		Name:   "frontend",
		Action: powerv1alpha1.ShutdownStepScaleWorkload,
		Target: powerv1alpha1.ShutdownStepTarget{
			WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/part-of": "web",
			}},
		},
	}}
	policy := powerv1alpha1.PowerShutdownTierPolicySpec{
		SelectorRules: []powerv1alpha1.PowerShutdownTierSelectorRule{{
			Name:    "web-workloads-tier-zero",
			Subject: powerv1alpha1.PowerShutdownTierSubjectWorkload,
			Tier:    0,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/part-of": "web",
			}},
		}},
	}

	diagnostics := TierPolicyDiagnostics(groups, policy)

	if len(diagnostics) != 1 {
		t.Fatalf("expected exactly one diagnostic, got %d: %+v", len(diagnostics), diagnostics)
	}
	got := diagnostics[0]
	if got.Reason != "ShutdownTierZeroFromPolicy" {
		t.Fatalf("expected Reason ShutdownTierZeroFromPolicy, got %q", got.Reason)
	}
	if got.Subject != "frontend" {
		t.Fatalf("expected Subject to name the group, got %q", got.Subject)
	}
	if !strings.Contains(got.Message, "web-workloads-tier-zero") {
		t.Fatalf("expected the message to name the responsible rule, got %q", got.Message)
	}
}

func TestTierPolicyDiagnosticsNamesTheLabelThatAssignedTierZero(t *testing.T) {
	groups := []powerv1alpha1.ShutdownGroup{{
		Name:   "legacy-batch",
		Action: powerv1alpha1.ShutdownStepScaleWorkload,
		Target: powerv1alpha1.ShutdownStepTarget{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				powerv1alpha1.DefaultShutdownTierLabelKey: "0",
			}},
		},
	}}

	diagnostics := TierPolicyDiagnostics(groups, powerv1alpha1.PowerShutdownTierPolicySpec{})

	if len(diagnostics) != 1 {
		t.Fatalf("expected exactly one diagnostic, got %d: %+v", len(diagnostics), diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, powerv1alpha1.DefaultShutdownTierLabelKey) {
		t.Fatalf("expected the message to name the responsible label key, got %q", diagnostics[0].Message)
	}
}

func TestTierPolicyDiagnosticsSkipsAnExplicitGroupTierZero(t *testing.T) {
	groups := []powerv1alpha1.ShutdownGroup{{
		Name:         "explicitly-tier-zero",
		Action:       powerv1alpha1.ShutdownStepScaleWorkload,
		ShutdownTier: tierDiagnosticsInt32Ptr(0),
	}}

	diagnostics := TierPolicyDiagnostics(groups, powerv1alpha1.PowerShutdownTierPolicySpec{})

	if len(diagnostics) != 0 {
		t.Fatalf("expected no adapter diagnostic for an explicit spec.shutdownTier: 0 -- "+
			"the planner's own diagnostic already names the right source -- got %+v", diagnostics)
	}
}

func TestTierPolicyDiagnosticsIgnoresNonZeroPolicyTiers(t *testing.T) {
	groups := []powerv1alpha1.ShutdownGroup{{
		Name:   "databases",
		Action: powerv1alpha1.ShutdownStepScaleWorkload,
		Target: powerv1alpha1.ShutdownStepTarget{
			WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/part-of": "database",
			}},
		},
	}}
	policy := powerv1alpha1.PowerShutdownTierPolicySpec{
		SelectorRules: []powerv1alpha1.PowerShutdownTierSelectorRule{{
			Name:    "database-workloads",
			Subject: powerv1alpha1.PowerShutdownTierSubjectWorkload,
			Tier:    2,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/part-of": "database",
			}},
		}},
	}

	diagnostics := TierPolicyDiagnostics(groups, policy)

	if len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostic when the resolved policy tier is not 0, got %+v", diagnostics)
	}
}

// TestCompileFlowSurfacesBothTierZeroDiagnostics proves the wiring in
// CompileFlowWithHistoryAndHooks, not just the standalone function: a group whose tier came from
// a selector rule should carry both the planner's generic ShutdownTierZeroTargeted diagnostic and
// the adapter's ShutdownTierZeroFromPolicy one naming the rule, in the same compiled result.
func TestCompileFlowSurfacesBothTierZeroDiagnostics(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{{
				Name:   "frontend",
				Action: powerv1alpha1.ShutdownStepScaleWorkload,
				Target: powerv1alpha1.ShutdownStepTarget{
					WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
						"app.kubernetes.io/part-of": "web",
					}},
				},
			}},
		},
	}
	policy := powerv1alpha1.PowerShutdownTierPolicySpec{
		SelectorRules: []powerv1alpha1.PowerShutdownTierSelectorRule{{
			Name:    "web-workloads-tier-zero",
			Subject: powerv1alpha1.PowerShutdownTierSubjectWorkload,
			Tier:    0,
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/part-of": "web",
			}},
		}},
	}

	compiled := CompileFlow(flow, resolver.StructuralBundle{}, policy)

	var sawPlannerDiagnostic, sawAdapterDiagnostic bool
	for _, diagnostic := range compiled.Diagnostics {
		switch diagnostic.Reason {
		case "ShutdownTierZeroTargeted":
			sawPlannerDiagnostic = true
		case "ShutdownTierZeroFromPolicy":
			sawAdapterDiagnostic = true
			if !strings.Contains(diagnostic.Message, "web-workloads-tier-zero") {
				t.Fatalf("expected the adapter diagnostic to name the rule, got %q", diagnostic.Message)
			}
		}
	}
	if !sawPlannerDiagnostic {
		t.Fatalf("expected the planner's ShutdownTierZeroTargeted diagnostic, got %+v", compiled.Diagnostics)
	}
	if !sawAdapterDiagnostic {
		t.Fatalf("expected the adapter's ShutdownTierZeroFromPolicy diagnostic, got %+v", compiled.Diagnostics)
	}
}
