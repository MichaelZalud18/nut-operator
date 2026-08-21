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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

type fakeAuditWriter struct {
	mu sync.Mutex
	// writeErr fails every write keyed on execution_id, which is the shape F-100 had in
	// production: a SHA-256 digest going into a uuid column made all six tables reject their
	// rows with SQLSTATE 22P02 while the shutdown itself ran normally.
	writeErr                 error
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
	w.mu.Lock()
	defer w.mu.Unlock()
	w.powerEvents = append(w.powerEvents, event)
	return nil
}

func (w *fakeAuditWriter) RecordTelemetrySnapshot(_ context.Context, snapshot audit.TelemetrySnapshot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.telemetrySnapshots = append(w.telemetrySnapshots, snapshot)
	return nil
}

func (w *fakeAuditWriter) RecordCapabilityProfileMatch(_ context.Context, match audit.CapabilityProfileMatch) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.capabilityProfileMatches = append(w.capabilityProfileMatches, match)
	return nil
}

func (w *fakeAuditWriter) RecordCapabilityProfileVerification(_ context.Context, verification audit.CapabilityProfileVerification) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.capabilityVerifications = append(w.capabilityVerifications, verification)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowCompilation(_ context.Context, compilation audit.ShutdownFlowCompilation) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shutdownFlowCompilations = append(w.shutdownFlowCompilations, compilation)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowDecision(_ context.Context, decision audit.ShutdownFlowDecision) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shutdownFlowDecisions = append(w.shutdownFlowDecisions, decision)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowExecution(_ context.Context, execution audit.ShutdownFlowExecution) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return w.writeErr
	}
	w.executions = append(w.executions, execution)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowExecutionWave(_ context.Context, wave audit.ShutdownFlowExecutionWave) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return w.writeErr
	}
	w.waves = append(w.waves, wave)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowExecutionGroup(_ context.Context, group audit.ShutdownFlowExecutionGroup) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeErr != nil {
		return w.writeErr
	}
	w.groups = append(w.groups, group)
	return nil
}

func (w *fakeAuditWriter) RecordShutdownFlowActionAttempt(_ context.Context, attempt audit.ShutdownFlowActionAttempt) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.actionAttempts = append(w.actionAttempts, attempt)
	return nil
}

func (w *fakeAuditWriter) RecordNodeRelease(_ context.Context, release audit.NodeReleaseRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nodeReleases = append(w.nodeReleases, release)
	return nil
}

func (w *fakeAuditWriter) RecordNodeSignalHandoff(_ context.Context, handoff audit.NodeSignalHandoff) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nodeSignalHandoffs = append(w.nodeSignalHandoffs, handoff)
	return nil
}

func (w *fakeAuditWriter) UpsertExecutorResumeState(_ context.Context, state audit.ExecutorResumeState) error {
	w.mu.Lock()
	defer w.mu.Unlock()
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
					AgentReady:     true,
					Cleared:        true,
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

func TestExecutorRecordsWaitTierOverrun(t *testing.T) {
	writer := &fakeAuditWriter{}
	current := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)
	tier4 := int32(4)
	tier3 := int32(3)
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return current
		},
		NewID: sequenceIDs(),
		Sleep: func(context.Context, time.Duration) error {
			current = current.Add(3 * time.Minute)
			return nil
		},
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:        "execution-wait-overrun",
		ObservedAt:         current,
		ShutdownFlow:       "conserve-power",
		Mode:               ModeDryRun,
		PlanConfigHash:     "plan-hash-overrun",
		TierOverrunPolicy:  TierOverrunPolicyWait,
		SelectedUPSDevices: []string{"ups-a"},
		Waves: []Wave{
			{Index: 0, ShutdownTier: &tier4, Duration: time.Minute, Groups: []string{"tier-4"}},
			{Index: 1, ShutdownTier: &tier3, Duration: time.Minute, Groups: []string{"tier-3"}},
		},
		Groups: []Group{
			{Name: "tier-4", Action: ActionWait, WaitDuration: 3 * time.Minute},
			{Name: "tier-3", Action: ActionWait},
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted {
		t.Fatalf("expected completed execution, got %#v", result)
	}
	if len(result.TierOverruns) != 1 {
		t.Fatalf("expected one tier overrun, got %#v", result.TierOverruns)
	}
	overrun := result.TierOverruns[0]
	if overrun.Policy != TierOverrunPolicyWait || overrun.Action != TierOverrunActionWaited {
		t.Fatalf("unexpected overrun policy/action: %#v", overrun)
	}
	if overrun.ShutdownTier == nil || *overrun.ShutdownTier != tier4 {
		t.Fatalf("expected tier 4 overrun, got %#v", overrun.ShutdownTier)
	}
	if overrun.DeclaredDuration != time.Minute || overrun.EffectiveDuration != time.Minute || overrun.ActualDuration != 3*time.Minute || overrun.OverrunDuration != 2*time.Minute {
		t.Fatalf("unexpected overrun durations: %#v", overrun)
	}
	if len(writer.waves) < 2 {
		t.Fatalf("expected wave records, got %#v", writer.waves)
	}
	details, ok := writer.waves[1].Details["tierOverrun"].(map[string]any)
	if !ok {
		t.Fatalf("expected completed wave to record tierOverrun details: %#v", writer.waves[1].Details)
	}
	if details["policy"] != TierOverrunPolicyWait || details["action"] != TierOverrunActionWaited {
		t.Fatalf("unexpected overrun details: %#v", details)
	}
}

