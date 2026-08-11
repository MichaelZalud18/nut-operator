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

// Package trigger evaluates shutdown trigger conditions against normalized UPS
// state. It is pure logic: callers provide the clock value, telemetry, and prior
// hold state explicitly.
package trigger

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

const (
	TypeOnBattery      = "OnBattery"
	TypeLowBattery     = "LowBattery"
	TypeRuntimeBelow   = "RuntimeBelow"
	TypeChargeBelow    = "ChargeBelow"
	TypeTelemetryStale = "TelemetryStale"

	DiagnosticWarning = "Warning"
	DiagnosticError   = "Error"
	// DiagnosticInfo records something worth seeing in the evaluation trail that is not a problem.
	// A declared fallback firing is the case it exists for: expected behavior, but the device is
	// acting on a different condition than the trigger names, which belongs in the record.
	DiagnosticInfo = "Info"
)

// Inputs are the complete trigger-evaluation state for one decision tick.
type Inputs struct {
	ObservedAt time.Time
	Triggers   []Trigger
	UPSStates  []UPSState
	Holds      []HoldState
}

// Trigger is the evaluator-side trigger definition.
type Trigger struct {
	ID   string
	Type string
	// FallbackType is the coarser class evaluated for a device that does not report the telemetry
	// Type needs (OD-9). Empty means no substitution: such a device simply does not match.
	FallbackType        string
	UPSDevices          []string
	PowerDomains        []string
	For                 time.Duration
	RuntimeBelowSeconds *int64
	ChargeBelowPercent  *int32
}

// UPSState is the evaluator-side view of live UPS telemetry.
type UPSState struct {
	UPSDevice      string
	PowerDomains   []string
	ObservedAt     time.Time
	Phase          string
	OnBattery      bool
	LowBattery     bool
	Stale          bool
	RuntimeSeconds *int64
	ChargePercent  *int32
}

// HoldState records when one trigger-device condition first became true.
type HoldState struct {
	TriggerID string
	UPSDevice string
	StartedAt time.Time
}

// Evaluation summarizes all trigger decisions for one tick.
type Evaluation struct {
	ObservedAt          time.Time
	Approved            bool
	Reason              string
	SelectedUPSDevices  []string
	Decisions           []Decision
	Holds               []HoldState
	Diagnostics         []Diagnostic
	MatchedTriggerCount int
}

// Decision is the result for one configured trigger.
type Decision struct {
	TriggerID          string
	Type               string
	Matched            bool
	Eligible           bool
	Reason             string
	SelectedUPSDevices []string
	HoldStartedAt      *time.Time
	EligibleAt         *time.Time
}

// Diagnostic records a non-fatal warning or evaluator rejection.
type Diagnostic struct {
	Severity string
	Reason   string
	Subject  string
	Message  string
}

// Evaluate resolves all triggers against the supplied UPS state. A flow is
// approved for the next stage when any trigger is currently eligible.
func Evaluate(inputs Inputs) Evaluation {
	states := normalizeUPSStates(inputs.UPSStates)
	holds := holdIndex(inputs.Holds)
	evaluation := Evaluation{ObservedAt: inputs.ObservedAt.UTC()}

	selectedForEvaluation := map[string]struct{}{}
	for index, inputTrigger := range inputs.Triggers {
		trigger := normalizeTrigger(index, inputTrigger)
		decision, nextHolds, diagnostics := evaluateTrigger(evaluation.ObservedAt, trigger, states, holds)
		evaluation.Decisions = append(evaluation.Decisions, decision)
		evaluation.Diagnostics = append(evaluation.Diagnostics, diagnostics...)
		evaluation.Holds = append(evaluation.Holds, nextHolds...)
		if decision.Matched {
			evaluation.MatchedTriggerCount++
		}
		if decision.Eligible {
			evaluation.Approved = true
			for _, device := range decision.SelectedUPSDevices {
				selectedForEvaluation[device] = struct{}{}
			}
		}
	}

	evaluation.SelectedUPSDevices = sortedKeys(selectedForEvaluation)
	sortHolds(evaluation.Holds)
	evaluation.Reason = evaluationReason(evaluation)
	return evaluation
}

