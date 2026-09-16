package controller

import (
	"sync"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *ShutdownFlowReconciler) startExecutionProgress(flow *power.ShutdownFlow, input executor.Input, evaluation *power.ShutdownTriggerEvaluationStatus) func(executor.Progress) {
	if r.executionProgress == nil {
		return nil
	}
	flow.Status.LastExecution = &power.ShutdownExecutionStatus{
		ExecutionID: input.ExecutionID, DeduplicationKey: input.DeduplicationKey,
		TriggerActive: true, Phase: power.ShutdownExecutionPhaseRunning,
		Mode: effectiveShutdownFlowMode(flow.Spec.Mode), DryRun: input.DryRun, Rehearsal: input.Rehearsal,
		PlanConfigHash: input.PlanConfigHash, SelectedUPSDevices: append([]string(nil), input.SelectedUPSDevices...),
		StartedAt: &metav1.Time{Time: input.ObservedAt.UTC()}, WaveCount: int32(len(input.Waves)), Reason: evaluation.Reason,
	}
	flow.Status.Phase = power.ShutdownFlowPhaseRunning
	setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, true, "ExecutionRunning", "shutdown flow execution is running")
	r.executionProgress()
	var mu sync.Mutex
	return func(progress executor.Progress) {
		mu.Lock()
		defer mu.Unlock()
		flow.Status.LastExecution.GroupCount += int32(progress.Groups)
		flow.Status.LastExecution.ActionAttemptCount += int32(progress.ActionAttempts)
		flow.Status.LastExecution.NodeReleaseCount += int32(progress.NodeReleases)
		if progress.Adaptive != nil {
			flow.Status.LastExecution.Adaptive = adaptiveStatusFromResult(*progress.Adaptive)
		}
		r.executionProgress()
	}
}

// Workload selectors and hooks have effects beyond a fixed node set. They take
// an exclusive claim rather than guessing which pods/external systems may move.
// Agent shutdown can remove shared control-plane/communication infrastructure,
// so it too is exclusive across flows; ordering belongs in one combined plan.
func executionResourceClaims(flow *power.ShutdownFlow, input executor.Input, bundle resolver.StructuralBundle) []string {
	if input.DryRun {
		return nil
	}
	mutating := false
	for _, group := range input.Groups {
		switch group.Action {
		case executor.ActionWait, string(power.ShutdownStepNotify):
		case string(power.ShutdownStepCordonNodes), string(power.ShutdownStepDrainNodes):
			mutating = true
		default:
			return []string{"*"}
		}
	}
	if !mutating {
		return nil
	}
	var keys []string
	for _, membership := range shutdownflow.PlannerGroupNodes(flowForCompiledExecution(flow), bundle) {
		if membership.Unresolved {
			return []string{"*"}
		}
		for _, node := range membership.Acts {
			keys = append(keys, "node:"+node)
		}
		for _, node := range membership.Releases {
			keys = append(keys, "node:"+node)
		}
	}
	return keys
}
