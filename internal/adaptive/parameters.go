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

// Package adaptive implements the tier-pointer and timing-mode model from
// docs/design/adaptive-execution-tier-pointer.md (AE-1 through AE-6).
//
// Pure by construction, like internal/planner and internal/trigger: no I/O, no
// Kubernetes client, no wall-clock reads. Every function takes the observations
// and the current state and returns the next state plus what changed. The caller
// owns time, persistence, and publication.
//
// That split is what makes the model testable at all. The behavior worth being
// sure about -- that the pointer never ascends into re-executing work, that
// escalation is immediate and relaxation is not -- is a property of a state
// machine, and a state machine that reads the clock can only be tested by waiting.
//
// The package is deliberately three separable pieces:
//
//   - parameters.go — the OD-27 through OD-30 values, as data
//   - pointer.go    — AE-1 through AE-3: where the shutdown has progressed to
//   - timing.go     — timing-mode selection and its asymmetric hysteresis
//
// The pointer and the timing mode are independent. The design is explicit that
// timing adaptation is "independent of the pointer": one answers how far down the
// tiers to go, the other how much time to allow at each step. Keeping them in
// separate files with no shared mutable state is what stops the next change from
// quietly coupling them.
package adaptive

import "time"

// Parameters carries the tunables the design left open as OD-27 through OD-30.
//
// They are data rather than constants so the open decisions can be settled by
// choosing values, and so an operator-visible configuration surface can be added
// later without restructuring anything. Nothing in this package reads a global.
type Parameters struct {
	// EscalateAfterObservations is how many consecutive observations justify moving
	// to a more urgent timing mode. Fixed at 1 by AE's own rule -- escalation is
	// immediate -- and present as a field only so the asymmetry is visible next to
	// its counterpart rather than buried in code. (OD-27)
	EscalateAfterObservations int

	// RelaxAfterObservations is how many consecutive improved observations justify
	// moving to a less urgent mode. Greater than one by design: a single good
	// reading during an outage is as likely to be a measurement artifact as a
	// recovery, and relaxing on it hands time back that may not exist. (OD-27)
	RelaxAfterObservations int

	// RelaxImprovementMargin is how much better than the mode's own threshold a
	// reading must be before it counts as improvement. Without it, a value sitting
	// exactly on a boundary alternates modes on measurement noise alone. (OD-27)
	RelaxImprovementMargin float64

	// UrgentRuntimeSeconds and NominalRuntimeSeconds are the remaining-runtime
	// boundaries between modes. At or below Urgent the flow is racing the battery;
	// at or below Nominal it has margin but not comfort; above it, Relaxed.
	UrgentRuntimeSeconds  int64
	NominalRuntimeSeconds int64

	// IdleCadence and ActiveCadence are the AE-5 heartbeat intervals. They differ
	// because the value of a snapshot differs: during a flow a subscriber is
	// tracking progress, and between flows it is only confirming the publisher is
	// alive. (OD-30)
	IdleCadence   time.Duration
	ActiveCadence time.Duration

	// ReserveFraction is the share of remaining runtime held back from the plan's
	// own durations. The plan measures the work it declares; it does not measure the
	// node-agent handoff, the actuator's own checks, or the seconds between a node
	// being released and actually losing power. Compressing the plan to fill exactly
	// the measured runtime would spend that tail.
	ReserveFraction float64

	// MinimumCompression is the point below which the plan does not fit at all.
	// Compressing further would not make the shutdown finish; it would only produce
	// timeouts too short for any action to complete, converting a tight deadline
	// into a guaranteed group failure and engaging abort policy at the worst
	// possible moment. Reaching it is reported rather than silently applied.
	MinimumCompression float64

	// AssumedRuntimeUnknown is the runtime budget used when no trustworthy figure is
	// available. It is deliberately the urgent threshold rather than a separate
	// number: "I do not know how long I have" is treated as having exactly as much
	// time as the point at which the flow would already be racing (PL-32, AE-6).
	AssumedRuntimeUnknown time.Duration
}

