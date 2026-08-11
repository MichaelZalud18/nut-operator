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
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
)

// AdaptiveInput carries the tier-pointer and timing-mode state across the
// executor boundary.
//
// It is the executor's whole interface to internal/adaptive. The model itself
// stays pure -- it reads no clock, no client, and no telemetry -- so everything
// impure about running it lives here: reading the power state, persisting the
// resulting state, and emitting the events.
//
// State arrives on the input rather than being held on the Executor because the
// executor may restart mid-flow (EX-14). The caller loads the last persisted
// pointer and timing state from executor_resume_states and hands them back, which
// is what stops a restarted instance resuming at the wrong depth or silently
// reverting to a fresh mode.
type AdaptiveInput struct {
	// Parameters are the OD-27 through OD-30 tunables. Zero values are filled from
	// adaptive.DefaultParameters, so a caller that has no opinion still gets the
	// validated defaults rather than a model that escalates on nothing.
	Parameters adaptive.Parameters

	// Pointer and Timing are the state to resume from. Zero values are a fresh
	// flow: pointer not started, mode Relaxed.
	Pointer adaptive.PointerState
	Timing  adaptive.TimingState

	// Observation is the power state to use when no Observer is injected. It is
	// what makes the executor testable and what a single-shot caller supplies.
	Observation adaptive.PowerObservation

	// FinalTier is the last tier this flow may execute. Tier 0 is last-ditch and is
	// excluded from flow targeting entirely (OD-4), so a normal flow ends at 1. Zero
	// is read as 1 rather than as permission to reach tier 0.
	FinalTier int32

	// StartTier is the tier the flow began at, and the highest the pointer may
	// ascend back to. Ascent is bookkeeping only (AE-3); the limit exists so the
	// recorded position stays inside the flow's own range.
	StartTier int32

	// SuspendOnRecovery stops the flow starting further waves once power is back.
	// AE-1: the operator stops descending and publishes, and restores nothing.
	//
	// Not a halt. The pointer is left where it is, unlatched, so a later degrade
	// descends from that depth and re-attempts the tiers below it as no-ops (AE-2).
	SuspendOnRecovery bool
}

// PowerObserver reads the current power state at a wave boundary.
//
// Injected rather than called directly because reading live telemetry is I/O, and
// the executor's testability depends on every impure edge being replaceable. The
// design evaluates timing at wave boundaries only, so this is called once per
// wave and never inside one.
type PowerObserver func(ctx context.Context) (adaptive.PowerObservation, error)

// Sleeper waits for a duration or returns early when the context ends.
//
// Injected for the same reason as the clock: a Wait action that calls time.Sleep
// directly can only be tested by actually waiting, which makes the executor's own
// tests slow and flaky in exchange for nothing.
type Sleeper func(ctx context.Context, duration time.Duration) error

// AdaptiveResult is the adaptive state a completed execution ended on, for the
// caller to persist and publish.
type AdaptiveResult struct {
	Pointer adaptive.PointerState
	Timing  adaptive.TimingState
	// Observation is the last power state read.
	Observation adaptive.PowerObservation
	// Events are the AE-4 log lines produced, in order.
	Events []string
	// Suspended records that the flow stopped descending because power recovered.
	// The pointer is left where it stopped so a later degrade resumes from there.
	Suspended bool
}

// waveAdaptiveState is the per-wave outcome of evaluating the adaptive model.
type waveAdaptiveState struct {
	Mode        adaptive.TimingMode
	Pointer     adaptive.PointerState
	Timing      adaptive.TimingState
	Observation adaptive.PowerObservation
	Movement    adaptive.PointerMovement
	Transition  adaptive.TimingTransition
	Events      []string
	// Suspend is true when power recovered and the flow should stop starting waves.
	Suspend bool
	// Budget is the derived timing budget applied to this wave's declared durations.
	Budget adaptive.TimingBudget
}

// evaluateWave folds one power observation into the pointer and the timing mode.
//
// The two are evaluated independently, which the design requires: one answers how
// far down the tiers to go, the other how much time to allow at each step. They
// are computed in the same call only because they are driven by the same
// observation, and neither reads the other's result.
func (e Executor) evaluateWave(ctx context.Context, input AdaptiveInput, wave Wave, remainingPlan time.Duration) (waveAdaptiveState, error) {
	parameters := input.Parameters.WithDefaults()
	observation := input.Observation
	if e.Observer != nil {
		read, err := e.Observer(ctx)
		if err != nil {
			// A failed read is not permission to assume the good case. PL-32: missing data
			// never yields an optimistic verdict, so the flow keeps the state it had rather
			// than relaxing into an unread one.
			return waveAdaptiveState{}, fmt.Errorf("read power state for wave %d: %w", wave.Index, err)
		}
		observation = read
	}

	state := waveAdaptiveState{Observation: observation}

	timing, transition := input.Timing.Observe(observation, parameters)
	state.Timing = timing
	state.Transition = transition
	state.Mode = timing.Mode
	if transition.Changed {
		state.Events = append(state.Events, transition.Describe())
	}

	// The mode says how the flow is doing; the budget says what it may spend. They
	// are computed from the same observation and never from each other, so a mode
	// label can never quietly become the source of a timeout.
	state.Budget = parameters.Budget(observation, remainingPlan)
	state.Events = append(state.Events, state.Budget.Describe())

	pointer := input.Pointer
	switch {
	case adaptive.ShouldDescend(observation):
		pointer, state.Movement = descendToWaveTier(pointer, wave, finalTier(input))
	case input.SuspendOnRecovery:
		// AE-1 and AE-3: record that power improved, restore nothing, and stop
		// descending. The pointer stays where it is, unlatched, so a later degrade
		// resumes from this depth. The subscriber owns recovery.
		pointer, state.Movement = pointer.Ascend(startTier(input, wave))
		state.Suspend = true
	default:
		pointer, state.Movement = descendToWaveTier(pointer, wave, finalTier(input))
	}
	state.Pointer = pointer
	state.Events = append(state.Events, state.Movement.Describe(observation))

	return state, nil
}

