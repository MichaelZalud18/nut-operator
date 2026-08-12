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
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// adaptiveInputForFlow assembles the tier-pointer and timing-mode state the
// executor resumes from.
//
// The tier range comes from the tiers the plan actually compiled rather than from
// a constant, so a flow whose last tier is 3 does not report descending past it.
// Tier 0 is last-ditch and excluded from flow targeting (OD-4), so the final tier
// is never below 1 regardless of what the waves say.
func adaptiveInputForFlow(flow *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, observation adaptive.PowerObservation) executorpkg.AdaptiveInput {
	final, start := compiledTierRange(flow.Status.CompiledWaves)
	return executorpkg.AdaptiveInput{
		Parameters:  adaptive.DefaultParameters(),
		Pointer:     resumedPointerState(flow.Status.LastExecution),
		Timing:      resumedTimingState(flow.Status.LastExecution),
		Observation: observation,
		FinalTier:   final,
		StartTier:   start,
	}
}

// compiledTierRange reports the last and first tiers the compiled waves occupy.
// An untiered plan yields tier 1 for both.
func compiledTierRange(waves []powerv1alpha1.CompiledShutdownWave) (int32, int32) {
	last := int32(0)
	first := int32(0)
	for _, wave := range waves {
		if wave.ShutdownTier == nil {
			continue
		}
		tier := *wave.ShutdownTier
		if tier < 1 {
			// Tier 0 cannot be targeted by a flow. A wave claiming it is already a compile
			// error; clamping here means the pointer cannot act on one that slipped through.
			tier = 1
		}
		if last == 0 || tier < last {
			last = tier
		}
		if tier > first {
			first = tier
		}
	}
	if last == 0 {
		last = 1
	}
	if first < last {
		first = last
	}
	return last, first
}

// publishCadence is the EX-29 heartbeat interval for this flow's current activity
// (OD-30).
//
// The two paths EX-29 requires are not interchangeable. Change emission already
// covers transitions; this is the other one -- a periodic republish that happens
// whether or not anything moved, so a subscriber can tell "nothing is happening"
// from "the publisher died". Both look like silence otherwise.
//
// It is expressed as a reconcile interval rather than a separate timer because
// reconciling is what republishes status: a second loop emitting snapshots
// alongside the reconciler could report a state the reconciler had already moved
// past.
func publishCadence(flow *powerv1alpha1.ShutdownFlow, parameters adaptive.Parameters) time.Duration {
	return parameters.WithDefaults().Cadence(flowIsActive(flow))
}

// cadenceParameters folds the cluster-wide EX-29 heartbeat settings onto the defaults (OD-30).
//
// Cluster-wide because the cadence describes the publisher's liveness and there is one publisher.
// A nil cluster, a nil block, or an unset field each keep the default rather than collapsing to
// zero -- a zero interval would mean "never republish", which is the one value that defeats the
// heartbeat entirely.
func cadenceParameters(cluster *powerv1alpha1.PowerManagementCluster) adaptive.Parameters {
	parameters := adaptive.DefaultParameters()
	if cluster == nil || cluster.Spec.Observability.PublishCadence == nil {
		return parameters
	}
	cadence := cluster.Spec.Observability.PublishCadence
	if cadence.Idle != nil && cadence.Idle.Duration > 0 {
		parameters.IdleCadence = cadence.Idle.Duration
	}
	if cadence.Active != nil && cadence.Active.Duration > 0 {
		parameters.ActiveCadence = cadence.Active.Duration
	}
	// An active cadence slower than idle inverts the split's whole purpose. Parameters.Validate
	// reports it as CadenceSlowerDuringFlow; here the safe reading is to keep the configured idle
	// and fall back to the default active rather than publish more slowly mid-outage.
	if parameters.ActiveCadence > parameters.IdleCadence {
		parameters.ActiveCadence = adaptive.DefaultParameters().ActiveCadence
	}
	return parameters
}

// flowIsActive reports whether a flow is mid-outage, which is when a subscriber
// is tracking progress rather than only confirming the publisher is alive.
//
// A trigger that has matched but is still serving its hold counts, which might not
// look like it: the condition is true and the clock is running, so the flow is
// mid-outage by definition even though no wave has started.
func flowIsActive(flow *powerv1alpha1.ShutdownFlow) bool {
	if flow == nil {
		return false
	}
	if evaluation := flow.Status.TriggerEvaluation; evaluation != nil {
		if evaluation.Eligible {
			return true
		}
		for _, decision := range evaluation.Decisions {
			if decision.Matched {
				return true
			}
		}
	}
	execution := flow.Status.LastExecution
	if execution == nil {
		return false
	}
	switch execution.Phase {
	case powerv1alpha1.ShutdownExecutionPhaseRunning:
		return true
	default:
		return false
	}
}

