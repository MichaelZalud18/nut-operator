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

import (
	"fmt"
	"time"
)

// TimingMode is how much time the executor allows at each step.
//
// The three modes are compiled together, deterministically, from one structural
// input. The executor selects among them and never recomputes timings or
// recompiles mid-flow, which would invalidate the plan hash (PL-14) and break
// determinism (PL-27).
type TimingMode string

const (
	// ModeRelaxed is the ordinary case: plenty of runtime, full grace periods.
	ModeRelaxed TimingMode = "Relaxed"
	// ModeNominal has margin but not comfort.
	ModeNominal TimingMode = "Nominal"
	// ModeUrgent is racing the battery. Timings are at their shortest.
	ModeUrgent TimingMode = "Urgent"
)

// urgency orders the modes so escalation and relaxation can be compared
// numerically. Higher is more urgent.
func (m TimingMode) urgency() int {
	switch m {
	case ModeUrgent:
		return 2
	case ModeNominal:
		return 1
	default:
		return 0
	}
}

// TimingState is the mode currently in force plus the evidence accumulated toward
// changing it.
type TimingState struct {
	// Mode is the mode in force.
	Mode TimingMode `json:"mode"`
	// PendingMode is the mode consecutive observations have been pointing at.
	PendingMode TimingMode `json:"pendingMode,omitempty"`
	// PendingCount is how many consecutive observations have pointed there. It
	// resets whenever the indicated mode changes, so "consecutive" means what it
	// says -- an improving trend interrupted by one bad reading starts over.
	PendingCount int `json:"pendingCount,omitempty"`
}

// TimingTransition is what one evaluation decided about the mode.
type TimingTransition struct {
	From TimingMode `json:"from"`
	To   TimingMode `json:"to"`
	// Changed is false when the observation only accumulated evidence.
	Changed bool `json:"changed"`
	// Reason names why, for the AE-4 event log.
	Reason string `json:"reason"`
}

// IndicatedMode is the mode an observation points at, before hysteresis.
//
// Unknown or untrusted runtime yields Urgent, not Relaxed. Two separate rules
// converge here: PL-32 says missing data never produces an optimistic verdict, and
// AE-6 says a device whose firmware reports a static runtime estimate cannot drive
// timing adaptation. In both cases the honest answer is that the flow does not
// know how long it has, and the safe reading of "I don't know" while running on
// battery is that there is not much.
//
// While mains is present the mode is Relaxed regardless of runtime: a full battery
// reading is not a reason to hurry, and a low one on mains is a charging battery,
// not a deadline.
func IndicatedMode(observation PowerObservation, parameters Parameters) TimingMode {
	parameters = parameters.WithDefaults()

	if !observation.OnBattery && !observation.LowBattery {
		return ModeRelaxed
	}
	// The device's own low-battery assertion outranks any runtime number. It is the
	// one judgement the hardware is better placed to make than the operator.
	if observation.LowBattery {
		return ModeUrgent
	}
	if observation.RuntimeSeconds == nil || !observation.RuntimeTrusted {
		return ModeUrgent
	}
	switch {
	case *observation.RuntimeSeconds <= parameters.UrgentRuntimeSeconds:
		return ModeUrgent
	case *observation.RuntimeSeconds <= parameters.NominalRuntimeSeconds:
		return ModeNominal
	default:
		return ModeRelaxed
	}
}