// DefaultParameters returns the starting values for OD-27 through OD-30.
//
// These are defaults, not settled answers. They are chosen to be defensible and
// conservative rather than tuned, and the decisions stay open until they have been
// exercised against a real outage:
//
//   - Escalate on one observation, relax on three. Asymmetric on purpose: getting
//     more urgent too early costs a shorter grace period, getting less urgent too
//     early costs the shutdown not finishing.
//   - A 10% improvement margin, which is wider than typical UPS runtime jitter and
//     narrow enough that genuine recovery still registers.
//   - 300s urgent, 900s nominal. Five minutes is roughly the least time draining a
//     node and powering it off; fifteen gives a multi-wave flow room.
//   - 60s idle cadence, 10s active. Fast enough that a subscriber joining mid-flow
//     is not looking at stale state, slow enough not to be its own load.
//   - A 20% runtime reserve, covering the handoff and actuation tail the plan's own
//     durations do not measure.
//   - 10% minimum compression. Below that the plan is not being hurried, it is being
//     pretended.
func DefaultParameters() Parameters {
	return Parameters{
		EscalateAfterObservations: 1,
		RelaxAfterObservations:    3,
		RelaxImprovementMargin:    0.10,
		UrgentRuntimeSeconds:      300,
		NominalRuntimeSeconds:     900,
		IdleCadence:               60 * time.Second,
		ActiveCadence:             10 * time.Second,
		ReserveFraction:           0.20,
		MinimumCompression:        0.10,
		AssumedRuntimeUnknown:     300 * time.Second,
	}
}

// WithDefaults fills unset fields from DefaultParameters.
//
// Zero is not a meaningful value for any of these -- an escalation threshold of
// zero would escalate on nothing, a cadence of zero would publish continuously --
// so treating zero as "unset" is unambiguous rather than a guess.
func (p Parameters) WithDefaults() Parameters {
	defaults := DefaultParameters()
	if p.EscalateAfterObservations <= 0 {
		p.EscalateAfterObservations = defaults.EscalateAfterObservations
	}
	if p.RelaxAfterObservations <= 0 {
		p.RelaxAfterObservations = defaults.RelaxAfterObservations
	}
	if p.RelaxImprovementMargin <= 0 {
		p.RelaxImprovementMargin = defaults.RelaxImprovementMargin
	}
	if p.UrgentRuntimeSeconds <= 0 {
		p.UrgentRuntimeSeconds = defaults.UrgentRuntimeSeconds
	}
	if p.NominalRuntimeSeconds <= 0 {
		p.NominalRuntimeSeconds = defaults.NominalRuntimeSeconds
	}
	if p.IdleCadence <= 0 {
		p.IdleCadence = defaults.IdleCadence
	}
	if p.ActiveCadence <= 0 {
		p.ActiveCadence = defaults.ActiveCadence
	}
	if p.ReserveFraction <= 0 {
		p.ReserveFraction = defaults.ReserveFraction
	}
	if p.MinimumCompression <= 0 {
		p.MinimumCompression = defaults.MinimumCompression
	}
	if p.AssumedRuntimeUnknown <= 0 {
		// Derived from the urgent threshold rather than defaulted separately, so the two
		// cannot drift apart when an operator retunes one of them.
		p.AssumedRuntimeUnknown = time.Duration(p.UrgentRuntimeSeconds) * time.Second
	}
	return p
}

// Validate reports parameter combinations that cannot behave as intended.
//
// Returned as diagnostics rather than an error because this mirrors the planner's
// contract: the caller decides whether a problem rejects or degrades, and gets
// every finding at once instead of only the first.
func (p Parameters) Validate() []Diagnostic {
	var diagnostics []Diagnostic
	if p.UrgentRuntimeSeconds >= p.NominalRuntimeSeconds {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "TimingThresholdsInverted",
			Message: "urgent runtime threshold must be below the nominal one; " +
				"otherwise no reading can ever select Nominal",
		})
	}
	if p.RelaxAfterObservations <= p.EscalateAfterObservations {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "HysteresisNotAsymmetric",
			Message: "relaxing should need more observations than escalating; " +
				"symmetric thresholds let the mode oscillate on ordinary telemetry jitter",
		})
	}
	if p.RelaxImprovementMargin >= 1 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "ImprovementMarginUnreachable",
			Message: "an improvement margin of 100% or more can never be met, so the flow would " +
				"never relax once escalated",
		})
	}
	if p.ActiveCadence > p.IdleCadence {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "CadenceSlowerDuringFlow",
			Message: "publishing more slowly during a flow than at rest inverts AE-5: the active " +
				"period is when subscribers most need current state",
		})
	}
	if p.ReserveFraction >= 1 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "ReserveConsumesAllRuntime",
			Message: "reserving all remaining runtime leaves the plan no time to run in; the " +
				"reserve covers the handoff tail, not the shutdown itself",
		})
	}
	if p.MinimumCompression >= 1 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "MinimumCompressionRejectsEveryPlan",
			Message: "a minimum compression of 1 or more means no plan may ever be shortened, so " +
				"every plan that does not already fit is reported as not fitting",
		})
	}
	return diagnostics
}
