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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// FromAPI converts the ShutdownFlow node-target selector shape into the same
// labels.Selector implementation Kubernetes uses for native selector matching.
func FromAPI(selector *metav1.LabelSelector, requirements []corev1.NodeSelectorRequirement) (labels.Selector, error) {
	compiled := labels.Everything()
	if selector != nil {
		labelSelector, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return nil, err
		}
		compiled = labelSelector
	}
	for _, requirement := range requirements {
		labelRequirement, err := Requirement(requirement)
		if err != nil {
			return nil, err
		}
		compiled = compiled.Add(*labelRequirement)
	}
	return compiled, nil
}

func Requirement(requirement corev1.NodeSelectorRequirement) (*labels.Requirement, error) {
	operator, err := Operator(requirement.Operator)
	if err != nil {
		return nil, err
	}
	return labels.NewRequirement(requirement.Key, operator, requirement.Values)
}

func Operator(operator corev1.NodeSelectorOperator) (selection.Operator, error) {
	switch operator {
	case corev1.NodeSelectorOpIn:
		return selection.In, nil
	case corev1.NodeSelectorOpNotIn:
		return selection.NotIn, nil
	case corev1.NodeSelectorOpExists:
		return selection.Exists, nil
	case corev1.NodeSelectorOpDoesNotExist:
		return selection.DoesNotExist, nil
	case corev1.NodeSelectorOpGt:
		return selection.GreaterThan, nil
	case corev1.NodeSelectorOpLt:
		return selection.LessThan, nil
	default:
		return "", fmt.Errorf("unsupported node selector operator %q", operator)
	}
}
