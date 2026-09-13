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
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type communicationOrderRunner func(context.Context, executor.Action) (executor.ActionOutcome, error)

func (f communicationOrderRunner) RunAction(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	return f(ctx, action)
}

func TestCommunicationOrderThroughInventoryAndExecutor(t *testing.T) {
	for _, mode := range []string{executor.ModeEnforce, executor.ModeDryRun} {
		for _, failGroup := range []string{"", "drain-consumer", "halt-consumer"} {
			if mode == executor.ModeDryRun && failGroup != "" {
				continue
			}
			t.Run(fmt.Sprintf("%s/fail=%s", mode, failGroup), func(t *testing.T) {
				flow, bundle := communicationOrderFixture(t)
				compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
				if compiled.ConfigHash == "" || compiled.Artifact == nil {
					t.Fatalf("compile rejected: %+v", compiled.Diagnostics)
				}
				if len(compiled.Waves) != 3 {
					t.Fatalf("communication order lost across resolver/planner: %+v", compiled.Waves)
				}
				if !slices.ContainsFunc(compiled.Artifact.Graph.Edges, func(e power.PlannerGraphEdgeStatus) bool {
					return e.From == "halt-consumer" && e.To == "halt-carrier" && e.Relation == planner.GraphEdgeRelationCommunicationPath && len(e.Sources) > 0
				}) {
					t.Fatal("published graph lost derived order/provenance")
				}

				// Readiness and clearance are assumed here; this test exercises ordering,
				// not the independently tested actuation gates or physical shutdown.
				groups := []executor.Group{{Name: "drain-consumer", Action: "DrainNodes"}}
				for _, node := range []string{"consumer", "carrier"} {
					groups = append(groups, executor.Group{Name: "halt-" + node, Action: executor.ActionAgentShutdown,
						NodeReleases: []executor.NodeRelease{{NodeName: node, NodePowerAgent: node + "-agent", AgentReady: true, TelemetryFresh: true, Cleared: true}},
					})
				}
				var mu sync.Mutex
				var called []string
				runner := communicationOrderRunner(func(_ context.Context, action executor.Action) (executor.ActionOutcome, error) {
					mu.Lock()
					defer mu.Unlock()
					called = append(called, action.Group.Name)
					if action.Group.Name == failGroup {
						return executor.ActionOutcome{}, errors.New("dependent fixture failure")
					}
					return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
				})
				writer := &fakeAuditStore{}
				e := executor.Executor{Writer: writer, Runner: runner, Clock: time.Now, NewID: func() string { return "communication-order" }}
				result, err := e.Execute(context.Background(), executor.Input{
					ShutdownFlow: flow.Name, PlanConfigHash: compiled.ConfigHash, Mode: mode, Approved: true,
					Waves: executorWavesFromFlow(compiled.Waves, compiled.Steps), Groups: groups,
				})
				want := []string{"drain-consumer", "halt-consumer", "halt-carrier"}
				if failGroup != "" {
					want = want[:slices.Index(want, failGroup)+1]
				}
				if mode == executor.ModeDryRun {
					if len(called) != 0 {
						t.Fatal("dry run dispatched an action")
					}
					for _, record := range writer.actionAttempts {
						called = append(called, record.GroupName)
					}
				}
				if !slices.Equal(called, want) {
					t.Fatalf("action order=%v, want %v", called, want)
				}
				if (err != nil) != (failGroup != "") {
					t.Fatalf("unexpected execution result: %+v, %v", result, err)
				}
			})
		}
	}
}

func communicationOrderFixture(t *testing.T) (*power.ShutdownFlow, resolver.StructuralBundle) {
	t.Helper()
	topology, diagnostics, err := inventory.Compile(inventory.Snapshot{
		Entities: []inventory.Entity{
			{ID: "ups", Kind: inventory.EntityKindUPSDevice, PowerDomains: []string{"rack"}},
			{ID: "consumer", Kind: inventory.EntityKindNode},
			{ID: "switch", Kind: inventory.EntityKindPowerInfrastructure},
			{ID: "carrier", Kind: inventory.EntityKindNode, CommunicationPathExempt: true},
		},
		Edges: []inventory.Edge{
			{From: "ups", To: "consumer", Relation: inventory.EdgeRelationFeeds, Input: "power"},
			{From: "ups", To: "switch", Relation: inventory.EdgeRelationFeeds, Input: "power"},
			{From: "ups", To: "carrier", Relation: inventory.EdgeRelationFeeds, Input: "power"},
			{From: "carrier", To: "switch", Relation: inventory.EdgeRelationCarries, SourceID: "upstream"},
			{From: "switch", To: "consumer", Relation: inventory.EdgeRelationCarries, SourceID: "access"},
		},
	})
	if err != nil {
		t.Fatalf("inventory: %v: %+v", err, diagnostics)
	}
	flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "communication-order"}, Spec: power.ShutdownFlowSpec{
		Triggers: []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery}},
		Groups: []power.ShutdownGroup{
			{Name: "halt-carrier", Action: power.ShutdownStepAgentShutdown, Target: power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: "carrier-agent"}}}},
			{Name: "drain-consumer", Action: power.ShutdownStepDrainNodes, Target: power.ShutdownStepTarget{NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "consumer"}}}},
			{Name: "halt-consumer", Action: power.ShutdownStepAgentShutdown, Target: power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: "consumer-agent"}}}},
		},
	}}
	return flow, resolver.StructuralBundle{Topology: topology,
		ClusterNodes:  []resolver.ClusterNode{{Name: "consumer", Labels: map[string]string{"role": "consumer"}}, {Name: "carrier"}},
		AgentCoverage: []resolver.AgentCoverage{{Name: "carrier-agent", Nodes: []string{"carrier"}}, {Name: "consumer-agent", Nodes: []string{"consumer"}}},
	}
}

