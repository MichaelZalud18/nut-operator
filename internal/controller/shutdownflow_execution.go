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
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/nodeselector"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// triggerNotEligibleReason and triggerNotEligibleMessage are the single statement published
// whenever no trigger is eligible, so the ExecutionReady condition and status.lastExecution cannot
// drift into saying different things about the same state.
const (
	triggerNotEligibleReason  = "TriggerNotEligible"
	triggerNotEligibleMessage = "shutdown flow execution has not started because no trigger is eligible"
)

func (r *ShutdownFlowReconciler) executeShutdownFlow(ctx context.Context, writer audit.Writer, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, inputHash, configHash string, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, bundle resolver.StructuralBundle) error {
	if writer == nil || flow == nil || evaluation == nil {
		return nil
	}
	rehearsal := shutdownFlowRehearsalRequest(flow)
	rehearsalRun := false
	executionEvaluation := evaluation
	if !evaluation.Eligible {
		if rehearsal.Requested {
			if effectiveShutdownFlowMode(flow.Spec.Mode) != powerv1alpha1.ShutdownFlowModeEnforce {
				setExecutionReadyCondition(
					&flow.Status.Conditions,
					flow.Generation,
					false,
					"RehearsalRequiresEnforce",
					"rehearsal execution requires spec.mode Enforce because dry-run does not produce honest action durations",
				)
				return nil
			}
			selected := rehearsalSelectedUPSDevices(flow, bundle)
			if len(selected) == 0 {
				setExecutionReadyCondition(
					&flow.Status.Conditions,
					flow.Generation,
					false,
					"RehearsalSelectionEmpty",
					"rehearsal execution could not select any UPS devices from the flow trigger scopes",
				)
				return nil
			}
			rehearsalRun = true
			executionEvaluation = rehearsalExecutionEvaluation(evaluation, selected, configHash)
		} else {
			deactivateLastExecution(&flow.Status.LastExecution)
			setExecutionReadyCondition(
				&flow.Status.Conditions,
				flow.Generation,
				false,
				triggerNotEligibleReason,
				triggerNotEligibleMessage,
			)
			return nil
		}
	}

	dedupeKey := shutdownExecutionDeduplicationKey(flow, executionEvaluation, configHash)
	if rehearsalRun {
		dedupeKey = shutdownRehearsalDeduplicationKey(flow, rehearsal, configHash, executionEvaluation.SelectedUPSDevices)
	}
	if executionAlreadyRecorded(flow.Status.LastExecution, dedupeKey) {
		markExecutionAlreadyRecorded(flow, rehearsalRun)
		return nil
	}

	input, err := r.shutdownExecutionInput(ctx, flow, observedAt, inputHash, configHash, executionEvaluation, dedupeKey, bundle, rehearsalRun)
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
	if rehearsalRun {
		input.RehearsalRequest = rehearsal.Token
		input.RehearsalReason = rehearsal.Reason
	}
	if r.runs != nil {
		if err := r.runs.claim(flow.Name, executionResourceClaims(flow, input, bundle)); err != nil {
			setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, false, "ExecutionConflict", err.Error())
			return err
		}
	}
	progress := r.startExecutionProgress(flow, input, executionEvaluation)
	executeStart := time.Now()
	result, err := executorpkg.Executor{
		Progress: progress,
		Writer:   writer,
		Runner:   r.ExecutorRunner,
		Clock:    r.now,
		// Re-read at each wave boundary rather than closing over the trigger-time
		// snapshot: the whole reason to evaluate at boundaries is that power may have
		// moved since the last one.
		Observer: r.powerObserverForFlow(
			flow, bundle, executionEvaluation.SelectedUPSDevices,
			input.Adaptive.Observation,
		),
		ApprovalChecker:    r.approvalChecker(flow),
		RefreshNodeRelease: r.refreshNodeReleaseEvidence,
		ResolveTargets:     r.waveTargetResolver(flow, bundle),
	}.Execute(ctx, input)
	executionMode := "Enforce"
	if input.DryRun {
		executionMode = "DryRun"
	}
	metrics.ShutdownFlowExecutionDurationSeconds.WithLabelValues(flow.Name, executionMode).Observe(time.Since(executeStart).Seconds())
	recordTierOverrunMetrics(flow.Name, result.TierOverruns)
	completedAt := metav1.NewTime(r.now())
	status := &powerv1alpha1.ShutdownExecutionStatus{
		ExecutionID:        result.ExecutionID,
		DeduplicationKey:   dedupeKey,
		TriggerActive:      true,
		Phase:              shutdownExecutionPhase(result.Phase, err),
		Mode:               effectiveShutdownFlowMode(flow.Spec.Mode),
		DryRun:             result.DryRun,
		Rehearsal:          rehearsalRun,
		PlanConfigHash:     configHash,
		SelectedUPSDevices: append([]string(nil), executionEvaluation.SelectedUPSDevices...),
		StartedAt:          &metav1.Time{Time: observedAt.UTC()},
		CompletedAt:        &completedAt,
		WaveCount:          int32(result.Waves),
		GroupCount:         int32(result.Groups),
		ActionAttemptCount: int32(result.ActionAttempts),
		NodeReleaseCount:   int32(result.NodeReleases),
		Reason:             executionEvaluation.Reason,
		Adaptive:           adaptiveStatusFromResult(result.Adaptive),
		TierOverruns:       tierOverrunStatusFromResult(result.TierOverruns),
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
	} else if result.Degraded {
		status.Message = result.DegradedMessage
		setDegradedCondition(
			&flow.Status.Conditions,
			flow.Generation,
			true,
			result.DegradedReason,
			result.DegradedMessage,
		)
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			true,
			"ExecutionRecordedWithDegradation",
			"shutdown flow execution evidence recorded with advisory hook degradation",
		)
	} else {
		status.Message = "shutdown flow execution evidence recorded"
		reason := "ExecutionRecorded"
		if rehearsalRun {
			status.Message = "shutdown flow rehearsal evidence recorded"
			reason = "RehearsalRecorded"
		}
		setExecutionReadyCondition(
			&flow.Status.Conditions,
			flow.Generation,
			true,
			reason,
			status.Message,
		)
	}
	// Report missing audit evidence as Degraded independently of the execution phase,
	// which describes the shutdown outcome rather than the availability of its record.
	if result.RecordError != nil {
		log := logf.FromContext(ctx)
		log.Error(result.RecordError, "Could not record shutdown flow execution evidence",
			"shutdownflow", flow.Name, "executionID", result.ExecutionID)
		message := "shutdown flow execution ran but its audit evidence was not recorded: " + result.RecordError.Error()
		setDegradedCondition(
			&flow.Status.Conditions,
			flow.Generation,
			true,
			"ExecutionAuditRecordFailed",
			message,
		)
		if status.Message == "" {
			status.Message = message
		}
	}

	flow.Status.LastExecution = status
	applyLastExecutionPhase(flow)
	if err != nil {
		return err
	}
	// Returned so the caller's audit-failure path still fires, but after the status above has
	// already said what happened -- the record error must be visible on the resource, not only in
	// a log line the reader has to know to go looking for.
	return result.RecordError
}