func evaluateTrigger(observedAt time.Time, trigger Trigger, states []UPSState, holds map[string]HoldState) (Decision, []HoldState, []Diagnostic) {
	decision := Decision{
		TriggerID: trigger.ID,
		Type:      trigger.Type,
		Reason:    "ConditionFalse",
	}
	candidates := selectUPSStates(trigger, states)
	if len(candidates) == 0 {
		return decision, nil, []Diagnostic{{
			Severity: DiagnosticWarning,
			Reason:   "TriggerSelectionEmpty",
			Subject:  trigger.ID,
			Message:  fmt.Sprintf("trigger %q selected no UPS telemetry states", trigger.ID),
		}}
	}

	var diagnostics []Diagnostic
	var nextHolds []HoldState
	var matched []string
	var earliestHold *time.Time
	var earliestEligible *time.Time
	for _, state := range candidates {
		matches, stateDiagnostics := triggerMatchesState(trigger, state)
		diagnostics = append(diagnostics, stateDiagnostics...)
		if !matches {
			continue
		}
		matched = append(matched, state.UPSDevice)
		startedAt := observedAt
		if existing, ok := holds[holdKey(trigger.ID, state.UPSDevice)]; ok {
			startedAt = existing.StartedAt.UTC()
		}
		nextHolds = append(nextHolds, HoldState{
			TriggerID: trigger.ID,
			UPSDevice: state.UPSDevice,
			StartedAt: startedAt,
		})
		eligibleAt := startedAt.Add(trigger.For).UTC()
		if earliestHold == nil || startedAt.Before(*earliestHold) {
			holdCopy := startedAt
			earliestHold = &holdCopy
		}
		if earliestEligible == nil || eligibleAt.Before(*earliestEligible) {
			eligibleCopy := eligibleAt
			earliestEligible = &eligibleCopy
		}
		if trigger.For <= 0 || !observedAt.Before(eligibleAt) {
			decision.Eligible = true
		}
	}

	sort.Strings(matched)
	decision.SelectedUPSDevices = matched
	decision.Matched = len(matched) > 0
	decision.HoldStartedAt = earliestHold
	decision.EligibleAt = earliestEligible
	switch {
	case decision.Eligible:
		decision.Reason = "TriggerEligible"
	case decision.Matched:
		decision.Reason = "TriggerHoldPending"
	default:
		decision.Reason = "ConditionFalse"
	}
	return decision, nextHolds, diagnostics
}

// matchOutcome distinguishes "this device says no" from "this device cannot
// answer the question", which is the distinction OD-9 substitution turns on.
type matchOutcome int

const (
	outcomeConditionFalse matchOutcome = iota
	outcomeMatched
	outcomeTelemetryUnavailable
)

// triggerMatchesState evaluates one trigger against one device, substituting the
// declared fallback when the device cannot report the telemetry the primary class
// needs (OD-9).
//
// Substitution keys off the value actually being absent at this tick rather than
// off the capability profile. A profile is a declaration about a model; this is
// the device in front of us, and a driver that declares battery.runtime and then
// reports nothing is exactly the case the fallback exists for.
func triggerMatchesState(trigger Trigger, state UPSState) (bool, []Diagnostic) {
	outcome, diagnostics := evaluateTriggerClass(trigger.Type, trigger, state)
	if outcome != outcomeTelemetryUnavailable || trigger.FallbackType == "" {
		return outcome == outcomeMatched, diagnostics
	}

	fallbackOutcome, fallbackDiagnostics := evaluateTriggerClass(trigger.FallbackType, trigger, state)
	if fallbackOutcome == outcomeTelemetryUnavailable {
		// The fallback cannot answer either. Report both gaps rather than substituting silently:
		// compilation rejects this combination, so seeing it at runtime means the plan was compiled
		// against a different capability picture than the device now presents.
		return false, append(diagnostics, fallbackDiagnostics...)
	}

	// The primary's missing-telemetry warning is replaced, not added to. The absence is expected and
	// handled here, and re-reporting it every tick would train operators to ignore the one diagnostic
	// that means a device went dark unexpectedly.
	substitution := Diagnostic{
		Severity: DiagnosticInfo,
		Reason:   "TriggerFallbackApplied",
		Subject:  trigger.ID,
		Message: fmt.Sprintf("trigger %q evaluated %q for %q, which does not report the telemetry %q needs",
			trigger.ID, trigger.FallbackType, state.UPSDevice, trigger.Type),
	}
	return fallbackOutcome == outcomeMatched, append(fallbackDiagnostics, substitution)
}

// evaluateTriggerClass evaluates one trigger class against one device's state.
//
// triggerType is passed separately from trigger so the fallback class can be run
// against the same trigger definition and device.
func evaluateTriggerClass(triggerType string, trigger Trigger, state UPSState) (matchOutcome, []Diagnostic) {
	switch triggerType {
	case TypeOnBattery:
		return boolOutcome(state.OnBattery ||
			state.Phase == telemetry.PhaseOnBattery || state.Phase == telemetry.PhaseLowBattery), nil
	case TypeLowBattery:
		return boolOutcome(state.LowBattery || state.Phase == telemetry.PhaseLowBattery), nil
	case TypeRuntimeBelow:
		// A missing threshold is a misconfigured flow, not a device that cannot answer, so it never
		// substitutes -- falling back here would paper over an authoring mistake.
		if trigger.RuntimeBelowSeconds == nil {
			return outcomeConditionFalse,
				[]Diagnostic{missingTriggerValue(trigger.ID, "RuntimeThresholdMissing", "runtimeBelowSeconds")}
		}
		if state.RuntimeSeconds == nil {
			return outcomeTelemetryUnavailable,
				[]Diagnostic{missingTelemetryValue(trigger.ID, state.UPSDevice, "RuntimeTelemetryMissing", "runtime remaining")}
		}
		return boolOutcome(*state.RuntimeSeconds <= *trigger.RuntimeBelowSeconds), nil
	case TypeChargeBelow:
		if trigger.ChargeBelowPercent == nil {
			return outcomeConditionFalse,
				[]Diagnostic{missingTriggerValue(trigger.ID, "ChargeThresholdMissing", "chargeBelowPercent")}
		}
		if state.ChargePercent == nil {
			return outcomeTelemetryUnavailable,
				[]Diagnostic{missingTelemetryValue(trigger.ID, state.UPSDevice, "ChargeTelemetryMissing", "battery charge")}
		}
		return boolOutcome(*state.ChargePercent <= *trigger.ChargeBelowPercent), nil
	case TypeTelemetryStale:
		return boolOutcome(state.Stale ||
			state.Phase == telemetry.PhaseStale || state.Phase == telemetry.PhaseUnavailable), nil
	default:
		return outcomeConditionFalse, []Diagnostic{{
			Severity: DiagnosticError,
			Reason:   "TriggerTypeUnsupported",
			Subject:  trigger.ID,
			Message:  fmt.Sprintf("trigger %q has unsupported type %q", trigger.ID, triggerType),
		}}
	}
}

