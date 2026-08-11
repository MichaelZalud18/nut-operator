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

// Package executor records deterministic shutdown execution progress for a
// compiled plan. Kubernetes mutation and host actuation are injected at the
// action-runner boundary; this package owns ordering and evidence.
package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

const (
	ModeDryRun  = "DryRun"
	ModeEnforce = "Enforce"

	PhaseRunning   = "Running"
	PhaseCompleted = "Completed"
	PhaseAborted   = "Aborted"
	PhaseFailed    = "Failed"
	// PhaseSuspended is a flow that stopped descending because power came back.
	//
	// Deliberately not "halted". EX-30 reserves halt for abort: a deliberate stop that
	// latches and never resumes. Power recovery is the opposite kind of event -- the
	// pointer ascends, nothing is restored, and if power degrades again the flow
	// descends from wherever the pointer sits, re-attempting already-executed tiers
	// as no-ops (EX-26). A suspended run has more to do; a halted one does not.
	//
	// Also distinct from Completed, which means every wave ran, and from Aborted,
	// which means something failed.
	PhaseSuspended = "Suspended"

	OutcomeSimulated = "Simulated"
	OutcomeSucceeded = "Succeeded"
	OutcomeBlocked   = "Blocked"
	OutcomeTimedOut  = "TimedOut"

	ActionAgentShutdown = "AgentShutdown"
	// ActionWait holds the flow for a declared duration. Handled here rather than in
	// the Kubernetes action runner because waiting is not a cluster mutation, and
	// because its duration is exactly the class of value timing adaptation scales.
	ActionWait = "Wait"

	DefaultSignalPath = "/run/power-agent/shutdown.json"
	DefaultSignalTTL  = 2 * time.Minute
)

// Executor writes the ordered execution evidence for one compiled shutdown run.
type Executor struct {
	Writer audit.Writer
	Runner ActionRunner
	Clock  func() time.Time
	NewID  func() string

	// Observer reads live power state at each wave boundary, driving the tier
	// pointer and the timing mode. Nil means the flow runs against
	// Input.Adaptive.Observation for its whole duration.
	Observer PowerObserver

	// Sleep implements the Wait action. Nil uses a context-aware timer.
	Sleep Sleeper
}

// Input is the executor's pure data contract. It contains already-compiled
// waves plus the action metadata needed to produce execution evidence.
type Input struct {
	ExecutionID        string
	ObservedAt         time.Time
	ShutdownFlow       string
	TriggerDecisionID  string
	Mode               string
	Reason             string
	PlanConfigHash     string
	InputHash          string
	Approved           bool
	DryRun             bool
	SelectedUPSDevices []string
	SignalTTL          time.Duration
	Waves              []Wave
	Groups             []Group

	// Adaptive carries the tier pointer and timing mode across the boundary. The
	// zero value runs a flow with default parameters from a fresh pointer.
	Adaptive AdaptiveInput
}

// Wave is one ordered unit from the compiled plan.
type Wave struct {
	Index int32
	// ShutdownTier is the tier this wave belongs to, when tier policy assigned the
	// wave's groups a shared one. Nil for an untiered flow, which is legitimate:
	// tiers are optional.
	ShutdownTier *int32

	// Duration is the wave's compiled expected duration. Summed across the waves not
	// yet run, it is the "how much plan is left" half of the compression ratio.
	Duration time.Duration

	Groups []string
}

// Group describes the action executed for a compiled group name.
type Group struct {
	Name            string
	Action          string
	Params          map[string]string
	SelectedTargets []Target
	NodeReleases    []NodeRelease
	Details         map[string]any

	// Timeout is the group's declared limit (EX-11). Zero means no limit. The
	// declared value is the most the flow will allow; the measured timing budget may
	// shorten it but never lengthens it past what the flow author wrote.
	Timeout time.Duration

	// WaitDuration is how long a Wait group holds the flow. Scaled by the timing
	// mode on the same terms as Timeout.
	WaitDuration time.Duration
}