// Observe folds one observation into the timing state, applying the asymmetric
// hysteresis the design requires: escalation on a single observation, relaxation
// only on sustained improvement.
//
// The asymmetry is the whole point, and it is not symmetric caution. Escalating
// too early costs a shorter grace period on a workload. Relaxing too early hands
// back time the flow may need and cannot get again, and the cost lands at the end
// of the outage, when there is no margin left to absorb it. So evidence for
// getting more urgent is accepted immediately, and evidence for getting less
// urgent has to survive being counted.
func (s TimingState) Observe(observation PowerObservation, parameters Parameters) (TimingState, TimingTransition) {
	parameters = parameters.WithDefaults()
	if s.Mode == "" {
		s.Mode = ModeRelaxed
	}
	indicated := IndicatedMode(observation, parameters)
	hold := TimingTransition{From: s.Mode, To: s.Mode, Reason: "ModeUnchanged"}

	if indicated == s.Mode {
		// Agreement clears accumulated evidence: a relaxation streak interrupted by a
		// reading at the current mode is not a streak.
		s.PendingMode = ""
		s.PendingCount = 0
		return s, hold
	}

	next := s
	if indicated.urgency() > s.Mode.urgency() {
		next.PendingMode = ""
		next.PendingCount = 0
		if parameters.EscalateAfterObservations <= 1 {
			next.Mode = indicated
			return next, TimingTransition{From: s.Mode, To: indicated, Changed: true, Reason: "Escalated"}
		}
		// Only reachable when an operator has deliberately configured escalation to
		// require repetition, which the defaults do not.
		return s.accumulate(indicated, parameters.EscalateAfterObservations, "Escalated")
	}

	// Relaxation. The margin is checked before the count so a reading that only
	// just crosses a boundary never starts a streak at all.
	if !relaxationEarned(observation, indicated, parameters) {
		next.PendingMode = ""
		next.PendingCount = 0
		return next, TimingTransition{From: s.Mode, To: s.Mode, Reason: "ImprovementWithinMargin"}
	}
	return s.accumulate(indicated, parameters.RelaxAfterObservations, "Relaxed")
}

// accumulate counts consecutive observations pointing at the same mode and applies
// the change once the threshold is met.
func (s TimingState) accumulate(indicated TimingMode, threshold int, reason string) (TimingState, TimingTransition) {
	next := s
	if s.PendingMode != indicated {
		next.PendingMode = indicated
		next.PendingCount = 1
	} else {
		next.PendingCount = s.PendingCount + 1
	}

	if next.PendingCount < threshold {
		return next, TimingTransition{
			From:   s.Mode,
			To:     s.Mode,
			Reason: fmt.Sprintf("%sPending", reason),
		}
	}

	next.Mode = indicated
	next.PendingMode = ""
	next.PendingCount = 0
	return next, TimingTransition{From: s.Mode, To: indicated, Changed: true, Reason: reason}
}

// relaxationEarned reports whether an improving reading clears the threshold it is
// relaxing away from by the configured margin.
//
// Without this a value resting on a boundary flips modes on measurement noise
// alone, and every flip is an event subscribers see. The margin is applied to the
// threshold being left behind, so it scales with the boundary rather than being a
// fixed number of seconds that means something different at each mode.
func relaxationEarned(observation PowerObservation, indicated TimingMode, parameters Parameters) bool {
	// Mains restored is unambiguous and needs no margin: there is no reading to be
	// noisy about.
	if !observation.OnBattery && !observation.LowBattery {
		return true
	}
	if observation.RuntimeSeconds == nil || !observation.RuntimeTrusted {
		// Cannot demonstrate improvement without a trustworthy number, and unknown is
		// never read as improvement.
		return false
	}

	var threshold int64
	switch indicated {
	case ModeNominal:
		threshold = parameters.UrgentRuntimeSeconds
	case ModeRelaxed:
		threshold = parameters.NominalRuntimeSeconds
	default:
		return false
	}
	required := float64(threshold) * (1 + parameters.RelaxImprovementMargin)
	return float64(*observation.RuntimeSeconds) >= required
}

// TimingBudget is how much the remaining plan has to be compressed to fit the
// runtime actually left.
//
// Every number in it is derived. Nothing here is a chosen multiplier: the
// compression is the ratio between the time the devices report and the time the
// plan's own declared durations add up to. That is what makes it answer the real
// question -- not "how urgent does this feel" but "does what is left still fit".
type TimingBudget struct {
	// Compression multiplies every declared duration. Always in
	// (MinimumCompression, 1]: adaptation only ever shortens.
	Compression float64
	// Available is the runtime the plan may use, after the reserve is held back.
	Available time.Duration
	// Required is what the remaining plan declared it needs.
	Required time.Duration
	// Fits is false when the plan cannot be compressed enough to finish in the
	// runtime available. The flow still runs -- refusing mid-outage is the worse
	// outcome (PL-31) -- but the verdict is published rather than buried.
	Fits bool
	// RuntimeMeasured is false when no trustworthy runtime figure was available and
	// Available came from the assumed budget instead.
	RuntimeMeasured bool
}