func TestCommunicationOrderThroughLinearAdapter(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reverse=%v", reverse), func(t *testing.T) {
			flow, bundle := communicationOrderFixture(t)
			for _, index := range []int{1, 2, 0} {
				group := flow.Spec.Groups[index]
				flow.Spec.Steps = append(flow.Spec.Steps, power.ShutdownStep{ID: group.Name, Type: group.Action, Target: group.Target})
			}
			flow.Spec.Groups = nil
			if reverse {
				slices.Reverse(flow.Spec.Steps)
			}
			compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
			if reverse {
				if compiled.ConfigHash != "" || !slices.ContainsFunc(compiled.Diagnostics, func(d planner.Diagnostic) bool { return d.Reason == "CommunicationReleaseOrderInvalid" }) {
					t.Fatalf("unsafe linear order accepted: %+v", compiled.Diagnostics)
				}
				return
			}
			if compiled.Artifact == nil || !slices.ContainsFunc(compiled.Artifact.Graph.Edges, func(e power.PlannerGraphEdgeStatus) bool {
				return e.Relation == planner.GraphEdgeRelationCommunicationPath
			}) {
				t.Fatalf("linear adapter lost dependency: %+v", compiled.Diagnostics)
			}
			waves := executorWavesFromFlow(compiled.Waves, compiled.Steps)
			if len(waves) != 3 || !slices.Equal(waves[2].Groups, []string{"halt-carrier"}) {
				t.Fatalf("linear order changed: %+v", waves)
			}
		})
	}
}

func TestSharedServicePathFromInventoryToExecutor(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			flow, bundle := communicationOrderFixture(t)
			flow.Spec.Groups = append(flow.Spec.Groups[:1], power.ShutdownGroup{Name: "notify", Action: power.ShutdownStepNotify})
			flow.Spec.CommunicationPaths = []power.FlowCommunicationPath{{Service: "OperatorAPI", Entities: []string{"switch"}}, {Service: "NUT", Exempt: true}}
			compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
			if compiled.Artifact == nil || len(compiled.Waves) != 2 {
				t.Fatalf("compile: %+v", compiled.Diagnostics)
			}
			waves := executorWavesFromFlow(compiled.Waves, compiled.Steps)
			applyCommunicationBarriers(waves, compiled.Artifact)
			if !waves[1].CommunicationBarrier || !slices.Equal(waves[0].Groups, []string{"notify"}) {
				t.Fatalf("waves: %+v", waves)
			}
			if !slices.ContainsFunc(compiled.Artifact.Graph.Edges, func(edge power.PlannerGraphEdgeStatus) bool {
				return edge.From == "notify" && edge.To == "halt-carrier" && slices.ContainsFunc(edge.Sources, func(source power.PlannerGraphSourceRefStatus) bool {
					return source.Field == "spec.communicationPaths" && source.Name == "OperatorAPI"
				})
			}) {
				t.Fatal("missing shared service ordering provenance")
			}
			var calls []string
			e := executor.Executor{Runner: communicationOrderRunner(func(_ context.Context, action executor.Action) (executor.ActionOutcome, error) {
				calls = append(calls, action.Group.Name)
				if fail && action.Group.Name == "notify" {
					return executor.ActionOutcome{}, errors.New("notification failed")
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})}
			_, err := e.Execute(context.Background(), executor.Input{ShutdownFlow: flow.Name, PlanConfigHash: compiled.ConfigHash, Mode: executor.ModeEnforce, Approved: true, Waves: waves,
				Groups: []executor.Group{{Name: "notify", Action: "Notify"}, {Name: "halt-carrier", Action: executor.ActionAgentShutdown, NodeReleases: []executor.NodeRelease{{NodeName: "carrier", NodePowerAgent: "carrier-agent", AgentReady: true, TelemetryFresh: true, Cleared: true}}}},
			})
			want := []string{"notify", "halt-carrier"}
			if fail {
				want = want[:1]
			}
			if (err != nil) != fail || !slices.Equal(calls, want) {
				t.Fatalf("calls=%v error=%v", calls, err)
			}
		})
	}
}
