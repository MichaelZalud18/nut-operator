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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
)

func (r *ShutdownFlowReconciler) recordShutdownFlowExecution(ctx context.Context, writer audit.Writer, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, inputHash, configHash string, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) error {
	if writer == nil || flow == nil || evaluation == nil {
		return nil
	}
	if !evaluation.Eligible {
		deactivateLastExecution(&flow.Status.LastExecution)
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			false,
			"TriggerNotEligible",
			"shutdown flow execution has not started because no trigger is eligible",
		)
		return nil
	}

	dedupeKey := shutdownExecutionDeduplicationKey(flow, evaluation, configHash)
	if executionAlreadyRecorded(flow.Status.LastExecution, dedupeKey) {
		flow.Status.LastExecution.TriggerActive = true
		flow.Status.LastExecution.Reason = "AlreadyExecuted"
		flow.Status.LastExecution.Message = "eligible trigger episode already has execution evidence"
		applyLastExecutionPhase(flow)
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			true,
			"AlreadyExecuted",
			"eligible trigger episode already has execution evidence",
		)
		return nil
	}

	input, err := r.shutdownExecutionInput(ctx, flow, observedAt, inputHash, configHash, evaluation, dedupeKey)
	if err != nil {
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			false,
			"ExecutionInputInvalid",
			err.Error(),
		)
		return err
	}
	result, err := executorpkg.Executor{
		Writer: writer,
		Runner: r.ExecutorRunner,
		Clock:  r.now,
	}.Execute(ctx, input)
	completedAt := metav1.NewTime(r.now())
	status := &powerv1alpha1.ShutdownExecutionStatus{
		ExecutionID:        result.ExecutionID,
		DeduplicationKey:   dedupeKey,
		TriggerActive:      true,
		Phase:              shutdownExecutionPhase(result.Phase, err),
		Mode:               effectiveShutdownFlowMode(flow.Spec.Mode),
		DryRun:             result.DryRun,
		PlanConfigHash:     configHash,
		SelectedUPSDevices: append([]string(nil), evaluation.SelectedUPSDevices...),
		StartedAt:          &metav1.Time{Time: observedAt.UTC()},
		CompletedAt:        &completedAt,
		WaveCount:          int32(result.Waves),
		GroupCount:         int32(result.Groups),
		ActionAttemptCount: int32(result.ActionAttempts),
		NodeReleaseCount:   int32(result.NodeReleases),
		Reason:             evaluation.Reason,
	}
	if err != nil {
		status.Message = err.Error()
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			false,
			"ExecutionFailed",
			err.Error(),
		)
	} else {
		status.Message = "shutdown flow execution evidence recorded"
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			true,
			"ExecutionRecorded",
			"shutdown flow execution evidence recorded",
		)
	}
	flow.Status.LastExecution = status
	applyLastExecutionPhase(flow)
	return err
}

func applyLastExecutionPhase(flow *powerv1alpha1.ShutdownFlow) {
	if flow == nil || flow.Status.LastExecution == nil {
		return
	}
	status := flow.Status.LastExecution
	switch status.Phase {
	case powerv1alpha1.ShutdownExecutionPhaseCompleted:
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseCompleted
	case powerv1alpha1.ShutdownExecutionPhaseAborted, powerv1alpha1.ShutdownExecutionPhaseFailed:
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseAborted
	}
}

func (r *ShutdownFlowReconciler) shutdownExecutionInput(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, inputHash, configHash string, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, executionID string) (executorpkg.Input, error) {
	waves := executorWavesFromFlow(flow.Status.CompiledWaves, flow.Status.CompiledSteps)
	groups, err := r.executorGroupsFromFlow(ctx, flow)
	if err != nil {
		return executorpkg.Input{}, err
	}
	return executorpkg.Input{
		ExecutionID:        executionID,
		ObservedAt:         observedAt,
		ShutdownFlow:       flow.Name,
		TriggerDecisionID:  eligibleTriggerDecisionID(evaluation),
		Mode:               string(effectiveShutdownFlowMode(flow.Spec.Mode)),
		Reason:             evaluation.Reason,
		PlanConfigHash:     configHash,
		InputHash:          inputHash,
		Approved:           effectiveShutdownFlowMode(flow.Spec.Mode) == powerv1alpha1.ShutdownFlowModeEnforce,
		DryRun:             effectiveShutdownFlowMode(flow.Spec.Mode) != powerv1alpha1.ShutdownFlowModeEnforce,
		SelectedUPSDevices: append([]string(nil), evaluation.SelectedUPSDevices...),
		Waves:              waves,
		Groups:             groups,
	}, nil
}

