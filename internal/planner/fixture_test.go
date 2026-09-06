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
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type plannerFixture struct {
	Structural fixtureStructuralInputs `json:"structural"`
	Telemetry  TelemetryInputs         `json:"telemetry"`
	History    fixtureHistoryInputs    `json:"history"`
	Want       fixtureExpectations     `json:"want"`
}

type fixtureStructuralInputs struct {
	SourceID           string                  `json:"sourceID,omitempty"`
	ObservedAt         string                  `json:"observedAt,omitempty"`
	ResolvedInputHash  string                  `json:"resolvedInputHash,omitempty"`
	TierPolicy         TierPolicy              `json:"tierPolicy,omitempty"`
	Triggers           []Trigger               `json:"triggers,omitempty"`
	Groups             []fixtureGroup          `json:"groups,omitempty"`
	Steps              []Step                  `json:"steps,omitempty"`
	AbortBehavior      string                  `json:"abortBehavior,omitempty"`
	TierOverrunPolicy  string                  `json:"tierOverrunPolicy,omitempty"`
	DeviceCapabilities []DeviceCapability      `json:"deviceCapabilities,omitempty"`
	PowerDomains       []PowerDomainMembership `json:"powerDomains,omitempty"`
	GroupNodes         []GroupNodeMembership   `json:"groupNodes,omitempty"`
	NodeTiers          []NodeTier              `json:"nodeTiers,omitempty"`
	HookDigests        []HookDigest            `json:"hookDigests,omitempty"`
}

type fixtureGroup struct {
	Name                string            `json:"name"`
	Description         string            `json:"description,omitempty"`
	Action              string            `json:"action"`
	HookRef             *HookReference    `json:"hookRef,omitempty"`
	Target              Target            `json:"target,omitempty"`
	Requires            []string          `json:"requires,omitempty"`
	Before              []string          `json:"before,omitempty"`
	After               []string          `json:"after,omitempty"`
	ShutdownTier        *int32            `json:"shutdownTier,omitempty"`
	Timeout             string            `json:"timeout,omitempty"`
	Params              map[string]string `json:"params,omitempty"`
	TierInversionPolicy string            `json:"tierInversionPolicy,omitempty"`
}

type fixtureHistoryInputs struct {
	GroupDurations map[string][]string `json:"groupDurations,omitempty"`
}

type fixtureExpectations struct {
	Waves             []fixtureWaveExpectation     `json:"waves"`
	StartupWaves      []fixtureWaveExpectation     `json:"startupWaves"`
	EstimatedDuration string                       `json:"estimatedDuration"`
	ObservedDuration  string                       `json:"observedDuration"`
	Feasibility       Feasibility                  `json:"feasibility"`
	Diagnostics       []string                     `json:"diagnostics,omitempty"`
	Edges             []fixtureEdgeExpectation     `json:"edges,omitempty"`
	GroupEstimates    []fixtureEstimateExpectation `json:"groupEstimates,omitempty"`
	AbsentSteps       []string                     `json:"absentSteps,omitempty"`
}

type fixtureWaveExpectation struct {
	Groups             []string `json:"groups"`
	ShutdownTier       *int32   `json:"shutdownTier,omitempty"`
	Duration           string   `json:"duration"`
	CumulativeDuration string   `json:"cumulativeDuration"`
}

type fixtureEdgeExpectation struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Relation   string `json:"relation"`
	Provenance string `json:"provenance"`
}

type fixtureEstimateExpectation struct {
	Group    string `json:"group"`
	Duration string `json:"duration"`
	Source   string `json:"source"`
	Samples  int    `json:"samples,omitempty"`
	Declared string `json:"declared"`
}

func TestCompilePlannerFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatalf("find planner fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one planner fixture")
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		t.Run(name, func(t *testing.T) {
			assertPlannerFixture(t, path)
		})
	}
}

func assertPlannerFixture(t *testing.T, path string) {
	t.Helper()
	fixture := loadPlannerFixture(t, path)

	plan, diagnostics, err := CompileWithHistory(
		fixture.Structural.toPlanner(t),
		fixture.Telemetry,
		fixture.History.toPlanner(t),
	)
	if err != nil {
		t.Fatalf("CompileWithHistory returned error: %v with diagnostics %#v", err, diagnostics)
	}

	assertFixtureWaves(t, "shutdown", plan.Waves, fixture.Want.Waves)
	assertFixtureWaves(t, "startup", plan.StartupWaves, fixture.Want.StartupWaves)
	assertDuration(t, "estimated duration", plan.EstimatedDuration.Duration, fixture.Want.EstimatedDuration)
	assertDuration(t, "observed duration", plan.ObservedDuration.Duration, fixture.Want.ObservedDuration)

	if !reflect.DeepEqual(plan.Feasibility, fixture.Want.Feasibility) {
		t.Fatalf("feasibility = %#v, want %#v", plan.Feasibility, fixture.Want.Feasibility)
	}
	assertDiagnosticReasons(t, diagnostics, fixture.Want.Diagnostics)
	assertFixtureEdges(t, plan.Graph.Edges, fixture.Want.Edges)
	assertFixtureGroupEstimates(t, plan.GroupEstimates, fixture.Want.GroupEstimates)

	for _, step := range fixture.Want.AbsentSteps {
		if hasCompiledStep(plan.Steps, step) {
			t.Fatalf("expected scoped plan to omit %q, got steps %#v", step, compiledStepIDs(plan.Steps))
		}
	}
	if plan.Hash == "" || plan.StructuralHash == "" {
		t.Fatalf("expected stable plan hashes, got hash=%q structuralHash=%q", plan.Hash, plan.StructuralHash)
	}
}