func boolOutcome(matched bool) matchOutcome {
	if matched {
		return outcomeMatched
	}
	return outcomeConditionFalse
}

func selectUPSStates(trigger Trigger, states []UPSState) []UPSState {
	deviceSet := stringSet(trigger.UPSDevices)
	domainSet := stringSet(trigger.PowerDomains)
	if len(deviceSet) == 0 && len(domainSet) == 0 {
		return states
	}
	selected := make([]UPSState, 0)
	for _, state := range states {
		if _, ok := deviceSet[state.UPSDevice]; ok {
			selected = append(selected, state)
			continue
		}
		for _, domain := range state.PowerDomains {
			if _, ok := domainSet[domain]; ok {
				selected = append(selected, state)
				break
			}
		}
	}
	return selected
}

func normalizeTrigger(index int, trigger Trigger) Trigger {
	trigger.ID = strings.TrimSpace(trigger.ID)
	trigger.Type = strings.TrimSpace(trigger.Type)
	if trigger.ID == "" {
		trigger.ID = fmt.Sprintf("trigger-%03d-%s", index, strings.ToLower(trigger.Type))
	}
	trigger.UPSDevices = sortedUniqueStrings(trigger.UPSDevices)
	trigger.PowerDomains = sortedUniqueStrings(trigger.PowerDomains)
	return trigger
}

func normalizeUPSStates(states []UPSState) []UPSState {
	normalized := make([]UPSState, 0, len(states))
	for _, state := range states {
		state.UPSDevice = strings.TrimSpace(state.UPSDevice)
		if state.UPSDevice == "" {
			continue
		}
		state.PowerDomains = sortedUniqueStrings(state.PowerDomains)
		state.ObservedAt = state.ObservedAt.UTC()
		normalized = append(normalized, state)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].UPSDevice < normalized[j].UPSDevice
	})
	return normalized
}

func holdIndex(holds []HoldState) map[string]HoldState {
	index := map[string]HoldState{}
	for _, hold := range holds {
		hold.TriggerID = strings.TrimSpace(hold.TriggerID)
		hold.UPSDevice = strings.TrimSpace(hold.UPSDevice)
		if hold.TriggerID == "" || hold.UPSDevice == "" {
			continue
		}
		hold.StartedAt = hold.StartedAt.UTC()
		index[holdKey(hold.TriggerID, hold.UPSDevice)] = hold
	}
	return index
}

func holdKey(triggerID, upsDevice string) string {
	return triggerID + "\x00" + upsDevice
}

func missingTriggerValue(triggerID, reason, field string) Diagnostic {
	return Diagnostic{
		Severity: DiagnosticError,
		Reason:   reason,
		Subject:  triggerID,
		Message:  fmt.Sprintf("trigger %q requires %s", triggerID, field),
	}
}

func missingTelemetryValue(triggerID, upsDevice, reason, valueName string) Diagnostic {
	return Diagnostic{
		Severity: DiagnosticWarning,
		Reason:   reason,
		Subject:  upsDevice,
		Message:  fmt.Sprintf("trigger %q selected UPSDevice %q but %s telemetry is missing", triggerID, upsDevice, valueName),
	}
}

func evaluationReason(evaluation Evaluation) string {
	switch {
	case evaluation.Approved:
		return "TriggerEligible"
	case len(evaluation.Holds) > 0:
		return "TriggerHoldPending"
	case len(evaluation.Decisions) == 0:
		return "NoTriggersConfigured"
	default:
		return "NoTriggerMatched"
	}
}

func stringSet(values []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			set[trimmed] = struct{}{}
		}
	}
	return set
}

func sortedUniqueStrings(values []string) []string {
	set := stringSet(values)
	return sortedKeys(set)
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortHolds(holds []HoldState) {
	sort.Slice(holds, func(i, j int) bool {
		if holds[i].TriggerID != holds[j].TriggerID {
			return holds[i].TriggerID < holds[j].TriggerID
		}
		return holds[i].UPSDevice < holds[j].UPSDevice
	})
}