// soonestRequeue picks the earlier of two reconcile intervals, ignoring
// non-positive ones.
//
// The cadence bounds how long the operator may stay quiet; it never delays how
// soon it may act. A trigger hold expiring in ten seconds must not be deferred to
// the next heartbeat.
func soonestRequeue(current, candidate time.Duration) time.Duration {
	if candidate <= 0 {
		return current
	}
	if current <= 0 || candidate < current {
		return candidate
	}
	return current
}

// resumedPointerState reads back the pointer an earlier execution left behind
// (OD-17, EX-14).
//
// A restarted executor that started from a fresh pointer would re-report tiers it
// already descended as new work, which is a reporting error at exactly the moment
// a subscriber is trying to understand a second dip.
func resumedPointerState(status *powerv1alpha1.ShutdownExecutionStatus) adaptive.PointerState {
	if status == nil || status.Adaptive == nil {
		return adaptive.PointerState{}
	}
	return adaptive.PointerState{
		Tier:    status.Adaptive.Tier,
		Deepest: status.Adaptive.DeepestTier,
		Started: status.Adaptive.PointerStarted,
	}
}

// resumedTimingState reads back the timing mode. Restarting into a fresh mode
// would silently relax a flow that had escalated, handing back time it may need
// and cannot get again.
func resumedTimingState(status *powerv1alpha1.ShutdownExecutionStatus) adaptive.TimingState {
	if status == nil || status.Adaptive == nil || status.Adaptive.TimingMode == "" {
		return adaptive.TimingState{}
	}
	return adaptive.TimingState{Mode: adaptive.TimingMode(status.Adaptive.TimingMode)}
}

// adaptiveStatusFromResult publishes the adaptive state a run ended on (EX-28).
//
// Facts only: the tier reached, the mode in force, and the power observed. There
// is deliberately no "recovering", "flickering", or estimated completion here --
// those need a theory about what the history means, which is a subscriber's to
// hold, not the operator's to assert.
func adaptiveStatusFromResult(result executorpkg.AdaptiveResult) *powerv1alpha1.ShutdownExecutionAdaptiveStatus {
	status := &powerv1alpha1.ShutdownExecutionAdaptiveStatus{
		Tier:           result.Pointer.Tier,
		DeepestTier:    result.Pointer.Deepest,
		PointerStarted: result.Pointer.Started,
		TimingMode:     string(result.Timing.Mode),
		OnBattery:      result.Observation.OnBattery,
		LowBattery:     result.Observation.LowBattery,
		RuntimeTrusted: result.Observation.RuntimeTrusted,
		Events:         append([]string(nil), result.Events...),
	}
	if result.Observation.RuntimeSeconds != nil {
		seconds := *result.Observation.RuntimeSeconds
		status.RuntimeSeconds = &seconds
	}
	if status.TimingMode == "" {
		status.TimingMode = string(adaptive.ModeRelaxed)
	}
	return status
}

// powerObserverForDevices reads live power state from the UPS devices a firing
// trigger selected.
//
// Called at each wave boundary, so it reads through the API rather than closing
// over a snapshot: the whole point of evaluating at wave boundaries is that the
// power state may have moved since the last one.
//
// A device that cannot be read degrades the observation instead of failing the
// flow. Both rules that bear on this point the same way. PL-32 forbids resolving
// missing data optimistically, and an unreadable device is treated here exactly
// like a stale one -- it contributes no runtime, and unknown runtime on battery
// selects the most urgent timings. PL-31 forbids the other half: refusing to run
// mid-outage is the worst available outcome, and a cluster that dies ungracefully
// because one UPSDevice object went missing is a worse result than one that
// shuts down on a degraded reading.
func (r *ShutdownFlowReconciler) powerObserverForDevices(deviceNames []string, trusted bool, fallback adaptive.PowerObservation) executorpkg.PowerObserver {
	if len(deviceNames) == 0 {
		return nil
	}
	names := append([]string(nil), deviceNames...)
	sort.Strings(names)
	return func(ctx context.Context) (adaptive.PowerObservation, error) {
		devices := make([]powerv1alpha1.UPSDevice, 0, len(names))
		for _, name := range names {
			var device powerv1alpha1.UPSDevice
			if err := r.Get(ctx, client.ObjectKey{Name: name}, &device); err != nil {
				// Recorded as a device in an unknown phase, which is what it is.
				devices = append(devices, powerv1alpha1.UPSDevice{
					Status: powerv1alpha1.UPSDeviceStatus{Phase: powerv1alpha1.UPSDevicePhaseUnknown},
				})
				continue
			}
			devices = append(devices, device)
		}
		observation := powerObservationFromDevices(devices, trusted)
		if !observation.OnBattery && !observation.LowBattery && !anyDeviceReporting(devices) {
			// Nothing could be read at all. The trigger fired on a power event, so the
			// honest carry-forward is the state that fired it, not an unobserved "mains is
			// fine" that would halt the flow on no evidence.
			return fallback, nil
		}
		return observation, nil
	}
}

