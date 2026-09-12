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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

func historyRuntime(seconds int64) planner.HistoryObservation {
	return planner.HistoryObservation{RuntimeSeconds: &seconds}
}

func TestCompileHistoryUsesNewIdentityBeforeStatusUpdate(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: "history-identity"},
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Triggers: []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerOnBattery}},
			Groups: []powerv1alpha1.ShutdownGroup{{Name: "work", Action: powerv1alpha1.ShutdownStepScaleWorkload,
				Timeout: &metav1.Duration{Duration: time.Minute},
				Target:  powerv1alpha1.ShutdownStepTarget{WorkloadSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
			}},
		},
	}
	bundle := resolver.StructuralBundle{}
	policy := powerv1alpha1.PowerShutdownTierPolicySpec{}
	initial := compileShutdownFlowWithHistory(flow, bundle, policy, nil, nil)
	if initial.ConfigHash == "" {
		t.Fatalf("initial plan rejected: %v", initial.Diagnostics)
	}
	flow.Status.ConfigHash = initial.ConfigHash
	flow.Spec.Groups[0].Target.WorkloadSelector.MatchLabels["app"] = "database"
	newPlan := compileShutdownFlowWithHistory(flow, bundle, policy, nil, nil)
	if newPlan.ConfigHash == initial.ConfigHash || newPlan.ConfigHash == "" {
		t.Fatal("target edit must produce a new valid identity")
	}
	for _, hasNewHistory := range []bool{false, true} {
		calls := 0
		compiled := compileShutdownFlowWithHistory(flow, bundle, policy, func(hash string) planner.HistoryInputs {
			calls++
			if hash != newPlan.ConfigHash {
				t.Errorf("history requested for %q, want new hash %q", hash, newPlan.ConfigHash)
			}
			if hash == initial.ConfigHash {
				return planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"work": {59 * time.Minute}}}
			}
			if hasNewHistory {
				return planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"work": {7 * time.Second}}}
			}
			return planner.HistoryInputs{}
		}, nil)
		if calls != 1 || compiled.ConfigHash != newPlan.ConfigHash {
			t.Fatalf("lookup count/hash changed: calls=%d hash=%s", calls, compiled.ConfigHash)
		}
		if len(compiled.GroupEstimates) != 1 {
			t.Fatalf("missing estimates: %#v", compiled)
		}
		want := time.Minute
		if hasNewHistory {
			want = 7 * time.Second
		}
		if compiled.GroupEstimates[0].Duration.Duration != want {
			t.Fatalf("duration = %v, want %v", compiled.GroupEstimates[0].Duration, want)
		}
		if flow.Status.ConfigHash != initial.ConfigHash {
			t.Fatal("compile mutated existing status")
		}
	}
	flow.Spec.Triggers = nil
	rejected := compileShutdownFlowWithHistory(flow, bundle, policy, func(string) planner.HistoryInputs {
		t.Fatal("rejected plan must not query history")
		return planner.HistoryInputs{}
	}, nil)
	if rejected.ConfigHash != "" {
		t.Fatal("invalid plan accepted")
	}
}

func historyPowerTelemetry(runtime int64, charge, load int32) planner.HistoryObservation {
	return planner.HistoryObservation{
		RuntimeSeconds: &runtime,
		ChargePercent:  &charge,
		LoadPercent:    &load,
	}
}

// OD-12: the plan is never blocked or truncated. A plan that does not fit still runs, and the
// warning has to say so plainly rather than reading like a refusal.
func TestPlanFeasibilityWarnsWithoutBlocking(t *testing.T) {
	estimated := 20 * time.Minute
	status := planFeasibilityStatus(&estimated, historyRuntime(480),
		planner.EstimateConfidence{ObservedGroups: 2, DeclaredGroups: 1})

	if status == nil {
		t.Fatal("a plan with an estimate must publish a feasibility comparison")
	}
	if status.Fits {
		t.Fatalf("status = %#v, want fits false for 20m against 8m", status)
	}
	if status.PlanSeconds != 1200 || status.RuntimeSeconds == nil || *status.RuntimeSeconds != 480 {
		t.Fatalf("status = %#v, want both numbers published", status)
	}
	if !strings.Contains(status.Message, "will still run") {
		t.Fatalf("message = %q, must say the flow still runs", status.Message)
	}
	if status.ObservedGroups != 2 || status.DeclaredGroups != 1 {
		t.Fatalf("status = %#v, want the estimate provenance carried through", status)
	}
}

