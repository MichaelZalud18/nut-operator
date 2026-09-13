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
	"slices"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// historySampleLimit bounds one flow's history read.
//
// Generous rather than tuned: the estimate takes the slowest observation, so extra
// samples can only sharpen it, and the read happens at planning time rather than
// during an outage.
const historySampleLimit = 200

// resolveFlowHistory reads observed group durations for this flow (EX-32).
//
// Failure is not propagated. A history read that errors leaves the estimates on
// declared timeouts, which is exactly where every deployment starts, so the flow
// still compiles and still shuts the cluster down. Refusing to plan because a
// reporting database was unreachable is the PL-31 failure in a new place.
//
// Scoped by plan config hash so a group renamed onto different work is not compared
// against its predecessor's timings.
func resolveFlowHistory(ctx context.Context, store audit.Store, flow *powerv1alpha1.ShutdownFlow, planConfigHash string) planner.HistoryInputs {
	log := logf.FromContext(ctx)

	reader, ok := store.(audit.HistoryReader)
	if !ok || flow == nil || planConfigHash == "" {
		return planner.HistoryInputs{}
	}

	samples, err := reader.GroupDurations(ctx, flow.Name, planConfigHash, historySampleLimit)
	if err != nil {
		log.Error(err, "Failed to read execution history; estimates fall back to declared timeouts",
			"shutdownFlow", flow.Name)
		return planner.HistoryInputs{}
	}
	if len(samples) == 0 {
		return planner.HistoryInputs{}
	}

	durations := make(map[string][]time.Duration, len(samples))
	includeRehearsals := includeRehearsalHistory(flow)
	for _, sample := range samples {
		if sample.Rehearsal && !includeRehearsals {
			continue
		}
		durations[sample.GroupName] = append(durations[sample.GroupName], sample.Observed)
	}
	if len(durations) == 0 {
		return planner.HistoryInputs{}
	}
	return planner.HistoryInputs{GroupDurations: durations}
}

// planFeasibilityStatus compares the plan estimate against selected UPS telemetry (OD-12).
//
// Warns, never blocks. The flow author holds the risk; this operator holds the
// numbers and owes them a clear statement of both. Truncating the plan or refusing
// to run it would substitute the operator's judgement for theirs at the moment they
// can least review it.
func planFeasibilityStatus(estimated *time.Duration, observation planner.HistoryObservation, confidence planner.EstimateConfidence) *powerv1alpha1.PlanFeasibilityStatus {
	if estimated == nil || *estimated <= 0 {
		return nil
	}
	planSeconds := int64(estimated.Seconds())
	status := &powerv1alpha1.PlanFeasibilityStatus{
		PlanSeconds:    planSeconds,
		ObservedGroups: int32(confidence.ObservedGroups),
		DeclaredGroups: int32(confidence.DeclaredGroups),
		ThinGroups:     append([]string(nil), confidence.ThinGroups...),
	}
	if observation.ChargePercent != nil {
		charge := *observation.ChargePercent
		status.ChargePercent = &charge
	}
	if observation.LoadPercent != nil {
		load := *observation.LoadPercent
		status.LoadPercent = &load
	}

	if observation.RuntimeSeconds == nil {
		// PL-32: unknown runtime never yields an optimistic verdict. Fits stays false and
		// the message says why rather than implying the plan is too long.
		status.Message = fmt.Sprintf("plan estimate %s; UPS runtime unknown, so it cannot be confirmed to fit",
			estimated.Round(time.Second))
		return status
	}

	runtime := *observation.RuntimeSeconds
	status.RuntimeSeconds = &runtime
	status.Fits = planSeconds <= runtime

	if status.Fits {
		status.Message = fmt.Sprintf("plan estimate %s against %s of reported runtime",
			estimated.Round(time.Second), (time.Duration(runtime) * time.Second).Round(time.Second))
	} else {
		status.Message = fmt.Sprintf("plan estimate %s exceeds %s of reported runtime by %s; the flow will still run",
			estimated.Round(time.Second),
			(time.Duration(runtime) * time.Second).Round(time.Second),
			(time.Duration(planSeconds-runtime) * time.Second).Round(time.Second))
	}
	return status
}

