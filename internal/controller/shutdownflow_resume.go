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
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
)

type shutdownExecutionResumeEvidence struct {
	state *audit.ExecutorResumeState
	input executorpkg.ResumeInput
}

func (r *ShutdownFlowReconciler) shutdownExecutionResumeEvidence(ctx context.Context, reader audit.ResumeReader, executionID, flowName, configHash string) (shutdownExecutionResumeEvidence, error) {
	if reader == nil || executionID == "" {
		return shutdownExecutionResumeEvidence{}, nil
	}
	state, err := reader.ExecutorResumeState(ctx, executionID)
	if err != nil {
		return shutdownExecutionResumeEvidence{}, err
	}
	if state == nil {
		return shutdownExecutionResumeEvidence{}, nil
	}
	if state.ShutdownFlow != flowName || state.PlanConfigHash != configHash {
		return shutdownExecutionResumeEvidence{}, fmt.Errorf("executor resume state %q belongs to flow %q plan %q, not flow %q plan %q",
			executionID, state.ShutdownFlow, state.PlanConfigHash, flowName, configHash)
	}

	progress, err := reader.ExecutionGroupProgress(ctx, executionID)
	evidence := shutdownExecutionResumeEvidence{
		state: state,
		input: resumeInputFromAudit(state, progress),
	}
	return evidence, err
}

func resumeInputFromAudit(state *audit.ExecutorResumeState, progress []audit.ExecutionGroupProgress) executorpkg.ResumeInput {
	if state == nil {
		return executorpkg.ResumeInput{}
	}
	input := executorpkg.ResumeInput{
		Phase: state.Phase,
	}
	if state.CurrentWaveIndex != nil {
		index := *state.CurrentWaveIndex
		input.CurrentWaveIndex = &index
	}
	for _, group := range progress {
		input.CompletedGroups = append(input.CompletedGroups, executorpkg.CompletedGroup{
			WaveIndex: group.WaveIndex,
			GroupName: group.GroupName,
			Action:    group.Action,
			Phase:     group.Phase,
		})
	}
	return input
}

func resumeExecutionCompleted(evidence shutdownExecutionResumeEvidence) bool {
	return evidence.state != nil && evidence.state.Phase == executorpkg.PhaseCompleted
}

func markExecutionAlreadyRecorded(
	flow *powerv1alpha1.ShutdownFlow,
	executionID string,
	dedupeKey string,
	configHash string,
	evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus,
	rehearsalRun bool,
	resumeState *audit.ExecutorResumeState,
) {
	reason := "AlreadyExecuted"
	message := "eligible trigger episode already has execution evidence"
	if rehearsalRun {
		reason = "RehearsalAlreadyExecuted"
		message = "rehearsal request already has execution evidence"
	}
	status := flow.Status.LastExecution
	if status == nil || status.DeduplicationKey != dedupeKey {
		status = &powerv1alpha1.ShutdownExecutionStatus{
			ExecutionID:        executionID,
			DeduplicationKey:   dedupeKey,
			Phase:              powerv1alpha1.ShutdownExecutionPhaseCompleted,
			Mode:               effectiveShutdownFlowMode(flow.Spec.Mode),
			DryRun:             effectiveShutdownFlowMode(flow.Spec.Mode) != powerv1alpha1.ShutdownFlowModeEnforce,
			Rehearsal:          rehearsalRun,
			PlanConfigHash:     configHash,
			SelectedUPSDevices: selectedUPSDevicesFromEvaluation(evaluation),
			Adaptive:           adaptiveStatusFromResumeState(resumeState),
		}
		if resumeState != nil && !resumeState.ObservedAt.IsZero() {
			completedAt := metav1.NewTime(resumeState.ObservedAt)
			status.CompletedAt = &completedAt
		}
		flow.Status.LastExecution = status
	}
	status.TriggerActive = true
	status.Reason = reason
	status.Message = message
	applyLastExecutionPhase(flow)
	setExecutionReadyCondition(
		&flow.Status.Conditions,
		flow.Generation,
		true,
		reason,
		message,
	)
}

func selectedUPSDevicesFromEvaluation(evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) []string {
	if evaluation == nil {
		return nil
	}
	return append([]string(nil), evaluation.SelectedUPSDevices...)
}

func adaptiveStatusFromResumeState(state *audit.ExecutorResumeState) *powerv1alpha1.ShutdownExecutionAdaptiveStatus {
	if state == nil || len(state.State) == 0 {
		return nil
	}
	status := &powerv1alpha1.ShutdownExecutionAdaptiveStatus{}
	var populated bool
	if tier, ok := resumeStateInt32(state.State, "tier"); ok {
		status.Tier = tier
		populated = true
	}
	if deepest, ok := resumeStateInt32(state.State, "deepestTier"); ok {
		status.DeepestTier = deepest
		populated = true
	}
	if started, ok := resumeStateBool(state.State, "pointerStarted"); ok {
		status.PointerStarted = started
		populated = true
	}
	if mode, ok := resumeStateString(state.State, "timingMode"); ok {
		status.TimingMode = mode
		populated = true
	}
	if onBattery, ok := resumeStateBool(state.State, "onBattery"); ok {
		status.OnBattery = onBattery
		populated = true
	}
	if lowBattery, ok := resumeStateBool(state.State, "lowBattery"); ok {
		status.LowBattery = lowBattery
		populated = true
	}
	if trusted, ok := resumeStateBool(state.State, "runtimeTrusted"); ok {
		status.RuntimeTrusted = trusted
		populated = true
	}
	if runtime, ok := resumeStateInt64(state.State, "runtimeSeconds"); ok {
		status.RuntimeSeconds = &runtime
		populated = true
	}
	if !populated {
		return nil
	}
	return status
}

func resumeStateString(state map[string]any, key string) (string, bool) {
	value, ok := state[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok && text != ""
}

func resumeStateBool(state map[string]any, key string) (bool, bool) {
	value, ok := state[key]
	if !ok {
		return false, false
	}
	typed, ok := value.(bool)
	return typed, ok
}

func resumeStateInt32(state map[string]any, key string) (int32, bool) {
	value, ok := resumeStateInt64(state, key)
	if !ok || value < -1<<31 || value > 1<<31-1 {
		return 0, false
	}
	return int32(value), true
}

func resumeStateInt64(state map[string]any, key string) (int64, bool) {
	value, ok := state[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
