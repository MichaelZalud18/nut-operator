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
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	shutdownflowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func executionScopeFixture(t *testing.T) (*ShutdownFlowReconciler, *power.ShutdownFlow, resolver.StructuralBundle) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := upsDeviceTelemetry("ups-a", power.UPSDevicePhaseOnBattery, 300, 50, 10)
	b := upsDeviceTelemetry("ups-b", power.UPSDevicePhaseOnline, 900, 90, 10)
	objects := []runtime.Object{&a, &b}
	flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "scope"}, Spec: power.ShutdownFlowSpec{Mode: power.ShutdownFlowModeEnforce,
		Triggers: []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery, PowerDomains: []string{"rack-a"}}, {Type: power.ShutdownTriggerOnBattery, PowerDomains: []string{"rack-b"}}},
	}}
	bundle := resolver.StructuralBundle{Topology: inventory.Topology{Domains: []inventory.PowerDomain{{Name: "rack-a", UPSDevices: []string{"ups-a"}, Nodes: []string{"a"}}, {Name: "rack-b", UPSDevices: []string{"ups-b"}, Nodes: []string{"b"}}}}}
	for _, node := range []string{"a", "b", "unknown"} {
		labels := map[string]string{"node": node}
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node, Labels: labels}})
		bundle.ClusterNodes = append(bundle.ClusterNodes, resolver.ClusterNode{Name: node, Labels: labels})
		flow.Spec.Groups = append(flow.Spec.Groups, power.ShutdownGroup{Name: node, Action: power.ShutdownStepCordonNodes, Target: power.ShutdownStepTarget{NodeSelector: &metav1.LabelSelector{MatchLabels: labels}}})
	}
	flow.Spec.Groups = append(flow.Spec.Groups,
		power.ShutdownGroup{Name: "mixed", Action: power.ShutdownStepCordonNodes, Target: power.ShutdownStepTarget{NodeSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "node", Operator: metav1.LabelSelectorOpIn, Values: []string{"a", "b"}}}}}},
		power.ShutdownGroup{Name: "notify", Action: power.ShutdownStepNotify})
	r := &ShutdownFlowReconciler{Scheme: scheme, Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&power.UPSDevice{}).WithRuntimeObjects(objects...).Build()}
	return r, flow, bundle
}

func TestEligibleDomainSelectionReachesExecutor(t *testing.T) {
	for _, scenario := range []string{"one", "both", "held", "global", "communication"} {
		for _, linear := range []bool{false, true} {
			t.Run(scenario+"/linear="+map[bool]string{false: "false", true: "true"}[linear], func(t *testing.T) {
				r, flow, bundle := executionScopeFixture(t)
				ctx := context.Background()
				now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
				if scenario == "both" || scenario == "held" {
					device := power.UPSDevice{}
					if err := r.Get(ctx, client.ObjectKey{Name: "ups-b"}, &device); err != nil {
						t.Fatal(err)
					}
					device.Status.Phase = power.UPSDevicePhaseOnBattery
					if err := r.Status().Update(ctx, &device); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "held" {
					flow.Spec.Triggers[1].For = &metav1.Duration{Duration: time.Minute}
				}
				if scenario == "global" {
					flow.Spec.Triggers = []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery}}
				}
				if scenario == "communication" {
					bundle.Topology.CommunicationOrders = []inventory.DerivedEdge{{From: "b", To: "switch-a", Source: "uplink"}}
					bundle.Topology.Domains[0].Infrastructure = []string{"switch-a"}
				}
				if linear {
					for _, group := range flow.Spec.Groups {
						flow.Spec.Steps = append(flow.Spec.Steps, power.ShutdownStep{ID: group.Name, Type: group.Action, Target: group.Target})
					}
					flow.Spec.Groups = nil
				}
				original := flow.DeepCopy()
				_, evaluation, holds, err := evaluateShutdownFlowTriggers(ctx, r.Client, flow, bundle, now, "")
				if err != nil || !evaluation.Eligible {
					t.Fatalf("evaluation: %+v %v", evaluation, err)
				}
				compiled := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, evaluation)
				if compiled.ConfigHash == "" {
					t.Fatalf("compile: %+v", compiled.Diagnostics)
				}
				if !reflect.DeepEqual(original.Spec, flow.Spec) {
					t.Fatal("scope rewrote authored policy")
				}
				flow.Status.CompiledSteps, flow.Status.CompiledWaves = compiled.Steps, compiled.Waves
				flow.Status.PublishedArtifact = compiled.Artifact
				input, err := r.shutdownExecutionInput(ctx, flow, now, "inputs", compiled.ConfigHash, evaluation, "scope-test", bundle, false, shutdownExecutionResumeEvidence{})
				if err != nil {
					t.Fatal(err)
				}
				var calls []string
				e := executor.Executor{Runner: communicationOrderRunner(func(_ context.Context, action executor.Action) (executor.ActionOutcome, error) {
					calls = append(calls, action.Group.Name)
					return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
				})}
				if _, err := e.Execute(ctx, input); err != nil {
					t.Fatal(err)
				}
				want := []string{"a", "mixed", "notify", "unknown"}
				if scenario == "both" || scenario == "communication" {
					want = append(want, "b")
				}
				slices.Sort(want)
				slices.Sort(calls)
				if !slices.Equal(calls, want) || len(input.Groups) != len(want) {
					t.Fatalf("executed=%v want=%v groups=%+v", calls, want, input.Groups)
				}
				if scenario == "held" {
					flow.Status.TriggerHoldStates = holds
					_, next, _, err := evaluateShutdownFlowTriggers(ctx, r.Client, flow, bundle, now.Add(time.Minute), compiled.ConfigHash)
					if err != nil || !slices.Equal(next.SelectedUPSDevices, []string{"ups-a", "ups-b"}) {
						t.Fatalf("hold expansion: %+v %v", next, err)
					}
					expanded := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, next)
					if len(expanded.Steps) != 5 || expanded.ConfigHash == compiled.ConfigHash {
						t.Fatal("eligible scope expansion lost work or identity")
					}
				}
			})
		}
	}
}