func (r *ShutdownFlowReconciler) shutdownExecutionInput(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, inputHash, configHash string, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, dedupeKey string, bundle resolver.StructuralBundle, rehearsal bool) (executorpkg.Input, error) {
	waves := executorWavesFromFlow(flow.Status.CompiledWaves, flow.Status.CompiledSteps)
	applyCommunicationBarriers(waves, flow.Status.PublishedArtifact)
	groups, err := r.executorGroups(ctx, flowForCompiledExecution(flow), false)
	if err != nil {
		return executorpkg.Input{}, err
	}
	controlPlane := resolver.AttachResolvedInputHash(planner.StructuralInputs{}, bundle).ControlPlaneNodes
	for i := range groups {
		for j := range groups[i].NodeReleases {
			for _, node := range controlPlane {
				groups[i].NodeReleases[j].ControlPlaneNodes = append(groups[i].NodeReleases[j].ControlPlaneNodes, node.Name)
				if node.QuorumMember {
					groups[i].NodeReleases[j].QuorumMembers = append(groups[i].NodeReleases[j].QuorumMembers, node.Name)
				}
			}
		}
	}
	observation := adaptive.PowerObservation{
		RuntimeTrusted: runtimeIsTrustedForFlow(bundle.CapabilityMatches, evaluation.SelectedUPSDevices),
	}
	if !rehearsal {
		// Seeded from the trigger-time state so a flow with no Observer still runs against
		// a real reading rather than an empty one, which would look like mains present.
		observation.OnBattery = true
	} else {
		observation = r.rehearsalPowerObservation(ctx, evaluation.SelectedUPSDevices, observation.RuntimeTrusted)
	}
	return executorpkg.Input{
		ExecutionID:        shutdownExecutionIdentity(dedupeKey),
		DeduplicationKey:   dedupeKey,
		ObservedAt:         observedAt,
		ShutdownFlow:       flow.Name,
		TriggerDecisionID:  eligibleTriggerDecisionID(evaluation),
		Mode:               string(effectiveShutdownFlowMode(flow.Spec.Mode)),
		Reason:             evaluation.Reason,
		PlanConfigHash:     configHash,
		InputHash:          inputHash,
		Approved:           effectiveShutdownFlowMode(flow.Spec.Mode) == powerv1alpha1.ShutdownFlowModeEnforce,
		DryRun:             effectiveShutdownFlowMode(flow.Spec.Mode) != powerv1alpha1.ShutdownFlowModeEnforce,
		Rehearsal:          rehearsal,
		SelectedUPSDevices: append([]string(nil), evaluation.SelectedUPSDevices...),
		TierOverrunPolicy:  string(effectiveShutdownTierOverrunPolicy(flow.Spec.TierOverrunPolicy)),
		Waves:              waves,
		Groups:             groups,
		Adaptive:           adaptiveInputForFlow(flow, observation),
	}, nil
}

// Resolve only compiled actions. Pruned hooks, agents, and selectors must not
// perform reads or prevent an unrelated domain's execution from starting.
func flowForCompiledExecution(flow *powerv1alpha1.ShutdownFlow) *powerv1alpha1.ShutdownFlow {
	selected := map[string]bool{}
	for _, step := range flow.Status.CompiledSteps {
		selected[step.ID] = true
	}
	out := flow.DeepCopy()
	out.Spec.Groups = nil
	out.Spec.Steps = nil
	if len(flow.Spec.Groups) > 0 {
		for _, group := range flow.Spec.Groups {
			if selected[group.Name] {
				out.Spec.Groups = append(out.Spec.Groups, group)
			}
		}
	} else {
		for _, step := range flow.Spec.Steps {
			if selected[step.ID] {
				out.Spec.Steps = append(out.Spec.Steps, step)
			}
		}
	}
	return out
}

