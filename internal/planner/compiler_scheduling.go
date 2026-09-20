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
	"fmt"
	"sort"
	"strings"
	"time"
)

func compileSchedule(input StructuralInputs, index *communicationIndex) (Plan, []Diagnostic) {
	var diagnostics []Diagnostic
	var plan Plan
	if len(input.Groups) > 0 {
		plan.Graph = buildGroupGraphForInputs(input, index)
		steps, waves, duration, stalled := compileGroups(input.Groups, plan.Graph)
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
			return Plan{}, diagnostics
		}
		plan.Steps = steps
		plan.Waves = waves
		plan.StartupWaves = advisoryStartupWaves(waves)
		plan.EstimatedDuration = Duration{Duration: duration}
	} else {
		plan.Graph = buildStepGraph(input.Steps)
		plan.Graph.Edges = append(plan.Graph.Edges, collectLinearCommunicationGraphEdgesWithIndex(input, index)...)
		sortGraph(plan.Graph)
		steps, duration := compileSteps(input.Steps)
		plan.Steps = steps
		plan.EstimatedDuration = Duration{Duration: duration}
	}
	return plan, diagnostics
}

func applyGroupEstimates(plan *Plan, groups []Group, history HistoryInputs) {
	plan.GroupEstimates = compileGroupEstimates(groups, history)
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
	if observed, changed := observedPlanDuration(plan.Waves, plan.GroupEstimates); changed {
		plan.ObservedDuration = Duration{Duration: observed}
	}
}

// compileGroups descends the group graph into waves, and reports the groups it could not schedule
// rather than spinning on them.
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
