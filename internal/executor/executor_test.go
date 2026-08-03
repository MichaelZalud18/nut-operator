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

package executor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

type fakeAuditWriter struct {
	powerEvents              []audit.PowerEvent
	telemetrySnapshots       []audit.TelemetrySnapshot
	capabilityProfileMatches []audit.CapabilityProfileMatch
	capabilityVerifications  []audit.CapabilityProfileVerification
	shutdownFlowCompilations []audit.ShutdownFlowCompilation
	shutdownFlowDecisions    []audit.ShutdownFlowDecision
	executions               []audit.ShutdownFlowExecution
	waves                    []audit.ShutdownFlowExecutionWave
	groups                   []audit.ShutdownFlowExecutionGroup
	actionAttempts           []audit.ShutdownFlowActionAttempt
	nodeReleases             []audit.NodeReleaseRecord
	nodeSignalHandoffs       []audit.NodeSignalHandoff
	resumeStates             []audit.ExecutorResumeState
}

func (w *fakeAuditWriter) RecordPowerEvent(_ context.Context, event audit.PowerEvent) error {
	w.powerEvents = append(w.powerEvents, event)
	return nil
}

func (w *fakeAuditWriter) RecordTelemetrySnapshot(_ context.Context, snapshot audit.TelemetrySnapshot) error {
	w.telemetrySnapshots = append(w.telemetrySnapshots, snapshot)
	return nil
}

func (w *fakeAuditWriter) RecordCapabilityProfileMatch(_ context.Context, match audit.CapabilityProfileMatch) error {
	w.capabilityProfileMatches = append(w.capabilityProfileMatches, match)
	return nil
}

