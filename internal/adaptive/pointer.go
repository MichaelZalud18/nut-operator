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

package adaptive

import "fmt"

const (
	DiagnosticError   = "Error"
	DiagnosticWarning = "Warning"
	DiagnosticInfo    = "Info"
)

// Diagnostic is a machine-readable finding, matching the shape the planner,
// resolver, and trigger evaluator already use.
type Diagnostic struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

// PowerObservation is the power state at one decision point.
//
// Deliberately not a telemetry snapshot: this package takes the few values the
// model reasons about, so it cannot accidentally grow a dependency on live
// telemetry shape or start making device-level judgements.
type PowerObservation struct {
	// OnBattery is whether mains is currently lost.
	OnBattery bool
	// LowBattery is the device's own judgement that the battery is nearly spent.
	LowBattery bool
	// RuntimeSeconds is remaining runtime. Nil means unknown, which is never read
	// as good news.
	RuntimeSeconds *int64
	// RuntimeTrusted is whether runtime responds to load (CR-4). A static firmware
	// estimate must not drive timing decisions.
	RuntimeTrusted bool
}

// PointerState is where a flow has progressed to, and what it has already done.
//
// Tiers count down: higher tiers stop earlier, tier 1 is the final stop for nodes.
// "Descending" therefore means the pointer's numeric value decreases.
type PointerState struct {
	// Tier is the tier currently being executed, or the last one executed.
	Tier int32 `json:"tier"`
	// Deepest is the lowest-numbered tier ever reached in this flow. It is what
	// makes re-descent recognizable as re-descent (EX-26) rather than new work.
	//
	// Only meaningful once Started. Zero on a started state means unset rather than
	// "reached tier 0": tier 0 is last-ditch and cannot be targeted by a flow
	// (OD-4), so no flow legitimately reaches it. normalize() resolves that case.
	Deepest int32 `json:"deepest"`
	// Started is false before the first descent, which distinguishes "at tier 5 and
	// waiting" from "at tier 5 and already executed it".
	Started bool `json:"started"`
	// Halted records that abort was requested. EX-30: abort stops descent and
	// restores nothing, so this is a one-way latch.
	Halted bool `json:"halted"`
}

// PointerMovement is what one evaluation decided.
type PointerMovement struct {
	// From and To are the tiers before and after. Equal when nothing moved.
	From int32 `json:"from"`
	To   int32 `json:"to"`
	// Direction is Descend, Ascend, or Hold.
	Direction string `json:"direction"`
	// Reexecution is true when descending back over tiers already executed. The
	// actions are idempotent (EX-26), so this is expected -- but it is published,
	// because a subscriber watching a second dip should be able to tell that from a
	// first descent without diffing history itself.
	Reexecution bool `json:"reexecution,omitempty"`
}

// Pointer movement directions.
const (
	DirectionDescend = "Descend"
	DirectionAscend  = "Ascend"
	DirectionHold    = "Hold"
)

// Descend moves the pointer one tier deeper, toward the flow's final tier.
//
// EX-26 is what makes this safe to call repeatedly: every shutdown action is a
// no-op when already applied, so re-descending over executed tiers re-attempts
// them harmlessly. That is a requirement on the actions, not a property this
// package can check, and it is the reason no flapping protection exists on the
// descent path.
//
// finalTier is the last tier this flow may execute. Tier 0 is last-ditch and
// cannot be targeted by a flow (OD-4), so callers pass 1 for a normal flow.
func (s PointerState) Descend(finalTier int32) (PointerState, PointerMovement) {
	s = s.normalize()
	if s.Halted {
		return s, PointerMovement{From: s.Tier, To: s.Tier, Direction: DirectionHold}
	}

	next := s
	if !s.Started {
		// The first descent occupies the starting tier rather than stepping past it,
		// so a flow starting at tier 5 executes tier 5.
		next.Started = true
		next.Deepest = s.Tier
		return next, PointerMovement{From: s.Tier, To: s.Tier, Direction: DirectionDescend}
	}
	if s.Tier <= finalTier {
		return s, PointerMovement{From: s.Tier, To: s.Tier, Direction: DirectionHold}
	}

	next.Tier = s.Tier - 1
	reexecution := next.Tier >= s.Deepest
	if next.Tier < next.Deepest {
		next.Deepest = next.Tier
	}
	return next, PointerMovement{
		From:        s.Tier,
		To:          next.Tier,
		Direction:   DirectionDescend,
		Reexecution: reexecution,
	}
}