func loadPlannerFixture(t *testing.T, path string) plannerFixture {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read planner fixture: %v", err)
	}
	var fixture plannerFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode planner fixture: %v", err)
	}
	return fixture
}

func (input fixtureStructuralInputs) toPlanner(t *testing.T) StructuralInputs {
	t.Helper()
	groups := make([]Group, 0, len(input.Groups))
	for _, group := range input.Groups {
		groups = append(groups, Group{
			Name:                group.Name,
			Description:         group.Description,
			Action:              group.Action,
			HookRef:             group.HookRef,
			Target:              group.Target,
			Requires:            group.Requires,
			Before:              group.Before,
			After:               group.After,
			ShutdownTier:        group.ShutdownTier,
			Timeout:             parsePlannerDuration(t, group.Timeout),
			Params:              group.Params,
			TierInversionPolicy: group.TierInversionPolicy,
		})
	}
	return StructuralInputs{
		SourceID:           input.SourceID,
		ObservedAt:         input.ObservedAt,
		ResolvedInputHash:  input.ResolvedInputHash,
		TierPolicy:         input.TierPolicy,
		Triggers:           input.Triggers,
		Groups:             groups,
		Steps:              input.Steps,
		AbortBehavior:      input.AbortBehavior,
		TierOverrunPolicy:  input.TierOverrunPolicy,
		DeviceCapabilities: input.DeviceCapabilities,
		PowerDomains:       input.PowerDomains,
		GroupNodes:         input.GroupNodes,
		NodeTiers:          input.NodeTiers,
		HookDigests:        input.HookDigests,
	}
}

func (history fixtureHistoryInputs) toPlanner(t *testing.T) HistoryInputs {
	t.Helper()
	durations := make(map[string][]time.Duration, len(history.GroupDurations))
	for group, samples := range history.GroupDurations {
		for _, sample := range samples {
			durations[group] = append(durations[group], parsePlannerDuration(t, sample).Duration)
		}
	}
	return HistoryInputs{GroupDurations: durations}
}

func assertFixtureWaves(t *testing.T, label string, got []Wave, want []fixtureWaveExpectation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s waves len = %d, want %d: %#v", label, len(got), len(want), got)
	}
	for i := range want {
		if got[i].Index != int32(i) {
			t.Fatalf("%s wave %d index = %d, want %d", label, i, got[i].Index, i)
		}
		if !reflect.DeepEqual(got[i].Groups, want[i].Groups) {
			t.Fatalf("%s wave %d groups = %#v, want %#v", label, i, got[i].Groups, want[i].Groups)
		}
		assertDuration(t, label+" wave duration", got[i].Duration.Duration, want[i].Duration)
		assertDuration(t, label+" wave cumulative duration", got[i].CumulativeDuration.Duration, want[i].CumulativeDuration)
		if !sameInt32Ptr(got[i].ShutdownTier, want[i].ShutdownTier) {
			t.Fatalf("%s wave %d tier = %#v, want %#v", label, i, got[i].ShutdownTier, want[i].ShutdownTier)
		}
	}
}

func assertFixtureEdges(t *testing.T, got []GraphEdge, want []fixtureEdgeExpectation) {
	t.Helper()
	for _, expectation := range want {
		edge := findGraphEdge(got, expectation.From, expectation.To, expectation.Relation)
		if edge == nil {
			t.Fatalf("expected graph edge %#v in %#v", expectation, got)
		}
		if edge.Provenance != expectation.Provenance {
			t.Fatalf("edge %#v provenance = %q, want %q", expectation, edge.Provenance, expectation.Provenance)
		}
		if edge.Explanation == "" {
			t.Fatalf("edge %#v should publish an explanation", expectation)
		}
	}
}

func assertFixtureGroupEstimates(t *testing.T, got []GroupEstimate, want []fixtureEstimateExpectation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("group estimate len = %d, want %d: %#v", len(got), len(want), got)
	}
	byGroup := make(map[string]GroupEstimate, len(got))
	for _, estimate := range got {
		byGroup[estimate.Group] = estimate
	}
	for _, expectation := range want {
		estimate, found := byGroup[expectation.Group]
		if !found {
			t.Fatalf("missing estimate for %q in %#v", expectation.Group, got)
		}
		assertDuration(t, expectation.Group+" estimate", estimate.Duration.Duration, expectation.Duration)
		assertDuration(t, expectation.Group+" declared estimate", estimate.Declared.Duration, expectation.Declared)
		if estimate.Source != expectation.Source {
			t.Fatalf("%s estimate source = %q, want %q", expectation.Group, estimate.Source, expectation.Source)
		}
		if estimate.Samples != expectation.Samples {
			t.Fatalf("%s estimate samples = %d, want %d", expectation.Group, estimate.Samples, expectation.Samples)
		}
	}
}

func assertDiagnosticReasons(t *testing.T, diagnostics []Diagnostic, want []string) {
	t.Helper()
	got := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		got = append(got, diagnostic.Reason)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic reasons = %#v, want %#v; diagnostics %#v", got, want, diagnostics)
	}
}

func assertDuration(t *testing.T, label string, got time.Duration, wantRaw string) {
	t.Helper()
	want := parsePlannerDuration(t, wantRaw).Duration
	if got != want {
		t.Fatalf("%s = %s, want %s", label, got, want)
	}
}

func parsePlannerDuration(t *testing.T, raw string) Duration {
	t.Helper()
	if raw == "" {
		return Duration{}
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("parse duration %q: %v", raw, err)
	}
	return Duration{Duration: duration}
}

func sameInt32Ptr(left, right *int32) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return *left == *right
	}
}