// PL-32: missing data never yields an optimistic verdict. Unknown runtime must not report a fit.
func TestUnknownRuntimeNeverReportsAFit(t *testing.T) {
	estimated := 2 * time.Minute
	status := planFeasibilityStatus(&estimated, planner.HistoryObservation{}, planner.EstimateConfidence{})

	if status.Fits {
		t.Fatal("unknown runtime must not be read as headroom")
	}
	if status.RuntimeSeconds != nil {
		t.Fatalf("runtime = %d, want it left absent", *status.RuntimeSeconds)
	}
	if !strings.Contains(status.Message, "unknown") {
		t.Fatalf("message = %q, should say the runtime is unknown rather than imply the plan is too long", status.Message)
	}
}

// A plan that fits still publishes both numbers: the comparison is the point, not just the verdict.
func TestAFittingPlanStillPublishesTheComparison(t *testing.T) {
	estimated := 4 * time.Minute
	status := planFeasibilityStatus(&estimated, historyRuntime(1200), planner.EstimateConfidence{})

	if !status.Fits {
		t.Fatalf("status = %#v, want fits true for 4m against 20m", status)
	}
	if status.PlanSeconds != 240 || *status.RuntimeSeconds != 1200 {
		t.Fatalf("status = %#v, want both numbers published", status)
	}
}

// The "metrics" feeding the runtime side are the operator's generic UPS telemetry fields, not
// private Prometheus queries or site-local node metrics.
func TestPlanFeasibilityPublishesGenericPowerTelemetry(t *testing.T) {
	estimated := 4 * time.Minute
	status := planFeasibilityStatus(&estimated, historyPowerTelemetry(1200, 42, 73), planner.EstimateConfidence{})

	if status.ChargePercent == nil || *status.ChargePercent != 42 {
		t.Fatalf("charge = %#v, want generic UPS charge telemetry", status.ChargePercent)
	}
	if status.LoadPercent == nil || *status.LoadPercent != 73 {
		t.Fatalf("load = %#v, want generic UPS load telemetry", status.LoadPercent)
	}
}

func TestFlowRuntimeObservationAggregatesSelectedUPSDeviceTelemetry(t *testing.T) {
	runtimeA := int64(900)
	runtimeB := int64(1200)
	chargeA := int32(42)
	chargeB := int32(61)
	loadA := int32(28)
	loadB := int32(73)
	reconciler := shutdownFlowReconcilerWithUPSDevices(t,
		upsDeviceTelemetry("ups-a", powerv1alpha1.UPSDevicePhaseOnBattery, runtimeA, chargeA, loadA),
		upsDeviceTelemetry("ups-b", powerv1alpha1.UPSDevicePhaseOnline, runtimeB, chargeB, loadB),
	)

	observation := reconciler.flowRuntimeObservation(context.Background(),
		&powerv1alpha1.ShutdownTriggerEvaluationStatus{SelectedUPSDevices: []string{"ups-a", "ups-b"}},
		resolver.StructuralBundle{CapabilityMatches: []capability.MatchResult{
			dynamicRuntimeMatch("ups-a"),
			dynamicRuntimeMatch("ups-b"),
		}})

	if observation.RuntimeSeconds == nil || *observation.RuntimeSeconds != runtimeA {
		t.Fatalf("runtime = %#v, want shortest selected UPS runtime", observation.RuntimeSeconds)
	}
	if observation.ChargePercent == nil || *observation.ChargePercent != chargeA {
		t.Fatalf("charge = %#v, want lowest selected UPS charge", observation.ChargePercent)
	}
	if observation.LoadPercent == nil || *observation.LoadPercent != loadB {
		t.Fatalf("load = %#v, want highest selected UPS load", observation.LoadPercent)
	}
}