// Ascend records that power improved. It performs no actions and restores nothing
// (EX-25, EX-27).
//
// Because ascent is bookkeeping, it needs no hysteresis: moving the pointer up on
// a brief flicker costs nothing, and the next descent simply starts from higher up
// and re-crosses tiers that are already no-ops. Deepest is deliberately not reset
// -- it is the record of how far the flow actually got, and forgetting it would
// make a re-descent indistinguishable from a first one.
// normalize repairs a state whose Deepest was never set or has drifted above the
// current tier.
//
// This exists because the state is persisted and handed back across executor
// restarts, so it can arrive from a older record, a partially written row, or a
// caller constructing it directly. Deepest is what makes re-descent recognizable,
// and a wrong value there silently mislabels re-execution as new work -- a
// reporting error at exactly the moment a subscriber is trying to understand a
// second dip. Repairing to the current tier is the conservative reading: it
// claims only what the state itself already proves.
func (s PointerState) normalize() PointerState {
	if !s.Started {
		s.Deepest = 0
		return s
	}
	if s.Deepest <= 0 || s.Deepest > s.Tier {
		s.Deepest = s.Tier
	}
	return s
}

func (s PointerState) Ascend(startTier int32) (PointerState, PointerMovement) {
	s = s.normalize()
	if s.Halted || !s.Started || s.Tier >= startTier {
		return s, PointerMovement{From: s.Tier, To: s.Tier, Direction: DirectionHold}
	}
	next := s
	next.Tier = s.Tier + 1
	return next, PointerMovement{From: s.Tier, To: next.Tier, Direction: DirectionAscend}
}

// Halt latches the abort state (EX-30). Abort is halt, not undo: descent stops and
// nothing already shut down is restored, which is why it is safe at any point in
// the flow and why there is no un-halt.
func (s PointerState) Halt() PointerState {
	s.Halted = true
	return s
}

// ShouldDescend reports whether an observation justifies going deeper.
//
// The rule is intentionally blunt, per EX-25: the operator is a recorder, not a
// forecaster. It descends while power is lost and does not try to judge whether
// the outage is durable, where the curve is heading, or whether a dip is a
// flicker. Interpretation belongs to the subscriber.
//
// Unknown runtime does not block descent. PL-32's discipline is that missing data
// never produces an optimistic verdict, and on this path the optimistic verdict is
// "stay up".
func ShouldDescend(observation PowerObservation) bool {
	return observation.OnBattery || observation.LowBattery
}

// ShouldAscend reports whether an observation justifies recording improvement.
//
// Strictly the inverse of descent: mains is back and the device is no longer
// asserting a low battery. Nothing is restored either way (EX-27).
func ShouldAscend(observation PowerObservation) bool {
	return !observation.OnBattery && !observation.LowBattery
}

// Describe renders a movement as one event-log line, matching the EX-28 shape.
//
// EX-28 draws the line at stating what is versus interpreting what it means, so
// this reports the transition and the power state observed at that moment, and
// never characterizes the sequence as a dip, a flicker, or a recovery.
func (m PointerMovement) Describe(observation PowerObservation) string {
	power := "OL"
	switch {
	case observation.LowBattery:
		power = "OB LB"
	case observation.OnBattery:
		power = "OB"
	}
	runtime := "runtime unknown"
	if observation.RuntimeSeconds != nil {
		runtime = fmt.Sprintf("runtime %ds", *observation.RuntimeSeconds)
	}
	switch m.Direction {
	case DirectionDescend:
		if m.Reexecution {
			return fmt.Sprintf("re-entered tier %d  power: %s, %s", m.To, power, runtime)
		}
		return fmt.Sprintf("entered tier %d  power: %s, %s", m.To, power, runtime)
	case DirectionAscend:
		return fmt.Sprintf("ascended to tier %d  power: %s, %s", m.To, power, runtime)
	default:
		return fmt.Sprintf("held at tier %d  power: %s, %s", m.To, power, runtime)
	}
}