func TestExecutionScopeHistoryUsesExecutedPlanHash(t *testing.T) {
	_, flow, bundle := executionScopeFixture(t)
	preview := shutdownflowadapter.CompileWithHistoryLookup(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil)
	evaluation := &power.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}}
	var lookup string
	compiled := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, func(hash string) planner.HistoryInputs {
		lookup = hash
		return planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"a": {time.Second}}}
	}, nil, evaluation)
	if compiled.ConfigHash == "" || lookup != compiled.ConfigHash || lookup == preview.ConfigHash {
		t.Fatalf("lookup=%s compiled=%s preview=%s", lookup, compiled.ConfigHash, preview.ConfigHash)
	}
}

func TestScopedExecutionStillValidatesConfiguredPlan(t *testing.T) {
	_, flow, bundle := executionScopeFixture(t)
	flow.Spec.Groups[1].After = []string{"b"}
	compiled := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, &power.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}})
	if compiled.ConfigHash != "" {
		t.Fatal("inactive scope hid an invalid configured dependency")
	}
}

func TestPrunedAgentReferenceIsNotResolvedForExecution(t *testing.T) {
	r, flow, bundle := executionScopeFixture(t)
	flow.Spec.Groups[1].Action = power.ShutdownStepAgentShutdown
	flow.Spec.Groups[1].Target = power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: "missing-agent"}}}
	bundle.AgentCoverage = []resolver.AgentCoverage{{Name: "missing-agent", Nodes: []string{"b"}}}
	evaluation := &power.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}}
	compiled := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, evaluation)
	if compiled.ConfigHash == "" {
		t.Fatalf("compile: %+v", compiled.Diagnostics)
	}
	flow.Status.CompiledSteps, flow.Status.CompiledWaves = compiled.Steps, compiled.Waves
	if _, err := r.shutdownExecutionInput(context.Background(), flow, time.Now(), "inputs", compiled.ConfigHash, evaluation, "episode", bundle, false, shutdownExecutionResumeEvidence{}); err != nil {
		t.Fatalf("pruned agent blocked unrelated work: %v", err)
	}
}

func TestPartiallyUnresolvedAgentTargetRemainsInScope(t *testing.T) {
	for _, linear := range []bool{false, true} {
		_, flow, bundle := executionScopeFixture(t)
		flow.Spec.Groups = []power.ShutdownGroup{{Name: "partly-resolved", Action: power.ShutdownStepAgentShutdown,
			Target: power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: "healthy-agent"}, {Name: "unknown-agent"}}},
		}}
		bundle.AgentCoverage = []resolver.AgentCoverage{{Name: "healthy-agent", Nodes: []string{"b"}}}
		flow.Spec.CommunicationPaths = []power.FlowCommunicationPath{{Service: "OperatorAPI", Exempt: true}, {Service: "NUT", Exempt: true}}
		bundle.Topology.CommunicationOrders = []inventory.DerivedEdge{{From: "other-node", To: "transit"}}
		bundle.Topology.Domains[0].Infrastructure = []string{"transit"}
		if linear {
			g := flow.Spec.Groups[0]
			flow.Spec.Steps = []power.ShutdownStep{{ID: g.Name, Type: g.Action, Target: g.Target}}
			flow.Spec.Groups = nil
		}
		compiled := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, &power.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}})
		if compiled.ConfigHash == "" || len(compiled.Steps) != 1 {
			t.Fatalf("partly unresolved target lost linear=%v: %+v", linear, compiled.Diagnostics)
		}
		budget := compiled.Artifact.CommunicationBudget
		if !slices.Equal(budget.UnresolvedActions, []string{"partly-resolved"}) || !slices.Equal(budget.UPSDevices, []string{"ups-a"}) {
			t.Fatalf("unresolved agent lost conservative communication coverage: %+v", budget)
		}
	}
}
