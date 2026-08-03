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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
)

func (r *ShutdownFlowReconciler) recordShutdownFlowExecution(ctx context.Context, store audit.Store, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, inputHash, configHash string, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) error {
	if store == nil || flow == nil || evaluation == nil {
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
		Writer: store,
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
	if status.Phase == powerv1alpha1.ShutdownExecutionPhaseCompleted {
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseCompleted
	} else if status.Phase == powerv1alpha1.ShutdownExecutionPhaseAborted || status.Phase == powerv1alpha1.ShutdownExecutionPhaseFailed {
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
			releases, err := r.nodeReleasesForTarget(ctx, group.Target)
			if err != nil {
				return nil, err
			}
			groups = append(groups, executorpkg.Group{
				Name:            group.Name,
				Action:          string(group.Action),
				SelectedTargets: executorTargetsFromTarget(group.Target),
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
		releases, err := r.nodeReleasesForTarget(ctx, step.Target)
		if err != nil {
			return nil, err
		}
		groups = append(groups, executorpkg.Group{
			Name:            step.ID,
			Action:          string(step.Type),
			SelectedTargets: executorTargetsFromTarget(step.Target),
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
		for _, nodeName := range agent.Status.SelectedNodes {
			releases = append(releases, executorpkg.NodeRelease{
				NodeName:       nodeName,
				NodePowerAgent: agent.Name,
				SignalPath:     nodePowerAgentSignalPath(&agent),
			})
		}
	}
	return releases, nil
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
			Kind:      ref.Kind,
			Namespace: ref.Namespace,
			Name:      ref.Name,
		})
	}
	for _, ref := range target.AgentRefs {
		targets = append(targets, executorpkg.Target{Kind: "NodePowerAgent", Name: ref.Name})
	}
	return targets
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
