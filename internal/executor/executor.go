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

	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

const (
	ModeDryRun  = "DryRun"
	ModeEnforce = "Enforce"

	PhaseRunning   = "Running"
	PhaseCompleted = "Completed"
	PhaseAborted   = "Aborted"
	PhaseFailed    = "Failed"

	OutcomeSimulated = "Simulated"
	OutcomeSucceeded = "Succeeded"
	OutcomeBlocked   = "Blocked"

	ActionAgentShutdown = "AgentShutdown"

	DefaultSignalPath = "/run/power-agent/shutdown.json"
	DefaultSignalTTL  = 2 * time.Minute
)

// Executor writes the ordered execution evidence for one compiled shutdown run.
type Executor struct {
	Writer audit.Writer
	Runner ActionRunner
	Clock  func() time.Time
	NewID  func() string
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
}

// Wave is one ordered unit from the compiled plan.
type Wave struct {
	Index  int32
	Groups []string
}

// Group describes the action executed for a compiled group name.
type Group struct {
	Name            string
	Action          string
	SelectedTargets []Target
	NodeReleases    []NodeRelease
	Details         map[string]any
}

// Target is an execution-time concrete object selected for an action.
type Target struct {
	Kind      string
	Namespace string
	Name      string
}

// NodeRelease describes a terminal node-agent handoff candidate.
type NodeRelease struct {
	NodeName       string
	NodePowerAgent string
	SignalPath     string
}

// Action is passed to an injected action runner for non-dry-run execution.
type Action struct {
	ExecutionID    string
	ShutdownFlow   string
	PlanConfigHash string
	WaveIndex      int32
	Group          Group
	DryRun         bool
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

	for _, wave := range input.Waves {
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
			State: map[string]any{
				"currentWaveIndex": wave.Index,
				"groups":           append([]string(nil), wave.Groups...),
			},
		}))
		recordErr = errors.Join(recordErr, writer.RecordShutdownFlowExecutionWave(ctx, audit.ShutdownFlowExecutionWave{
			WaveRecordID: waveRecordID,
			ExecutionID:  executionID,
			ObservedAt:   waveStart,
			WaveIndex:    wave.Index,
			Phase:        PhaseRunning,
			StartedAt:    &waveStart,
			GroupNames:   append([]string(nil), wave.Groups...),
			Details: map[string]any{
				"dryRun": dryRun,
			},
		}))

		for _, groupName := range wave.Groups {
			group := groups[groupName]
			groupResult, groupErr := e.executeGroup(ctx, writer, input, executionID, mode, dryRun, wave.Index, group)
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
			Details:      map[string]any{"dryRun": dryRun},
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
		State: map[string]any{
			"completedWaveCount": len(input.Waves),
			"groupCount":         result.Groups,
		},
	}))

	return result, recordErr
}

type groupExecutionResult struct {
	ActionAttempts int
	NodeReleases   int
	RecordError    error
}

func (e Executor) executeGroup(ctx context.Context, writer audit.Writer, input Input, executionID, mode string, dryRun bool, waveIndex int32, group Group) (groupExecutionResult, error) {
	startedAt := e.now()
	completedAt := startedAt
	outcome := ActionOutcome{
		Outcome: OutcomeSimulated,
		Details: map[string]any{"dryRun": true},
	}
	var actionErr error
	if !dryRun {
		if e.Runner == nil {
			actionErr = fmt.Errorf("enforce execution requires an action runner")
			outcome = ActionOutcome{Outcome: OutcomeBlocked, Error: actionErr.Error()}
		} else {
			outcome, actionErr = e.Runner.RunAction(ctx, Action{
				ExecutionID:    executionID,
				ShutdownFlow:   input.ShutdownFlow,
				PlanConfigHash: input.PlanConfigHash,
				WaveIndex:      waveIndex,
				Group:          group,
				DryRun:         false,
			})
			if outcome.Outcome == "" && actionErr == nil {
				outcome.Outcome = OutcomeSucceeded
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
	if group.Action == ActionAgentShutdown && actionErr == nil {
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
		released := !dryRun
		reason := "ReleaseApproved"
		if dryRun {
			reason = "DryRunRelease"
		}
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
				"dryRun": dryRun,
				"group":  group.Name,
			},
			Details: map[string]any{
				"selectedUPSDevices": append([]string(nil), input.SelectedUPSDevices...),
			},
		}))
		handoffReason := "SignalAccepted"
		if dryRun {
			handoffReason = "DryRunSignal"
		}
		recordErr = errors.Join(recordErr, writer.RecordNodeSignalHandoff(ctx, audit.NodeSignalHandoff{
			HandoffID:      e.newID(),
			ExecutionID:    executionID,
			ObservedAt:     observedAt,
			NodeName:       release.NodeName,
			NodePowerAgent: release.NodePowerAgent,
			SignalPath:     signalPath,
			SignalPayload: map[string]any{
				"dryRun":             dryRun,
				"executionID":        executionID,
				"nodeName":           release.NodeName,
				"planConfigHash":     input.PlanConfigHash,
				"reason":             reason,
				"selectedUPSDevices": append([]string(nil), input.SelectedUPSDevices...),
				"shutdownFlow":       input.ShutdownFlow,
				"timestamp":          observedAt.UTC().Format(time.RFC3339Nano),
			},
			StaleAfter: &staleAfter,
			Accepted:   !dryRun,
			Reason:     handoffReason,
			Details:    map[string]any{"group": group.Name},
		}))
	}
	return len(group.NodeReleases), recordErr
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
		if target.Namespace != "" {
			entry["namespace"] = target.Namespace
		}
		out = append(out, entry)
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
