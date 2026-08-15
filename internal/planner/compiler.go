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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

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
	normalized := normalizeStructuralInputs(structural)
	diagnostics := validateStructuralInputs(normalized)
	if hasError(diagnostics) {
		return Plan{}, diagnostics, ErrRejected
	}

	var plan Plan
	if len(normalized.Groups) > 0 {
		plan.Graph = buildGroupGraph(normalized.Groups, normalized.TierPolicy, normalized.GroupNodes)
		steps, waves, duration := compileGroups(normalized.Groups, plan.Graph)
		plan.Steps = steps
		plan.Waves = waves
		plan.StartupWaves = advisoryStartupWaves(waves)
		plan.EstimatedDuration = Duration{Duration: duration}
		plan.GroupEstimates = compileGroupEstimates(normalized.Groups, history)
		plan.EstimateConfidence = summarizeEstimateConfidence(plan.GroupEstimates)
		// Published beside EstimatedDuration rather than replacing it, and deliberately
		// outside the plan hash.
		//
		// Plan identity has to mean "this plan", not "this plan as currently estimated".
		// Folding history into the hash would also close a loop on itself: history is
		// looked up by plan hash, so an estimate that moved the hash would change which
		// history the next compile finds, which would move the estimate again. Keeping
		// the declared total in the hash breaks that, and keeps an existing deployment's
		// plan identity fixed when this mechanism starts producing numbers.
		if observed, changed := observedPlanDuration(waves, plan.GroupEstimates); changed {
			plan.ObservedDuration = Duration{Duration: observed}
		}
	} else {
		plan.Graph = buildStepGraph(normalized.Steps)
		steps, duration := compileSteps(normalized.Steps)
		plan.Steps = steps
		plan.EstimatedDuration = Duration{Duration: duration}
	}
	plan.BlockedNodes = blockedNodesFromInversions(detectTierInversions(normalized))
	plan.Explanations = graphExplanations(plan.Graph, len(plan.Waves), len(plan.StartupWaves))
	plan.Diagrams = renderDiagramExports(plan.Graph)
	plan.Feasibility = advisoryFeasibility(telemetry, normalized.DeviceCapabilities)
	plan.StructuralHash = stableHash(normalized)
	plan.Hash = stableHash(struct {
		StructuralHash string         `json:"structuralHash"`
		Steps          []CompiledStep `json:"steps,omitempty"`
		Waves          []Wave         `json:"waves,omitempty"`
		StartupWaves   []Wave         `json:"startupWaves,omitempty"`
		Graph          Graph          `json:"graph,omitempty"`
		Duration       Duration       `json:"estimatedDuration,omitempty"`
	}{
		StructuralHash: plan.StructuralHash,
		Steps:          plan.Steps,
		Waves:          plan.Waves,
		StartupWaves:   plan.StartupWaves,
		Graph:          plan.Graph,
		Duration:       plan.EstimatedDuration,
	})

	return plan, diagnostics, nil
}

