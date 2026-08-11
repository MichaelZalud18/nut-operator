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
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
)

func adaptiveTierPtr(tier int32) *int32 { return &tier }

func adaptiveRuntimePtr(seconds int64) *int64 { return &seconds }

func deviceIn(phase powerv1alpha1.UPSDevicePhase, runtime *int64) powerv1alpha1.UPSDevice {
	return powerv1alpha1.UPSDevice{
		Status: powerv1alpha1.UPSDeviceStatus{Phase: phase, RuntimeSeconds: runtime},
	}
}

// A flow spanning several devices is as constrained as its worst-off device. One device on
// battery means the flow is running on battery, whatever the others report.
func TestOneDeviceOnBatteryPutsTheWholeFlowOnBattery(t *testing.T) {
	observation := powerObservationFromDevices([]powerv1alpha1.UPSDevice{
		deviceIn(powerv1alpha1.UPSDevicePhaseOnline, adaptiveRuntimePtr(3600)),
		deviceIn(powerv1alpha1.UPSDevicePhaseOnBattery, adaptiveRuntimePtr(600)),
	}, true)

	if !observation.OnBattery {
		t.Fatalf("observation = %#v, want OnBattery", observation)
	}
	if observation.RuntimeSeconds == nil || *observation.RuntimeSeconds != 600 {
		t.Fatalf("runtime = %#v, want the shortest of the set", observation.RuntimeSeconds)
	}
}

// A device's own low-battery assertion propagates: it is the judgement the hardware is best
// placed to make, and it must not be diluted by a healthier neighbor.
func TestLowBatteryOnAnyDevicePropagates(t *testing.T) {
	observation := powerObservationFromDevices([]powerv1alpha1.UPSDevice{
		deviceIn(powerv1alpha1.UPSDevicePhaseOnBattery, adaptiveRuntimePtr(900)),
		deviceIn(powerv1alpha1.UPSDevicePhaseLowBattery, adaptiveRuntimePtr(120)),
	}, true)

	if !observation.LowBattery || !observation.OnBattery {
		t.Fatalf("observation = %#v, want OnBattery and LowBattery", observation)
	}
}

// PL-32: one silent device makes the aggregate runtime unknown rather than merely less precise.
// The shortest runtime across a set cannot be known while part of the set is not reporting.
func TestASilentDeviceMakesTheAggregateRuntimeUnknown(t *testing.T) {
	for name, phase := range map[string]powerv1alpha1.UPSDevicePhase{
		"stale":       powerv1alpha1.UPSDevicePhaseStale,
		"unavailable": powerv1alpha1.UPSDevicePhaseUnavailable,
		"unknown":     powerv1alpha1.UPSDevicePhaseUnknown,
	} {
		t.Run(name, func(t *testing.T) {
			observation := powerObservationFromDevices([]powerv1alpha1.UPSDevice{
				deviceIn(powerv1alpha1.UPSDevicePhaseOnBattery, adaptiveRuntimePtr(600)),
				deviceIn(phase, adaptiveRuntimePtr(30)),
			}, true)

			if observation.RuntimeSeconds != nil {
				t.Fatalf("runtime = %d, want unknown while a device is silent", *observation.RuntimeSeconds)
			}
		})
	}
}

// A device on battery that reports no runtime at all leaves the aggregate unknown too.
func TestAMissingRuntimeMakesTheAggregateUnknown(t *testing.T) {
	observation := powerObservationFromDevices([]powerv1alpha1.UPSDevice{
		deviceIn(powerv1alpha1.UPSDevicePhaseOnBattery, adaptiveRuntimePtr(600)),
		deviceIn(powerv1alpha1.UPSDevicePhaseOnBattery, nil),
	}, true)

	if observation.RuntimeSeconds != nil {
		t.Fatalf("runtime = %d, want unknown", *observation.RuntimeSeconds)
	}
}