func (w *fakeAuditWriter) RecordCapabilityProfileVerification(_ context.Context, verification audit.CapabilityProfileVerification) error {
	w.capabilityVerifications = append(w.capabilityVerifications, verification)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowCompilation(_ context.Context, compilation audit.ShutdownFlowCompilation) error {
	w.shutdownFlowCompilations = append(w.shutdownFlowCompilations, compilation)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowDecision(_ context.Context, decision audit.ShutdownFlowDecision) error {
	w.shutdownFlowDecisions = append(w.shutdownFlowDecisions, decision)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowExecution(_ context.Context, execution audit.ShutdownFlowExecution) error {
	w.executions = append(w.executions, execution)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowExecutionWave(_ context.Context, wave audit.ShutdownFlowExecutionWave) error {
	w.waves = append(w.waves, wave)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowExecutionGroup(_ context.Context, group audit.ShutdownFlowExecutionGroup) error {
	w.groups = append(w.groups, group)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowActionAttempt(_ context.Context, attempt audit.ShutdownFlowActionAttempt) error {
	w.actionAttempts = append(w.actionAttempts, attempt)
	return nil
}

func (w *fakeAuditWriter) RecordNodeRelease(_ context.Context, release audit.NodeReleaseRecord) error {
	w.nodeReleases = append(w.nodeReleases, release)
	return nil
}

func (w *fakeAuditWriter) RecordNodeSignalHandoff(_ context.Context, handoff audit.NodeSignalHandoff) error {
	w.nodeSignalHandoffs = append(w.nodeSignalHandoffs, handoff)
	return nil
}

func (w *fakeAuditWriter) UpsertExecutorResumeState(_ context.Context, state audit.ExecutorResumeState) error {
	w.resumeStates = append(w.resumeStates, state)
	return nil
}

func TestExecutorRecordsOrderedDryRunEvidence(t *testing.T) {
	writer := &fakeAuditWriter{}
	fixed := time.Date(2026, 8, 2, 13, 0, 0, 0, time.UTC)
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return fixed
		},
		NewID: sequenceIDs(),
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:        "execution-a",
		ObservedAt:         fixed,
		ShutdownFlow:       "conserve-power",
		TriggerDecisionID:  "00000000-0000-4000-8000-000000000001",
		Mode:               ModeDryRun,
		Reason:             "TriggerEligible",
		PlanConfigHash:     "plan-hash-a",
		InputHash:          "input-hash-a",
		SelectedUPSDevices: []string{"ups-a"},
		SignalTTL:          3 * time.Minute,
		Waves: []Wave{
			{Index: 0, Groups: []string{"applications"}},
			{Index: 1, Groups: []string{"node-a"}},
		},
		Groups: []Group{
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				SelectedTargets: []Target{{
					Kind:      "Deployment",
					Namespace: "apps",
					Name:      "web",
				}},
			},
			{
				Name:   "node-a",
				Action: ActionAgentShutdown,
				SelectedTargets: []Target{{
					Kind: "Node",
					Name: "node-a",
				}},
				NodeReleases: []NodeRelease{{
					NodeName:       "node-a",
					NodePowerAgent: "rack-a-agents",
					SignalPath:     "/run/power-agent/shutdown.json",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted || !result.DryRun || result.Waves != 2 || result.Groups != 2 || result.ActionAttempts != 2 || result.NodeReleases != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if gotPhases := executionPhases(writer.executions); fmt.Sprint(gotPhases) != "[Running Completed]" {
		t.Fatalf("unexpected execution phases: %#v", gotPhases)
	}
	if gotPhases := wavePhases(writer.waves); fmt.Sprint(gotPhases) != "[0:Running 0:Completed 1:Running 1:Completed]" {
		t.Fatalf("unexpected wave phases: %#v", gotPhases)
	}
	if len(writer.groups) != 2 {
		t.Fatalf("expected two group records, got %d", len(writer.groups))
	}
	if writer.groups[0].GroupName != "applications" || writer.groups[1].GroupName != "node-a" {
		t.Fatalf("groups were not recorded in compiled wave order: %#v", writer.groups)
	}
	if len(writer.actionAttempts) != 2 {
		t.Fatalf("expected two action attempts, got %d", len(writer.actionAttempts))
	}
	for _, attempt := range writer.actionAttempts {
		if attempt.Outcome != OutcomeSimulated || !attempt.DryRun {
			t.Fatalf("expected simulated dry-run attempt, got %#v", attempt)
		}
	}
	if len(writer.nodeReleases) != 1 {
		t.Fatalf("expected one node release record, got %d", len(writer.nodeReleases))
	}
	if writer.nodeReleases[0].Released {
		t.Fatalf("dry-run node release must not mark released: %#v", writer.nodeReleases[0])
	}
	if len(writer.nodeSignalHandoffs) != 1 {
		t.Fatalf("expected one node signal handoff, got %d", len(writer.nodeSignalHandoffs))
	}
	handoff := writer.nodeSignalHandoffs[0]
	if handoff.Accepted {
		t.Fatalf("dry-run signal handoff must not mark accepted: %#v", handoff)
	}
	if handoff.StaleAfter == nil || !handoff.StaleAfter.Equal(fixed.Add(3*time.Minute)) {
		t.Fatalf("unexpected signal stale time: %#v", handoff.StaleAfter)
	}
	if handoff.SignalPayload["planConfigHash"] != "plan-hash-a" || handoff.SignalPayload["shutdownFlow"] != "conserve-power" {
		t.Fatalf("unexpected signal payload: %#v", handoff.SignalPayload)
	}
	if gotPhases := resumePhases(writer.resumeStates); fmt.Sprint(gotPhases) != "[Running Running Completed]" {
		t.Fatalf("unexpected resume phases: %#v", gotPhases)
	}
}

func TestExecutorRejectsUnknownWaveGroup(t *testing.T) {
	executor := Executor{Writer: &fakeAuditWriter{}, NewID: sequenceIDs()}
	_, err := executor.Execute(context.Background(), Input{
		ShutdownFlow:   "conserve-power",
		PlanConfigHash: "plan-hash-a",
		Waves:          []Wave{{Index: 0, Groups: []string{"missing"}}},
		Groups: []Group{{
			Name:   "applications",
			Action: "ScaleWorkload",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown group "missing"`) {
		t.Fatalf("expected unknown group error, got %v", err)
	}
}

func TestExecutorBlocksEnforceWithoutActionRunner(t *testing.T) {
	writer := &fakeAuditWriter{}
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return time.Date(2026, 8, 2, 14, 0, 0, 0, time.UTC)
		},
		NewID: sequenceIDs(),
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:    "execution-b",
		ShutdownFlow:   "conserve-power",
		Mode:           ModeEnforce,
		Approved:       true,
		PlanConfigHash: "plan-hash-b",
		Waves:          []Wave{{Index: 0, Groups: []string{"applications"}}},
		Groups: []Group{{
			Name:   "applications",
			Action: "ScaleWorkload",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "requires an action runner") {
		t.Fatalf("expected action runner error, got %v", err)
	}
	if result.Phase != PhaseAborted || result.DryRun {
		t.Fatalf("expected aborted non-dry-run result, got %#v", result)
	}
	if len(writer.actionAttempts) != 1 {
		t.Fatalf("expected one action attempt, got %d", len(writer.actionAttempts))
	}
	attempt := writer.actionAttempts[0]
	if attempt.Outcome != OutcomeBlocked || attempt.DryRun {
		t.Fatalf("expected blocked enforce attempt, got %#v", attempt)
	}
	if gotPhases := executionPhases(writer.executions); fmt.Sprint(gotPhases) != "[Running Aborted]" {
		t.Fatalf("unexpected execution phases: %#v", gotPhases)
	}
}

func sequenceIDs() func() string {
	var id int
	return func() string {
		id++
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", id)
	}
}

func executionPhases(executions []audit.ShutdownFlowExecution) []string {
	phases := make([]string, 0, len(executions))
	for _, execution := range executions {
		phases = append(phases, execution.Phase)
	}
	return phases
}

func wavePhases(waves []audit.ShutdownFlowExecutionWave) []string {
	phases := make([]string, 0, len(waves))
	for _, wave := range waves {
		phases = append(phases, fmt.Sprintf("%d:%s", wave.WaveIndex, wave.Phase))
	}
	return phases
}

func resumePhases(states []audit.ExecutorResumeState) []string {
	phases := make([]string, 0, len(states))
	for _, state := range states {
		phases = append(phases, state.Phase)
	}
	return phases
}
