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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestBoolToFloat(t *testing.T) {
	if got := BoolToFloat(true); got != 1 {
		t.Fatalf("BoolToFloat(true) = %v, want 1", got)
	}
	if got := BoolToFloat(false); got != 0 {
		t.Fatalf("BoolToFloat(false) = %v, want 0", got)
	}
}

func TestShutdownFlowCompileTotalCountsByResult(t *testing.T) {
	before := testutil.ToFloat64(ShutdownFlowCompileTotal.WithLabelValues("test-metrics-flow", "Accepted"))
	ShutdownFlowCompileTotal.WithLabelValues("test-metrics-flow", "Accepted").Inc()
	after := testutil.ToFloat64(ShutdownFlowCompileTotal.WithLabelValues("test-metrics-flow", "Accepted"))
	if after != before+1 {
		t.Fatalf("ShutdownFlowCompileTotal did not increment: before=%v after=%v", before, after)
	}
}

func TestShutdownFlowCompileDurationSecondsRecordsObservations(t *testing.T) {
	before := testutil.CollectAndCount(ShutdownFlowCompileDurationSeconds)
	ShutdownFlowCompileDurationSeconds.WithLabelValues("test-metrics-flow-duration").Observe(0.5)
	after := testutil.CollectAndCount(ShutdownFlowCompileDurationSeconds)
	if after != before+1 {
		t.Fatalf("ShutdownFlowCompileDurationSeconds series count did not grow: before=%d after=%d", before, after)
	}
}

func TestShutdownFlowPlanHashChangesTotalCounts(t *testing.T) {
	before := testutil.ToFloat64(ShutdownFlowPlanHashChangesTotal.WithLabelValues("test-metrics-flow"))
	ShutdownFlowPlanHashChangesTotal.WithLabelValues("test-metrics-flow").Inc()
	after := testutil.ToFloat64(ShutdownFlowPlanHashChangesTotal.WithLabelValues("test-metrics-flow"))
	if after != before+1 {
		t.Fatalf("ShutdownFlowPlanHashChangesTotal did not increment: before=%v after=%v", before, after)
	}
}

func TestShutdownFlowTriggerEvaluationsTotalCountsByEligibility(t *testing.T) {
	before := testutil.ToFloat64(ShutdownFlowTriggerEvaluationsTotal.WithLabelValues("test-metrics-flow", "true"))
	ShutdownFlowTriggerEvaluationsTotal.WithLabelValues("test-metrics-flow", "true").Inc()
	after := testutil.ToFloat64(ShutdownFlowTriggerEvaluationsTotal.WithLabelValues("test-metrics-flow", "true"))
	if after != before+1 {
		t.Fatalf("ShutdownFlowTriggerEvaluationsTotal did not increment: before=%v after=%v", before, after)
	}
}

func TestShutdownFlowDegradedReflectsLatestSet(t *testing.T) {
	ShutdownFlowDegraded.WithLabelValues("test-metrics-flow-degraded").Set(BoolToFloat(true))
	if got := testutil.ToFloat64(ShutdownFlowDegraded.WithLabelValues("test-metrics-flow-degraded")); got != 1 {
		t.Fatalf("ShutdownFlowDegraded = %v, want 1 after Set(true)", got)
	}
	ShutdownFlowDegraded.WithLabelValues("test-metrics-flow-degraded").Set(BoolToFloat(false))
	if got := testutil.ToFloat64(ShutdownFlowDegraded.WithLabelValues("test-metrics-flow-degraded")); got != 0 {
		t.Fatalf("ShutdownFlowDegraded = %v, want 0 after Set(false)", got)
	}
}

func TestShutdownFlowExecutionDurationSecondsRecordsObservations(t *testing.T) {
	before := testutil.CollectAndCount(ShutdownFlowExecutionDurationSeconds)
	ShutdownFlowExecutionDurationSeconds.WithLabelValues("test-metrics-flow", "DryRun").Observe(1.2)
	after := testutil.CollectAndCount(ShutdownFlowExecutionDurationSeconds)
	if after != before+1 {
		t.Fatalf("ShutdownFlowExecutionDurationSeconds series count did not grow: before=%d after=%d", before, after)
	}
}

func TestShutdownFlowTierOverrunMetricsRecordOccurrencesAndSeconds(t *testing.T) {
	before := testutil.ToFloat64(ShutdownFlowTierOverrunsTotal.WithLabelValues("test-metrics-flow-overrun", "4", "Wait", "Waited"))
	ShutdownFlowTierOverrunsTotal.WithLabelValues("test-metrics-flow-overrun", "4", "Wait", "Waited").Inc()
	ShutdownFlowTierOverrunSeconds.WithLabelValues("test-metrics-flow-overrun", "4", "Wait", "Waited").Observe(2.5)
	after := testutil.ToFloat64(ShutdownFlowTierOverrunsTotal.WithLabelValues("test-metrics-flow-overrun", "4", "Wait", "Waited"))
	if after != before+1 {
		t.Fatalf("ShutdownFlowTierOverrunsTotal did not increment: before=%v after=%v", before, after)
	}
}