// Target is an execution-time concrete object selected for an action.
type Target struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// NodeRelease describes a terminal node-agent handoff candidate.
type NodeRelease struct {
	NodeName              string
	NodePowerAgent        string
	SignalPath            string
	SignalSecretKey       string
	SignalSecretName      string
	SignalSecretNamespace string
	AgentReady            bool
	ReadinessReason       string
	ReadinessMessage      string
	PodName               string
	LastHeartbeatTime     *time.Time

	// TelemetryFresh reflects the originating NodePowerAgent's spec.shutdown.requireFreshTelemetry
	// gate (default true): whether every UPS device that agent monitors currently reports non-stale
	// telemetry. True when the agent doesn't require fresh telemetry at all. Computed by the caller
	// (internal/controller), not the executor, since only the caller has Kubernetes read access.
	TelemetryFresh       bool
	TelemetryStaleReason string

	// Cleared is the execution-time node-clearance verdict (EX-9, PL-43). Compile-time
	// clearance edges are the plan; this is the proof, re-derived against the pods
	// actually on the node when the wave runs, because placement moves between
	// compile and execution (OD-11).
	//
	// Computed by the caller for the same reason as TelemetryFresh: it needs to read
	// the cluster, and this package does no I/O.
	Cleared bool
	// ClearanceReason names why the node is not clear.
	ClearanceReason string
	// BlockingWorkloads names the pods still running on the node, so the record says
	// what to go look at rather than only that something was there.
	BlockingWorkloads []string
}

// Action is passed to an injected action runner for non-dry-run execution.
type Action struct {
	ExecutionID        string
	ShutdownFlow       string
	PlanConfigHash     string
	SelectedUPSDevices []string
	WaveIndex          int32
	Group              Group
	DryRun             bool
}

// ActionOutcome summarizes one action-runner result.
type ActionOutcome struct {
	Outcome string
	Error   string
	Details map[string]any
}

// ActionRunner performs effectful Kubernetes work for enforce mode.
type ActionRunner interface {
	RunAction(ctx context.Context, action Action) (ActionOutcome, error)
}

// Result summarizes the evidence emitted by one execution attempt.
type Result struct {
	ExecutionID    string
	Phase          string
	DryRun         bool
	Waves          int
	Groups         int
	ActionAttempts int
	NodeReleases   int

	// Adaptive is the pointer and timing state the run ended on, for the caller to
	// persist and publish.
	Adaptive AdaptiveResult
}