type shutdownRehearsalRequest struct {
	Requested bool
	Token     string
	Reason    string
}

func shutdownFlowRehearsalRequest(flow *powerv1alpha1.ShutdownFlow) shutdownRehearsalRequest {
	if flow == nil {
		return shutdownRehearsalRequest{}
	}
	token := strings.TrimSpace(flow.Annotations[powerv1alpha1.ShutdownFlowRehearsalRequestAnnotation])
	if token == "" {
		return shutdownRehearsalRequest{}
	}
	return shutdownRehearsalRequest{
		Requested: true,
		Token:     token,
		Reason:    strings.TrimSpace(flow.Annotations[powerv1alpha1.ShutdownFlowRehearsalReasonAnnotation]),
	}
}

func rehearsalExecutionEvaluation(evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, selected []string, configHash string) *powerv1alpha1.ShutdownTriggerEvaluationStatus {
	out := &powerv1alpha1.ShutdownTriggerEvaluationStatus{}
	if evaluation != nil {
		*out = *evaluation
		out.Decisions = append([]powerv1alpha1.ShutdownTriggerDecisionStatus(nil), evaluation.Decisions...)
		out.Diagnostics = append([]powerv1alpha1.ShutdownTriggerDiagnosticStatus(nil), evaluation.Diagnostics...)
	}
	out.Eligible = true
	out.Reason = "RehearsalRequested"
	out.SelectedUPSDevices = append([]string(nil), selected...)
	out.PlanConfigHash = configHash
	return out
}

func rehearsalSelectedUPSDevices(flow *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) []string {
	if flow == nil {
		return nil
	}
	allDevices := allBundleUPSDevices(bundle)
	domains := bundleUPSDevicesByDomain(bundle)
	selected := map[string]struct{}{}
	for _, trigger := range flow.Spec.Triggers {
		switch {
		case len(trigger.UPSDeviceRefs) > 0:
			for _, ref := range trigger.UPSDeviceRefs {
				if ref.Name != "" {
					selected[ref.Name] = struct{}{}
				}
			}
		case len(trigger.PowerDomains) > 0:
			for _, domain := range trigger.PowerDomains {
				for _, device := range domains[domain] {
					selected[device] = struct{}{}
				}
			}
		default:
			for _, device := range allDevices {
				selected[device] = struct{}{}
			}
		}
	}
	return sortedStringSet(selected)
}

func allBundleUPSDevices(bundle resolver.StructuralBundle) []string {
	selected := map[string]struct{}{}
	for _, match := range bundle.CapabilityMatches {
		if match.DeviceID != "" {
			selected[match.DeviceID] = struct{}{}
		}
	}
	for _, domain := range bundle.Topology.Domains {
		for _, device := range domain.UPSDevices {
			if device != "" {
				selected[device] = struct{}{}
			}
		}
	}
	return sortedStringSet(selected)
}

func bundleUPSDevicesByDomain(bundle resolver.StructuralBundle) map[string][]string {
	out := make(map[string][]string, len(bundle.Topology.Domains))
	for _, domain := range bundle.Topology.Domains {
		if domain.Name == "" {
			continue
		}
		devices := append([]string(nil), domain.UPSDevices...)
		sort.Strings(devices)
		out[domain.Name] = devices
	}
	return out
}

func sortedStringSet(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (r *ShutdownFlowReconciler) rehearsalPowerObservation(ctx context.Context, selected []string, trusted bool) adaptive.PowerObservation {
	devices := make([]powerv1alpha1.UPSDevice, 0, len(selected))
	for _, name := range selected {
		var device powerv1alpha1.UPSDevice
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &device); err != nil {
			return adaptive.PowerObservation{RuntimeTrusted: trusted}
		}
		devices = append(devices, device)
	}
	if len(devices) == 0 {
		return adaptive.PowerObservation{RuntimeTrusted: trusted}
	}
	return powerObservationFromDevices(devices, trusted)
}

func includeRehearsalHistory(flow *powerv1alpha1.ShutdownFlow) bool {
	if flow == nil || flow.Spec.Rehearsal.IncludeInEstimates == nil {
		return true
	}
	return *flow.Spec.Rehearsal.IncludeInEstimates
}

