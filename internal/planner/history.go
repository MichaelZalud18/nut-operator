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
	"sort"
	"time"
)

// Estimate provenance values. An estimate always says where its number came from,
// because "two minutes" from one interrupted run and "two minutes" from thirty
// executions are not the same claim.
const (
	// EstimateDeclared is the flow author's timeout, used when nothing was observed.
	EstimateDeclared = "Declared"
	// EstimateObserved is derived from what previous executions actually took.
	EstimateObserved = "Observed"
)

// HistoryInputs carries observed durations into compilation (EX-32).
//
// An input rather than a lookup the compiler performs. PL-27 requires identical
// structural input to produce a byte-identical plan, which a compiler reaching out
// to a database cannot promise — two compiles a second apart would differ because a
// row landed between them. Resolving history to a value first keeps the compile
// pure and makes the history part of what the plan hash covers, so a plan whose
// estimates moved is a different plan and says so.
type HistoryInputs struct {
	// GroupDurations holds observed samples keyed by group name, newest first.
	GroupDurations map[string][]time.Duration `json:"groupDurations,omitempty"`
}

// GroupEstimate is one group's duration estimate and where it came from.
type GroupEstimate struct {
	Group    string   `json:"group"`
	Duration Duration `json:"duration"`
	// Source is EstimateDeclared or EstimateObserved.
	Source string `json:"source"`
	// Samples is how many observations produced an observed estimate, and 0 for a
	// declared one. Published because one sample is not a trend and must not be read
	// as one.
	Samples int `json:"samples,omitempty"`
	// Declared is the author's own timeout, kept alongside an observed estimate so a
	// reader can see the gap without a second lookup. A group that consistently runs
	// longer than its declared timeout is the single most useful thing this whole
	// mechanism surfaces.
	Declared Duration `json:"declared,omitempty"`
}

// estimateGroupDuration picks the number to plan with.
//
// Observed beats declared whenever anything was observed, including a single
// sample: one real measurement of this cluster's own work is better evidence than a
// number typed before the work ever ran. The sample count travels with the estimate
// so a reader can weigh it, rather than being used here to suppress the value.
//
// The statistic is the slowest observation, not the mean. When a battery is
// draining, the question is not how long this usually takes but how long it might,
// and an average is exactly the wrong answer to that — it is beaten half the time
// by construction. Taking the maximum makes the estimate conservative in the
// direction that costs nothing to be wrong about: over-estimating warns earlier
// than necessary, under-estimating runs out of power mid-flow.
func estimateGroupDuration(group Group, samples []time.Duration) GroupEstimate {
	declared := group.Timeout.Duration
	estimate := GroupEstimate{
		Group:    group.Name,
		Duration: Duration{Duration: declared},
		Source:   EstimateDeclared,
		Declared: Duration{Duration: declared},
	}

	var slowest time.Duration
	counted := 0
	for _, sample := range samples {
		if sample <= 0 {
			// A non-positive duration is a broken record, not a fast run.
			continue
		}
		counted++
		if sample > slowest {
			slowest = sample
		}
	}
	if counted == 0 {
		return estimate
	}

	estimate.Duration = Duration{Duration: slowest}
	estimate.Source = EstimateObserved
	estimate.Samples = counted
	return estimate
}

// compileGroupEstimates produces one estimate per group, in stable group order.
//
// Deterministic ordering matters because the result is hashed into plan identity:
// map iteration order would make the same inputs produce different plan hashes.
func compileGroupEstimates(groups []Group, history HistoryInputs) []GroupEstimate {
	estimates := make([]GroupEstimate, 0, len(groups))
	for _, group := range groups {
		estimates = append(estimates, estimateGroupDuration(group, history.GroupDurations[group.Name]))
	}
	sort.Slice(estimates, func(i, j int) bool { return estimates[i].Group < estimates[j].Group })
	return estimates
}

// thinHistoryThreshold is the sample count below which an estimate is reported as
// thin.
//
// Two, because the distinction that matters is "this has happened more than once".
// A single sample cannot show variance at all, so an estimate built on one run
// states a duration with no evidence about how much it moves — and how much it
// moves is the whole question when the plan is being checked against a battery.
const thinHistoryThreshold = 2

// EstimateConfidence summarizes how much evidence the plan's estimates rest on, for
// the EX-33 rehearsal recommendation.
type EstimateConfidence struct {
	// ObservedGroups and DeclaredGroups partition the plan's groups by estimate source.
	ObservedGroups int `json:"observedGroups"`
	DeclaredGroups int `json:"declaredGroups"`
	// ThinGroups are groups whose estimate rests on fewer than two observations,
	// including those with none. These are what a rehearsal would improve.
	ThinGroups []string `json:"thinGroups,omitempty"`
}

// summarizeEstimateConfidence reports what the estimates are built on.
func summarizeEstimateConfidence(estimates []GroupEstimate) EstimateConfidence {
	summary := EstimateConfidence{}
	for _, estimate := range estimates {
		if estimate.Source == EstimateObserved {
			summary.ObservedGroups++
		} else {
			summary.DeclaredGroups++
		}
		if estimate.Samples < thinHistoryThreshold {
			summary.ThinGroups = append(summary.ThinGroups, estimate.Group)
		}
	}
	return summary
}

// observedPlanDuration recomputes the plan total from the group estimates.
//
// Waves run in sequence and the groups inside one wave run together, so a wave
// takes as long as its slowest group and the plan takes the sum of its waves —
// the same shape compileGroups already uses for declared timeouts, applied to the
// estimates instead.
//
// Reports changed=false when nothing was observed, so a plan with no history keeps
// the declared total byte-for-byte and no existing deployment's plan hash moves
// until it has actually run something.
func observedPlanDuration(waves []Wave, estimates []GroupEstimate) (time.Duration, bool) {
	byGroup := make(map[string]GroupEstimate, len(estimates))
	observed := false
	for _, estimate := range estimates {
		byGroup[estimate.Group] = estimate
		if estimate.Source == EstimateObserved {
			observed = true
		}
	}
	if !observed {
		return 0, false
	}

	var total time.Duration
	for _, wave := range waves {
		var slowest time.Duration
		for _, group := range wave.Groups {
			if estimate, ok := byGroup[group]; ok && estimate.Duration.Duration > slowest {
				slowest = estimate.Duration.Duration
			}
		}
		total += slowest
	}
	return total, true
}

// HistoryObservation is the runtime figure a feasibility comparison is made against.
//
// Its own type rather than a bare pointer so the absent case is explicit at every
// call site: unknown runtime is a distinct answer from "zero seconds remaining",
// and PL-32 requires it never be read as the good case.
type HistoryObservation struct {
	// RuntimeSeconds is the shortest runtime across the selected devices, or nil when
	// no device reported one that may be trusted (CR-4).
	RuntimeSeconds *int64
}