// AE-6: every selected device must affirmatively declare a dynamic runtime estimate. One fixed
// estimate is enough to make the aggregate untrustworthy, since the shortest runtime across the
// set could be that constant.
func TestRuntimeTrustRequiresEveryDeviceToDeclareDynamic(t *testing.T) {
	dynamic := capability.MatchResult{
		DeviceID:           "ups-a",
		TelemetryVariables: []string{"battery.runtime"},
		RuntimeEstimate:    capability.RuntimeEstimateDynamic,
	}
	static := capability.MatchResult{
		DeviceID:           "ups-b",
		TelemetryVariables: []string{"battery.runtime"},
		RuntimeEstimate:    capability.RuntimeEstimateStatic,
	}
	unset := capability.MatchResult{
		DeviceID:           "ups-b",
		TelemetryVariables: []string{"battery.runtime"},
	}

	for name, testCase := range map[string]struct {
		matches  []capability.MatchResult
		devices  []string
		expected bool
	}{
		"every device dynamic": {
			[]capability.MatchResult{dynamic, {DeviceID: "ups-b", TelemetryVariables: []string{"battery.runtime"}, RuntimeEstimate: capability.RuntimeEstimateDynamic}},
			[]string{"ups-a", "ups-b"}, true},
		"one device static":     {[]capability.MatchResult{dynamic, static}, []string{"ups-a", "ups-b"}, false},
		"one device unverified": {[]capability.MatchResult{dynamic, unset}, []string{"ups-a", "ups-b"}, false},
		"a device has no match": {[]capability.MatchResult{dynamic}, []string{"ups-a", "ups-b"}, false},
		"no devices selected":   {[]capability.MatchResult{dynamic}, nil, false},
		"no capability matches": {nil, []string{"ups-a"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := runtimeIsTrustedForFlow(testCase.matches, testCase.devices); got != testCase.expected {
				t.Fatalf("runtimeIsTrustedForFlow = %v, want %v", got, testCase.expected)
			}
		})
	}
}

// "Every device says mains is back" and "no device said anything" reduce to the same
// observation but mean opposite things. Only the first may halt a flow.
func TestReportingIsDistinguishedFromSilence(t *testing.T) {
	silent := []powerv1alpha1.UPSDevice{
		deviceIn(powerv1alpha1.UPSDevicePhaseUnknown, nil),
		deviceIn(powerv1alpha1.UPSDevicePhaseStale, nil),
	}
	if anyDeviceReporting(silent) {
		t.Fatal("devices in Unknown/Stale phases are not reporting")
	}
	if !anyDeviceReporting([]powerv1alpha1.UPSDevice{deviceIn(powerv1alpha1.UPSDevicePhaseOnline, nil)}) {
		t.Fatal("an Online device is reporting")
	}
}

// PL-31: refusing to run mid-outage is the worst available outcome. A missing UPSDevice object
// must degrade the reading, never abort the shutdown.
func TestAnUnreadableDeviceDegradesRatherThanFailing(t *testing.T) {
	testScheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(testScheme); err != nil {
		t.Fatalf("build scheme: %v", err)
	}
	reconciler := &ShutdownFlowReconciler{Client: fake.NewClientBuilder().WithScheme(testScheme).Build()}
	fallback := adaptive.PowerObservation{OnBattery: true}

	observer := reconciler.powerObserverForDevices([]string{"ups-does-not-exist"}, true, fallback)
	observation, err := observer(t.Context())
	if err != nil {
		t.Fatalf("a missing device must not fail the flow, got %v", err)
	}
	if !observation.OnBattery {
		t.Fatalf("observation = %#v, want the firing state carried forward", observation)
	}
	if observation.RuntimeSeconds != nil {
		t.Fatalf("runtime = %d, want unknown", *observation.RuntimeSeconds)
	}
}

// The pointer's floor comes from the tiers the plan actually compiled, so a flow that only
// reaches tier 3 does not report descending past it.
func TestCompiledTierRangeFollowsTheCompiledWaves(t *testing.T) {
	floor, ceiling := compiledTierRange([]powerv1alpha1.CompiledShutdownWave{
		{Index: 0, ShutdownTier: adaptiveTierPtr(5)},
		{Index: 1, ShutdownTier: adaptiveTierPtr(3)},
	})

	if floor != 3 || ceiling != 5 {
		t.Fatalf("range = %d..%d, want 3..5", floor, ceiling)
	}
}

// Tier 0 is last-ditch and excluded from flow targeting (OD-4). A wave claiming it is already a
// compile error; the range must not let one that slipped through become the pointer's floor.
func TestCompiledTierRangeNeverFloorsAtTheLastDitchTier(t *testing.T) {
	floor, _ := compiledTierRange([]powerv1alpha1.CompiledShutdownWave{
		{Index: 0, ShutdownTier: adaptiveTierPtr(2)},
		{Index: 1, ShutdownTier: adaptiveTierPtr(0)},
	})

	if floor != 1 {
		t.Fatalf("floor = %d, want 1; tier 0 is last-ditch", floor)
	}
}