func shutdownRehearsalDeduplicationKey(flow *powerv1alpha1.ShutdownFlow, request shutdownRehearsalRequest, configHash string, selectedUPSDevices []string) string {
	selected := append([]string(nil), selectedUPSDevices...)
	sort.Strings(selected)
	keyPayload := struct {
		Flow               string   `json:"flow"`
		Generation         int64    `json:"generation"`
		Mode               string   `json:"mode"`
		PlanConfigHash     string   `json:"planConfigHash"`
		RehearsalRequest   string   `json:"rehearsalRequest"`
		SelectedUPSDevices []string `json:"selectedUPSDevices"`
	}{
		Flow:               flow.Name,
		Generation:         flow.Generation,
		Mode:               string(effectiveShutdownFlowMode(flow.Spec.Mode)),
		PlanConfigHash:     configHash,
		RehearsalRequest:   request.Token,
		SelectedUPSDevices: selected,
	}
	encoded, err := json.Marshal(keyPayload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
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

func applyCommunicationBarriers(waves []executorpkg.Wave, artifact *powerv1alpha1.PublishedPlannerArtifactStatus) {
	if artifact == nil {
		return
	}
	carriers := map[string]bool{}
	for _, edge := range artifact.Graph.Edges {
		if edge.Relation == planner.GraphEdgeRelationCommunicationPath {
			carriers[edge.To] = true
		}
	}
	for i := range waves {
		for _, group := range waves[i].Groups {
			if carriers[group] {
				waves[i].CommunicationBarrier = true
			}
		}
	}
}

func executorWavesFromFlow(compiledWaves []powerv1alpha1.CompiledShutdownWave, compiledSteps []powerv1alpha1.CompiledShutdownStep) []executorpkg.Wave {
	if len(compiledWaves) > 0 {
		waves := make([]executorpkg.Wave, 0, len(compiledWaves))
		for _, wave := range compiledWaves {
			waves = append(waves, executorpkg.Wave{
				Index: wave.Index,
				// Carries the tier the planner assigned so the pointer follows the compiled
				// plan rather than counting waves, which would drift the moment a tier spans
				// more than one wave.
				ShutdownTier: wave.ShutdownTier,
				// The plan's own statement of what this wave needs. Summed across the waves
				// still to run, it is what the runtime gets measured against.
				Duration: durationOrZero(wave.Duration),
				Groups:   append([]string(nil), wave.Groups...),
			})
		}
		return waves
	}
	waves := make([]executorpkg.Wave, 0, len(compiledSteps))
	var previousDuration time.Duration
	for _, step := range compiledSteps {
		cumulative := durationOrZero(step.CumulativeDuration)
		waves = append(waves, executorpkg.Wave{
			Index:    step.Index,
			Groups:   []string{step.ID},
			Duration: cumulative - previousDuration,
		})
		previousDuration = cumulative
	}
	return waves
}

func (r *ShutdownFlowReconciler) executorGroupsFromFlow(ctx context.Context, flow *powerv1alpha1.ShutdownFlow) ([]executorpkg.Group, error) {
	return r.executorGroups(ctx, flow, true)
}

func (r *ShutdownFlowReconciler) executorGroups(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, resolveTargets bool) ([]executorpkg.Group, error) {
	blocked := blockedNodeNames(flow.Status.BlockedNodeReleases)
	cluster, err := r.getManagementCluster(ctx, flow)
	if err != nil {
		return nil, err
	}
	dryRun := effectiveShutdownFlowMode(flow.Spec.Mode) != powerv1alpha1.ShutdownFlowModeEnforce
	if len(flow.Spec.Groups) > 0 {
		groups := make([]executorpkg.Group, 0, len(flow.Spec.Groups))
		for _, group := range flow.Spec.Groups {
			targets, err := r.initialExecutionTargets(ctx, group.Action, group.Target, resolveTargets)
			if err != nil {
				return nil, err
			}
			timeout := durationOrZero(group.Timeout)
			if timeout == 0 && group.Action == powerv1alpha1.ShutdownStepRunHook {
				hookTimeout, err := r.shutdownHookTimeout(ctx, group.HookRef, cluster, dryRun)
				if err != nil {
					return nil, err
				}
				timeout = hookTimeout
			}
			releases, err := r.nodeReleasesForTarget(ctx, group.Target)
			if err != nil {
				return nil, err
			}
			releases = withholdBlockedNodeReleases(releases, blocked)
			groups = append(groups, executorpkg.Group{
				Name:            group.Name,
				Action:          string(group.Action),
				Params:          copyActionParams(group.Params),
				HookRef:         executorHookReference(group.HookRef),
				SelectedTargets: targets,
				NodeReleases:    releases,
				Timeout:         timeout,
				// Groups carry no typed duration field, so a Wait group declares it as a
				// parameter. Steps have their own typed field.
				WaitDuration: waitDurationFromParams(group.Params),
				Details: map[string]any{
					"description": group.Description,
				},
			})
		}
		return groups, nil
	}
	groups := make([]executorpkg.Group, 0, len(flow.Spec.Steps))
	for _, step := range flow.Spec.Steps {
		targets, err := r.initialExecutionTargets(ctx, step.Type, step.Target, resolveTargets)
		if err != nil {
			return nil, err
		}
		timeout := durationOrZero(step.Timeout)
		if timeout == 0 && step.Type == powerv1alpha1.ShutdownStepRunHook {
			hookTimeout, err := r.shutdownHookTimeout(ctx, step.HookRef, cluster, dryRun)
			if err != nil {
				return nil, err
			}
			timeout = hookTimeout
		}
		releases, err := r.nodeReleasesForTarget(ctx, step.Target)
		if err != nil {
			return nil, err
		}
		releases = withholdBlockedNodeReleases(releases, blocked)
		groups = append(groups, executorpkg.Group{
			Name:            step.ID,
			Action:          string(step.Type),
			Params:          copyActionParams(step.Params),
			HookRef:         executorHookReference(step.HookRef),
			SelectedTargets: targets,
			NodeReleases:    releases,
			Timeout:         timeout,
			WaitDuration:    durationOrZero(step.Duration),
			Details: map[string]any{
				"description": step.Description,
			},
		})
	}
	return groups, nil
}

// durationOrZero unwraps an optional duration. Zero means no limit, which is not
// the same as a limit of zero: an undeclared timeout leaves the action unbounded
// rather than failing it instantly.
func durationOrZero(duration *metav1.Duration) time.Duration {
	if duration == nil {
		return 0
	}
	return duration.Duration
}

func copyInt32Ptr(value *int32) *int32 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

// waitDurationFromParams reads a group's Wait duration.
//
// Groups carry only string parameters, so the value is parsed here and a
// malformed one is treated as undeclared. Rejecting the flow over it would take a
// cluster's shutdown offline for a typo in an advisory pause; leaving the wait at
// zero skips the pause and runs everything else, which is the safer direction.
func waitDurationFromParams(params map[string]string) time.Duration {
	return planner.WaitDurationFromParams(params)
}

// blockedNodeNames indexes the nodes the compiled plan declined to power off.
func blockedNodeNames(blocked []powerv1alpha1.BlockedNodeReleaseStatus) map[string]struct{} {
	if len(blocked) == 0 {
		return nil
	}
	names := make(map[string]struct{}, len(blocked))
	for _, entry := range blocked {
		names[entry.NodeName] = struct{}{}
	}
	return names
}

// withholdBlockedNodeReleases drops nodes the plan blocked under OD-18.
//
// The filter runs here rather than inside the executor because a blocked node is
// a planning decision, not an execution failure: the executor should never be
// handed a release it is expected to decline. The flow still runs — it simply
// powers off fewer nodes than an unblocked plan would, which is the intended
// failure direction when a workload is scheduled to outlive its host.
func withholdBlockedNodeReleases(releases []executorpkg.NodeRelease, blocked map[string]struct{}) []executorpkg.NodeRelease {
	if len(blocked) == 0 || len(releases) == 0 {
		return releases
	}
	kept := make([]executorpkg.NodeRelease, 0, len(releases))
	for _, release := range releases {
		if _, withheld := blocked[release.NodeName]; withheld {
			continue
		}
		kept = append(kept, release)
	}
	return kept
}

func effectiveShutdownTierOverrunPolicy(policy powerv1alpha1.ShutdownTierOverrunPolicy) powerv1alpha1.ShutdownTierOverrunPolicy {
	if policy == "" {
		return powerv1alpha1.ShutdownTierOverrunWait
	}
	return policy
}

func tierOverrunStatusFromResult(overruns []executorpkg.TierOverrun) []powerv1alpha1.ShutdownTierOverrunStatus {
	if len(overruns) == 0 {
		return nil
	}
	statuses := make([]powerv1alpha1.ShutdownTierOverrunStatus, 0, len(overruns))
	for _, overrun := range overruns {
		statuses = append(statuses, powerv1alpha1.ShutdownTierOverrunStatus{
			WaveIndex:        overrun.WaveIndex,
			ShutdownTier:     copyInt32Ptr(overrun.ShutdownTier),
			Policy:           powerv1alpha1.ShutdownTierOverrunPolicy(overrun.Policy),
			Action:           overrun.Action,
			DeclaredSeconds:  durationStatusSeconds(overrun.DeclaredDuration),
			EffectiveSeconds: durationStatusSeconds(overrun.EffectiveDuration),
			ActualSeconds:    durationStatusSeconds(overrun.ActualDuration),
			OverrunSeconds:   durationStatusSeconds(overrun.OverrunDuration),
		})
	}
	return statuses
}

func durationStatusSeconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	seconds := duration / time.Second
	if duration%time.Second != 0 {
		seconds++
	}
	return int64(seconds)
}

func recordTierOverrunMetrics(flowName string, overruns []executorpkg.TierOverrun) {
	for _, overrun := range overruns {
		tier := tierOverrunTierLabel(overrun.ShutdownTier)
		metrics.ShutdownFlowTierOverrunsTotal.WithLabelValues(flowName, tier, overrun.Policy, overrun.Action).Inc()
		metrics.ShutdownFlowTierOverrunSeconds.WithLabelValues(flowName, tier, overrun.Policy, overrun.Action).Observe(overrun.OverrunDuration.Seconds())
	}
}

func tierOverrunTierLabel(tier *int32) string {
	if tier == nil {
		return "untiered"
	}
	return strconv.FormatInt(int64(*tier), 10)
}

func (r *ShutdownFlowReconciler) nodeReleasesForTarget(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.NodeRelease, error) {
	releases := make([]executorpkg.NodeRelease, 0)
	if len(target.AgentRefs) == 0 {
		return releases, nil
	}
	// Resolved once for the whole target: the namespaces that legitimately still hold
	// pods when a node powers off are a property of the install, not of the node.
	protectedNamespaces, err := r.clearanceExemptNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, ref := range target.AgentRefs {
		var agent powerv1alpha1.NodePowerAgent
		if err := r.reader().Get(ctx, client.ObjectKey{Name: ref.Name}, &agent); err != nil {
			return nil, fmt.Errorf("get NodePowerAgent %q for shutdown execution: %w", ref.Name, err)
		}
		cluster, err := r.getNodePowerAgentManagementCluster(ctx, &agent)
		if err != nil {
			return nil, err
		}
		agentNamespace := nodePowerAgentNamespace(&agent, cluster)
		nodeStatuses := nodePowerAgentStatusByNode(agent.Status.NodeStatuses)
		telemetryFresh, telemetryStaleReason, err := r.nodePowerAgentTelemetryFreshness(ctx, &agent)
		if err != nil {
			return nil, err
		}
		for _, nodeName := range agent.Status.SelectedNodes {
			nodeStatus, found := nodeStatuses[nodeName]
			readinessReason := "AgentReadinessUnknown"
			readinessMessage := "NodePowerAgent has not published readiness for this selected node"
			if found {
				readinessReason = nodeStatus.Reason
				readinessMessage = nodeStatus.Message
			}
			cleared, clearanceReason, blockingWorkloads, err := r.nodeClearance(ctx, nodeName, protectedNamespaces)
			if err != nil {
				return nil, err
			}
			releases = append(releases, executorpkg.NodeRelease{
				AgentUID:              string(agent.UID),
				AgentGeneration:       agent.Generation,
				ActuatorPolicy:        string(nodePowerAgentActuatorPolicy(&agent)),
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
				TelemetryFresh:        telemetryFresh,
				TelemetryStaleReason:  telemetryStaleReason,
				Cleared:               cleared,
				ClearanceReason:       clearanceReason,
				BlockingWorkloads:     blockingWorkloads,
			})
		}
	}
	return releases, nil
}

// nodeClearance re-derives whether a node is actually empty enough to power off
// (EX-9, PL-43).
//
// Compile-time clearance edges order the plan; this is the proof. They are not
// interchangeable, because OD-11 resolves concrete workload instances at execution:
// a pod that rescheduled onto this node after the plan compiled is invisible to
// the graph and very visible to whoever loses it.
//
// The question asked is "what would still be running when the power goes", not
// "did the drain command succeed". Three classes are excluded, each because it is
// expected to be there right up until the node goes down:
//
//   - Pods in protected namespaces — the node agent that performs the shutdown, and
//     the manager running the flow. Waiting for those to leave would deadlock.
//   - DaemonSet pods, which eviction deliberately does not remove.
//   - Static and mirror pods, which no controller can reschedule anyway.
//
// Anything else still running is reported, by name, because "the node is not
// clear" is not actionable and "etcd-backup is still on it" is.
func (r *ShutdownFlowReconciler) nodeClearance(ctx context.Context, nodeName string, protected map[string]struct{}) (bool, string, []string, error) {
	var pods corev1.PodList
	// Read straight from the API server rather than the informer cache. This is the
	// last check before power is cut, and a cache that is a few seconds behind is
	// exactly long enough to miss a pod that just landed. It also avoids caching every
	// pod in the cluster for a query this operator makes rarely.
	if err := r.reader().List(ctx, &pods, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		// Failing closed. An unreadable pod list is not evidence the node is empty, and
		// this is the last check before power is cut.
		return false, "NodeClearanceUnknown", nil, fmt.Errorf("list pods on node %q for clearance: %w", nodeName, err)
	}

	blocking := make([]string, 0)
	for _, pod := range pods.Items {
		if podIsTerminal(pod) || isDaemonSetPod(pod) || isMirrorPod(pod) {
			continue
		}
		if _, isProtected := protected[pod.Namespace]; isProtected {
			continue
		}
		blocking = append(blocking, pod.Namespace+"/"+pod.Name)
	}
	if len(blocking) == 0 {
		return true, "", nil, nil
	}
	sort.Strings(blocking)
	return false, "NodeNotCleared", blocking, nil
}

// clearanceExemptNamespaces resolves the namespaces whose pods legitimately outlive
// a node's release: every NodePowerAgent's operand namespace and the manager's own.
//
// Excluded rather than waited for. The agent is the process that performs the
// shutdown and the manager is the one running the flow, so a clearance check that
// waited for them to leave would wait forever on work that is supposed to be there.
func (r *ShutdownFlowReconciler) clearanceExemptNamespaces(ctx context.Context) (map[string]struct{}, error) {
	var agents powerv1alpha1.NodePowerAgentList
	if err := r.reader().List(ctx, &agents); err != nil {
		return nil, fmt.Errorf("list NodePowerAgents for node clearance: %w", err)
	}
	exempt := make(map[string]struct{}, len(agents.Items)+1)
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		exempt[namespace] = struct{}{}
	}
	for i := range agents.Items {
		cluster, err := r.getNodePowerAgentManagementCluster(ctx, &agents.Items[i])
		if err != nil {
			return nil, err
		}
		exempt[nodePowerAgentNamespace(&agents.Items[i], cluster)] = struct{}{}
	}
	return exempt, nil
}