// Execute records the execution in compiled wave order.
func (e Executor) Execute(ctx context.Context, input Input) (Result, error) {
	if input.ShutdownFlow == "" || input.PlanConfigHash == "" {
		return Result{}, fmt.Errorf("executor input requires shutdown flow and plan config hash")
	}
	if len(input.Waves) == 0 {
		return Result{}, fmt.Errorf("executor input requires at least one compiled wave")
	}
	groups, err := indexGroups(input.Groups)
	if err != nil {
		return Result{}, err
	}
	for _, wave := range input.Waves {
		if len(wave.Groups) == 0 {
			return Result{}, fmt.Errorf("compiled wave %d requires at least one group", wave.Index)
		}
		for _, groupName := range wave.Groups {
			if _, ok := groups[groupName]; !ok {
				return Result{}, fmt.Errorf("compiled wave %d references unknown group %q", wave.Index, groupName)
			}
		}
	}

	writer := e.writer()
	executionID := input.ExecutionID
	if executionID == "" {
		executionID = e.newID()
	}
	observedAt := input.ObservedAt
	if observedAt.IsZero() {
		observedAt = e.now()
	}
	mode := input.Mode
	if mode == "" {
		mode = ModeDryRun
	}
	dryRun := effectiveDryRun(input)
	reason := input.Reason
	if reason == "" {
		reason = "TriggerEligible"
	}
	startedAt := observedAt
	result := Result{
		ExecutionID: executionID,
		Phase:       PhaseCompleted,
		DryRun:      dryRun,
		Waves:       len(input.Waves),
	}

	var recordErr error
	recordErr = errors.Join(recordErr, writer.RecordShutdownFlowExecution(ctx, audit.ShutdownFlowExecution{
		ExecutionID:       executionID,
		ObservedAt:        observedAt,
		ShutdownFlow:      input.ShutdownFlow,
		TriggerDecisionID: input.TriggerDecisionID,
		Mode:              mode,
		Phase:             PhaseRunning,
		Reason:            reason,
		PlanConfigHash:    input.PlanConfigHash,
		InputHash:         input.InputHash,
		StartedAt:         &startedAt,
		DryRun:            dryRun,
		Approved:          input.Approved,
		ApprovalEvidence: map[string]any{
			"approved":        input.Approved,
			"requestedMode":   mode,
			"effectiveDryRun": dryRun,
		},
		Revalidation: map[string]any{
			"inputHash": input.InputHash,
		},
		Details: map[string]any{
			"selectedUPSDevices": append([]string(nil), input.SelectedUPSDevices...),
			"waveCount":          len(input.Waves),
		},
	}))

	adaptiveInput := input.Adaptive
	for waveIndex, wave := range input.Waves {
		// Evaluated at wave boundaries only, never inside one: adaptation may change
		// timings but never wave order or membership, which are hashed into plan
		// identity (PL-14). The compression is measured against the plan still to run,
		// so it tightens as the flow proceeds and the runtime drains.
		waveState, adaptiveErr := e.evaluateWave(ctx, adaptiveInput, wave, remainingPlanDuration(input.Waves, waveIndex))
		if adaptiveErr != nil {
			result.Phase = PhaseFailed
			return result, errors.Join(adaptiveErr, recordErr)
		}
		adaptiveInput.Pointer = waveState.Pointer
		adaptiveInput.Timing = waveState.Timing
		adaptiveInput.Observation = waveState.Observation
		result.Adaptive = adaptiveResultFrom(result.Adaptive, waveState)

		if waveState.Suspend {
			// Power came back. Stop descending, restore nothing, and leave the pointer where
			// it is so a later degrade resumes from there (EX-25, EX-27).
			result.Phase = PhaseSuspended
			result.Adaptive.Suspended = true
			recordErr = errors.Join(recordErr, e.recordSuspension(ctx, writer, input, executionID, mode, dryRun, reason, startedAt, waveState, result))
			return result, recordErr
		}

		waveStart := e.now()
		waveRecordID := e.newID()
		currentWave := wave.Index
		recordErr = errors.Join(recordErr, writer.UpsertExecutorResumeState(ctx, audit.ExecutorResumeState{
			ExecutionID:      executionID,
			ObservedAt:       waveStart,
			ShutdownFlow:     input.ShutdownFlow,
			PlanConfigHash:   input.PlanConfigHash,
			CurrentWaveIndex: &currentWave,
			Phase:            PhaseRunning,
			State: mergeDetails(adaptiveStateRecord(waveState), map[string]any{
				"currentWaveIndex": wave.Index,
				"groups":           append([]string(nil), wave.Groups...),
			}),
		}))
		recordErr = errors.Join(recordErr, writer.RecordShutdownFlowExecutionWave(ctx, audit.ShutdownFlowExecutionWave{
			WaveRecordID: waveRecordID,
			ExecutionID:  executionID,
			ObservedAt:   waveStart,
			WaveIndex:    wave.Index,
			Phase:        PhaseRunning,
			StartedAt:    &waveStart,
			GroupNames:   append([]string(nil), wave.Groups...),
			Details: mergeDetails(adaptiveStateRecord(waveState), map[string]any{
				"dryRun": dryRun,
				"events": append([]string(nil), waveState.Events...),
			}),
		}))

		for _, groupName := range wave.Groups {
			group := groups[groupName]
			groupResult, groupErr := e.executeGroup(ctx, writer, input, executionID, mode, dryRun, wave.Index, group, waveState)
			result.Groups++
			result.ActionAttempts += groupResult.ActionAttempts
			result.NodeReleases += groupResult.NodeReleases
			recordErr = errors.Join(recordErr, groupResult.RecordError)
			if groupErr != nil {
				result.Phase = PhaseAborted
				completedAt := e.now()
				recordErr = errors.Join(recordErr, writer.RecordShutdownFlowExecution(ctx, audit.ShutdownFlowExecution{
					ExecutionID:       executionID,
					ObservedAt:        completedAt,
					ShutdownFlow:      input.ShutdownFlow,
					TriggerDecisionID: input.TriggerDecisionID,
					Mode:              mode,
					Phase:             PhaseAborted,
					Reason:            groupErr.Error(),
					PlanConfigHash:    input.PlanConfigHash,
					InputHash:         input.InputHash,
					StartedAt:         &startedAt,
					CompletedAt:       &completedAt,
					DryRun:            dryRun,
					Approved:          input.Approved,
					ApprovalEvidence:  map[string]any{"approved": input.Approved, "requestedMode": mode, "effectiveDryRun": dryRun},
					Revalidation:      map[string]any{"inputHash": input.InputHash},
					Details:           map[string]any{"failedGroup": group.Name},
				}))
				return result, errors.Join(groupErr, recordErr)
			}
		}

		waveCompletedAt := e.now()
		recordErr = errors.Join(recordErr, writer.RecordShutdownFlowExecutionWave(ctx, audit.ShutdownFlowExecutionWave{
			WaveRecordID: waveRecordID,
			ExecutionID:  executionID,
			ObservedAt:   waveCompletedAt,
			WaveIndex:    wave.Index,
			Phase:        PhaseCompleted,
			StartedAt:    &waveStart,
			CompletedAt:  &waveCompletedAt,
			GroupNames:   append([]string(nil), wave.Groups...),
			Details: mergeDetails(adaptiveStateRecord(waveState), map[string]any{
				"dryRun": dryRun,
			}),
		}))
	}

	completedAt := e.now()
	recordErr = errors.Join(recordErr, writer.RecordShutdownFlowExecution(ctx, audit.ShutdownFlowExecution{
		ExecutionID:       executionID,
		ObservedAt:        completedAt,
		ShutdownFlow:      input.ShutdownFlow,
		TriggerDecisionID: input.TriggerDecisionID,
		Mode:              mode,
		Phase:             PhaseCompleted,
		Reason:            reason,
		PlanConfigHash:    input.PlanConfigHash,
		InputHash:         input.InputHash,
		StartedAt:         &startedAt,
		CompletedAt:       &completedAt,
		DryRun:            dryRun,
		Approved:          input.Approved,
		ApprovalEvidence:  map[string]any{"approved": input.Approved, "requestedMode": mode, "effectiveDryRun": dryRun},
		Revalidation:      map[string]any{"inputHash": input.InputHash},
		Details: map[string]any{
			"selectedUPSDevices": append([]string(nil), input.SelectedUPSDevices...),
			"waveCount":          len(input.Waves),
			"groupCount":         result.Groups,
			"actionAttempts":     result.ActionAttempts,
			"nodeReleases":       result.NodeReleases,
		},
	}))
	recordErr = errors.Join(recordErr, writer.UpsertExecutorResumeState(ctx, audit.ExecutorResumeState{
		ExecutionID:    executionID,
		ObservedAt:     completedAt,
		ShutdownFlow:   input.ShutdownFlow,
		PlanConfigHash: input.PlanConfigHash,
		Phase:          PhaseCompleted,
		State: mergeDetails(finalAdaptiveStateRecord(result.Adaptive), map[string]any{
			"completedWaveCount": len(input.Waves),
			"groupCount":         result.Groups,
		}),
	}))

	return result, recordErr
}

