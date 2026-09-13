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
	"slices"
	"sort"
	"strings"
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
	normalized, err := normalizeStructuralInputs(structural)
	if err != nil {
		return Plan{}, []Diagnostic{{
			Severity: DiagnosticError,
			Reason:   "TriggerEncodingFailed",
			Message:  fmt.Sprintf("triggers could not be hashed for deterministic ordering: %v", err),
		}}, ErrRejected
	}
	scoped, scopeDiagnostics := scopeStructuralInputs(normalized)
	diagnostics := validateStructuralInputs(scoped)
	diagnostics = append(diagnostics, communicationDiagnostics(scoped)...)
	diagnostics = append(diagnostics, scopeDiagnostics...)
	if hasError(diagnostics) {
		return Plan{}, diagnostics, ErrRejected
	}

	var plan Plan
	if len(scoped.Groups) > 0 {
		plan.Graph = buildGroupGraph(scoped.Groups, scoped.TierPolicy, scoped.GroupNodes)
		steps, waves, duration, stalled := compileGroups(scoped.Groups, plan.Graph)
		// Rejected rather than published half-descended. A plan missing the groups that could not be
		// scheduled would still compile, still hash, and still look like a plan -- and the groups it
		// dropped are the ones nothing would then shut down.
		if len(stalled) > 0 {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "PlanNotSchedulable",
				Subject:  strings.Join(stalled, ","),
				Message: fmt.Sprintf(
					"shutdown groups %s never became schedulable, so wave descent could not finish; "+
						"this is a planner defect rather than a flow error, because a dependency cycle is "+
						"rejected as DependencyCycle before this point",
					strings.Join(stalled, ", ")),
			})
			return Plan{}, diagnostics, ErrRejected
		}
		plan.Steps = steps
		plan.Waves = waves
		plan.StartupWaves = advisoryStartupWaves(waves)
		plan.EstimatedDuration = Duration{Duration: duration}
		plan.GroupEstimates = compileGroupEstimates(scoped.Groups, history)
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
		plan.Graph = buildStepGraph(scoped.Steps)
		steps, duration := compileSteps(scoped.Steps)
		plan.Steps = steps
		plan.EstimatedDuration = Duration{Duration: duration}
	}
	plan.PowerDomains = powerDomainArtifacts(scoped.PowerDomains)
	plan.BlockedNodes = blockedNodesFromInversions(detectTierInversions(scoped))
	plan.Explanations = graphExplanations(plan.Graph, len(plan.Waves), len(plan.StartupWaves))
	plan.Explanations = append(plan.Explanations, communicationExplanations(scoped)...)
	plan.Diagrams = renderDiagramExports(plan.Graph)
	plan.Feasibility = advisoryFeasibility(telemetry, scoped.DeviceCapabilities)
	plan.StructuralHash, err = stableHash(scoped)
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "PlanHashEncodingFailed",
			Message:  fmt.Sprintf("plan structural hash could not be computed: %v", err),
		})
		return Plan{}, diagnostics, ErrRejected
	}
	plan.Hash, err = stableHash(struct {
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
	if err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "PlanHashEncodingFailed",
			Message:  fmt.Sprintf("plan hash could not be computed: %v", err),
		})
		return Plan{}, diagnostics, ErrRejected
	}

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
	// reportedStepIDs guards against F-121: a third or later occurrence of the same id must not
	// emit another diagnostic on top of the one already raised for the second.
	reportedStepIDs := map[string]struct{}{}
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
			if _, alreadyReported := reportedStepIDs[step.ID]; !alreadyReported {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "DuplicateStepID",
					Subject:  step.ID,
					Message:  fmt.Sprintf("shutdown step id %q is duplicated", step.ID),
				})
				reportedStepIDs[step.ID] = struct{}{}
			}
		}
		stepIDs[step.ID] = struct{}{}
	}

	groupNames := map[string]struct{}{}
	reportedGroupNames := map[string]struct{}{}
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
			if _, alreadyReported := reportedGroupNames[group.Name]; !alreadyReported {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "DuplicateGroupName",
					Subject:  group.Name,
					Message:  fmt.Sprintf("shutdown group name %q is duplicated", group.Name),
				})
				reportedGroupNames[group.Name] = struct{}{}
			}
		}
		groupNames[group.Name] = struct{}{}
	}
	diagnostics = append(diagnostics, validateGroupShutdownTiers(input.Groups)...)
	diagnostics = append(diagnostics, reportDefaultedShutdownTiers(input)...)
	diagnostics = append(diagnostics, validateTierInversion(input)...)
	for _, group := range input.Groups {
		// F-119: slices.Concat always allocates its own backing array, unlike
		// append(group.Before, group.After...), which can write into group.Before's backing
		// array in place when it has spare capacity -- harmless with today's callers, which
		// happen to hand it exactly-sized slices, but not a guarantee this expression's own
		// signature makes.
		for _, dependency := range slices.Concat(group.Requires, group.Before, group.After) {
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

// compileGroups descends the group graph into waves, and reports the groups it could not schedule
// rather than spinning on them (F-117).
//
// The last return is empty on success and holds every group still waiting when the descent stopped
// making progress. That can only happen if the graph has a cycle, which validateStructuralInputs
// already rejects with DependencyCycle from this same buildGroupGraph output -- so reaching it means
// the two disagreed, which is a defect in this package and not a mistake by a flow author.
//
// It is guarded anyway because of where the loop runs. This is a reconcile path during a power
// event, and the failure without a guard is not a wrong plan but no plan at all: the loop finds
// nothing at indegree zero, deletes nothing, and spins forever holding the worker. A hung reconcile
// reports nothing, times out nothing, and looks identical to a cluster that is simply slow -- at the
// moment when the operator has minutes of battery to work with. Two lines convert that into a
// diagnostic naming the groups involved.
func compileGroups(groups []Group, graph Graph) ([]CompiledStep, []Wave, time.Duration, []string) {
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
		// Every group whose dependencies are satisfied enters the same wave. A wave is the set of
		// work with nothing left to wait for, so anything that narrows it below that is claiming an
		// ordering constraint the author did not write. Ordering comes from tiers and from
		// before/after; the sort is presentation only, and byName keeps the compiled plan stable
		// across runs rather than following Go's map iteration order.
		waveGroups := make([]Group, 0, len(indegree))
		for name, count := range indegree {
			if count == 0 {
				waveGroups = append(waveGroups, byName[name])
			}
		}
		sort.Slice(waveGroups, func(i, j int) bool {
			return waveGroups[i].Name < waveGroups[j].Name
		})

		// Nothing schedulable while groups remain: the next pass would examine the same indegrees and
		// reach the same conclusion, so this is the loop's only exit that is not progress.
		if len(waveGroups) == 0 {
			stalled := make([]string, 0, len(indegree))
			for name := range indegree {
				stalled = append(stalled, name)
			}
			sort.Strings(stalled)
			return nil, nil, 0, stalled
		}

		var waveDuration time.Duration
		for _, group := range waveGroups {
			if duration := declaredGroupDuration(group); duration > waveDuration {
				waveDuration = duration
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

	return steps, waves, cumulative, nil
}

func compileSteps(steps []Step) ([]CompiledStep, time.Duration) {
	compiled := make([]CompiledStep, 0, len(steps))
	var cumulative time.Duration
	for i, step := range steps {
		if step.Action == "Wait" {
			cumulative += boundedWaitDuration(step.Duration.Duration, step.Timeout.Duration)
		} else if step.Duration.Duration > 0 {
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

func normalizeStructuralInputs(input StructuralInputs) (StructuralInputs, error) {
	normalized := StructuralInputs{
		SourceID:          input.SourceID,
		ObservedAt:        input.ObservedAt,
		ResolvedInputHash: input.ResolvedInputHash,
		TierPolicy:        normalizeTierPolicy(input.TierPolicy),
		AbortBehavior:     input.AbortBehavior,
		TierOverrunPolicy: input.TierOverrunPolicy,
		Triggers:          append([]Trigger(nil), input.Triggers...),
		Groups:            append([]Group(nil), input.Groups...),
		Steps:             append([]Step(nil), input.Steps...),

		DeviceCapabilities:        append([]DeviceCapability(nil), input.DeviceCapabilities...),
		PowerDomains:              append([]PowerDomainMembership(nil), input.PowerDomains...),
		CommunicationDependencies: normalizeCommunicationDependencies(input.CommunicationDependencies),
		GroupNodes:                append([]GroupNodeMembership(nil), input.GroupNodes...),
		NodeTiers:                 append([]NodeTier(nil), input.NodeTiers...),
		HookDigests:               append([]HookDigest(nil), input.HookDigests...),
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
	if err := sortTriggers(normalized.Triggers); err != nil {
		return StructuralInputs{}, err
	}
	sortGroups(normalized.Groups)
	return normalized, nil
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

// sortTriggers orders triggers deterministically by content hash. Each trigger's hash is computed
// once, up front, and carried alongside it through the sort (F-118) rather than recomputed inside
// the comparator on every call sort.SliceStable makes -- a hidden O(n log n) JSON-marshal-and-hash
// cost at every call site, for work with only n distinct answers.
func sortTriggers(triggers []Trigger) error {
	type hashedTrigger struct {
		trigger Trigger
		hash    string
	}
	decorated := make([]hashedTrigger, len(triggers))
	for i, trigger := range triggers {
		hash, err := stableHash(trigger)
		if err != nil {
			return fmt.Errorf("hash trigger %d for deterministic ordering: %w", i, err)
		}
		decorated[i] = hashedTrigger{trigger: trigger, hash: hash}
	}
	sort.SliceStable(decorated, func(i, j int) bool {
		return decorated[i].hash < decorated[j].hash
	})
	for i, d := range decorated {
		triggers[i] = d.trigger
	}
	return nil
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

// stableHash returns an error rather than panicking on an encoding failure (F-123). The input
// shapes make json.Marshal failing here effectively unreachable today -- everything hashed is
// plain structural data, no channels, funcs, or cycles -- but Compile already returns an error
// for every other rejection, this runs during power-event planning, and a panic mid-reconcile is
// a worse failure mode than a clean, diagnosable rejection for a "cannot happen" that turns out
// to happen anyway.
func stableHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode value for hashing: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