// descendToWaveTier walks the pointer down to the tier the wave belongs to.
//
// Waves are compiled in tier order, but a tier can span several waves and a tier
// can be empty, so the pointer may need more than one step or none at all. Each
// step is taken through Descend rather than by assignment so re-execution
// reporting (AE-2) and the final-tier limit both stay in one place.
//
// A wave with no tier moves the pointer only on the first descent, which is what
// marks the flow as started. An untiered flow is a legitimate configuration --
// tiers are optional -- and it should still report a position.
func descendToWaveTier(pointer adaptive.PointerState, wave Wave, final int32) (adaptive.PointerState, adaptive.PointerMovement) {
	if wave.ShutdownTier == nil {
		return pointer.Descend(final)
	}

	target := *wave.ShutdownTier
	if target < final {
		target = final
	}
	if !pointer.Started {
		// Occupy the wave's own tier rather than stepping past it, so a flow whose first
		// wave is tier 5 reports entering tier 5.
		pointer.Tier = target
		return pointer.Descend(final)
	}

	first := adaptive.PointerMovement{From: pointer.Tier, To: pointer.Tier, Direction: adaptive.DirectionHold}
	movement := first
	for pointer.Tier > target {
		next, step := pointer.Descend(final)
		if step.Direction != adaptive.DirectionDescend || next.Tier == pointer.Tier {
			break
		}
		pointer = next
		movement = adaptive.PointerMovement{
			From:        first.From,
			To:          step.To,
			Direction:   adaptive.DirectionDescend,
			Reexecution: movement.Reexecution || step.Reexecution,
		}
	}
	return pointer, movement
}

func finalTier(input AdaptiveInput) int32 {
	if input.FinalTier <= 0 {
		// Tier 0 is last-ditch and cannot be targeted by a flow (OD-4). Defaulting to 1
		// rather than 0 means an unset limit never authorizes reaching it.
		return 1
	}
	return input.FinalTier
}

func startTier(input AdaptiveInput, wave Wave) int32 {
	if input.StartTier > 0 {
		return input.StartTier
	}
	if wave.ShutdownTier != nil {
		return *wave.ShutdownTier
	}
	return finalTier(input)
}

// remainingPlanDuration sums the declared durations of the waves not yet run.
//
// This is what the compression ratio is measured against, so it is the plan's own
// statement of what it needs rather than an estimate this package invents. Waves
// that declared nothing contribute nothing: an undeclared duration is not a claim
// of zero, and treating it as one would make every plan look like it fits.
func remainingPlanDuration(waves []Wave, fromIndex int) time.Duration {
	var total time.Duration
	for _, wave := range waves[fromIndex:] {
		total += wave.Duration
	}
	return total
}

// adaptiveStateRecord renders the pointer and timing state for the
// executor_resume_states payload (OD-17).
//
// Written as plain values rather than the Go structs so the record stays readable
// and stable if the model's internals change: a resume row outlives the build that
// wrote it.
func adaptiveStateRecord(state waveAdaptiveState) map[string]any {
	return map[string]any{
		"tier":              state.Pointer.Tier,
		"deepestTier":       state.Pointer.Deepest,
		"pointerStarted":    state.Pointer.Started,
		"pointerHalted":     state.Pointer.Halted,
		"timingMode":        string(state.Timing.Mode),
		"timingPendingMode": string(state.Timing.PendingMode),
		"timingPending":     state.Timing.PendingCount,
		"onBattery":         state.Observation.OnBattery,
		"lowBattery":        state.Observation.LowBattery,
		"runtimeTrusted":    state.Observation.RuntimeTrusted,
		"runtimeSeconds":    observedRuntime(state.Observation),
	}
}

// durationSeconds renders a duration for an audit record, keeping "no limit"
// distinguishable from "zero seconds" the same way observedRuntime does.
func durationSeconds(duration time.Duration) any {
	if duration <= 0 {
		return nil
	}
	return duration.Seconds()
}

// observedRuntime renders unknown runtime as nil rather than as zero. Zero seconds
// remaining and "the device did not say" are different facts, and a subscriber
// reading the record must be able to tell them apart.
func observedRuntime(observation adaptive.PowerObservation) any {
	if observation.RuntimeSeconds == nil {
		return nil
	}
	return *observation.RuntimeSeconds
}