// recordSuspension writes the evidence for a flow that stopped descending because
// power recovered.
//
// A separate path from completion because it is a different outcome, and
// collapsing the two would make "the outage ended" indistinguishable from "the
// cluster finished shutting down" in the audit trail -- precisely the distinction
// a subscriber reading a recovery needs.
//
// The resume state written here is the point of the whole exercise: it is what a
// later descent reads to continue from this depth rather than starting over.
func (e Executor) recordSuspension(ctx context.Context, writer audit.Writer, input Input, executionID, mode string, dryRun bool, reason string, startedAt time.Time, waveState waveAdaptiveState, result Result) error {
	suspendedAt := e.now()
	recordErr := writer.RecordShutdownFlowExecution(ctx, audit.ShutdownFlowExecution{
		ExecutionID:       executionID,
		ObservedAt:        suspendedAt,
		ShutdownFlow:      input.ShutdownFlow,
		TriggerDecisionID: input.TriggerDecisionID,
		Mode:              mode,
		Phase:             PhaseSuspended,
		Reason:            "PowerRecovered",
		PlanConfigHash:    input.PlanConfigHash,
		InputHash:         input.InputHash,
		StartedAt:         &startedAt,
		CompletedAt:       &suspendedAt,
		DryRun:            dryRun,
		Approved:          input.Approved,
		ApprovalEvidence:  map[string]any{"approved": input.Approved, "requestedMode": mode, "effectiveDryRun": dryRun},
		Revalidation:      map[string]any{"inputHash": input.InputHash},
		Details: mergeDetails(adaptiveStateRecord(waveState), map[string]any{
			"triggerReason":      reason,
			"completedWaveCount": result.Waves,
			"groupCount":         result.Groups,
			"events":             append([]string(nil), waveState.Events...),
		}),
	})
	return errors.Join(recordErr, writer.UpsertExecutorResumeState(ctx, audit.ExecutorResumeState{
		ExecutionID:    executionID,
		ObservedAt:     suspendedAt,
		ShutdownFlow:   input.ShutdownFlow,
		PlanConfigHash: input.PlanConfigHash,
		Phase:          PhaseSuspended,
		State:          adaptiveStateRecord(waveState),
	}))
}