func validateStructuralInputs(input StructuralInputs) []Diagnostic {
	var diagnostics []Diagnostic
	diagnostics = append(diagnostics, validateTierPolicy(input.TierPolicy)...)
	diagnostics = append(diagnostics, validateTriggerCapabilities(input)...)
	if len(input.Triggers) == 0 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "TriggersRequired",
			Message:  "at least one trigger definition is required",
		})
	}
	if len(input.Groups) == 0 && len(input.Steps) == 0 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "PlanRequired",
			Message:  "groups or steps require at least one shutdown action",
		})
	}
	if len(input.Groups) > 0 && len(input.Steps) > 0 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "GroupsPreferred",
			Message:  "groups are present, so linear steps are ignored",
		})
	}

	stepIDs := map[string]struct{}{}
	for _, step := range input.Steps {
		if step.ID == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "StepIDRequired",
				Message:  "every shutdown step requires an id",
			})
			continue
		}
		if _, exists := stepIDs[step.ID]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateStepID",
				Subject:  step.ID,
				Message:  fmt.Sprintf("shutdown step id %q is duplicated", step.ID),
			})
		}
		stepIDs[step.ID] = struct{}{}
	}

	groupNames := map[string]struct{}{}
	for _, group := range input.Groups {
		if group.Name == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "GroupNameRequired",
				Message:  "every shutdown group requires a name",
			})
			continue
		}
		if _, exists := groupNames[group.Name]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateGroupName",
				Subject:  group.Name,
				Message:  fmt.Sprintf("shutdown group name %q is duplicated", group.Name),
			})
		}
		groupNames[group.Name] = struct{}{}
	}
	diagnostics = append(diagnostics, validateGroupShutdownTiers(input.Groups)...)
	diagnostics = append(diagnostics, reportDefaultedShutdownTiers(input)...)
	diagnostics = append(diagnostics, validateTierInversion(input)...)
	for _, group := range input.Groups {
		for _, dependency := range append(append([]string{}, group.Requires...), append(group.Before, group.After...)...) {
			if _, exists := groupNames[dependency]; !exists {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "UnknownDependency",
					Subject:  group.Name,
					Message:  fmt.Sprintf("shutdown group %q references unknown dependency %q", group.Name, dependency),
				})
			}
		}
	}
	if hasGroupCycle(input.Groups, input.TierPolicy, input.GroupNodes) {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "DependencyCycle",
			Message:  "shutdown groups contain a dependency cycle",
		})
	}

	return diagnostics
}

func compileGroups(groups []Group, graph Graph) ([]CompiledStep, []Wave, time.Duration) {
	byName := map[string]Group{}
	indegree := map[string]int{}
	edges := graphSuccessors(graph)
	tiers := graphShutdownTiers(graph)
	for _, group := range groups {
		byName[group.Name] = group
		indegree[group.Name] = 0
	}
	for _, successors := range edges {
		for _, successor := range successors {
			indegree[successor]++
		}
	}

	var waves []Wave
	var steps []CompiledStep
	var cumulative time.Duration
	var stepIndex int32
	var waveIndex int32

	for len(indegree) > 0 {
		ready := make([]Group, 0)
		for name, count := range indegree {
			if count == 0 {
				ready = append(ready, byName[name])
			}
		}
		sort.Slice(ready, func(i, j int) bool {
			phaseI, phaseJ := groupPhase(ready[i]), groupPhase(ready[j])
			if phaseI != phaseJ {
				return phaseI < phaseJ
			}
			return ready[i].Name < ready[j].Name
		})

		phase := groupPhase(ready[0])
		waveGroups := make([]Group, 0, len(ready))
		for _, group := range ready {
			if groupPhase(group) == phase {
				waveGroups = append(waveGroups, group)
			}
		}

		var waveDuration time.Duration
		for _, group := range waveGroups {
			if group.Timeout.Duration > waveDuration {
				waveDuration = group.Timeout.Duration
			}
		}

		groupNames := make([]string, 0, len(waveGroups))
		for _, group := range waveGroups {
			groupNames = append(groupNames, group.Name)
			steps = append(steps, CompiledStep{
				ID:                 group.Name,
				Index:              stepIndex,
				Action:             group.Action,
				ShutdownTier:       shutdownTierPtr(group.Name, tiers),
				TargetSummary:      summarizeTarget(group.Target),
				CumulativeDuration: Duration{Duration: cumulative + waveDuration},
			})
			stepIndex++
		}

		cumulative += waveDuration
		wave := Wave{
			Index:              waveIndex,
			Groups:             groupNames,
			Duration:           Duration{Duration: waveDuration},
			CumulativeDuration: Duration{Duration: cumulative},
		}
		if phase != 0 {
			phaseCopy := phase
			wave.Phase = &phaseCopy
		}
		wave.ShutdownTier = sharedShutdownTier(groupNames, tiers)
		waves = append(waves, wave)
		waveIndex++

		for _, group := range waveGroups {
			delete(indegree, group.Name)
			for _, successor := range edges[group.Name] {
				indegree[successor]--
			}
		}
	}

	return steps, waves, cumulative
}