func executorWavesFromFlow(compiledWaves []powerv1alpha1.CompiledShutdownWave, compiledSteps []powerv1alpha1.CompiledShutdownStep) []executorpkg.Wave {
	if len(compiledWaves) > 0 {
		waves := make([]executorpkg.Wave, 0, len(compiledWaves))
		for _, wave := range compiledWaves {
			waves = append(waves, executorpkg.Wave{
				Index:  wave.Index,
				Groups: append([]string(nil), wave.Groups...),
			})
		}
		return waves
	}
	waves := make([]executorpkg.Wave, 0, len(compiledSteps))
	for _, step := range compiledSteps {
		waves = append(waves, executorpkg.Wave{
			Index:  step.Index,
			Groups: []string{step.ID},
		})
	}
	return waves
}

func (r *ShutdownFlowReconciler) executorGroupsFromFlow(ctx context.Context, flow *powerv1alpha1.ShutdownFlow) ([]executorpkg.Group, error) {
	if len(flow.Spec.Groups) > 0 {
		groups := make([]executorpkg.Group, 0, len(flow.Spec.Groups))
		for _, group := range flow.Spec.Groups {
			targets, err := r.executorTargetsForAction(ctx, group.Action, group.Target)
			if err != nil {
				return nil, err
			}
			releases, err := r.nodeReleasesForTarget(ctx, group.Target)
			if err != nil {
				return nil, err
			}
			groups = append(groups, executorpkg.Group{
				Name:            group.Name,
				Action:          string(group.Action),
				Params:          copyActionParams(group.Params),
				SelectedTargets: targets,
				NodeReleases:    releases,
				Details: map[string]any{
					"description": group.Description,
				},
			})
		}
		return groups, nil
	}
	groups := make([]executorpkg.Group, 0, len(flow.Spec.Steps))
	for _, step := range flow.Spec.Steps {
		targets, err := r.executorTargetsForAction(ctx, step.Type, step.Target)
		if err != nil {
			return nil, err
		}
		releases, err := r.nodeReleasesForTarget(ctx, step.Target)
		if err != nil {
			return nil, err
		}
		groups = append(groups, executorpkg.Group{
			Name:            step.ID,
			Action:          string(step.Type),
			Params:          copyActionParams(step.Params),
			SelectedTargets: targets,
			NodeReleases:    releases,
			Details: map[string]any{
				"description": step.Description,
			},
		})
	}
	return groups, nil
}

func (r *ShutdownFlowReconciler) nodeReleasesForTarget(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.NodeRelease, error) {
	releases := make([]executorpkg.NodeRelease, 0)
	for _, ref := range target.AgentRefs {
		var agent powerv1alpha1.NodePowerAgent
		if err := r.Get(ctx, client.ObjectKey{Name: ref.Name}, &agent); err != nil {
			return nil, fmt.Errorf("get NodePowerAgent %q for shutdown execution: %w", ref.Name, err)
		}
		cluster, err := r.getNodePowerAgentManagementCluster(ctx, &agent)
		if err != nil {
			return nil, err
		}
		agentNamespace := nodePowerAgentNamespace(&agent, cluster)
		nodeStatuses := nodePowerAgentStatusByNode(agent.Status.NodeStatuses)
		for _, nodeName := range agent.Status.SelectedNodes {
			nodeStatus, found := nodeStatuses[nodeName]
			readinessReason := "AgentReadinessUnknown"
			readinessMessage := "NodePowerAgent has not published readiness for this selected node"
			if found {
				readinessReason = nodeStatus.Reason
				readinessMessage = nodeStatus.Message
			}
			releases = append(releases, executorpkg.NodeRelease{
				NodeName:              nodeName,
				NodePowerAgent:        agent.Name,
				SignalPath:            nodePowerAgentSignalPath(&agent),
				SignalSecretNamespace: agentNamespace,
				SignalSecretName:      nodePowerAgentSignalSecretName(&agent),
				SignalSecretKey:       nodePowerAgentSignalKey(nodeName),
				AgentReady:            found && nodeStatus.Ready,
				ReadinessReason:       readinessReason,
				ReadinessMessage:      readinessMessage,
				PodName:               nodeStatus.PodName,
				LastHeartbeatTime:     metav1TimeToTimePtr(nodeStatus.LastHeartbeatTime),
			})
		}
	}
	return releases, nil
}

