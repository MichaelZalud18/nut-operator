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
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *NodePowerAgentReconciler) nodePowerAgentNodeStatuses(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, selectedNodes []string) ([]powerv1alpha1.NodePowerAgentNodeStatus, int32, int32, error) {
	podsByNode, err := r.nodePowerAgentPodsByNode(ctx, agent, namespace, selectedNodes)
	if err != nil {
		return nil, 0, 0, err
	}

	statuses := make([]powerv1alpha1.NodePowerAgentNodeStatus, 0, len(selectedNodes))
	var readyCount int32
	var unavailableCount int32
	for _, nodeName := range selectedNodes {
		pod, found := podsByNode[nodeName]
		if !found {
			unavailableCount++
			statuses = append(statuses, powerv1alpha1.NodePowerAgentNodeStatus{
				NodeName: nodeName,
				Ready:    false,
				Reason:   "AgentPodMissing",
				Message:  "no NodePowerAgent pod is currently observed on this selected node",
			})
			continue
		}
		status := nodePowerAgentNodeStatusFromPod(nodeName, pod)
		if status.Ready {
			readyCount++
		} else {
			unavailableCount++
		}
		statuses = append(statuses, status)
	}
	return statuses, readyCount, unavailableCount, nil
}

func (r *NodePowerAgentReconciler) nodePowerAgentPodsByNode(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, selectedNodes []string) (map[string]corev1.Pod, error) {
	selected := make(map[string]struct{}, len(selectedNodes))
	for _, nodeName := range selectedNodes {
		selected[nodeName] = struct{}{}
	}

	var pods corev1.PodList
	if err := r.List(
		ctx,
		&pods,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(labelsForNodePowerAgent(agent))},
	); err != nil {
		return nil, fmt.Errorf("list NodePowerAgent pods: %w", err)
	}

	byNode := make(map[string]corev1.Pod, len(selectedNodes))
	for _, pod := range pods.Items {
		nodeName := pod.Spec.NodeName
		if _, selected := selected[nodeName]; nodeName == "" || !selected {
			continue
		}
		existing, found := byNode[nodeName]
		if !found || preferredNodePowerAgentPod(pod, existing) {
			byNode[nodeName] = pod
		}
	}
	return byNode, nil
}

func nodePowerAgentNodeStatusFromPod(nodeName string, pod corev1.Pod) powerv1alpha1.NodePowerAgentNodeStatus {
	readyCondition := podReadyCondition(pod)
	lastHeartbeatTime := readyConditionTime(readyCondition, pod.Status.StartTime)
	status := powerv1alpha1.NodePowerAgentNodeStatus{
		NodeName:          nodeName,
		Ready:             false,
		PodName:           pod.Name,
		Phase:             pod.Status.Phase,
		LastHeartbeatTime: lastHeartbeatTime,
	}
	switch {
	case podIsDeleting(pod):
		status.Reason = "AgentPodDeleting"
		status.Message = "observed NodePowerAgent pod is deleting"
	case readyCondition != nil && readyCondition.Status == corev1.ConditionTrue:
		status.Ready = true
		status.Reason = "AgentPodReady"
		status.Message = "ready NodePowerAgent pod is observed on this node"
	case readyCondition != nil && readyCondition.Reason != "":
		status.Reason = readyCondition.Reason
		status.Message = readyCondition.Message
	default:
		status.Reason = "AgentPodNotReady"
		status.Message = "observed NodePowerAgent pod is not ready"
	}
	if status.Message == "" {
		status.Message = "observed NodePowerAgent pod is not ready"
	}
	return status
}

func preferredNodePowerAgentPod(candidate, existing corev1.Pod) bool {
	candidateReady := podIsReady(candidate)
	existingReady := podIsReady(existing)
	if candidateReady != existingReady {
		return candidateReady
	}
	return podObservedTime(candidate).After(podObservedTime(existing))
}

func podIsReady(pod corev1.Pod) bool {
	if podIsDeleting(pod) {
		return false
	}
	condition := podReadyCondition(pod)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

func podIsDeleting(pod corev1.Pod) bool {
	return pod.DeletionTimestamp != nil && !pod.DeletionTimestamp.IsZero()
}

func podReadyCondition(pod corev1.Pod) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

func readyConditionTime(condition *corev1.PodCondition, startTime *metav1.Time) *metav1.Time {
	if condition != nil && !condition.LastTransitionTime.IsZero() {
		heartbeat := condition.LastTransitionTime
		return &heartbeat
	}
	if startTime != nil && !startTime.IsZero() {
		heartbeat := *startTime
		return &heartbeat
	}
	return nil
}

func podObservedTime(pod corev1.Pod) time.Time {
	if condition := podReadyCondition(pod); condition != nil && !condition.LastTransitionTime.IsZero() {
		return condition.LastTransitionTime.Time
	}
	if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
		return pod.Status.StartTime.Time
	}
	return pod.CreationTimestamp.Time
}