func compileSteps(steps []Step) ([]CompiledStep, time.Duration) {
	compiled := make([]CompiledStep, 0, len(steps))
	var cumulative time.Duration
	for i, step := range steps {
		if step.Duration.Duration > 0 {
			cumulative += step.Duration.Duration
		}
		if step.Timeout.Duration > 0 && step.Action != "Wait" {
			cumulative += step.Timeout.Duration
		}
		compiled = append(compiled, CompiledStep{
			ID:                 step.ID,
			Index:              int32(i),
			Action:             step.Action,
			TargetSummary:      summarizeTarget(step.Target),
			CumulativeDuration: Duration{Duration: cumulative},
		})
	}
	return compiled, cumulative
}

func groupEdges(groups []Group, policy TierPolicy, membership []GroupNodeMembership) map[string][]string {
	return graphSuccessors(buildGroupGraph(groups, policy, membership))
}

func hasGroupCycle(groups []Group, policy TierPolicy, membership []GroupNodeMembership) bool {
	edges := groupEdges(groups, policy, membership)
	visiting := map[string]bool{}
	visited := map[string]bool{}

	var visit func(string) bool
	visit = func(name string) bool {
		if visiting[name] {
			return true
		}
		if visited[name] {
			return false
		}
		visiting[name] = true
		for _, next := range edges[name] {
			if visit(next) {
				return true
			}
		}
		visiting[name] = false
		visited[name] = true
		return false
	}

	for _, group := range groups {
		if visit(group.Name) {
			return true
		}
	}
	return false
}

func groupPhase(group Group) int32 {
	if group.Phase == nil {
		return 0
	}
	return *group.Phase
}