func TestExecutorPreemptsTierOverrunAndContinuesLowerTier(t *testing.T) {
	writer := &fakeAuditWriter{}
	current := time.Now().UTC().Add(25 * time.Millisecond)
	tier4 := int32(4)
	tier3 := int32(3)
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return current
		},
		NewID: sequenceIDs(),
		Sleep: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			current = current.Add(3 * time.Second)
			return ctx.Err()
		},
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:        "execution-preempt-overrun",
		ObservedAt:         current,
		ShutdownFlow:       "conserve-power",
		Mode:               ModeDryRun,
		PlanConfigHash:     "plan-hash-preempt",
		TierOverrunPolicy:  TierOverrunPolicyPreempt,
		SelectedUPSDevices: []string{"ups-a"},
		Waves: []Wave{
			{Index: 0, ShutdownTier: &tier4, Duration: time.Millisecond, Groups: []string{"tier-4"}},
			{Index: 1, ShutdownTier: &tier3, Duration: time.Millisecond, Groups: []string{"tier-3"}},
		},
		Groups: []Group{
			{Name: "tier-4", Action: ActionWait, WaitDuration: time.Hour},
			{Name: "tier-3", Action: ActionWait},
		},
	})
	if err != nil {
		t.Fatalf("preempted tier should not abort the whole flow: %v", err)
	}
	if result.Phase != PhaseCompleted || result.Groups != 2 {
		t.Fatalf("expected completed flow with both tier records, got %#v", result)
	}
	if len(result.TierOverruns) != 1 {
		t.Fatalf("expected one tier overrun, got %#v", result.TierOverruns)
	}
	overrun := result.TierOverruns[0]
	if overrun.Policy != TierOverrunPolicyPreempt || overrun.Action != TierOverrunActionPreempted {
		t.Fatalf("unexpected preempt overrun: %#v", overrun)
	}
	if overrun.OverrunDuration <= 0 {
		t.Fatalf("expected positive overrun, got %#v", overrun)
	}
	if gotPhases := wavePhases(writer.waves); fmt.Sprint(gotPhases) != "[0:Running 0:Aborted 1:Running 1:Completed]" {
		t.Fatalf("unexpected wave phases: %#v", gotPhases)
	}
	if writer.groups[0].Phase != PhaseFailed || writer.actionAttempts[0].Outcome != OutcomeBlocked {
		t.Fatalf("expected preempted wait group to record failure/block, got group=%#v attempt=%#v", writer.groups[0], writer.actionAttempts[0])
	}
}