// An untiered plan is legitimate and must still yield a usable floor.
func TestCompiledTierRangeDefaultsForAnUntieredPlan(t *testing.T) {
	floor, ceiling := compiledTierRange([]powerv1alpha1.CompiledShutdownWave{{Index: 0}})

	if floor != 1 || ceiling != 1 {
		t.Fatalf("range = %d..%d, want 1..1", floor, ceiling)
	}
}

// OD-17: a restarted executor must resume from the pointer it left behind, or it re-reports
// tiers it already descended as new work.
func TestResumedStateReadsBackTheLastPublishedPointer(t *testing.T) {
	status := &powerv1alpha1.ShutdownExecutionStatus{
		Adaptive: &powerv1alpha1.ShutdownExecutionAdaptiveStatus{
			Tier: 4, DeepestTier: 2, PointerStarted: true, TimingMode: string(adaptive.ModeUrgent),
		},
	}

	pointer := resumedPointerState(status)
	if pointer.Tier != 4 || pointer.Deepest != 2 || !pointer.Started {
		t.Fatalf("pointer = %#v, want tier 4 deepest 2 started", pointer)
	}
	if mode := resumedTimingState(status).Mode; mode != adaptive.ModeUrgent {
		t.Fatalf("mode = %q, want Urgent", mode)
	}
}

// A flow with no prior execution starts fresh rather than panicking on a nil status.
func TestResumedStateHandlesAFlowThatNeverExecuted(t *testing.T) {
	if pointer := resumedPointerState(nil); pointer.Started {
		t.Fatalf("pointer = %#v, want an unstarted pointer", pointer)
	}
	if mode := resumedTimingState(nil).Mode; mode != "" {
		t.Fatalf("mode = %q, want empty so the executor defaults it", mode)
	}
}

// A malformed Wait duration leaves the pause undeclared rather than rejecting the flow.
// Refusing to shut a cluster down over a typo in an advisory pause is the worse failure.
func TestAMalformedWaitDurationIsTreatedAsUndeclared(t *testing.T) {
	for name, params := range map[string]map[string]string{
		"absent":   nil,
		"garbage":  {"duration": "soon"},
		"negative": {"duration": "-30s"},
		"empty":    {"duration": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := waitDurationFromParams(params); got != 0 {
				t.Fatalf("waitDurationFromParams = %s, want 0", got)
			}
		})
	}

	if got := waitDurationFromParams(map[string]string{"duration": "45s"}); got != 45*time.Second {
		t.Fatalf("waitDurationFromParams = %s, want 45s", got)
	}
}

// A suspended execution must not count as "already executed". It stopped because power came back,
// with the pointer partway down, and the whole point of leaving it there is that a second dip
// descends again. Treating suspension as done would make dedupe the thing that blocks re-descent --
// during a dip-recover-dip outage, which is exactly what the tier pointer exists to handle.
func TestASuspendedExecutionDoesNotBlockTheNextDescent(t *testing.T) {
	suspended := &powerv1alpha1.ShutdownExecutionStatus{
		TriggerActive:    true,
		DeduplicationKey: "key-a",
		Phase:            powerv1alpha1.ShutdownExecutionPhaseSuspended,
	}
	if executionAlreadyRecorded(suspended, "key-a") {
		t.Fatal("a suspended execution has more to do and must not block a later descent")
	}

	completed := &powerv1alpha1.ShutdownExecutionStatus{
		TriggerActive:    true,
		DeduplicationKey: "key-a",
		Phase:            powerv1alpha1.ShutdownExecutionPhaseCompleted,
	}
	if !executionAlreadyRecorded(completed, "key-a") {
		t.Fatal("a completed execution for the same episode must still deduplicate")
	}
}

// The published pointer is what the next execution resumes from, so the round trip through status
// has to preserve it exactly -- including Deepest, which is what makes re-descent recognizable.
func TestThePublishedPointerRoundTripsBackIntoTheNextRun(t *testing.T) {
	published := adaptiveStatusFromResult(executorpkg.AdaptiveResult{
		Pointer:   adaptive.PointerState{Tier: 4, Deepest: 2, Started: true},
		Timing:    adaptive.TimingState{Mode: adaptive.ModeUrgent},
		Suspended: true,
	})

	resumed := resumedPointerState(&powerv1alpha1.ShutdownExecutionStatus{Adaptive: published})
	if resumed.Tier != 4 || resumed.Deepest != 2 || !resumed.Started {
		t.Fatalf("resumed = %#v, want tier 4 deepest 2 started", resumed)
	}
	// Suspension is not a halt: the resumed pointer must be free to descend again (AE-6).
	if resumed.Halted {
		t.Fatal("a suspended run must not resume into a latched pointer")
	}
}