func (r *ShutdownFlowReconciler) getNodePowerAgentManagementCluster(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (*powerv1alpha1.PowerManagementCluster, error) {
	if agent.Spec.ManagementClusterRef == nil || agent.Spec.ManagementClusterRef.Name == "" {
		return nil, nil
	}
	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, client.ObjectKey{Name: agent.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
		return nil, fmt.Errorf("get PowerManagementCluster %q for NodePowerAgent %q: %w", agent.Spec.ManagementClusterRef.Name, agent.Name, err)
	}
	return &cluster, nil
}

func nodePowerAgentStatusByNode(statuses []powerv1alpha1.NodePowerAgentNodeStatus) map[string]powerv1alpha1.NodePowerAgentNodeStatus {
	indexed := make(map[string]powerv1alpha1.NodePowerAgentNodeStatus, len(statuses))
	for _, status := range statuses {
		if status.NodeName == "" {
			continue
		}
		indexed[status.NodeName] = status
	}
	return indexed
}

func metav1TimeToTimePtr(value *metav1.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	out := value.Time
	return &out
}

func (r *ShutdownFlowReconciler) executorTargetsForAction(ctx context.Context, action powerv1alpha1.ShutdownStepType, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.Target, error) {
	switch action {
	case powerv1alpha1.ShutdownStepScaleWorkload:
		return r.scaleWorkloadTargets(ctx, target)
	case powerv1alpha1.ShutdownStepCordonNodes, powerv1alpha1.ShutdownStepDrainNodes:
		return r.nodeTargets(ctx, target)
	case powerv1alpha1.ShutdownStepRunWorkflow:
		return r.workflowTargets(ctx, target)
	default:
		return executorTargetsFromTarget(target), nil
	}
}

func executorTargetsFromTarget(target powerv1alpha1.ShutdownStepTarget) []executorpkg.Target {
	targets := make([]executorpkg.Target, 0, len(target.WorkloadRefs)+len(target.Namespaces)+len(target.AgentRefs)+3)
	if target.NodeSelector != nil {
		targets = append(targets, executorpkg.Target{Kind: "NodeSelector", Name: "nodeSelector"})
	}
	for _, namespace := range target.Namespaces {
		targets = append(targets, executorpkg.Target{Kind: "Namespace", Name: namespace})
	}
	if target.NamespaceSelector != nil {
		targets = append(targets, executorpkg.Target{Kind: "NamespaceSelector", Name: "namespaceSelector"})
	}
	if target.WorkloadSelector != nil {
		targets = append(targets, executorpkg.Target{Kind: "WorkloadSelector", Name: "workloadSelector"})
	}
	for _, ref := range target.WorkloadRefs {
		targets = append(targets, executorpkg.Target{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Namespace:  ref.Namespace,
			Name:       ref.Name,
		})
	}
	for _, ref := range target.AgentRefs {
		targets = append(targets, executorpkg.Target{Kind: "NodePowerAgent", Name: ref.Name})
	}
	return targets
}

func (r *ShutdownFlowReconciler) scaleWorkloadTargets(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.Target, error) {
	targets := make([]executorpkg.Target, 0, len(target.WorkloadRefs))
	for _, ref := range target.WorkloadRefs {
		targets = append(targets, executorpkg.Target{
			APIVersion: ref.APIVersion,
			Kind:       ref.Kind,
			Namespace:  ref.Namespace,
			Name:       ref.Name,
		})
	}

	hasNamespaceConstraint := len(target.Namespaces) > 0 || target.NamespaceSelector != nil
	if target.WorkloadSelector == nil && !hasNamespaceConstraint {
		return dedupeExecutorTargets(targets), nil
	}

	selector, err := labelSelector(target.WorkloadSelector)
	if err != nil {
		return nil, fmt.Errorf("parse workload selector for shutdown execution: %w", err)
	}
	namespaces, err := r.selectedTargetNamespaces(ctx, target)
	if err != nil {
		return nil, err
	}
	if len(namespaces) == 0 {
		workloadTargets, err := r.listScalableWorkloads(ctx, "", selector)
		if err != nil {
			return nil, err
		}
		targets = append(targets, workloadTargets...)
		return dedupeExecutorTargets(targets), nil
	}
	for _, namespace := range namespaces {
		workloadTargets, err := r.listScalableWorkloads(ctx, namespace, selector)
		if err != nil {
			return nil, err
		}
		targets = append(targets, workloadTargets...)
	}
	return dedupeExecutorTargets(targets), nil
}

func (r *ShutdownFlowReconciler) listScalableWorkloads(ctx context.Context, namespace string, selector labels.Selector) ([]executorpkg.Target, error) {
	options := []client.ListOption{client.MatchingLabelsSelector{Selector: selector}}
	if namespace != "" {
		options = append(options, client.InNamespace(namespace))
	}
	targets := make([]executorpkg.Target, 0)

	var deployments appsv1.DeploymentList
	if err := r.List(ctx, &deployments, options...); err != nil {
		return nil, fmt.Errorf("list Deployments for shutdown execution: %w", err)
	}
	for _, item := range deployments.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: item.Namespace, Name: item.Name})
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.List(ctx, &statefulSets, options...); err != nil {
		return nil, fmt.Errorf("list StatefulSets for shutdown execution: %w", err)
	}
	for _, item := range statefulSets.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: item.Namespace, Name: item.Name})
	}

	var replicaSets appsv1.ReplicaSetList
	if err := r.List(ctx, &replicaSets, options...); err != nil {
		return nil, fmt.Errorf("list ReplicaSets for shutdown execution: %w", err)
	}
	for _, item := range replicaSets.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: item.Namespace, Name: item.Name})
	}

	return targets, nil
}