func summarizeTarget(target Target) string {
	switch {
	case target.NodeSelector:
		return "nodes selected by label selector"
	case target.NamespaceSelector:
		return "namespaces selected by label selector"
	case target.WorkloadSelector:
		return "workloads selected by label selector"
	case target.AgentRefCount > 0:
		return fmt.Sprintf("%d node power agent fleet(s)", target.AgentRefCount)
	case target.WorkloadRefCount > 0:
		return fmt.Sprintf("%d workload(s)", target.WorkloadRefCount)
	case target.NamespaceCount > 0:
		return fmt.Sprintf("%d namespace(s)", target.NamespaceCount)
	default:
		return "no explicit target"
	}
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

func normalizeStructuralInputs(input StructuralInputs) StructuralInputs {
	normalized := StructuralInputs{
		SourceID:          input.SourceID,
		ObservedAt:        input.ObservedAt,
		ResolvedInputHash: input.ResolvedInputHash,
		TierPolicy:        normalizeTierPolicy(input.TierPolicy),
		AbortBehavior:     input.AbortBehavior,
		Triggers:          append([]Trigger(nil), input.Triggers...),
		Groups:            append([]Group(nil), input.Groups...),
		Steps:             append([]Step(nil), input.Steps...),

		DeviceCapabilities: append([]DeviceCapability(nil), input.DeviceCapabilities...),
		PowerDomains:       append([]PowerDomainMembership(nil), input.PowerDomains...),
		GroupNodes:         append([]GroupNodeMembership(nil), input.GroupNodes...),
		NodeTiers:          append([]NodeTier(nil), input.NodeTiers...),
		HookDigests:        append([]HookDigest(nil), input.HookDigests...),
	}
	sort.SliceStable(normalized.NodeTiers, func(left, right int) bool {
		return normalized.NodeTiers[left].Name < normalized.NodeTiers[right].Name
	})
	for i := range normalized.GroupNodes {
		normalized.GroupNodes[i].Acts = append([]string(nil), normalized.GroupNodes[i].Acts...)
		normalized.GroupNodes[i].Releases = append([]string(nil), normalized.GroupNodes[i].Releases...)
		sort.Strings(normalized.GroupNodes[i].Acts)
		sort.Strings(normalized.GroupNodes[i].Releases)
	}
	sort.SliceStable(normalized.GroupNodes, func(left, right int) bool {
		return normalized.GroupNodes[left].Group < normalized.GroupNodes[right].Group
	})
	for i := range normalized.DeviceCapabilities {
		normalized.DeviceCapabilities[i].TelemetryVariables = append([]string(nil), normalized.DeviceCapabilities[i].TelemetryVariables...)
		sort.Strings(normalized.DeviceCapabilities[i].TelemetryVariables)
	}
	sort.SliceStable(normalized.DeviceCapabilities, func(left, right int) bool {
		return normalized.DeviceCapabilities[left].DeviceID < normalized.DeviceCapabilities[right].DeviceID
	})
	for i := range normalized.PowerDomains {
		normalized.PowerDomains[i].UPSDevices = append([]string(nil), normalized.PowerDomains[i].UPSDevices...)
		normalized.PowerDomains[i].Members = append([]string(nil), normalized.PowerDomains[i].Members...)
		normalized.PowerDomains[i].Nodes = append([]string(nil), normalized.PowerDomains[i].Nodes...)
		normalized.PowerDomains[i].Infrastructure = append([]string(nil), normalized.PowerDomains[i].Infrastructure...)
		sort.Strings(normalized.PowerDomains[i].UPSDevices)
		sort.Strings(normalized.PowerDomains[i].Members)
		sort.Strings(normalized.PowerDomains[i].Nodes)
		sort.Strings(normalized.PowerDomains[i].Infrastructure)
	}
	sort.SliceStable(normalized.PowerDomains, func(left, right int) bool {
		return normalized.PowerDomains[left].Name < normalized.PowerDomains[right].Name
	})
	sort.SliceStable(normalized.HookDigests, func(left, right int) bool {
		if normalized.HookDigests[left].Namespace == normalized.HookDigests[right].Namespace {
			return normalized.HookDigests[left].Name < normalized.HookDigests[right].Name
		}
		return normalized.HookDigests[left].Namespace < normalized.HookDigests[right].Namespace
	})
	for i := range normalized.Triggers {
		normalized.Triggers[i].UPSDevices = append([]string(nil), normalized.Triggers[i].UPSDevices...)
		normalized.Triggers[i].PowerDomains = append([]string(nil), normalized.Triggers[i].PowerDomains...)
		sort.Strings(normalized.Triggers[i].UPSDevices)
		sort.Strings(normalized.Triggers[i].PowerDomains)
	}
	for i := range normalized.Groups {
		normalized.Groups[i].Requires = append([]string(nil), normalized.Groups[i].Requires...)
		normalized.Groups[i].Before = append([]string(nil), normalized.Groups[i].Before...)
		normalized.Groups[i].After = append([]string(nil), normalized.Groups[i].After...)
		normalized.Groups[i].Phase = copyInt32Ptr(normalized.Groups[i].Phase)
		normalized.Groups[i].ShutdownTier = copyInt32Ptr(normalized.Groups[i].ShutdownTier)
		normalized.Groups[i].HookRef = copyHookReference(normalized.Groups[i].HookRef)
		normalized.Groups[i].Params = copyStringMap(normalized.Groups[i].Params)
		sort.Strings(normalized.Groups[i].Requires)
		sort.Strings(normalized.Groups[i].Before)
		sort.Strings(normalized.Groups[i].After)
	}
	for i := range normalized.Steps {
		normalized.Steps[i].HookRef = copyHookReference(normalized.Steps[i].HookRef)
		normalized.Steps[i].Params = copyStringMap(normalized.Steps[i].Params)
	}
	sortTriggers(normalized.Triggers)
	sortGroups(normalized.Groups)
	return normalized
}

func copyHookReference(input *HookReference) *HookReference {
	if input == nil {
		return nil
	}
	output := *input
	return &output
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func sortTriggers(triggers []Trigger) {
	sort.SliceStable(triggers, func(i, j int) bool {
		left, right := stableHash(triggers[i]), stableHash(triggers[j])
		return left < right
	})
}

func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})
}

func hasError(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == DiagnosticError {
			return true
		}
	}
	return false
}

func stableHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("planner input could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}