func TestFlowRuntimeObservationKeepsGenericTelemetryWhenRuntimeIsUntrusted(t *testing.T) {
	runtime := int64(1200)
	charge := int32(42)
	load := int32(73)
	reconciler := shutdownFlowReconcilerWithUPSDevices(t,
		upsDeviceTelemetry("ups-a", powerv1alpha1.UPSDevicePhaseOnBattery, runtime, charge, load),
	)

	observation := reconciler.flowRuntimeObservation(context.Background(),
		&powerv1alpha1.ShutdownTriggerEvaluationStatus{SelectedUPSDevices: []string{"ups-a"}},
		resolver.StructuralBundle{})

	if observation.RuntimeSeconds != nil {
		t.Fatalf("runtime = %#v, want absent when capability profiles do not trust it", observation.RuntimeSeconds)
	}
	if observation.ChargePercent == nil || *observation.ChargePercent != charge {
		t.Fatalf("charge = %#v, want public UPS charge telemetry still published", observation.ChargePercent)
	}
	if observation.LoadPercent == nil || *observation.LoadPercent != load {
		t.Fatalf("load = %#v, want public UPS load telemetry still published", observation.LoadPercent)
	}
}

func shutdownFlowReconcilerWithUPSDevices(t *testing.T, devices ...powerv1alpha1.UPSDevice) *ShutdownFlowReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add power API to scheme: %v", err)
	}
	objects := make([]runtime.Object, 0, len(devices))
	for i := range devices {
		objects = append(objects, &devices[i])
	}
	return &ShutdownFlowReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
}

func upsDeviceTelemetry(name string, phase powerv1alpha1.UPSDevicePhase, runtimeSeconds int64, chargePercent, loadPercent int32) powerv1alpha1.UPSDevice {
	return powerv1alpha1.UPSDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: powerv1alpha1.UPSDeviceStatus{
			Phase:                phase,
			RuntimeSeconds:       &runtimeSeconds,
			BatteryChargePercent: &chargePercent,
			LoadPercent:          &loadPercent,
		},
	}
}

func dynamicRuntimeMatch(device string) capability.MatchResult {
	return capability.MatchResult{
		DeviceID:           device,
		TelemetryVariables: []string{"battery.runtime"},
		RuntimeEstimate:    capability.RuntimeEstimateDynamic,
	}
}

// EX-33: the groups a rehearsal would improve are named, so the recommendation is actionable.
func TestThinGroupsAreNamedForRehearsal(t *testing.T) {
	estimated := time.Minute
	status := planFeasibilityStatus(&estimated, historyRuntime(600),
		planner.EstimateConfidence{ThinGroups: []string{"databases", "storage"}})

	if len(status.ThinGroups) != 2 {
		t.Fatalf("thin groups = %v, want both named", status.ThinGroups)
	}
}

func TestRehearsalHistoryIsIncludedByDefaultAndExcludable(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{}
	flow.Name = "history-flow"
	store := &fakeAuditStore{groupDurationSamples: []audit.GroupDurationSample{
		{GroupName: "applications", Observed: time.Minute, Rehearsal: true},
		{GroupName: "databases", Observed: 2 * time.Minute},
	}}

	history := resolveFlowHistory(context.Background(), store, flow, "plan-hash")
	if got := len(history.GroupDurations["applications"]); got != 1 {
		t.Fatalf("default rehearsal history samples = %d, want 1", got)
	}
	if got := len(history.GroupDurations["databases"]); got != 1 {
		t.Fatalf("real history samples = %d, want 1", got)
	}

	include := false
	flow.Spec.Rehearsal.IncludeInEstimates = &include
	history = resolveFlowHistory(context.Background(), store, flow, "plan-hash")
	if got := len(history.GroupDurations["applications"]); got != 0 {
		t.Fatalf("excluded rehearsal history samples = %d, want 0", got)
	}
	if got := len(history.GroupDurations["databases"]); got != 1 {
		t.Fatalf("real history samples after rehearsal opt-out = %d, want 1", got)
	}
}

// A plan with no estimate publishes nothing rather than a zero comparison that reads as a fit.
func TestNoEstimatePublishesNoComparison(t *testing.T) {
	if status := planFeasibilityStatus(nil, historyRuntime(600), planner.EstimateConfidence{}); status != nil {
		t.Fatalf("status = %#v, want nil when there is no estimate to compare", status)
	}
	zero := time.Duration(0)
	if status := planFeasibilityStatus(&zero, historyRuntime(600), planner.EstimateConfidence{}); status != nil {
		t.Fatalf("status = %#v, want nil for a zero estimate", status)
	}
}
