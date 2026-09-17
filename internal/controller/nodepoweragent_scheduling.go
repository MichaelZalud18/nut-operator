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
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// shutdownFlowHoldingRollouts names a ShutdownFlow whose execution episode is currently live, if any.
//
// Deliberately not scoped to flows that could release this agent's nodes. What makes a flow
// live is a power event, and a power event is when pod churn is least welcome anywhere in the
// cluster — including on nodes this flow will not touch, whose agents are the ones that still have
// to be watching when it does. Narrowing the scope would buy a faster config rollout during an
// outage, which is not a thing worth buying.
//
// DryRun flows hold too, for the same reason: the rehearsal is running because the trigger is
// eligible, which means the power event is real even when the response is not.
func (r *NodePowerAgentReconciler) shutdownFlowHoldingRollouts(ctx context.Context) (string, error) {
	var flows powerv1alpha1.ShutdownFlowList
	if err := r.List(ctx, &flows); err != nil {
		return "", fmt.Errorf("list ShutdownFlows for rollout hold: %w", err)
	}
	held := ""
	for i := range flows.Items {
		if !shutdownFlowIsLive(&flows.Items[i]) {
			continue
		}
		// Lowest name wins so the reported holder is stable across passes rather than dependent on
		// list order; a message that changes every reconcile reads like two flows are fighting.
		if held == "" || flows.Items[i].Name < held {
			held = flows.Items[i].Name
		}
	}
	return held, nil
}

// shutdownFlowIsLive reports whether a flow is mid-episode.
//
// TriggerActive is the flow's own answer to whether the power event that started the episode is
// still happening, which is the same signal signalStillAuthorized uses to decide whether a halt is
// still authorized. Phase is checked as well because a flow can be Running before its first
// execution record exists.
func shutdownFlowIsLive(flow *powerv1alpha1.ShutdownFlow) bool {
	if flow.Status.Phase == powerv1alpha1.ShutdownFlowPhaseRunning {
		return true
	}
	return flow.Status.LastExecution != nil && flow.Status.LastExecution.TriggerActive
}

func nodePowerAgentTolerations(agent *powerv1alpha1.NodePowerAgent) []corev1.Toleration {
	tolerations := []corev1.Toleration{
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
	}
	for _, toleration := range agent.Spec.Placement.Tolerations {
		if !hasToleration(tolerations, toleration) {
			tolerations = append(tolerations, toleration)
		}
	}
	return tolerations
}

func hasToleration(tolerations []corev1.Toleration, candidate corev1.Toleration) bool {
	for _, toleration := range tolerations {
		if toleration.Key == candidate.Key &&
			toleration.Operator == candidate.Operator &&
			toleration.Value == candidate.Value &&
			toleration.Effect == candidate.Effect &&
			toleration.TolerationSeconds == candidate.TolerationSeconds {
			return true
		}
	}
	return false
}

func nodePowerAgentPriorityClassName(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.Placement.PriorityClassName != "" {
		return agent.Spec.Placement.PriorityClassName
	}
	return nodePowerAgentDefaultPriorityClassName
}

func nodePowerAgentPodNodeSelector(agent *powerv1alpha1.NodePowerAgent) (map[string]string, error) {
	selector := make(map[string]string, len(agent.Spec.Placement.NodeSelector))
	for key, value := range agent.Spec.Placement.NodeSelector {
		selector[key] = value
	}
	if agent.Spec.NodeSelector == nil {
		return selector, nil
	}
	for key, value := range agent.Spec.NodeSelector.MatchLabels {
		if existing, found := selector[key]; found && existing != value {
			return nil, fmt.Errorf("node selector label %q has conflicting values %q and %q", key, existing, value)
		}
		selector[key] = value
	}
	return selector, nil
}

func nodePowerAgentAffinity(agent *powerv1alpha1.NodePowerAgent) (*corev1.Affinity, error) {
	var affinity *corev1.Affinity
	if agent.Spec.Placement.Affinity != nil {
		affinity = agent.Spec.Placement.Affinity.DeepCopy()
	}
	if agent.Spec.NodeSelector == nil || len(agent.Spec.NodeSelector.MatchExpressions) == 0 {
		return affinity, nil
	}
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		return nil, fmt.Errorf("nodeSelector.matchExpressions cannot be combined with placement.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution")
	}
	if affinity == nil {
		affinity = &corev1.Affinity{}
	}
	if affinity.NodeAffinity == nil {
		affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: nodeSelectorRequirements(agent.Spec.NodeSelector.MatchExpressions),
			},
		},
	}
	return affinity, nil
}

func nodeSelectorRequirements(expressions []metav1.LabelSelectorRequirement) []corev1.NodeSelectorRequirement {
	requirements := make([]corev1.NodeSelectorRequirement, 0, len(expressions))
	for _, expression := range expressions {
		requirements = append(requirements, corev1.NodeSelectorRequirement{
			Key:      expression.Key,
			Operator: corev1.NodeSelectorOperator(expression.Operator),
			Values:   expression.Values,
		})
	}
	return requirements
}