// reader prefers the uncached API reader when one is wired, falling back to the
// cached client so unit and envtest callers work unchanged.
func (r *ShutdownFlowReconciler) reader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// approvalChecker independently reconfirms flow enforcement approval at each wave boundary:
// an already-rendered actuator is not current authorization. Re-fetches flow fresh
// through r.reader() rather than trusting the snapshot Input.Approved was derived from at
// execution start -- the same reason APIReader exists for EX-9's node-clearance check: a cache a
// few seconds behind is exactly long enough to miss an operator flipping spec.mode back out of
// Enforce mid-execution.
func (r *ShutdownFlowReconciler) approvalChecker(flow *powerv1alpha1.ShutdownFlow) executorpkg.ApprovalChecker {
	key := client.ObjectKeyFromObject(flow)
	return func(ctx context.Context) (bool, error) {
		var current powerv1alpha1.ShutdownFlow
		if err := r.reader().Get(ctx, key, &current); err != nil {
			return false, err
		}
		if current.UID != flow.UID || current.Generation != flow.Generation || !current.DeletionTimestamp.IsZero() {
			return false, nil
		}
		return effectiveShutdownFlowMode(current.Spec.Mode) == powerv1alpha1.ShutdownFlowModeEnforce, nil
	}
}

func podIsTerminal(pod corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

func isDaemonSetPod(pod corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// isMirrorPod reports a static pod's API mirror. The kubelet owns these and no
// controller can move them, so waiting for one to leave waits forever.
func isMirrorPod(pod corev1.Pod) bool {
	_, mirrored := pod.Annotations[corev1.MirrorPodAnnotationKey]
	return mirrored
}

// nodePowerAgentTelemetryFreshness implements spec.shutdown.requireFreshTelemetry (defaulted true by
// the webhook). It fails closed: any error resolving the
// agent's monitored devices, any device this agent depends on with no status yet, and any device
// reporting Stale/Unavailable/Unknown telemetry are all treated as not fresh, consistent with
// resiliency-and-partitions.md's "lost connectivity degrades certainty, never grants optimistic
// action." Evidence is collected for initial guards and refreshed before each signal publication.
// The monitored UPS set is agent-wide, not per selected node.
func (r *ShutdownFlowReconciler) nodePowerAgentTelemetryFreshness(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (bool, string, error) {
	if agent.Spec.Shutdown.RequireFreshTelemetry != nil && !*agent.Spec.Shutdown.RequireFreshTelemetry {
		return true, "", nil
	}

	deviceNames := map[string]struct{}{}
	for _, ref := range agent.Spec.NUTServerRefs {
		var server powerv1alpha1.NUTServer
		if err := r.reader().Get(ctx, client.ObjectKey{Name: ref.Name}, &server); err != nil {
			return false, "", fmt.Errorf("get NUTServer %q for NodePowerAgent %q telemetry freshness: %w", ref.Name, agent.Name, err)
		}
		for _, name := range server.Status.SelectedDevices {
			deviceNames[name] = struct{}{}
		}
	}
	if len(deviceNames) == 0 {
		return false, "AgentTelemetryUnknown", nil
	}

	names := make([]string, 0, len(deviceNames))
	for name := range deviceNames {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var device powerv1alpha1.UPSDevice
		if err := r.reader().Get(ctx, client.ObjectKey{Name: name}, &device); err != nil {
			if apierrors.IsNotFound(err) {
				return false, "AgentTelemetryUnknown", nil
			}
			return false, "", fmt.Errorf("get UPSDevice %q for NodePowerAgent %q telemetry freshness: %w", name, agent.Name, err)
		}
		switch device.Status.Phase {
		case powerv1alpha1.UPSDevicePhaseOnline, powerv1alpha1.UPSDevicePhaseOnBattery, powerv1alpha1.UPSDevicePhaseLowBattery:
			if !nodeReleaseTelemetryRecent(&device, time.Now()) {
				return false, "AgentTelemetryStale", nil
			}
		default:
			return false, "AgentTelemetryStale", nil
		}
	}
	return true, "", nil
}

func (r *ShutdownFlowReconciler) getNodePowerAgentManagementCluster(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (*powerv1alpha1.PowerManagementCluster, error) {
	if agent.Spec.ManagementClusterRef == nil || agent.Spec.ManagementClusterRef.Name == "" {
		return nil, nil
	}
	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.reader().Get(ctx, client.ObjectKey{Name: agent.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
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
	case powerv1alpha1.ShutdownStepRunHook:
		return nil, nil
	default:
		return executorTargetsFromTarget(target), nil
	}
}

func (r *ShutdownFlowReconciler) initialExecutionTargets(ctx context.Context, action powerv1alpha1.ShutdownStepType, target powerv1alpha1.ShutdownStepTarget, resolve bool) ([]executorpkg.Target, error) {
	if !resolve {
		return nil, nil
	}
	return r.executorTargetsForAction(ctx, action, target)
}

func executorHookReference(ref *powerv1alpha1.NamespacedNameReference) *executorpkg.HookReference {
	if ref == nil {
		return nil
	}
	return &executorpkg.HookReference{
		Namespace: ref.Namespace,
		Name:      ref.Name,
	}
}

func executorTargetsFromTarget(target powerv1alpha1.ShutdownStepTarget) []executorpkg.Target {
	targets := make([]executorpkg.Target, 0, len(target.WorkloadRefs)+len(target.Namespaces)+len(target.AgentRefs)+3)
	if target.NodeSelector != nil || len(target.NodeSelectorRequirements) > 0 {
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
		if hasNamespaceConstraint {
			return dedupeExecutorTargets(targets), nil
		}
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
	if err := r.reader().List(ctx, &deployments, options...); err != nil {
		return nil, fmt.Errorf("list Deployments for shutdown execution: %w", err)
	}
	for _, item := range deployments.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: item.Namespace, Name: item.Name})
	}

	var statefulSets appsv1.StatefulSetList
	if err := r.reader().List(ctx, &statefulSets, options...); err != nil {
		return nil, fmt.Errorf("list StatefulSets for shutdown execution: %w", err)
	}
	for _, item := range statefulSets.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: item.Namespace, Name: item.Name})
	}

	var replicaSets appsv1.ReplicaSetList
	if err := r.reader().List(ctx, &replicaSets, options...); err != nil {
		return nil, fmt.Errorf("list ReplicaSets for shutdown execution: %w", err)
	}
	for _, item := range replicaSets.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "apps/v1", Kind: "ReplicaSet", Namespace: item.Namespace, Name: item.Name})
	}

	return targets, nil
}