// adaptiveResultFrom accumulates the running adaptive result across waves. Events
// append; state is replaced by the newest evaluation.
func adaptiveResultFrom(previous AdaptiveResult, waveState waveAdaptiveState) AdaptiveResult {
	return AdaptiveResult{
		Pointer:     waveState.Pointer,
		Timing:      waveState.Timing,
		Observation: waveState.Observation,
		Events:      append(previous.Events, waveState.Events...),
		Suspended:   previous.Suspended,
	}
}

// finalAdaptiveStateRecord renders the end-of-run state for the resume row. It
// reuses the per-wave shape so a resume row reads the same whether it was written
// mid-flow or at the end.
func finalAdaptiveStateRecord(result AdaptiveResult) map[string]any {
	return adaptiveStateRecord(waveAdaptiveState{
		Pointer:     result.Pointer,
		Timing:      result.Timing,
		Observation: result.Observation,
	})
}

type groupExecutionResult struct {
	ActionAttempts int
	NodeReleases   int
	RecordError    error
}

func (e Executor) executeGroup(ctx context.Context, writer audit.Writer, input Input, executionID, mode string, dryRun bool, waveIndex int32, group Group, waveState waveAdaptiveState) (groupExecutionResult, error) {
	timingMode := waveState.Mode
	effectiveTimeout := adaptive.ScaleDuration(group.Timeout, waveState.Budget)
	effectiveWait := adaptive.ScaleDuration(group.WaitDuration, waveState.Budget)

	startedAt := e.now()
	completedAt := startedAt
	outcome := ActionOutcome{
		Outcome: OutcomeSimulated,
		Details: map[string]any{"dryRun": true},
	}
	var actionErr error
	if group.Action == ActionAgentShutdown {
		if readinessErr := agentShutdownReadinessError(dryRun, group); readinessErr != nil {
			actionErr = readinessErr
			outcome = ActionOutcome{
				Outcome: OutcomeBlocked,
				Error:   readinessErr.Error(),
				Details: map[string]any{
					"blockedNodeReleases": blockedNodeReleaseDetails(group.NodeReleases),
				},
			}
		}
	}

	// Wait is honored in dry-run as well as enforce. EX-5 makes dry-run a faithful
	// rehearsal of everything except effects, and a rehearsal that skips the waits
	// reports a flow duration the real run will not reproduce -- which is the number
	// an operator is rehearsing to find out.
	if group.Action == ActionWait && actionErr == nil && effectiveWait > 0 {
		if waitErr := e.sleep(ctx, effectiveWait); waitErr != nil {
			actionErr = fmt.Errorf("wait group %q interrupted: %w", group.Name, waitErr)
			outcome = ActionOutcome{Outcome: OutcomeBlocked, Error: actionErr.Error()}
		} else {
			outcome = ActionOutcome{
				Outcome: waitOutcome(dryRun),
				Details: map[string]any{"action": ActionWait},
			}
		}
	}

	if !dryRun && actionErr == nil {
		if e.Runner == nil {
			actionErr = fmt.Errorf("enforce execution requires an action runner")
			outcome = ActionOutcome{Outcome: OutcomeBlocked, Error: actionErr.Error()}
		} else {
			// EX-11: the declared timeout is enforced as written, and expiry is a group
			// failure that engages abort policy rather than an implicit success.
			actionCtx := ctx
			var cancel context.CancelFunc
			if effectiveTimeout > 0 {
				actionCtx, cancel = context.WithTimeout(ctx, effectiveTimeout)
			}
			outcome, actionErr = e.Runner.RunAction(actionCtx, Action{
				ExecutionID:        executionID,
				ShutdownFlow:       input.ShutdownFlow,
				PlanConfigHash:     input.PlanConfigHash,
				SelectedUPSDevices: append([]string(nil), input.SelectedUPSDevices...),
				WaveIndex:          waveIndex,
				Group:              group,
				DryRun:             false,
			})
			timedOut := effectiveTimeout > 0 && actionCtx.Err() != nil && ctx.Err() == nil
			if cancel != nil {
				cancel()
			}
			if outcome.Outcome == "" && actionErr == nil {
				outcome.Outcome = OutcomeSucceeded
			}
			if timedOut {
				// A runner that returned success against an expired deadline did not finish in
				// the time the flow allowed. Reporting it as success would let the next wave
				// start on work that is still in flight.
				if actionErr == nil {
					actionErr = fmt.Errorf("group %q exceeded its %s timeout", group.Name, effectiveTimeout)
				}
				outcome.Outcome = OutcomeTimedOut
			}
			if actionErr != nil && outcome.Outcome == "" {
				outcome.Outcome = PhaseFailed
			}
			if actionErr != nil && outcome.Error == "" {
				outcome.Error = actionErr.Error()
			}
		}
	}

	phase := PhaseCompleted
	if actionErr != nil {
		phase = PhaseFailed
	}
	recordErr := writer.RecordShutdownFlowExecutionGroup(ctx, audit.ShutdownFlowExecutionGroup{
		GroupRecordID:   e.newID(),
		ExecutionID:     executionID,
		ObservedAt:      completedAt,
		WaveIndex:       waveIndex,
		GroupName:       group.Name,
		Action:          group.Action,
		Phase:           phase,
		StartedAt:       &startedAt,
		CompletedAt:     &completedAt,
		SelectedTargets: auditTargets(group.SelectedTargets),
		Details: mergeDetails(group.Details, map[string]any{
			"dryRun":  dryRun,
			"outcome": outcome.Outcome,
			"params":  copyStringMap(group.Params),
			// Both the declared and the effective value, plus the compression that produced
			// the difference, so an operator reading the record can tell a short timeout the
			// author wrote from a short one the runtime forced -- and see the arithmetic.
			"timingMode":       string(timingMode),
			"compression":      waveState.Budget.Compression,
			"planFits":         waveState.Budget.Fits,
			"declaredTimeout":  durationSeconds(group.Timeout),
			"effectiveTimeout": durationSeconds(effectiveTimeout),
			"declaredWait":     durationSeconds(group.WaitDuration),
			"effectiveWait":    durationSeconds(effectiveWait),
		}),
	})
	recordErr = errors.Join(recordErr, writer.RecordShutdownFlowActionAttempt(ctx, audit.ShutdownFlowActionAttempt{
		AttemptID:   e.newID(),
		ExecutionID: executionID,
		ObservedAt:  completedAt,
		WaveIndex:   &waveIndex,
		GroupName:   group.Name,
		Action:      group.Action,
		StartedAt:   &startedAt,
		CompletedAt: &completedAt,
		Outcome:     outcome.Outcome,
		Error:       outcome.Error,
		DryRun:      dryRun,
		Details: mergeDetails(outcome.Details, map[string]any{
			"selectedTargetCount": len(group.SelectedTargets),
		}),
	}))

	result := groupExecutionResult{ActionAttempts: 1, RecordError: recordErr}
	if group.Action == ActionAgentShutdown {
		releaseCount, releaseErr := e.recordNodeReleases(ctx, writer, input, executionID, dryRun, group, completedAt)
		result.NodeReleases = releaseCount
		result.RecordError = errors.Join(result.RecordError, releaseErr)
	}
	return result, actionErr
}