func TestExecutorOverlapsTierOverrunAndWaitsForPreviousTier(t *testing.T) {
	writer := &fakeAuditWriter{}
	tier4 := int32(4)
	tier3 := int32(3)
	tier4Started := make(chan struct{})
	tier3Started := make(chan struct{})
	releaseTier4 := make(chan struct{})
	var tier4Once sync.Once
	var tier3Once sync.Once
	executor := Executor{
		Writer: writer,
		NewID:  sequenceIDs(),
		Sleep: func(ctx context.Context, duration time.Duration) error {
			if duration > time.Minute {
				tier4Once.Do(func() { close(tier4Started) })
				select {
				case <-releaseTier4:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			tier3Once.Do(func() { close(tier3Started) })
			return nil
		},
	}

	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		result, err := executor.Execute(context.Background(), Input{
			ExecutionID:        "execution-overlap-overrun",
			ShutdownFlow:       "conserve-power",
			Mode:               ModeDryRun,
			PlanConfigHash:     "plan-hash-overlap",
			TierOverrunPolicy:  TierOverrunPolicyOverlap,
			SelectedUPSDevices: []string{"ups-a"},
			Waves: []Wave{
				{Index: 0, ShutdownTier: &tier4, Duration: 15 * time.Millisecond, Groups: []string{"tier-4"}},
				{Index: 1, ShutdownTier: &tier3, Duration: time.Millisecond, Groups: []string{"tier-3"}},
			},
			Groups: []Group{
				{Name: "tier-4", Action: ActionWait, WaitDuration: time.Hour},
				{Name: "tier-3", Action: ActionWait, WaitDuration: time.Millisecond},
			},
		})
		done <- struct {
			result Result
			err    error
		}{result: result, err: err}
	}()

	waitForSignal(t, tier4Started, "tier 4 to start")
	waitForSignal(t, tier3Started, "tier 3 to start while tier 4 is still running")
	close(releaseTier4)

	run := waitForExecution(t, done)
	if run.err != nil {
		t.Fatalf("Execute returned error: %v", run.err)
	}
	if run.result.Phase != PhaseCompleted || run.result.Groups != 2 || run.result.ActionAttempts != 2 {
		t.Fatalf("expected completed flow with both tiers recorded, got %#v", run.result)
	}
	if len(run.result.TierOverruns) != 1 {
		t.Fatalf("expected one tier overrun, got %#v", run.result.TierOverruns)
	}
	overrun := run.result.TierOverruns[0]
	if overrun.Policy != TierOverrunPolicyOverlap || overrun.Action != TierOverrunActionOverlapped {
		t.Fatalf("unexpected overlap overrun: %#v", overrun)
	}
	details := completedWaveDetails(writer.waves, 0)
	if details == nil {
		t.Fatalf("expected completed wave 0 details, got %#v", writer.waves)
	}
	tierOverrun, ok := details["tierOverrun"].(map[string]any)
	if !ok || tierOverrun["action"] != TierOverrunActionOverlapped {
		t.Fatalf("expected overlapped overrun details, got %#v", details["tierOverrun"])
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

func TestExecutorBlocksEnforceAgentShutdownWhenNodeAgentIsUnavailable(t *testing.T) {
	writer := &fakeAuditWriter{}
	fixed := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return fixed
		},
		NewID: sequenceIDs(),
		Runner: fakeActionRunner{
			outcome: ActionOutcome{Outcome: OutcomeSucceeded},
		},
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:    "execution-c",
		ShutdownFlow:   "conserve-power",
		Mode:           ModeEnforce,
		Approved:       true,
		PlanConfigHash: "plan-hash-c",
		Waves:          []Wave{{Index: 0, Groups: []string{"node-a"}}},
		Groups: []Group{{
			Name:   "node-a",
			Action: ActionAgentShutdown,
			NodeReleases: []NodeRelease{{
				NodeName:         "node-a",
				NodePowerAgent:   "rack-a-agents",
				ReadinessReason:  "AgentPodMissing",
				ReadinessMessage: "no NodePowerAgent pod is currently observed on this selected node",
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "AgentPodMissing") {
		t.Fatalf("expected unavailable node-agent error, got %v", err)
	}
	if result.Phase != PhaseAborted || result.NodeReleases != 1 {
		t.Fatalf("expected aborted result with one node release, got %#v", result)
	}
	if len(writer.actionAttempts) != 1 {
		t.Fatalf("expected one action attempt, got %d", len(writer.actionAttempts))
	}
	attempt := writer.actionAttempts[0]
	if attempt.Outcome != OutcomeBlocked || !strings.Contains(attempt.Error, "AgentPodMissing") {
		t.Fatalf("expected blocked action attempt, got %#v", attempt)
	}
	if len(writer.nodeReleases) != 1 {
		t.Fatalf("expected one node release, got %d", len(writer.nodeReleases))
	}
	release := writer.nodeReleases[0]
	if release.Released || release.Reason != "AgentPodMissing" {
		t.Fatalf("expected unreleased AgentPodMissing record, got %#v", release)
	}
	if len(writer.nodeSignalHandoffs) != 1 {
		t.Fatalf("expected one signal handoff record, got %d", len(writer.nodeSignalHandoffs))
	}
	handoff := writer.nodeSignalHandoffs[0]
	if handoff.Accepted || handoff.Reason != "AgentPodMissing" {
		t.Fatalf("expected rejected AgentPodMissing handoff, got %#v", handoff)
	}
}

func TestExecutorBlocksEnforceAgentShutdownWhenTelemetryIsStale(t *testing.T) {
	writer := &fakeAuditWriter{}
	fixed := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return fixed
		},
		NewID: sequenceIDs(),
		Runner: fakeActionRunner{
			outcome: ActionOutcome{Outcome: OutcomeSucceeded},
		},
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:    "execution-d",
		ShutdownFlow:   "conserve-power",
		Mode:           ModeEnforce,
		Approved:       true,
		PlanConfigHash: "plan-hash-d",
		Waves:          []Wave{{Index: 0, Groups: []string{"node-a"}}},
		Groups: []Group{{
			Name:   "node-a",
			Action: ActionAgentShutdown,
			NodeReleases: []NodeRelease{{
				NodeName:             "node-a",
				NodePowerAgent:       "rack-a-agents",
				AgentReady:           true,
				TelemetryFresh:       false,
				TelemetryStaleReason: "AgentTelemetryStale",
			}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "AgentTelemetryStale") {
		t.Fatalf("expected stale-telemetry error, got %v", err)
	}
	if result.Phase != PhaseAborted || result.NodeReleases != 1 {
		t.Fatalf("expected aborted result with one node release, got %#v", result)
	}
	release := writer.nodeReleases[0]
	if release.Released || release.Reason != "AgentTelemetryStale" {
		t.Fatalf("expected unreleased AgentTelemetryStale record, got %#v", release)
	}
	handoff := writer.nodeSignalHandoffs[0]
	if handoff.Accepted || handoff.Reason != "AgentTelemetryStale" {
		t.Fatalf("expected rejected AgentTelemetryStale handoff, got %#v", handoff)
	}
}

func TestExecutorReleasesEnforceAgentShutdownWhenReadyAndFresh(t *testing.T) {
	writer := &fakeAuditWriter{}
	fixed := time.Date(2026, 8, 2, 15, 0, 0, 0, time.UTC)
	runner := fakeActionRunner{outcome: ActionOutcome{Outcome: OutcomeSucceeded}}
	executor := Executor{
		Writer: writer,
		Clock: func() time.Time {
			return fixed
		},
		NewID:  sequenceIDs(),
		Runner: runner,
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:    "execution-e",
		ShutdownFlow:   "conserve-power",
		Mode:           ModeEnforce,
		Approved:       true,
		PlanConfigHash: "plan-hash-e",
		Waves:          []Wave{{Index: 0, Groups: []string{"node-a"}}},
		Groups: []Group{{
			Name:   "node-a",
			Action: ActionAgentShutdown,
			NodeReleases: []NodeRelease{{
				NodeName:       "node-a",
				NodePowerAgent: "rack-a-agents",
				AgentReady:     true,
				Cleared:        true,
				TelemetryFresh: true,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted || result.DryRun {
		t.Fatalf("expected completed enforce result, got %#v", result)
	}
	if len(writer.actionAttempts) != 1 || writer.actionAttempts[0].Outcome != OutcomeSucceeded {
		t.Fatalf("expected one succeeded action attempt, got %#v", writer.actionAttempts)
	}
	release := writer.nodeReleases[0]
	if !release.Released || release.Reason != "ReleaseApproved" {
		t.Fatalf("expected released ReleaseApproved record, got %#v", release)
	}
	handoff := writer.nodeSignalHandoffs[0]
	if !handoff.Accepted || handoff.Reason != "SignalAccepted" {
		t.Fatalf("expected accepted SignalAccepted handoff, got %#v", handoff)
	}
}

type fakeActionRunner struct {
	outcome ActionOutcome
}

func (r fakeActionRunner) RunAction(context.Context, Action) (ActionOutcome, error) {
	return r.outcome, nil
}

func sequenceIDs() func() string {
	var id int
	var mu sync.Mutex
	return func() string {
		mu.Lock()
		defer mu.Unlock()
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

func completedWaveDetails(waves []audit.ShutdownFlowExecutionWave, index int32) map[string]any {
	for _, wave := range waves {
		if wave.WaveIndex == index && wave.Phase == PhaseCompleted {
			return wave.Details
		}
	}
	return nil
}

func waitForSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForExecution(t *testing.T, ch <-chan struct {
	result Result
	err    error
}) struct {
	result Result
	err    error
} {
	t.Helper()
	select {
	case run := <-ch:
		return run
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for execution to finish")
		return struct {
			result Result
			err    error
		}{}
	}
}

func resumePhases(states []audit.ExecutorResumeState) []string {
	phases := make([]string, 0, len(states))
	for _, state := range states {
		phases = append(phases, state.Phase)
	}
	return phases
}

// F-100: a run that traverses every wave must not report as failed because the database rejected
// its evidence. The audit outage was indistinguishable from a shutdown failure, so the only symptom
// of a broken audit trail pointed the reader at the wrong system.
func TestExecutorSeparatesAuditFailureFromExecutionOutcome(t *testing.T) {
	writeErr := errors.New(`pq: invalid input syntax for type uuid (SQLSTATE 22P02)`)
	writer := &fakeAuditWriter{writeErr: writeErr}
	fixed := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	executor := Executor{
		Writer: writer,
		Clock:  func() time.Time { return fixed },
		NewID:  sequenceIDs(),
	}

	result, err := executor.Execute(context.Background(), Input{
		ExecutionID:        "00000000-0000-5000-8000-00000000000a",
		DeduplicationKey:   "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		ObservedAt:         fixed,
		ShutdownFlow:       "conserve-power",
		Mode:               ModeDryRun,
		PlanConfigHash:     "plan-hash-a",
		InputHash:          "input-hash-a",
		SelectedUPSDevices: []string{"ups-a"},
		Waves:              []Wave{{Index: 0, Groups: []string{"applications"}}},
		Groups: []Group{{
			Name:            "applications",
			Action:          "ScaleWorkload",
			SelectedTargets: []Target{{Kind: "Deployment", Namespace: "apps", Name: "web"}},
		}},
	})

	// The execution did what it was asked to do, so Execute reports no execution error.
	if err != nil {
		t.Fatalf("an audit-write failure was returned as an execution failure: %v", err)
	}
	if result.Phase != PhaseCompleted {
		t.Errorf("phase = %q, want Completed -- the waves all ran", result.Phase)
	}
	if result.Groups != 1 || result.ActionAttempts != 1 {
		t.Errorf("the run did not complete: %#v", result)
	}

	// And the outage is still reported, on its own channel. Losing it entirely would be worse than
	// mislabelling it: a shutdown whose evidence was never written must not look clean.
	if result.RecordError == nil {
		t.Fatal("every audit write failed and RecordError is nil")
	}
	if !errors.Is(result.RecordError, writeErr) {
		t.Errorf("RecordError does not carry the write failure: %v", result.RecordError)
	}
}

// The dedup key rides along to the audit row so a record can name the trigger episode it belongs
// to, now that the execution ID is derived from that digest rather than being it.
func TestExecutorStampsTheDeduplicationKeyOnExecutionRecords(t *testing.T) {
	const digest = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	writer := &fakeAuditWriter{}
	fixed := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	executor := Executor{
		Writer: writer,
		Clock:  func() time.Time { return fixed },
		NewID:  sequenceIDs(),
	}

	_, err := executor.Execute(context.Background(), Input{
		ExecutionID:        "00000000-0000-5000-8000-00000000000a",
		DeduplicationKey:   digest,
		ObservedAt:         fixed,
		ShutdownFlow:       "conserve-power",
		Mode:               ModeDryRun,
		PlanConfigHash:     "plan-hash-a",
		InputHash:          "input-hash-a",
		SelectedUPSDevices: []string{"ups-a"},
		Waves:              []Wave{{Index: 0, Groups: []string{"applications"}}},
		Groups: []Group{{
			Name:            "applications",
			Action:          "ScaleWorkload",
			SelectedTargets: []Target{{Kind: "Deployment", Namespace: "apps", Name: "web"}},
		}},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if len(writer.executions) == 0 {
		t.Fatal("no execution records were written")
	}
	for i, execution := range writer.executions {
		if execution.DeduplicationKey != digest {
			t.Errorf("execution record %d carries deduplication key %q, want the episode digest", i, execution.DeduplicationKey)
		}
	}
}
