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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
	triggerpkg "github.com/MichaelZalud18/nut-operator/internal/trigger"
)

func evaluateShutdownFlowTriggers(ctx context.Context, k8sClient client.Client, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, configHash string) (triggerpkg.Evaluation, *powerv1alpha1.ShutdownTriggerEvaluationStatus, []powerv1alpha1.ShutdownTriggerHoldStateStatus, error) {
	var devices powerv1alpha1.UPSDeviceList
	if err := k8sClient.List(ctx, &devices); err != nil {
		return triggerpkg.Evaluation{}, nil, nil, err
	}

	evaluation := triggerpkg.Evaluate(triggerpkg.Inputs{
		ObservedAt: observedAt,
		Triggers:   triggerInputsFromFlow(flow.Spec.Triggers),
		UPSStates:  triggerUPSStatesFromDevices(devices.Items),
		Holds:      triggerHoldsFromStatus(flow.Status.TriggerHoldStates),
	})
	status := triggerEvaluationStatus(evaluation, effectiveShutdownFlowMode(flow.Spec.Mode), configHash)
	holds := triggerHoldStatus(evaluation.Holds)
	return evaluation, &status, holds, nil
}

func triggerInputsFromFlow(triggers []powerv1alpha1.ShutdownTrigger) []triggerpkg.Trigger {
	inputs := make([]triggerpkg.Trigger, 0, len(triggers))
	for _, flowTrigger := range triggers {
		input := triggerpkg.Trigger{
			Type:                string(flowTrigger.Type),
			FallbackType:        flowTrigger.FallbackType.Value(),
			UPSDevices:          triggerUPSDeviceNames(flowTrigger.UPSDeviceRefs),
			PowerDomains:        flowTrigger.PowerDomains,
			RuntimeBelowSeconds: flowTrigger.RuntimeBelowSeconds,
			ChargeBelowPercent:  flowTrigger.ChargeBelowPercent,
		}
		if flowTrigger.For != nil {
			input.For = flowTrigger.For.Duration
		}
		inputs = append(inputs, input)
	}
	return inputs
}

func triggerUPSDeviceNames(refs []powerv1alpha1.ObjectNameReference) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

func triggerUPSStatesFromDevices(devices []powerv1alpha1.UPSDevice) []triggerpkg.UPSState {
	states := make([]triggerpkg.UPSState, 0, len(devices))
	for _, device := range devices {
		state := triggerpkg.UPSState{
			UPSDevice:      device.Name,
			PowerDomains:   device.Spec.PowerDomains,
			Phase:          telemetryPhaseFromUPSDevice(device.Status.Phase),
			OnBattery:      device.Status.Phase == powerv1alpha1.UPSDevicePhaseOnBattery || device.Status.Phase == powerv1alpha1.UPSDevicePhaseLowBattery,
			LowBattery:     device.Status.Phase == powerv1alpha1.UPSDevicePhaseLowBattery,
			Stale:          device.Status.Phase == powerv1alpha1.UPSDevicePhaseStale || device.Status.Phase == powerv1alpha1.UPSDevicePhaseUnavailable,
			RuntimeSeconds: device.Status.RuntimeSeconds,
			ChargePercent:  device.Status.BatteryChargePercent,
		}
		if device.Status.LastPollTime != nil {
			state.ObservedAt = device.Status.LastPollTime.Time
		}
		states = append(states, state)
	}
	return states
}

func telemetryPhaseFromUPSDevice(phase powerv1alpha1.UPSDevicePhase) string {
	switch phase {
	case powerv1alpha1.UPSDevicePhaseOnline:
		return telemetry.PhaseOnline
	case powerv1alpha1.UPSDevicePhaseOnBattery:
		return telemetry.PhaseOnBattery
	case powerv1alpha1.UPSDevicePhaseLowBattery:
		return telemetry.PhaseLowBattery
	case powerv1alpha1.UPSDevicePhaseStale:
		return telemetry.PhaseStale
	case powerv1alpha1.UPSDevicePhaseUnavailable:
		return telemetry.PhaseUnavailable
	default:
		return telemetry.PhaseUnknown
	}
}

