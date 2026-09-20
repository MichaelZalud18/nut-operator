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

package planner

import (
	"errors"

	"github.com/MichaelZalud18/nut-operator/internal/capability"
)

const (
	DiagnosticError   = "Error"
	DiagnosticWarning = "Warning"
	// DiagnosticInfo states something true and worth seeing that is neither a
	// failure nor a risk -- a default that was applied, for instance. It never
	// affects acceptance or degradation.
	DiagnosticInfo = "Info"

	FeasibilityAdvisoryUnknown = "Unknown"
	FeasibilityAdvisoryOK      = "OK"
)

var ErrRejected = errors.New("planner rejected structural inputs")

// Compile turns structural and telemetry inputs into a deterministic plan. It
// never reads ambient state. Telemetry influences advisory feasibility only, not
// structural or plan hashes.
func Compile(structural StructuralInputs, telemetry TelemetryInputs) (Plan, []Diagnostic, error) {
	return CompileWithHistory(structural, telemetry, HistoryInputs{})
}

// CompileWithHistory compiles with observed durations folded into the estimates
// (EX-32). Compile is this with no history, which is what a cluster that has never
// run a flow legitimately has.
func CompileWithHistory(structural StructuralInputs, telemetry TelemetryInputs, history HistoryInputs) (Plan, []Diagnostic, error) {
	normalized, diagnostics, err := prepareStructuralInputs(structural)
	if err != nil {
		return Plan{}, diagnostics, err
	}
	index := newCommunicationIndex(normalized)
	scoped, scopeDiagnostics := scopeStructuralInputsWithIndex(normalized, index)
	diagnostics = validateStructuralInputsWithIndex(scoped, index)
	diagnostics = append(diagnostics, communicationDiagnosticsWithIndex(scoped, index)...)
	diagnostics = append(diagnostics, scopeDiagnostics...)
	if hasError(diagnostics) {
		return Plan{}, diagnostics, ErrRejected
	}

	plan, schedulingDiagnostics := compileSchedule(scoped, index)
	diagnostics = append(diagnostics, schedulingDiagnostics...)
	if hasError(diagnostics) {
		return Plan{}, diagnostics, ErrRejected
	}
	if len(scoped.Groups) > 0 {
		applyGroupEstimates(&plan, scoped.Groups, history)
	}
	plan.PowerDomains = powerDomainArtifacts(scoped.PowerDomains)
	diagnostics = append(diagnostics, controlPlaneDiagnostics(scoped, plan)...)
	if hasError(diagnostics) {
		return Plan{}, diagnostics, ErrRejected
	}
	plan.CommunicationBudget = communicationBudgetWithIndex(scoped, index)
	plan.BlockedNodes = blockedNodesFromInversions(detectTierInversions(scoped))
	plan.Explanations = graphExplanations(plan.Graph, len(plan.Waves), len(plan.StartupWaves))
	plan.Explanations = append(plan.Explanations, communicationExplanationsWithIndex(scoped, index)...)
	plan.Diagrams = renderDiagramExports(plan.Graph)
	plan.Feasibility = advisoryFeasibility(telemetry, scoped.DeviceCapabilities)
	diagnostics = append(diagnostics, hashPlan(&plan, scoped)...)
	if hasError(diagnostics) {
		return Plan{}, diagnostics, ErrRejected
	}

	return plan, diagnostics, nil
}

func powerDomainArtifacts(domains []PowerDomainMembership) []PowerDomainArtifact {
	if len(domains) == 0 {
		return nil
	}
	artifacts := make([]PowerDomainArtifact, 0, len(domains))
	for _, domain := range domains {
		artifacts = append(artifacts, PowerDomainArtifact{
			Name:           domain.Name,
			UPSDevices:     append([]string(nil), domain.UPSDevices...),
			Members:        append([]string(nil), domain.Members...),
			Nodes:          append([]string(nil), domain.Nodes...),
			Infrastructure: append([]string(nil), domain.Infrastructure...),
		})
	}
	return artifacts
}

func advisoryFeasibility(telemetry TelemetryInputs, devices []DeviceCapability) Feasibility {
	if len(telemetry.PowerDomains) == 0 {
		return Feasibility{Verdict: FeasibilityAdvisoryUnknown, Reason: "TelemetryUnavailable"}
	}
	for _, snapshot := range telemetry.PowerDomains {
		if snapshot.Stale || snapshot.RuntimeRemainingSeconds == nil || snapshot.ChargePercent == nil || snapshot.OnBattery == nil {
			return Feasibility{Verdict: FeasibilityAdvisoryUnknown, Reason: "TelemetryIncomplete"}
		}
	}
	// A device whose firmware reports a fixed runtime estimate (CR-4) supplies a constant wearing a
	// projection's name. Feasibility exists to answer "is there enough runtime to finish", and that
	// question cannot be answered from a number that does not fall as the cluster draws on the
	// battery. PL-32 says missing or stale data never yields an optimistic verdict; a value that
	// cannot move is the same problem with a plausible number attached, so it lands on Unknown too.
	if device, static := firstStaticRuntimeEstimate(devices); static {
		return Feasibility{
			Verdict: FeasibilityAdvisoryUnknown,
			Reason:  "RuntimeEstimateStatic",
			Detail:  device,
		}
	}
	return Feasibility{Verdict: FeasibilityAdvisoryOK, Reason: "TelemetryPresent"}
}

// firstStaticRuntimeEstimate names the earliest device declaring a static runtime estimate.
//
// One device is enough. Domains are aggregated into a single runtime figure, so a static estimate
// anywhere in the set contaminates the total -- there is no partial version of this verdict.
func firstStaticRuntimeEstimate(devices []DeviceCapability) (string, bool) {
	for _, device := range devices {
		if device.RuntimeEstimate == string(capability.RuntimeEstimateStatic) {
			return device.DeviceID, true
		}
	}
	return "", false
}