func (e Executor) recordNodeReleases(ctx context.Context, writer audit.Writer, input Input, executionID string, dryRun bool, group Group, observedAt time.Time) (int, error) {
	var recordErr error
	for _, release := range group.NodeReleases {
		if release.NodeName == "" {
			recordErr = errors.Join(recordErr, fmt.Errorf("node release for group %q requires node name", group.Name))
			continue
		}
		signalPath := release.SignalPath
		if signalPath == "" {
			signalPath = DefaultSignalPath
		}
		staleAfter := observedAt.Add(signalTTL(input.SignalTTL))
		clearedForRelease := release.AgentReady && release.TelemetryFresh && release.Cleared
		released := clearedForRelease && !dryRun
		reason := nodeReleaseReason(release, dryRun)
		recordErr = errors.Join(recordErr, writer.RecordNodeRelease(ctx, audit.NodeReleaseRecord{
			ReleaseID:      e.newID(),
			ExecutionID:    executionID,
			ObservedAt:     observedAt,
			NodeName:       release.NodeName,
			NodePowerAgent: release.NodePowerAgent,
			PlanConfigHash: input.PlanConfigHash,
			Approved:       input.Approved,
			Released:       released,
			Reason:         reason,
			Clearance: map[string]any{
				"agentReady":           release.AgentReady,
				"cleared":              release.Cleared,
				"clearanceReason":      release.ClearanceReason,
				"blockingWorkloads":    append([]string(nil), release.BlockingWorkloads...),
				"dryRun":               dryRun,
				"group":                group.Name,
				"lastHeartbeatTime":    releaseHeartbeatTime(release),
				"podName":              release.PodName,
				"readinessReason":      release.ReadinessReason,
				"telemetryFresh":       release.TelemetryFresh,
				"telemetryStaleReason": release.TelemetryStaleReason,
			},
			Details: map[string]any{
				"readinessMessage":   release.ReadinessMessage,
				"selectedUPSDevices": append([]string(nil), input.SelectedUPSDevices...),
			},
		}))
		handoffReason := nodeSignalHandoffReason(release, dryRun)
		recordErr = errors.Join(recordErr, writer.RecordNodeSignalHandoff(ctx, audit.NodeSignalHandoff{
			HandoffID:      e.newID(),
			ExecutionID:    executionID,
			ObservedAt:     observedAt,
			NodeName:       release.NodeName,
			NodePowerAgent: release.NodePowerAgent,
			SignalPath:     signalPath,
			SignalPayload: map[string]any{
				"agentReady":           release.AgentReady,
				"dryRun":               dryRun,
				"executionID":          executionID,
				"lastHeartbeatTime":    releaseHeartbeatTime(release),
				"nodeName":             release.NodeName,
				"planConfigHash":       input.PlanConfigHash,
				"podName":              release.PodName,
				"readinessReason":      release.ReadinessReason,
				"reason":               reason,
				"selectedUPSDevices":   append([]string(nil), input.SelectedUPSDevices...),
				"shutdownFlow":         input.ShutdownFlow,
				"telemetryFresh":       release.TelemetryFresh,
				"telemetryStaleReason": release.TelemetryStaleReason,
				"timestamp":            observedAt.UTC().Format(time.RFC3339Nano),
			},
			StaleAfter: &staleAfter,
			Accepted:   clearedForRelease && !dryRun,
			Reason:     handoffReason,
			Details: map[string]any{
				"group":             group.Name,
				"readinessMessage":  release.ReadinessMessage,
				"readinessReason":   release.ReadinessReason,
				"lastHeartbeatTime": releaseHeartbeatTime(release),
			},
		}))
	}
	return len(group.NodeReleases), recordErr
}