func (r *ShutdownFlowReconciler) nodeTargets(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.Target, error) {
	if target.NodeSelector == nil {
		return nil, nil
	}
	selector, err := labelSelector(target.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("parse node selector for shutdown execution: %w", err)
	}
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("list Nodes for shutdown execution: %w", err)
	}
	targets := make([]executorpkg.Target, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "v1", Kind: "Node", Name: node.Name})
	}
	return dedupeExecutorTargets(targets), nil
}

func (r *ShutdownFlowReconciler) workflowTargets(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.Target, error) {
	namespaces, err := r.selectedTargetNamespaces(ctx, target)
	if err != nil {
		return nil, err
	}
	targets := make([]executorpkg.Target, 0, len(namespaces))
	for _, namespace := range namespaces {
		targets = append(targets, executorpkg.Target{APIVersion: "v1", Kind: "Namespace", Name: namespace})
	}
	return dedupeExecutorTargets(targets), nil
}

func (r *ShutdownFlowReconciler) selectedTargetNamespaces(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]string, error) {
	namespaces := append([]string(nil), target.Namespaces...)
	if target.NamespaceSelector != nil {
		selector, err := labelSelector(target.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("parse namespace selector for shutdown execution: %w", err)
		}
		var namespaceList corev1.NamespaceList
		if err := r.List(ctx, &namespaceList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return nil, fmt.Errorf("list Namespaces for shutdown execution: %w", err)
		}
		for _, namespace := range namespaceList.Items {
			namespaces = append(namespaces, namespace.Name)
		}
	}
	sort.Strings(namespaces)
	deduped := namespaces[:0]
	var previous string
	for _, namespace := range namespaces {
		if namespace == "" || namespace == previous {
			continue
		}
		deduped = append(deduped, namespace)
		previous = namespace
	}
	return deduped, nil
}