func TestActuatorActionAttemptsTotalCountsByLabelSet(t *testing.T) {
	before := testutil.ToFloat64(ActuatorActionAttemptsTotal.WithLabelValues("ScaleWorkload", "DryRun", "Simulated"))
	ActuatorActionAttemptsTotal.WithLabelValues("ScaleWorkload", "DryRun", "Simulated").Inc()
	after := testutil.ToFloat64(ActuatorActionAttemptsTotal.WithLabelValues("ScaleWorkload", "DryRun", "Simulated"))
	if after != before+1 {
		t.Fatalf("ActuatorActionAttemptsTotal did not increment: before=%v after=%v", before, after)
	}
}

func TestActuatorActionDurationSecondsRecordsObservations(t *testing.T) {
	before := testutil.CollectAndCount(ActuatorActionDurationSeconds)
	ActuatorActionDurationSeconds.WithLabelValues("DrainNodes").Observe(3.4)
	after := testutil.CollectAndCount(ActuatorActionDurationSeconds)
	if after != before+1 {
		t.Fatalf("ActuatorActionDurationSeconds series count did not grow: before=%d after=%d", before, after)
	}
}

func TestUPSDeviceTelemetryPollMetricsRecordAttempts(t *testing.T) {
	before := testutil.ToFloat64(UPSDeviceTelemetryPollsTotal.WithLabelValues("test-ups", "Succeeded"))
	UPSDeviceTelemetryPollsTotal.WithLabelValues("test-ups", "Succeeded").Inc()
	UPSDeviceTelemetryPollDurationSeconds.WithLabelValues("test-ups", "Succeeded").Observe(0.2)
	UPSDeviceTelemetryLastSuccessTimestampSeconds.WithLabelValues("test-ups").Set(1234)
	after := testutil.ToFloat64(UPSDeviceTelemetryPollsTotal.WithLabelValues("test-ups", "Succeeded"))
	if after != before+1 {
		t.Fatalf("UPSDeviceTelemetryPollsTotal did not increment: before=%v after=%v", before, after)
	}
	if got := testutil.ToFloat64(UPSDeviceTelemetryLastSuccessTimestampSeconds.WithLabelValues("test-ups")); got != 1234 {
		t.Fatalf("UPSDeviceTelemetryLastSuccessTimestampSeconds = %v, want 1234", got)
	}
}

func TestCapabilityMatchTotalCountsByMatchResult(t *testing.T) {
	before := testutil.ToFloat64(CapabilityMatchTotal.WithLabelValues("Matched", "ExactModel", "false"))
	CapabilityMatchTotal.WithLabelValues("Matched", "ExactModel", "false").Inc()
	after := testutil.ToFloat64(CapabilityMatchTotal.WithLabelValues("Matched", "ExactModel", "false"))
	if after != before+1 {
		t.Fatalf("CapabilityMatchTotal did not increment: before=%v after=%v", before, after)
	}
}

func TestInventoryMetricsPublishCurrentCompilerCounts(t *testing.T) {
	before := testutil.ToFloat64(InventoryCompileTotal.WithLabelValues("Accepted"))
	InventoryCompileTotal.WithLabelValues("Accepted").Inc()
	InventoryEntities.WithLabelValues("Node").Set(3)
	InventoryEdges.WithLabelValues("feeds").Set(4)
	InventoryPowerDomains.Set(2)
	InventoryOrphanNodes.Set(1)
	InventoryCommunicationPathUnmodeledNodes.Set(5)
	after := testutil.ToFloat64(InventoryCompileTotal.WithLabelValues("Accepted"))
	if after != before+1 {
		t.Fatalf("InventoryCompileTotal did not increment: before=%v after=%v", before, after)
	}
	for name, got := range map[string]float64{
		"entities":                           testutil.ToFloat64(InventoryEntities.WithLabelValues("Node")),
		"edges":                              testutil.ToFloat64(InventoryEdges.WithLabelValues("feeds")),
		"power_domains":                      testutil.ToFloat64(InventoryPowerDomains),
		"orphan_nodes":                       testutil.ToFloat64(InventoryOrphanNodes),
		"communication_path_unmodeled_nodes": testutil.ToFloat64(InventoryCommunicationPathUnmodeledNodes),
	} {
		if got <= 0 {
			t.Fatalf("%s metric was not set, got %v", name, got)
		}
	}
}
