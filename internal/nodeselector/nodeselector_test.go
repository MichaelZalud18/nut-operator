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

package nodeselector

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func TestFromAPISupportsNumericNodeRequirements(t *testing.T) {
	selector, err := FromAPI(&metav1.LabelSelector{
		MatchLabels: map[string]string{"rack": "a"},
	}, []corev1.NodeSelectorRequirement{{
		Key:      powerv1alpha1.DefaultShutdownTierLabelKey,
		Operator: corev1.NodeSelectorOpGt,
		Values:   []string{"2"},
	}})
	if err != nil {
		t.Fatalf("FromAPI returned error: %v", err)
	}

	matching := labels.Set{"rack": "a", powerv1alpha1.DefaultShutdownTierLabelKey: "3"}
	if !selector.Matches(matching) {
		t.Fatalf("expected selector to match %#v", matching)
	}

	lowTier := labels.Set{"rack": "a", powerv1alpha1.DefaultShutdownTierLabelKey: "1"}
	if selector.Matches(lowTier) {
		t.Fatalf("expected selector to reject %#v", lowTier)
	}

	otherRack := labels.Set{"rack": "b", powerv1alpha1.DefaultShutdownTierLabelKey: "4"}
	if selector.Matches(otherRack) {
		t.Fatalf("expected selector to reject %#v", otherRack)
	}
}

func TestRequirementUsesKubernetesValidation(t *testing.T) {
	_, err := Requirement(corev1.NodeSelectorRequirement{
		Key:      powerv1alpha1.DefaultShutdownTierLabelKey,
		Operator: corev1.NodeSelectorOpLt,
		Values:   []string{"database"},
	})
	if err == nil {
		t.Fatal("expected Kubernetes selector validation to reject a non-integer Lt value")
	}
}
