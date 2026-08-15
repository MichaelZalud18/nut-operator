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
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
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

func TestPlannerInputsCarryEffectiveTierOverrunPolicy(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{{
				Name:   "applications",
				Action: powerv1alpha1.ShutdownStepScaleWorkload,
			}},
		},
	}

	inputs := PlannerInputs(flow)
	if inputs.TierOverrunPolicy != string(powerv1alpha1.ShutdownTierOverrunWait) {
		t.Fatalf("expected default Wait policy in planner inputs, got %q", inputs.TierOverrunPolicy)
	}

	flow.Spec.TierOverrunPolicy = powerv1alpha1.ShutdownTierOverrunPreempt
	inputs = PlannerInputs(flow)
	if inputs.TierOverrunPolicy != string(powerv1alpha1.ShutdownTierOverrunPreempt) {
		t.Fatalf("expected Preempt policy in planner inputs, got %q", inputs.TierOverrunPolicy)
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

func TestCompileArtifactPublishesResolvedPowerDomains(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{
				Type:         powerv1alpha1.ShutdownTriggerOnBattery,
				PowerDomains: []string{"rack-a"},
			}},
			Groups: []powerv1alpha1.ShutdownGroup{{
				Name:   "applications",
				Action: powerv1alpha1.ShutdownStepScaleWorkload,
			}},
		},
	}
	bundle := resolver.StructuralBundle{
		Hash: "inventory-hash",
		Topology: inventory.Topology{
			Domains: []inventory.PowerDomain{{
				Name:           "rack-a",
				UPSDevices:     []string{"ups-a"},
				Members:        []string{"node-a", "switch-a", "ups-a"},
				Nodes:          []string{"node-a"},
				Infrastructure: []string{"switch-a"},
			}},
		},
	}

	_, _, _, _, artifact := CompileArtifactWithResolvedInputs(flow, bundle)
	if artifact == nil {
		t.Fatal("expected published artifact")
	}

	want := []powerv1alpha1.PublishedPowerDomainStatus{{
		Name:           "rack-a",
		UPSDevices:     []string{"ups-a"},
		Members:        []string{"node-a", "switch-a", "ups-a"},
		Nodes:          []string{"node-a"},
		Infrastructure: []string{"switch-a"},
	}}
	if !reflect.DeepEqual(artifact.PowerDomains, want) {
		t.Fatalf("expected resolved power domains %#v, got %#v", want, artifact.PowerDomains)
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

// nodeExpansionFlow drains nodes labelled for one rack and powers off the same
// rack through its agent.
func nodeExpansionFlow() *powerv1alpha1.ShutdownFlow {
	return &powerv1alpha1.ShutdownFlow{
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Groups: []powerv1alpha1.ShutdownGroup{
				{
					Name:   "drain-rack-a",
					Action: powerv1alpha1.ShutdownStepDrainNodes,
					Target: powerv1alpha1.ShutdownStepTarget{
						NodeSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"rack": "a"},
						},
					},
				},
				{
					Name:   "poweroff-rack-a",
					Action: powerv1alpha1.ShutdownStepAgentShutdown,
					Target: powerv1alpha1.ShutdownStepTarget{
						AgentRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a-agent"}},
					},
				},
			},
		},
	}
}

func nodeExpansionBundle() resolver.StructuralBundle {
	return resolver.StructuralBundle{
		ClusterNodes: []resolver.ClusterNode{
			{Name: "node-a1", Labels: map[string]string{"rack": "a"}},
			{Name: "node-a2", Labels: map[string]string{"rack": "a"}},
			{Name: "node-b1", Labels: map[string]string{"rack": "b"}},
		},
		AgentCoverage: []resolver.AgentCoverage{
			{Name: "rack-a-agent", Nodes: []string{"node-a1", "node-a2"}},
		},
	}
}

func TestPlannerGroupNodesExpandsSelectorsAndAgentCoverage(t *testing.T) {
	membership := PlannerGroupNodes(nodeExpansionFlow(), nodeExpansionBundle())
	if len(membership) != 2 {
		t.Fatalf("expected membership for both groups, got %#v", membership)
	}

	byGroup := map[string]planner.GroupNodeMembership{}
	for _, entry := range membership {
		byGroup[entry.Group] = entry
	}

	drain := byGroup["drain-rack-a"]
	if got, want := drain.Acts, []string{"node-a1", "node-a2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the selector to match only rack a, got %#v", got)
	}
	if len(drain.Releases) != 0 {
		t.Fatalf("a drain group releases nothing, got %#v", drain.Releases)
	}

	release := byGroup["poweroff-rack-a"]
	if got, want := release.Releases, []string{"node-a1", "node-a2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected agent coverage to supply released nodes, got %#v", got)
	}
	if len(release.Acts) != 0 {
		t.Fatalf("release membership comes from agent coverage alone, got %#v", release.Acts)
	}
}

// An absent node selector is not "every node". Treating it as Kubernetes'
// match-all would derive a clearance edge against the whole cluster from a group
// that never mentions a node.
func TestPlannerGroupNodesTreatsAbsentSelectorAsNoNodes(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Groups: []powerv1alpha1.ShutdownGroup{{
				Name:   "scale-apps",
				Action: powerv1alpha1.ShutdownStepScaleWorkload,
			}},
		},
	}
	if membership := PlannerGroupNodes(flow, nodeExpansionBundle()); membership != nil {
		t.Fatalf("expected no membership without a node selector, got %#v", membership)
	}
}

func TestPlannerGroupNodesWithoutClusterContextIsEmpty(t *testing.T) {
	if membership := PlannerGroupNodes(nodeExpansionFlow(), resolver.StructuralBundle{}); membership != nil {
		t.Fatalf("expected no membership without cluster context, got %#v", membership)
	}
}

// The expansion has to reach the compiled plan, not just exist: without the
// derived edge these two groups compile into one wave.
func TestCompileArtifactOrdersDrainBeforePowerOff(t *testing.T) {
	flow := nodeExpansionFlow()
	flow.Spec.Triggers = []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}}

	_, waves, _, _, artifact := CompileArtifactWithResolvedInputs(flow, nodeExpansionBundle())
	if len(waves) != 2 {
		t.Fatalf("expected drain and poweroff in separate waves, got %#v", waves)
	}
	if got, want := waves[0].Groups, []string{"drain-rack-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the drain to run first, got %#v", got)
	}
	if got, want := waves[1].Groups, []string{"poweroff-rack-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected the poweroff to run second, got %#v", got)
	}

	var found bool
	for _, edge := range artifact.Graph.Edges {
		if edge.Relation == planner.GraphEdgeRelationNodeClearance {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a published node-clearance edge, got %#v", artifact.Graph.Edges)
	}
}