func agentShutdownReadinessError(dryRun bool, group Group) error {
	if dryRun {
		return nil
	}
	var out error
	for _, release := range group.NodeReleases {
		if !release.AgentReady {
			reason := release.ReadinessReason
			if reason == "" {
				reason = "AgentReadinessUnknown"
			}
			out = errors.Join(out, fmt.Errorf("node %q is not ready for AgentShutdown: %s", release.NodeName, reason))
			continue
		}
		if !release.TelemetryFresh {
			reason := release.TelemetryStaleReason
			if reason == "" {
				reason = "AgentTelemetryStale"
			}
			out = errors.Join(out, fmt.Errorf("node %q is not ready for AgentShutdown: %s", release.NodeName, reason))
			continue
		}
		// EX-9: the last check before power is cut. A node whose workloads have not
		// actually moved is at least as dangerous as one whose agent is unready, so it
		// blocks on the same terms.
		if !release.Cleared {
			reason := release.ClearanceReason
			if reason == "" {
				reason = "NodeNotCleared"
			}
			out = errors.Join(out, fmt.Errorf("node %q is not ready for AgentShutdown: %s", release.NodeName, reason))
		}
	}
	return out
}

func blockedNodeReleaseDetails(releases []NodeRelease) []map[string]any {
	blocked := make([]map[string]any, 0)
	for _, release := range releases {
		if release.AgentReady && release.TelemetryFresh && release.Cleared {
			continue
		}
		blocked = append(blocked, map[string]any{
			"lastHeartbeatTime":    releaseHeartbeatTime(release),
			"nodeName":             release.NodeName,
			"nodePowerAgent":       release.NodePowerAgent,
			"podName":              release.PodName,
			"readinessMessage":     release.ReadinessMessage,
			"readinessReason":      release.ReadinessReason,
			"telemetryFresh":       release.TelemetryFresh,
			"telemetryStaleReason": release.TelemetryStaleReason,
			"cleared":              release.Cleared,
			"clearanceReason":      release.ClearanceReason,
			"blockingWorkloads":    append([]string(nil), release.BlockingWorkloads...),
		})
	}
	return blocked
}

