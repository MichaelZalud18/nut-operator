package controller

import (
	"context"
	"errors"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type guardRefreshRunner func(context.Context, executor.Action) (executor.ActionOutcome, error)

func (f guardRefreshRunner) RunAction(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	return f(ctx, action)
}

func TestGuardRefreshAfterEarlierWave(t *testing.T) {
	for _, scenario := range []string{"drained", "new workload", "readiness lost", "selection lost", "identity changed", "API failure", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			scheme, agent, _ := authorizedReleaseFixture(t)
			freshRequired := false
			agent.Spec.Shutdown.RequireFreshTelemetry = &freshRequired
			agent.Status.SelectedNodes = []string{"node-a"}
			agent.Status.NodeStatuses = []power.NodePowerAgentNodeStatus{{NodeName: "node-a", Ready: true}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "apps"}, Spec: corev1.PodSpec{NodeName: "node-a"}}
			cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent.DeepCopy(), pod.DeepCopy()).Build()
			live := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(agent).WithObjects(agent, pod).WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string { return []string{obj.(*corev1.Pod).Spec.NodeName} }).Build()
			r := &ShutdownFlowReconciler{Client: cached, APIReader: live}
			releases, err := r.nodeReleasesForTarget(context.Background(), power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: agent.Name}}})
			if err != nil || len(releases) != 1 || releases[0].Cleared {
				t.Fatalf("initial evidence=%+v error=%v", releases, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			released := false
			runner := guardRefreshRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
				if action.Group.Name == "drain" {
					if err := live.Delete(ctx, pod); err != nil {
						return executor.ActionOutcome{}, err
					}
					switch scenario {
					case "new workload":
						pod.ResourceVersion = ""
						pod.Name = "arrived-later"
						if err := live.Create(ctx, pod); err != nil {
							return executor.ActionOutcome{}, err
						}
					case "readiness lost", "selection lost":
						if scenario == "readiness lost" {
							agent.Status.NodeStatuses[0].Ready = false
						} else {
							agent.Status.SelectedNodes = nil
						}
						if err := live.Status().Update(ctx, agent); err != nil {
							return executor.ActionOutcome{}, err
						}
					case "identity changed":
						agent.Generation++
						if err := live.Update(ctx, agent); err != nil {
							return executor.ActionOutcome{}, err
						}
					case "API failure":
						if err := live.Delete(ctx, agent); err != nil {
							return executor.ActionOutcome{}, err
						}
					case "canceled":
						cancel()
					}
				} else {
					released = true
					if !action.Group.NodeReleases[0].Cleared {
						return executor.ActionOutcome{}, errors.New("stale guard reached runner")
					}
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})
			result, err := (executor.Executor{Runner: runner, RefreshNodeRelease: r.refreshNodeReleaseEvidence}).Execute(ctx, executor.Input{
				ShutdownFlow: "flow", PlanConfigHash: "hash", Approved: true, Mode: "Enforce",
				Waves:  []executor.Wave{{Index: 0, Groups: []string{"drain"}}, {Index: 1, Groups: []string{"release"}}},
				Groups: []executor.Group{{Name: "drain", Action: "DrainNodes"}, {Name: "release", Action: executor.ActionAgentShutdown, NodeReleases: releases}},
			})
			want := scenario == "drained"
			if released != want || (err == nil) != want {
				t.Fatalf("released=%v result=%+v error=%v", released, result, err)
			}
			if releases[0].Cleared {
				t.Fatal("input evidence mutated")
			}
		})
	}
}
