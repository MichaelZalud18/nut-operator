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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

func TestPlannerInputsResolveShutdownTierFromGroupAndTargetLabel(t *testing.T) {
	explicitTier := int32(2)
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{
				{
					Name:         "explicit",
					Action:       powerv1alpha1.ShutdownStepScaleWorkload,
					ShutdownTier: &explicitTier,
					Target: powerv1alpha1.ShutdownStepTarget{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
							powerv1alpha1.DefaultShutdownTierLabelKey: "5",
						}},
					},
				},
				{
					Name:   "labeled",
					Action: powerv1alpha1.ShutdownStepScaleWorkload,
					Target: powerv1alpha1.ShutdownStepTarget{
						WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
							powerv1alpha1.DefaultShutdownTierLabelKey: "4",
						}},
					},
				},
			},
		},
	}

	inputs := PlannerInputsWithTierPolicy(flow, powerv1alpha1.PowerShutdownTierPolicySpec{})
	groups := map[string]powerv1alpha1.ShutdownGroup{}
	for _, group := range flow.Spec.Groups {
		groups[group.Name] = group
	}

	if got := inputs.Groups[0].ShutdownTier; got == nil || *got != explicitTier {
		t.Fatalf("expected explicit tier %d to win, got %#v for %#v", explicitTier, got, groups["explicit"])
	}
	if got := inputs.Groups[1].ShutdownTier; got == nil || *got != 4 {
		t.Fatalf("expected selector tier 4, got %#v", got)
	}
}

func TestCompileArtifactWithTierPolicyPublishesTierStatus(t *testing.T) {
	appTier := int32(3)
	nodeTier := int32(1)
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{
				{
					Name:         "nodes",
					Action:       powerv1alpha1.ShutdownStepAgentShutdown,
					ShutdownTier: &nodeTier,
					Target: powerv1alpha1.ShutdownStepTarget{
						AgentRefs: []powerv1alpha1.ObjectNameReference{{Name: "standard-agents"}},
					},
				},
				{
					Name:         "applications",
					Action:       powerv1alpha1.ShutdownStepScaleWorkload,
					ShutdownTier: &appTier,
					Target: powerv1alpha1.ShutdownStepTarget{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "demo"}},
					},
				},
			},
		},
	}

	steps, waves, _, hash, artifact := CompileArtifactWithResolvedInputsAndTierPolicy(flow, resolver.StructuralBundle{Hash: "inventory-hash"}, powerv1alpha1.PowerShutdownTierPolicySpec{})

	if hash == "" || artifact == nil {
		t.Fatalf("expected compiled artifact and hash, got hash=%q artifact=%#v", hash, artifact)
	}
	if got := steps[0].ShutdownTier; got == nil || *got != appTier {
		t.Fatalf("expected first compiled step tier %d, got %#v", appTier, got)
	}
	if got := waves[0].ShutdownTier; got == nil || *got != appTier {
		t.Fatalf("expected first wave tier %d, got %#v", appTier, got)
	}
	if got := artifact.Graph.Vertices[0].ShutdownTier; got == nil || *got != appTier {
		t.Fatalf("expected first graph vertex tier %d, got %#v", appTier, got)
	}
	if len(artifact.Graph.Edges) != 1 || artifact.Graph.Edges[0].Relation != "ShutdownTier" {
		t.Fatalf("expected one shutdown tier graph edge, got %#v", artifact.Graph.Edges)
	}
}

func TestPlannerInputsResolveShutdownTierFromCentralSelectorRule(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{{
				Name:   "databases",
				Action: powerv1alpha1.ShutdownStepScaleWorkload,
				Target: powerv1alpha1.ShutdownStepTarget{
					WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
						"app.kubernetes.io/part-of": "database",
					}},
				},
			}},
		},
	}
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

	inputs := PlannerInputsWithTierPolicy(flow, policy)

	if got := inputs.Groups[0].ShutdownTier; got == nil || *got != 2 {
		t.Fatalf("expected selector rule tier 2, got %#v", got)
	}
}