func nodeReleaseReason(release NodeRelease, dryRun bool) string {
	if !release.AgentReady {
		if release.ReadinessReason != "" {
			return release.ReadinessReason
		}
		return "AgentReadinessUnknown"
	}
	if !release.TelemetryFresh {
		if release.TelemetryStaleReason != "" {
			return release.TelemetryStaleReason
		}
		return "AgentTelemetryStale"
	}
	if !release.Cleared {
		if release.ClearanceReason != "" {
			return release.ClearanceReason
		}
		return "NodeNotCleared"
	}
	if dryRun {
		return "DryRunRelease"
	}
	return "ReleaseApproved"
}

func nodeSignalHandoffReason(release NodeRelease, dryRun bool) string {
	if !release.AgentReady {
		if release.ReadinessReason != "" {
			return release.ReadinessReason
		}
		return "AgentReadinessUnknown"
	}
	if !release.TelemetryFresh {
		if release.TelemetryStaleReason != "" {
			return release.TelemetryStaleReason
		}
		return "AgentTelemetryStale"
	}
	if !release.Cleared {
		if release.ClearanceReason != "" {
			return release.ClearanceReason
		}
		return "NodeNotCleared"
	}
	if dryRun {
		return "DryRunSignal"
	}
	return "SignalAccepted"
}

func releaseHeartbeatTime(release NodeRelease) string {
	if release.LastHeartbeatTime == nil || release.LastHeartbeatTime.IsZero() {
		return ""
	}
	return release.LastHeartbeatTime.UTC().Format(time.RFC3339Nano)
}

func indexGroups(groups []Group) (map[string]Group, error) {
	indexed := make(map[string]Group, len(groups))
	for _, group := range groups {
		if group.Name == "" || group.Action == "" {
			return nil, fmt.Errorf("executor group requires name and action")
		}
		if _, exists := indexed[group.Name]; exists {
			return nil, fmt.Errorf("duplicate executor group %q", group.Name)
		}
		indexed[group.Name] = group
	}
	return indexed, nil
}

func effectiveDryRun(input Input) bool {
	if input.DryRun {
		return true
	}
	return !strings.EqualFold(input.Mode, ModeEnforce) || !input.Approved
}

func signalTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return DefaultSignalTTL
	}
	return ttl
}

func auditTargets(targets []Target) []map[string]string {
	if len(targets) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(targets))
	for _, target := range targets {
		entry := map[string]string{
			"kind": target.Kind,
			"name": target.Name,
		}
		if target.APIVersion != "" {
			entry["apiVersion"] = target.APIVersion
		}
		if target.Namespace != "" {
			entry["namespace"] = target.Namespace
		}
		out = append(out, entry)
	}
	return out
}

func copyStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func mergeDetails(base map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func (e Executor) writer() audit.Writer {
	if e.Writer != nil {
		return e.Writer
	}
	return audit.NoopStore{}
}

func (e Executor) now() time.Time {
	if e.Clock != nil {
		return e.Clock().UTC()
	}
	return time.Now().UTC()
}

func (e Executor) newID() string {
	if e.NewID != nil {
		return e.NewID()
	}
	return uuid.NewString()
}

// sleep waits out a Wait group. The default is context-aware so a cancelled flow
// stops waiting immediately rather than holding the executor for the remainder of
// a duration nobody is going to use.
func (e Executor) sleep(ctx context.Context, duration time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitOutcome labels a completed Wait. A dry-run Wait really did wait, so calling
// it Simulated would misreport it -- but it is still part of a simulated run, and
// the run's own dryRun flag is what says so.
func waitOutcome(dryRun bool) string {
	if dryRun {
		return OutcomeSimulated
	}
	return OutcomeSucceeded
}