// Budget derives the timing budget from an observation and the plan still to run.
//
// This replaces per-mode multipliers deliberately. A fixed "quarter of declared
// when urgent" answers a question nobody asked: whether the shutdown finishes
// depends on how much runtime is left and how much plan is left, and both are
// measurable at the moment the decision is made. Two clusters with the same UPS and
// different plans need different compression, and one number cannot express that.
//
// Compression is clamped to 1. The declared duration is what the flow author said
// the step needs, and plentiful runtime is not a reason to overrule it upward --
// only to honor it.
func (p Parameters) Budget(observation PowerObservation, remainingPlan time.Duration) TimingBudget {
	p = p.WithDefaults()

	measured := observation.RuntimeSeconds != nil && observation.RuntimeTrusted
	runtime := p.AssumedRuntimeUnknown
	if measured {
		runtime = time.Duration(*observation.RuntimeSeconds) * time.Second
	}
	// While mains is present there is no deadline to fit inside, so the plan runs as
	// its author wrote it. A full battery is not a reason to hurry.
	if !observation.OnBattery && !observation.LowBattery {
		return TimingBudget{
			Compression: 1, Available: runtime, Required: remainingPlan,
			Fits: true, RuntimeMeasured: measured,
		}
	}

	available := time.Duration(float64(runtime) * (1 - p.ReserveFraction))
	budget := TimingBudget{Available: available, Required: remainingPlan, RuntimeMeasured: measured}

	if remainingPlan <= 0 {
		// Nothing declared a duration, so there is nothing to compress and no basis to
		// claim the plan does not fit.
		budget.Compression = 1
		budget.Fits = true
		return budget
	}

	budget.Compression = float64(available) / float64(remainingPlan)
	budget.Fits = budget.Compression >= p.MinimumCompression
	switch {
	case budget.Compression > 1:
		budget.Compression = 1
	case !budget.Fits:
		// Compressing past this point does not make the shutdown finish; it makes every
		// action fail its deadline instead. Hold at the minimum and report the verdict.
		budget.Compression = p.MinimumCompression
	}
	return budget
}

// ScaleDuration applies a budget to one declared duration -- a group timeout, a
// Wait, or an inter-wave delay.
//
// This is the only thing timing adaptation is permitted to change. The design is
// explicit that adaptation touches timeouts, grace periods, and inter-wave delays
// and never wave order, membership, or edges, because those are hashed into plan
// identity (PL-14) and recompiling mid-flow would break determinism (PL-27).
//
// A zero or negative input stays zero: no declared duration means no limit, and
// compressing "unlimited" is still unlimited. Turning that into a timeout would
// invent a deadline the author never wrote.
func ScaleDuration(declared time.Duration, budget TimingBudget) time.Duration {
	if declared <= 0 {
		return 0
	}
	if budget.Compression <= 0 || budget.Compression >= 1 {
		return declared
	}
	return time.Duration(float64(declared) * budget.Compression)
}

// Describe renders a budget for the AE-4 event log. It states the measurement and
// the verdict, and does not characterize either.
func (b TimingBudget) Describe() string {
	source := "runtime assumed"
	if b.RuntimeMeasured {
		source = "runtime measured"
	}
	verdict := "plan fits"
	if !b.Fits {
		verdict = "plan does not fit"
	}
	return fmt.Sprintf("%s %s, plan needs %s, compression %.2f (%s)",
		source, b.Available, b.Required, b.Compression, verdict)
}

// Cadence returns the AE-5 publish interval for the current activity (OD-30).
func (p Parameters) Cadence(flowActive bool) time.Duration {
	p = p.WithDefaults()
	if flowActive {
		return p.ActiveCadence
	}
	return p.IdleCadence
}

// Describe renders a transition for the AE-4 event log. It states the modes and
// the reason code, and never characterizes what the sequence means.
func (t TimingTransition) Describe() string {
	if !t.Changed {
		return fmt.Sprintf("timing mode %s held (%s)", t.To, t.Reason)
	}
	return fmt.Sprintf("timing mode %s -> %s (%s)", t.From, t.To, t.Reason)
}