func labelSelector(selector *metav1.LabelSelector) (labels.Selector, error) {
	if selector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(selector)
}

func dedupeExecutorTargets(targets []executorpkg.Target) []executorpkg.Target {
	sort.Slice(targets, func(i, j int) bool {
		return executorTargetKey(targets[i]) < executorTargetKey(targets[j])
	})
	deduped := targets[:0]
	var previous string
	for _, target := range targets {
		key := executorTargetKey(target)
		if key == "" || key == previous {
			continue
		}
		deduped = append(deduped, target)
		previous = key
	}
	return deduped
}

func executorTargetKey(target executorpkg.Target) string {
	if target.Kind == "" || target.Name == "" {
		return ""
	}
	return target.APIVersion + "/" + target.Kind + "/" + target.Namespace + "/" + target.Name
}

func copyActionParams(params map[string]string) map[string]string {
	if params == nil {
		return nil
	}
	copied := make(map[string]string, len(params))
	for key, value := range params {
		copied[key] = value
	}
	return copied
}

func eligibleTriggerDecisionID(evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) string {
	if evaluation == nil {
		return ""
	}
	for _, decision := range evaluation.Decisions {
		if decision.Eligible {
			return decision.TriggerID
		}
	}
	return ""
}

func executionAlreadyRecorded(status *powerv1alpha1.ShutdownExecutionStatus, dedupeKey string) bool {
	if status == nil || dedupeKey == "" {
		return false
	}
	return status.TriggerActive && status.DeduplicationKey == dedupeKey
}

func deactivateLastExecution(status **powerv1alpha1.ShutdownExecutionStatus) {
	if status == nil || *status == nil {
		return
	}
	(*status).TriggerActive = false
	if (*status).Reason == "AlreadyExecuted" {
		(*status).Reason = "TriggerNotEligible"
	}
}

func shutdownExecutionDeduplicationKey(flow *powerv1alpha1.ShutdownFlow, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, configHash string) string {
	eligibleTriggers := make([]string, 0)
	if evaluation != nil {
		for _, decision := range evaluation.Decisions {
			if decision.Eligible {
				eligibleTriggers = append(eligibleTriggers, decision.TriggerID)
			}
		}
	}
	sort.Strings(eligibleTriggers)
	selectedUPSDevices := append([]string(nil), evaluation.SelectedUPSDevices...)
	sort.Strings(selectedUPSDevices)
	keyPayload := struct {
		Flow               string   `json:"flow"`
		Generation         int64    `json:"generation"`
		Mode               string   `json:"mode"`
		PlanConfigHash     string   `json:"planConfigHash"`
		EligibleTriggers   []string `json:"eligibleTriggers"`
		SelectedUPSDevices []string `json:"selectedUPSDevices"`
	}{
		Flow:               flow.Name,
		Generation:         flow.Generation,
		Mode:               string(effectiveShutdownFlowMode(flow.Spec.Mode)),
		PlanConfigHash:     configHash,
		EligibleTriggers:   eligibleTriggers,
		SelectedUPSDevices: selectedUPSDevices,
	}
	encoded, err := json.Marshal(keyPayload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func shutdownExecutionPhase(phase string, err error) powerv1alpha1.ShutdownExecutionPhase {
	if err != nil {
		if phase == executorpkg.PhaseAborted {
			return powerv1alpha1.ShutdownExecutionPhaseAborted
		}
		return powerv1alpha1.ShutdownExecutionPhaseFailed
	}
	switch phase {
	case executorpkg.PhaseCompleted:
		return powerv1alpha1.ShutdownExecutionPhaseCompleted
	case executorpkg.PhaseAborted:
		return powerv1alpha1.ShutdownExecutionPhaseAborted
	case executorpkg.PhaseRunning:
		return powerv1alpha1.ShutdownExecutionPhaseRunning
	default:
		return powerv1alpha1.ShutdownExecutionPhaseFailed
	}
}
