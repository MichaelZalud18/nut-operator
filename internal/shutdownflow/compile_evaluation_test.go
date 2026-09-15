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
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

func TestCompileForEvaluationPreservesScopedIdentityAndAuthoredPolicy(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{Spec: powerv1alpha1.ShutdownFlowSpec{
		Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
	}}
	bundle := resolver.StructuralBundle{}
	for _, name := range []string{"a", "b"} {
		labels := map[string]string{"node": name}
		flow.Spec.Groups = append(flow.Spec.Groups, powerv1alpha1.ShutdownGroup{Name: name, Action: powerv1alpha1.ShutdownStepCordonNodes,
			Target: powerv1alpha1.ShutdownStepTarget{NodeSelector: &metav1.LabelSelector{MatchLabels: labels}},
		})
		bundle.ClusterNodes = append(bundle.ClusterNodes, resolver.ClusterNode{Name: name, Labels: labels})
		bundle.Topology.Domains = append(bundle.Topology.Domains, inventory.PowerDomain{Name: name, UPSDevices: []string{"ups-" + name}, Nodes: []string{name}})
	}
	policy := powerv1alpha1.PowerShutdownTierPolicySpec{}
	preview := CompileWithHistoryLookup(flow, bundle, policy, nil, nil)
	if preview.ConfigHash == "" {
		t.Fatalf("preview rejected: %+v", preview.Diagnostics)
	}
	for _, tc := range []struct {
		name       string
		evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus
		want       []string
	}{
		{"preview", nil, []string{"a", "b"}},
		{"ineligible", &powerv1alpha1.ShutdownTriggerEvaluationStatus{SelectedUPSDevices: []string{"ups-a"}}, []string{"a", "b"}},
		{"scoped", &powerv1alpha1.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}}, []string{"a"}},
		{"both", &powerv1alpha1.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a", "ups-b"}}, []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := flow.DeepCopy()
			calls := 0
			var lookup string
			compiled := CompileForEvaluation(flow, bundle, policy, func(hash string) planner.HistoryInputs {
				calls++
				lookup = hash
				return planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"a": {time.Second}}}
			}, nil, tc.evaluation)
			if compiled.ConfigHash == "" || calls != 1 || lookup != compiled.ConfigHash {
				t.Fatalf("history identity changed: hash=%q lookup=%q calls=%d diagnostics=%+v", compiled.ConfigHash, lookup, calls, compiled.Diagnostics)
			}
			if tc.name == "scoped" && compiled.ConfigHash == preview.ConfigHash {
				t.Fatal("scoped plan reused preview identity")
			}
			var names []string
			for _, step := range compiled.Steps {
				names = append(names, step.ID)
			}
			slices.Sort(names)
			if !slices.Equal(names, tc.want) || !reflect.DeepEqual(before, flow) {
				t.Fatalf("compiled wrong scope or mutated authored flow: steps=%v want=%v", names, tc.want)
			}
		})
	}
	empty := CompileForEvaluation(flow, bundle, policy, func(string) planner.HistoryInputs {
		t.Fatal("empty eligible scope queried history")
		return planner.HistoryInputs{}
	}, nil, &powerv1alpha1.ShutdownTriggerEvaluationStatus{Eligible: true})
	if empty.ConfigHash != "" || len(empty.Diagnostics) == 0 || empty.Diagnostics[0].Reason != "ExecutionScopeEmpty" {
		t.Fatalf("empty eligible scope was not rejected: %+v", empty)
	}
	flow.Spec.Groups[1].After = []string{"b"}
	compiled := CompileForEvaluation(flow, bundle, policy, func(string) planner.HistoryInputs {
		t.Fatal("invalid authored plan queried history")
		return planner.HistoryInputs{}
	}, nil, &powerv1alpha1.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}})
	if compiled.ConfigHash != "" {
		t.Fatal("scope pruning hid an invalid authored dependency")
	}
}

func TestCompileHistoryUsesNewIdentityBeforeStatusUpdate(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "history-identity"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{{Name: "work", Action: powerv1alpha1.ShutdownStepScaleWorkload,
				Timeout: &metav1.Duration{Duration: time.Minute},
				Target:  powerv1alpha1.ShutdownStepTarget{WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
			}},
		},
	}
	bundle := resolver.StructuralBundle{}
	policy := powerv1alpha1.PowerShutdownTierPolicySpec{}
	initial := CompileWithHistoryLookup(flow, bundle, policy, nil, nil)
	if initial.ConfigHash == "" {
		t.Fatalf("initial plan rejected: %v", initial.Diagnostics)
	}
	flow.Status.ConfigHash = initial.ConfigHash
	flow.Spec.Groups[0].Target.WorkloadSelector.MatchLabels["app"] = "database"
	newPlan := CompileWithHistoryLookup(flow, bundle, policy, nil, nil)
	if newPlan.ConfigHash == initial.ConfigHash || newPlan.ConfigHash == "" {
		t.Fatal("target edit must produce a new valid identity")
	}
	for _, hasNewHistory := range []bool{false, true} {
		calls := 0
		compiled := CompileWithHistoryLookup(flow, bundle, policy, func(hash string) planner.HistoryInputs {
			calls++
			if hash != newPlan.ConfigHash {
				t.Errorf("history requested for %q, want new hash %q", hash, newPlan.ConfigHash)
			}
			if hash == initial.ConfigHash {
				return planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"work": {59 * time.Minute}}}
			}
			if hasNewHistory {
				return planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"work": {7 * time.Second}}}
			}
			return planner.HistoryInputs{}
		}, nil)
		if calls != 1 || compiled.ConfigHash != newPlan.ConfigHash {
			t.Fatalf("lookup count/hash changed: calls=%d hash=%s", calls, compiled.ConfigHash)
		}
		if len(compiled.GroupEstimates) != 1 {
			t.Fatalf("missing estimates: %#v", compiled)
		}
		want := time.Minute
		if hasNewHistory {
			want = 7 * time.Second
		}
		if compiled.GroupEstimates[0].Duration.Duration != want {
			t.Fatalf("duration = %v, want %v", compiled.GroupEstimates[0].Duration, want)
		}
		if flow.Status.ConfigHash != initial.ConfigHash {
			t.Fatal("compile mutated existing status")
		}
	}
	flow.Spec.Triggers = nil
	rejected := CompileWithHistoryLookup(flow, bundle, policy, func(string) planner.HistoryInputs {
		t.Fatal("rejected plan must not query history")
		return planner.HistoryInputs{}
	}, nil)
	if rejected.ConfigHash != "" {
		t.Fatal("invalid plan accepted")
	}
}
