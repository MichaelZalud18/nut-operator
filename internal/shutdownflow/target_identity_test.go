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

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func identityTarget() power.ShutdownStepTarget {
	selector := func() *metav1.LabelSelector {
		return &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": "web"},
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "zone", Operator: metav1.LabelSelectorOpIn, Values: []string{"b", "a"}},
				{Key: "ready", Operator: metav1.LabelSelectorOpExists},
			},
		}
	}
	return power.ShutdownStepTarget{
		NodeSelector: selector(), NamespaceSelector: selector(), WorkloadSelector: selector(),
		NodeSelectorRequirements: []corev1.NodeSelectorRequirement{{Key: "rack", Operator: corev1.NodeSelectorOpIn, Values: []string{"b", "a"}}, {Key: "size", Operator: corev1.NodeSelectorOpGt, Values: []string{"2"}}},
		Namespaces:               []string{"b", "a"},
		WorkloadRefs:             []power.WorkloadReference{{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "a", Name: "web"}, {APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "b", Name: "db"}},
		AgentRefs:                []power.ObjectNameReference{{Name: "b"}, {Name: "a"}},
	}
}

func targetPlanHash(t *testing.T, target power.ShutdownStepTarget, linear bool) string {
	t.Helper()
	flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "identity"}}
	flow.Spec.Triggers = []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery}}
	if linear {
		flow.Spec.Steps = []power.ShutdownStep{{ID: "work", Type: power.ShutdownStepScaleWorkload, Target: target}}
	} else {
		flow.Spec.Groups = []power.ShutdownGroup{{Name: "work", Action: power.ShutdownStepScaleWorkload, Target: target}}
	}
	_, _, _, hash := Compile(flow)
	if hash == "" {
		t.Fatal("expected valid compiled plan")
	}
	return hash
}

func TestPlanHashBindsTargetIdentity(t *testing.T) {
	mutations := map[string]func(*power.ShutdownStepTarget){
		"node label":       func(x *power.ShutdownStepTarget) { x.NodeSelector.MatchLabels["app"] = "db" },
		"namespace label":  func(x *power.ShutdownStepTarget) { x.NamespaceSelector.MatchLabels["app"] = "db" },
		"workload label":   func(x *power.ShutdownStepTarget) { x.WorkloadSelector.MatchLabels["app"] = "db" },
		"expression value": func(x *power.ShutdownStepTarget) { x.WorkloadSelector.MatchExpressions[0].Values[0] = "c" },
		"expression operator": func(x *power.ShutdownStepTarget) {
			x.WorkloadSelector.MatchExpressions[0].Operator = metav1.LabelSelectorOpNotIn
		},
		"node requirement": func(x *power.ShutdownStepTarget) { x.NodeSelectorRequirements[1].Values[0] = "3" },
		"namespace":        func(x *power.ShutdownStepTarget) { x.Namespaces[0] = "c" },
		"ref name":         func(x *power.ShutdownStepTarget) { x.WorkloadRefs[0].Name = "other" },
		"ref namespace":    func(x *power.ShutdownStepTarget) { x.WorkloadRefs[0].Namespace = "other" },
		"ref kind":         func(x *power.ShutdownStepTarget) { x.WorkloadRefs[0].Kind = "StatefulSet" },
		"ref version":      func(x *power.ShutdownStepTarget) { x.WorkloadRefs[0].APIVersion = "apps/v2" },
		"agent":            func(x *power.ShutdownStepTarget) { x.AgentRefs[0].Name = "c" },
	}
	for _, linear := range []bool{false, true} {
		base := identityTarget()
		before := targetPlanHash(t, base, linear)
		for name, mutate := range mutations {
			t.Run(name+map[bool]string{false: "/group", true: "/step"}[linear], func(t *testing.T) {
				changed := base.DeepCopy()
				mutate(changed)
				if targetPlanHash(t, *changed, linear) == before {
					t.Fatal("target change retained old plan identity")
				}
			})
		}
	}
}

func TestTargetIdentityCanonicalAndNonMutating(t *testing.T) {
	target := identityTarget()
	original := target.DeepCopy()
	reordered := target.DeepCopy()
	for _, s := range []*metav1.LabelSelector{reordered.NodeSelector, reordered.NamespaceSelector, reordered.WorkloadSelector} {
		slices.Reverse(s.MatchExpressions[0].Values)
		slices.Reverse(s.MatchExpressions)
	}
	slices.Reverse(reordered.NodeSelectorRequirements[0].Values)
	slices.Reverse(reordered.NodeSelectorRequirements)
	slices.Reverse(reordered.Namespaces)
	slices.Reverse(reordered.WorkloadRefs)
	slices.Reverse(reordered.AgentRefs)
	for _, linear := range []bool{false, true} {
		if targetPlanHash(t, target, linear) != targetPlanHash(t, *reordered, linear) {
			t.Fatal("set ordering changed plan identity")
		}
	}
	if !reflect.DeepEqual(target, *original) {
		t.Fatal("compilation mutated caller target")
	}
}