// flowExecutionHistory opens the audit store and reads this flow's observed durations.
//
// Storage is optional (Disabled mode) and a reporting database being unreachable is
// not a reason to stop planning, so every failure path here returns empty history
// rather than an error. The estimates fall back to declared timeouts, which is where
// every deployment starts.
func (r *ShutdownFlowReconciler) flowExecutionHistory(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster, flow *powerv1alpha1.ShutdownFlow, planConfigHash string) planner.HistoryInputs {
	if cluster == nil || flow == nil || planConfigHash == "" || !managementClusterStorageReady(cluster) {
		return planner.HistoryInputs{}
	}
	ctx, cancel := context.WithTimeout(ctx, shutdownAuditIOTimeout)
	defer cancel()
	store, err := r.storageConnector().OpenAuditStore(ctx, cluster)
	if err != nil || store == nil {
		return planner.HistoryInputs{}
	}
	defer func() { _ = store.Close() }()

	return resolveFlowHistory(ctx, store, flow, planConfigHash)
}

// bestPlanEstimate prefers the observed total when one exists.
//
// The declared total is what the author intended; the observed total is what this
// cluster actually does. When both are available the second is the better input to a
// warning, and the provenance published alongside it is what lets a reader see which
// one they are looking at.
func bestPlanEstimate(observed, declared *metav1.Duration) *time.Duration {
	if observed != nil && observed.Duration > 0 {
		duration := observed.Duration
		return &duration
	}
	if declared != nil && declared.Duration > 0 {
		duration := declared.Duration
		return &duration
	}
	return nil
}

// flowRuntimeObservation reads the UPS telemetry the warning is compared against.
//
// Reuses the same device read and the same reductions the executor uses at wave
// boundaries, so the runtime in the warning is the runtime the flow would actually act on. Trust is
// gated by CR-4: a device reporting a fixed firmware estimate contributes no runtime, because a
// constant cannot say whether this plan fits. Charge and load are still published as generic public
// UPS telemetry when every budget supply reports them; they are not site-local metrics and they
// do not change the plan hash.
func (r *ShutdownFlowReconciler) flowRuntimeObservation(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, bundle resolver.StructuralBundle) planner.HistoryObservation {
	if evaluation == nil || len(evaluation.SelectedUPSDevices) == 0 {
		return planner.HistoryObservation{}
	}
	supplies, unknownSupply := communicationBudgetDevices(flow, bundle)
	names := append(slices.Clone(evaluation.SelectedUPSDevices), supplies...)
	sort.Strings(names)
	names = slices.Compact(names)

	devices := make([]powerv1alpha1.UPSDevice, 0, len(names))
	for _, name := range names {
		var device powerv1alpha1.UPSDevice
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &device); err != nil {
			// An unreadable device makes the aggregate unknown rather than merely less
			// precise, exactly as it does on the execution path.
			return planner.HistoryObservation{}
		}
		devices = append(devices, device)
	}

	observation := runtimeBudgetFromDevices(devices, bundle, adaptive.PowerObservation{}, unknownSupply)
	history := planner.HistoryObservation{}
	if observation.RuntimeTrusted {
		history.RuntimeSeconds = observation.RuntimeSeconds
	}

	chargeKnown := true
	loadKnown := true
	for _, device := range devices {
		if !devicePhaseReportsPower(device.Status.Phase) {
			chargeKnown = false
			loadKnown = false
			continue
		}
		if device.Status.BatteryChargePercent == nil {
			chargeKnown = false
		} else if history.ChargePercent == nil || *device.Status.BatteryChargePercent < *history.ChargePercent {
			charge := *device.Status.BatteryChargePercent
			history.ChargePercent = &charge
		}
		if device.Status.LoadPercent == nil {
			loadKnown = false
		} else if history.LoadPercent == nil || *device.Status.LoadPercent > *history.LoadPercent {
			load := *device.Status.LoadPercent
			history.LoadPercent = &load
		}
	}
	if !chargeKnown {
		history.ChargePercent = nil
	}
	if !loadKnown {
		history.LoadPercent = nil
	}
	return history
}

func devicePhaseReportsPower(phase powerv1alpha1.UPSDevicePhase) bool {
	switch phase {
	case powerv1alpha1.UPSDevicePhaseOnline,
		powerv1alpha1.UPSDevicePhaseOnBattery,
		powerv1alpha1.UPSDevicePhaseLowBattery:
		return true
	default:
		return false
	}
}