func triggerHoldsFromStatus(status []powerv1alpha1.ShutdownTriggerHoldStateStatus) []triggerpkg.HoldState {
	holds := make([]triggerpkg.HoldState, 0, len(status))
	for _, hold := range status {
		holds = append(holds, triggerpkg.HoldState{
			TriggerID: hold.TriggerID,
			UPSDevice: hold.UPSDevice,
			StartedAt: hold.StartedAt.Time,
		})
	}
	return holds
}

func triggerEvaluationStatus(evaluation triggerpkg.Evaluation, mode powerv1alpha1.ShutdownFlowMode, configHash string) powerv1alpha1.ShutdownTriggerEvaluationStatus {
	observedAt := metav1.NewTime(evaluation.ObservedAt)
	status := powerv1alpha1.ShutdownTriggerEvaluationStatus{
		ObservedAt:          &observedAt,
		Mode:                mode,
		Eligible:            evaluation.Approved,
		Reason:              evaluation.Reason,
		MatchedTriggerCount: int32(evaluation.MatchedTriggerCount),
		SelectedUPSDevices:  evaluation.SelectedUPSDevices,
		PlanConfigHash:      configHash,
		Decisions:           make([]powerv1alpha1.ShutdownTriggerDecisionStatus, 0, len(evaluation.Decisions)),
		Diagnostics:         make([]powerv1alpha1.ShutdownTriggerDiagnosticStatus, 0, len(evaluation.Diagnostics)),
	}
	for _, decision := range evaluation.Decisions {
		status.Decisions = append(status.Decisions, triggerDecisionStatus(decision))
	}
	for _, diagnostic := range evaluation.Diagnostics {
		status.Diagnostics = append(status.Diagnostics, powerv1alpha1.ShutdownTriggerDiagnosticStatus{
			Severity: diagnostic.Severity,
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	return status
}

func triggerDecisionStatus(decision triggerpkg.Decision) powerv1alpha1.ShutdownTriggerDecisionStatus {
	status := powerv1alpha1.ShutdownTriggerDecisionStatus{
		TriggerID:          decision.TriggerID,
		Type:               powerv1alpha1.ShutdownTriggerType(decision.Type),
		Matched:            decision.Matched,
		Eligible:           decision.Eligible,
		Reason:             decision.Reason,
		SelectedUPSDevices: decision.SelectedUPSDevices,
	}
	if decision.HoldStartedAt != nil {
		holdStartedAt := metav1.NewTime(*decision.HoldStartedAt)
		status.HoldStartedAt = &holdStartedAt
	}
	if decision.EligibleAt != nil {
		eligibleAt := metav1.NewTime(*decision.EligibleAt)
		status.EligibleAt = &eligibleAt
	}
	return status
}

func triggerHoldStatus(holds []triggerpkg.HoldState) []powerv1alpha1.ShutdownTriggerHoldStateStatus {
	status := make([]powerv1alpha1.ShutdownTriggerHoldStateStatus, 0, len(holds))
	for _, hold := range holds {
		status = append(status, powerv1alpha1.ShutdownTriggerHoldStateStatus{
			TriggerID: hold.TriggerID,
			UPSDevice: hold.UPSDevice,
			StartedAt: metav1.NewTime(hold.StartedAt),
		})
	}
	return status
}

func effectiveShutdownFlowMode(mode powerv1alpha1.ShutdownFlowMode) powerv1alpha1.ShutdownFlowMode {
	if mode == "" {
		return powerv1alpha1.ShutdownFlowModeDryRun
	}
	return mode
}

func triggerRequeueAfter(evaluation triggerpkg.Evaluation, observedAt time.Time) time.Duration {
	if evaluation.Approved {
		return 0
	}
	var nextEligible time.Time
	for _, decision := range evaluation.Decisions {
		if !decision.Matched || decision.Eligible || decision.EligibleAt == nil {
			continue
		}
		if nextEligible.IsZero() || decision.EligibleAt.Before(nextEligible) {
			nextEligible = *decision.EligibleAt
		}
	}
	if nextEligible.IsZero() || !nextEligible.After(observedAt) {
		return 0
	}
	return nextEligible.Sub(observedAt)
}