func (r *ShutdownFlowReconciler) nodeTargets(ctx context.Context, target powerv1alpha1.ShutdownStepTarget) ([]executorpkg.Target, error) {
	if target.NodeSelector == nil && len(target.NodeSelectorRequirements) == 0 {
		return nil, nil
	}
	selector, err := nodeselector.FromAPI(target.NodeSelector, target.NodeSelectorRequirements)
	if err != nil {
		return nil, fmt.Errorf("parse node selector for shutdown execution: %w", err)
	}
	var nodes corev1.NodeList
	if err := r.reader().List(ctx, &nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("list Nodes for shutdown execution: %w", err)
	}
	targets := make([]executorpkg.Target, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		targets = append(targets, executorpkg.Target{APIVersion: "v1", Kind: "Node", Name: node.Name})
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
		if err := r.reader().List(ctx, &namespaceList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
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

// executionAlreadyRecorded reports whether this trigger episode has already been
// executed.
//
// Scoped to the episode, not to power state. Re-descent during a dip-recover-dip
// outage is not blocked by this, because power returning makes the trigger
// ineligible and deactivateLastExecution clears TriggerActive; the next dip is a
// fresh episode. Dedupe never has to reason about power, which is why there is no
// power-shaped exception here.
func executionAlreadyRecorded(status *powerv1alpha1.ShutdownExecutionStatus, dedupeKey string) bool {
	if status == nil || dedupeKey == "" {
		return false
	}
	return status.TriggerActive && status.DeduplicationKey == dedupeKey
}

// deactivateLastExecution clears the trigger episode and, with it, any reason that only made
// sense while the episode was live.
//
// Reason and Message describe one state, so update them together.
func deactivateLastExecution(status **powerv1alpha1.ShutdownExecutionStatus) {
	if status == nil || *status == nil {
		return
	}
	(*status).TriggerActive = false
	switch (*status).Reason {
	case "AlreadyExecuted", "RehearsalAlreadyExecuted":
		(*status).Reason = triggerNotEligibleReason
		(*status).Message = triggerNotEligibleMessage
	}
}

func shutdownExecutionDeduplicationKey(flow *powerv1alpha1.ShutdownFlow, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, configHash string) string {
	eligibleTriggers := make([]eligibleTriggerEpisode, 0)
	if evaluation != nil {
		for _, decision := range evaluation.Decisions {
			if decision.Eligible {
				eligibleTriggers = append(eligibleTriggers, eligibleTriggerEpisode{
					TriggerID:     decision.TriggerID,
					HoldStartedAt: triggerEpisodeBoundary(decision, evaluation),
				})
			}
		}
	}
	sort.Slice(eligibleTriggers, func(i, j int) bool {
		if eligibleTriggers[i].TriggerID != eligibleTriggers[j].TriggerID {
			return eligibleTriggers[i].TriggerID < eligibleTriggers[j].TriggerID
		}
		return eligibleTriggers[i].HoldStartedAt < eligibleTriggers[j].HoldStartedAt
	})
	var selectedUPSDevices []string
	if evaluation != nil {
		selectedUPSDevices = append([]string(nil), evaluation.SelectedUPSDevices...)
	}
	sort.Strings(selectedUPSDevices)
	keyPayload := struct {
		Flow               string                   `json:"flow"`
		Generation         int64                    `json:"generation"`
		Mode               string                   `json:"mode"`
		PlanConfigHash     string                   `json:"planConfigHash"`
		EligibleTriggers   []eligibleTriggerEpisode `json:"eligibleTriggers"`
		SelectedUPSDevices []string                 `json:"selectedUPSDevices"`
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

type eligibleTriggerEpisode struct {
	TriggerID     string `json:"triggerID"`
	HoldStartedAt string `json:"holdStartedAt"`
}

func triggerEpisodeBoundary(decision powerv1alpha1.ShutdownTriggerDecisionStatus, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) string {
	if decision.HoldStartedAt != nil {
		return decision.HoldStartedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if evaluation != nil && evaluation.ObservedAt != nil {
		return evaluation.ObservedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

// shutdownExecutionIDNamespace scopes the derived execution UUIDs to this project, so a digest
// that happened to collide with one from another system could not produce the same UUID.
var shutdownExecutionIDNamespace = uuid.NewSHA1(uuid.NameSpaceDNS, []byte("power.zalud.io"))

// shutdownExecutionIdentity derives the execution's identity from its trigger-episode digest.
//
// A UUID fits both the PostgreSQL execution_id type and the 63-character Kubernetes label
// limit; the 64-character trigger digest does not.
//
// UUIDv5 derivation is deterministic, so the same trigger
// episode always yields the same execution ID and a re-record lands on the primary key instead of
// creating a second row. The digest itself is kept beside it, in status and in
// `shutdownflow_executions.deduplication_key`.
func shutdownExecutionIdentity(dedupeKey string) string {
	if dedupeKey == "" {
		return uuid.NewString()
	}
	return uuid.NewSHA1(shutdownExecutionIDNamespace, []byte(dedupeKey)).String()
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

func markExecutionAlreadyRecorded(
	flow *powerv1alpha1.ShutdownFlow,
	rehearsalRun bool,
) {
	reason := "AlreadyExecuted"
	message := "eligible trigger episode already has execution evidence"
	if rehearsalRun {
		reason = "RehearsalAlreadyExecuted"
		message = "rehearsal request already has execution evidence"
	}
	status := flow.Status.LastExecution
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