// anyDeviceReporting reports whether at least one device published a phase the
// model can act on. Used to tell "every device says mains is back" from "no device
// said anything" -- which look identical in the reduced observation and mean
// opposite things.
func anyDeviceReporting(devices []powerv1alpha1.UPSDevice) bool {
	for _, device := range devices {
		switch device.Status.Phase {
		case powerv1alpha1.UPSDevicePhaseOnline,
			powerv1alpha1.UPSDevicePhaseOnBattery,
			powerv1alpha1.UPSDevicePhaseLowBattery:
			return true
		}
	}
	return false
}

// powerObservationFromDevices reduces several devices to the one observation the
// model reasons about.
//
// The reduction is deliberately pessimistic in every field. A flow spanning two
// UPS devices is as constrained as its worst-off device: if one is on battery the
// flow is running on battery, and the runtime that matters is the shortest one,
// because that is when part of the cluster starts losing power.
func powerObservationFromDevices(devices []powerv1alpha1.UPSDevice, trusted bool) adaptive.PowerObservation {
	observation := adaptive.PowerObservation{RuntimeTrusted: trusted}
	runtimeKnown := true

	for _, device := range devices {
		switch device.Status.Phase {
		case powerv1alpha1.UPSDevicePhaseLowBattery:
			observation.OnBattery = true
			observation.LowBattery = true
		case powerv1alpha1.UPSDevicePhaseOnBattery:
			observation.OnBattery = true
		case powerv1alpha1.UPSDevicePhaseOnline:
			// Contributes nothing: one healthy device does not offset a failing one.
		default:
			// Stale, Unavailable, or Unknown. The device is not reporting, so its runtime
			// cannot be counted and the flow must not read silence as headroom.
			runtimeKnown = false
			continue
		}
		if device.Status.RuntimeSeconds == nil {
			runtimeKnown = false
			continue
		}
		if observation.RuntimeSeconds == nil || *device.Status.RuntimeSeconds < *observation.RuntimeSeconds {
			shortest := *device.Status.RuntimeSeconds
			observation.RuntimeSeconds = &shortest
		}
	}

	if !runtimeKnown {
		// One unreadable device makes the aggregate runtime unknown rather than merely
		// less precise: the shortest runtime across the set cannot be known if part of
		// the set is silent.
		observation.RuntimeSeconds = nil
	}
	return observation
}

// runtimeIsTrustedForFlow reports whether the selected devices' runtime figures may
// drive timing decisions (CR-4).
//
// Every device must affirmatively declare a Dynamic runtime estimate. One device
// reporting a fixed number the load never moves is enough to make the aggregate
// untrustworthy, since the shortest runtime across the set could be that constant.
// Absence of the declaration is unverified, and unverified is not Dynamic.
func runtimeIsTrustedForFlow(matches []capability.MatchResult, deviceNames []string) bool {
	if len(deviceNames) == 0 || len(matches) == 0 {
		return false
	}
	selected := make(map[string]struct{}, len(deviceNames))
	for _, name := range deviceNames {
		selected[name] = struct{}{}
	}

	covered := 0
	for _, match := range matches {
		if _, wanted := selected[match.DeviceID]; !wanted {
			continue
		}
		if !match.SupportsTimingAdaptation() {
			return false
		}
		covered++
	}
	// A selected device with no capability match at all is unverified, not permitted.
	return covered == len(selected)
}
