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
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompileProducesDeterministicGraphPlan(t *testing.T) {
	appPhase := int32(10)
	dbPhase := int32(20)
	input := StructuralInputs{
		SourceID: "shutdownflows.power.zalud.io/test",
		Triggers: []Trigger{
			{
				Type:         "RuntimeBelow",
				PowerDomains: []string{"core"},
			},
		},
		Groups: []Group{
			{
				Name:    "databases",
				Action:  "ScaleWorkload",
				Phase:   &dbPhase,
				Timeout: Duration{Duration: 10 * time.Minute},
				Target:  Target{WorkloadSelector: true},
			},
			{
				Name:    "applications",
				Action:  "ScaleWorkload",
				Before:  []string{"databases"},
				Phase:   &appPhase,
				Timeout: Duration{Duration: 5 * time.Minute},
				Target:  Target{NamespaceSelector: true},
			},
		},
	}

	first, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	second, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected second compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected byte-equivalent plans from identical input\nfirst: %#v\nsecond: %#v", first, second)
	}
	if first.Hash == "" {
		t.Fatal("expected plan hash to be set")
	}
	if first.StructuralHash == "" {
		t.Fatal("expected structural hash to be set")
	}
	if first.EstimatedDuration.Duration != 15*time.Minute {
		t.Fatalf("expected 15m estimated duration, got %s", first.EstimatedDuration.Duration)
	}
	if got, want := waveGroups(first.Waves), [][]string{{"applications"}, {"databases"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected waves %#v, got %#v", want, got)
	}
}

func TestCompileDoesNotMutateStructuralInput(t *testing.T) {
	appPhase := int32(10)
	dbPhase := int32(20)
	input := StructuralInputs{
		Triggers: []Trigger{
			{
				Type:         "RuntimeBelow",
				UPSDevices:   []string{"ups-b", "ups-a"},
				PowerDomains: []string{"domain-b", "domain-a"},
			},
		},
		Groups: []Group{
			{
				Name:   "databases",
				Action: "ScaleWorkload",
				Before: []string{
					"network",
				},
				Phase: &dbPhase,
				Params: map[string]string{
					"shutdownMode": "ordered",
				},
			},
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Before: []string{
					"network",
					"databases",
				},
				Phase: &appPhase,
			},
			{
				Name:   "network",
				Action: "AgentShutdown",
				After:  []string{"databases"},
			},
		},
	}
	originalTriggerUPSDevices := append([]string(nil), input.Triggers[0].UPSDevices...)
	originalTriggerPowerDomains := append([]string(nil), input.Triggers[0].PowerDomains...)
	originalGroupNames := []string{input.Groups[0].Name, input.Groups[1].Name, input.Groups[2].Name}
	originalBefore := append([]string(nil), input.Groups[1].Before...)

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if !reflect.DeepEqual(input.Triggers[0].UPSDevices, originalTriggerUPSDevices) {
		t.Fatalf("compile mutated trigger ups devices: got %#v, want %#v", input.Triggers[0].UPSDevices, originalTriggerUPSDevices)
	}
	if !reflect.DeepEqual(input.Triggers[0].PowerDomains, originalTriggerPowerDomains) {
		t.Fatalf("compile mutated trigger power domains: got %#v, want %#v", input.Triggers[0].PowerDomains, originalTriggerPowerDomains)
	}
	if got := []string{input.Groups[0].Name, input.Groups[1].Name, input.Groups[2].Name}; !reflect.DeepEqual(got, originalGroupNames) {
		t.Fatalf("compile mutated group order: got %#v, want %#v", got, originalGroupNames)
	}
	if !reflect.DeepEqual(input.Groups[1].Before, originalBefore) {
		t.Fatalf("compile mutated group dependencies: got %#v, want %#v", input.Groups[1].Before, originalBefore)
	}
}

func TestCompileExcludesTelemetryFromPlanIdentity(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Steps: []Step{
			{
				ID:       "wait-for-drain",
				Action:   "Wait",
				Duration: Duration{Duration: 30 * time.Second},
			},
		},
	}
	runtimeSeconds := int64(1200)
	chargePercent := int32(96)
	onBattery := false

	noTelemetryPlan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile without telemetry to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	telemetryPlan, diagnostics, err := Compile(input, TelemetryInputs{
		PowerDomains: []PowerDomainSnapshot{
			{
				Domain:                  "core",
				RuntimeRemainingSeconds: &runtimeSeconds,
				ChargePercent:           &chargePercent,
				OnBattery:               &onBattery,
			},
		},
	})
	if err != nil {
		t.Fatalf("expected compile with telemetry to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if noTelemetryPlan.Hash != telemetryPlan.Hash {
		t.Fatalf("expected telemetry to be excluded from plan hash, got %q and %q", noTelemetryPlan.Hash, telemetryPlan.Hash)
	}
	if noTelemetryPlan.StructuralHash != telemetryPlan.StructuralHash {
		t.Fatalf("expected telemetry to be excluded from structural hash, got %q and %q", noTelemetryPlan.StructuralHash, telemetryPlan.StructuralHash)
	}
	if noTelemetryPlan.Feasibility.Verdict == telemetryPlan.Feasibility.Verdict {
		t.Fatalf("expected telemetry to change advisory feasibility, both were %q", noTelemetryPlan.Feasibility.Verdict)
	}
}

func TestCompileIncludesResolvedInputHashInPlanIdentity(t *testing.T) {
	input := StructuralInputs{
		ResolvedInputHash: "inventory-a",
		Triggers:          []Trigger{{Type: "OnBattery"}},
		Steps: []Step{
			{
				ID:     "notify",
				Action: "Notify",
			},
		},
	}

	first, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	input.ResolvedInputHash = "inventory-b"
	second, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected second compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if first.Hash == second.Hash {
		t.Fatalf("expected resolved input hash to affect plan hash, both were %q", first.Hash)
	}
	if first.StructuralHash == second.StructuralHash {
		t.Fatalf("expected resolved input hash to affect structural hash, both were %q", first.StructuralHash)
	}
}

func TestCompilePublishesGraphArtifactAndDiagramExports(t *testing.T) {
	appPhase := int32(10)
	dbPhase := int32(20)
	storagePhase := int32(30)
	input := StructuralInputs{
		SourceID: "shutdownflows.power.zalud.io/test",
		Triggers: []Trigger{
			{Type: "OnBattery"},
		},
		Groups: []Group{
			{
				Name:     "storage",
				Action:   "AgentShutdown",
				After:    []string{"databases"},
				Phase:    &storagePhase,
				Timeout:  Duration{Duration: 3 * time.Minute},
				Target:   Target{AgentRefCount: 1},
				Requires: []string{"network"},
			},
			{
				Name:    "applications",
				Action:  "ScaleWorkload",
				Before:  []string{"databases"},
				Phase:   &appPhase,
				Timeout: Duration{Duration: time.Minute},
				Target:  Target{NamespaceSelector: true},
			},
			{
				Name:    "databases",
				Action:  "ScaleWorkload",
				Phase:   &dbPhase,
				Timeout: Duration{Duration: 2 * time.Minute},
				Target:  Target{WorkloadSelector: true},
			},
			{
				Name:    "network",
				Action:  "AgentShutdown",
				Phase:   &storagePhase,
				Timeout: Duration{Duration: 4 * time.Minute},
				Target:  Target{AgentRefCount: 1},
			},
		},
	}

	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if got, want := len(plan.Graph.Vertices), 4; got != want {
		t.Fatalf("expected %d graph vertices, got %d", want, got)
	}
	if got, want := len(plan.Graph.Edges), 3; got != want {
		t.Fatalf("expected %d graph edges, got %d: %#v", want, got, plan.Graph.Edges)
	}
	for _, expectation := range []struct {
		from     string
		to       string
		relation string
	}{
		{from: "applications", to: "databases", relation: GraphEdgeRelationBefore},
		{from: "databases", to: "storage", relation: GraphEdgeRelationAfter},
		{from: "storage", to: "network", relation: GraphEdgeRelationRequires},
	} {
		edge := findGraphEdge(plan.Graph.Edges, expectation.from, expectation.to, expectation.relation)
		if edge == nil {
			t.Fatalf("expected graph edge %#v in %#v", expectation, plan.Graph.Edges)
		}
		if edge.Provenance != GraphEdgeProvenanceDeclared {
			t.Fatalf("expected declared provenance for edge %#v, got %q", expectation, edge.Provenance)
		}
		if edge.Explanation == "" {
			t.Fatalf("expected edge explanation for edge %#v", expectation)
		}
	}
	if got, want := waveGroups(plan.Waves), [][]string{{"applications"}, {"databases"}, {"storage"}, {"network"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected shutdown waves %#v, got %#v", want, got)
	}
	if got, want := waveGroups(plan.StartupWaves), [][]string{{"network"}, {"storage"}, {"databases"}, {"applications"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected advisory startup waves %#v, got %#v", want, got)
	}
	if !hasExplanationReason(plan.Explanations, "PlanCompiled") {
		t.Fatalf("expected PlanCompiled explanation, got %#v", plan.Explanations)
	}
	if plan.Diagrams.Mermaid == "" || plan.Diagrams.GraphvizDOT == "" || plan.Diagrams.D2 == "" {
		t.Fatalf("expected all diagram exports to be populated, got %#v", plan.Diagrams)
	}
	if !strings.Contains(plan.Diagrams.Mermaid, "applications -->|Before| databases") {
		t.Fatalf("expected Mermaid export to include applications->databases edge, got:\n%s", plan.Diagrams.Mermaid)
	}
	if !strings.Contains(plan.Diagrams.GraphvizDOT, `"storage" -> "network"`) {
		t.Fatalf("expected Graphviz export to include storage->network edge, got:\n%s", plan.Diagrams.GraphvizDOT)
	}
}

func TestCompileLinearStepsPublishesPolicyGraph(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Steps: []Step{
			{ID: "notify", Action: "Notify"},
			{ID: "wait", Action: "Wait", Duration: Duration{Duration: time.Minute}},
			{ID: "gate", Action: "Gate"},
		},
	}

	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if got, want := len(plan.Graph.Vertices), 3; got != want {
		t.Fatalf("expected %d graph vertices, got %d", want, got)
	}
	if got, want := len(plan.Graph.Edges), 2; got != want {
		t.Fatalf("expected %d graph edges, got %d", want, got)
	}
	edge := findGraphEdge(plan.Graph.Edges, "notify", "wait", GraphEdgeRelationLinearOrder)
	if edge == nil {
		t.Fatalf("expected notify->wait linear graph edge, got %#v", plan.Graph.Edges)
	}
	if edge.Provenance != GraphEdgeProvenancePolicy {
		t.Fatalf("expected policy provenance for linear edge, got %q", edge.Provenance)
	}
	if len(plan.StartupWaves) != 0 {
		t.Fatalf("linear fallback steps should not publish startup waves, got %#v", plan.StartupWaves)
	}
}

func TestCompileExpandsShutdownTiersIntoDerivedEdges(t *testing.T) {
	appTier := int32(3)
	dbTier := int32(2)
	nodeTier := int32(1)
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:         "nodes",
				Action:       "AgentShutdown",
				ShutdownTier: &nodeTier,
				Target:       Target{AgentRefCount: 1},
			},
			{
				Name:         "applications",
				Action:       "ScaleWorkload",
				ShutdownTier: &appTier,
				Target:       Target{NamespaceSelector: true},
			},
			{
				Name:         "databases",
				Action:       "ScaleWorkload",
				ShutdownTier: &dbTier,
				Target:       Target{WorkloadSelector: true},
			},
		},
	}

	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if got, want := waveGroups(plan.Waves), [][]string{{"applications"}, {"databases"}, {"nodes"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected shutdown waves %#v, got %#v", want, got)
	}
	if got, want := *plan.Waves[0].ShutdownTier, appTier; got != want {
		t.Fatalf("expected first wave tier %d, got %d", want, got)
	}
	edge := findGraphEdge(plan.Graph.Edges, "applications", "databases", GraphEdgeRelationShutdownTier)
	if edge == nil {
		t.Fatalf("expected applications->databases tier edge, got %#v", plan.Graph.Edges)
	}
	if edge.Provenance != GraphEdgeProvenanceDerived {
		t.Fatalf("expected derived tier edge provenance, got %q", edge.Provenance)
	}
	if !strings.Contains(edge.Explanation, "tier 3 stops before tier 2") {
		t.Fatalf("expected tier explanation, got %q", edge.Explanation)
	}
	if findGraphEdge(plan.Graph.Edges, "applications", "nodes", GraphEdgeRelationShutdownTier) != nil {
		t.Fatalf("expected tier edges only between adjacent occupied tiers, got %#v", plan.Graph.Edges)
	}
	if got := plan.Steps[0].ShutdownTier; got == nil || *got != appTier {
		t.Fatalf("expected compiled step shutdown tier %d, got %#v", appTier, got)
	}
	if got := findGraphVertex(plan.Graph.Vertices, "nodes").ShutdownTier; got == nil || *got != nodeTier {
		t.Fatalf("expected graph vertex shutdown tier %d, got %#v", nodeTier, got)
	}
}

func TestCompileAppliesDefaultShutdownTier(t *testing.T) {
	defaultTier := int32(4)
	nodeTier := int32(1)
	input := StructuralInputs{
		TierPolicy: TierPolicy{DefaultTier: &defaultTier},
		Triggers:   []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Target: Target{NamespaceSelector: true},
			},
			{
				Name:         "nodes",
				Action:       "AgentShutdown",
				ShutdownTier: &nodeTier,
				Target:       Target{AgentRefCount: 1},
			},
		},
	}

	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if got, want := waveGroups(plan.Waves), [][]string{{"applications"}, {"nodes"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected shutdown waves %#v, got %#v", want, got)
	}
	if edge := findGraphEdge(plan.Graph.Edges, "applications", "nodes", GraphEdgeRelationShutdownTier); edge == nil {
		t.Fatalf("expected default-tier applications->nodes edge, got %#v", plan.Graph.Edges)
	}
	if got := findGraphVertex(plan.Graph.Vertices, "applications").ShutdownTier; got == nil || *got != defaultTier {
		t.Fatalf("expected applications to inherit default tier %d, got %#v", defaultTier, got)
	}
}

func TestCompileRejectsTargetedTierZero(t *testing.T) {
	tierZero := int32(0)
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{{
			Name:         "last-ditch",
			Action:       "ScaleWorkload",
			ShutdownTier: &tierZero,
			Target:       Target{NamespaceSelector: true},
		}},
	}

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "ShutdownTierZeroTargeted") {
		t.Fatalf("expected ShutdownTierZeroTargeted diagnostic, got %#v", diagnostics)
	}
}

func TestCompileRejectsUnknownDependency(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Before: []string{
					"databases",
				},
			},
		},
	}

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "UnknownDependency") {
		t.Fatalf("expected UnknownDependency diagnostic, got %#v", diagnostics)
	}
}

func TestCompileRejectsDependencyCycle(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:   "applications",
				Action: "ScaleWorkload",
				Before: []string{
					"databases",
				},
			},
			{
				Name:   "databases",
				Action: "ScaleWorkload",
				Before: []string{
					"applications",
				},
			},
		},
	}

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "DependencyCycle") {
		t.Fatalf("expected DependencyCycle diagnostic, got %#v", diagnostics)
	}
}

func hasDiagnosticReason(diagnostics []Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			return true
		}
	}
	return false
}

func waveGroups(waves []Wave) [][]string {
	groups := make([][]string, 0, len(waves))
	for _, wave := range waves {
		groups = append(groups, wave.Groups)
	}
	return groups
}

func findGraphEdge(edges []GraphEdge, from, to, relation string) *GraphEdge {
	for i := range edges {
		if edges[i].From == from && edges[i].To == to && edges[i].Relation == relation {
			return &edges[i]
		}
	}
	return nil
}

func findGraphVertex(vertices []GraphVertex, id string) GraphVertex {
	for _, vertex := range vertices {
		if vertex.ID == id {
			return vertex
		}
	}
	return GraphVertex{}
}

func hasExplanationReason(explanations []Explanation, reason string) bool {
	for _, explanation := range explanations {
		if explanation.Reason == reason {
			return true
		}
	}
	return false
}

func triggerCapabilityInput(devices []DeviceCapability) StructuralInputs {
	timeout := Duration{Duration: 5 * time.Minute}
	return StructuralInputs{
		SourceID: "shutdownflows.power.zalud.io/test",
		Triggers: []Trigger{
			{Type: "RuntimeBelow", PowerDomains: []string{"core"}},
		},
		Groups: []Group{
			{Name: "applications", Action: "ScaleWorkload", Timeout: timeout, Target: Target{NamespaceSelector: true}},
		},
		DeviceCapabilities: devices,
		PowerDomains: []PowerDomainMembership{
			{Name: "core", UPSDevices: deviceIDs(devices)},
		},
	}
}

func deviceIDs(devices []DeviceCapability) []string {
	ids := make([]string, 0, len(devices))
	for _, device := range devices {
		ids = append(ids, device.DeviceID)
	}
	return ids
}

func TestCompileRejectsTriggerNoDeviceCanSatisfy(t *testing.T) {
	input := triggerCapabilityInput([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
		{DeviceID: "ups-2", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
	})

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err == nil {
		t.Fatal("a RuntimeBelow trigger no device can report must not compile")
	}
	if !hasPlannerDiagnostic(diagnostics, "TriggerUnsupportedByAllDevices") {
		t.Fatalf("expected TriggerUnsupportedByAllDevices, got %#v", diagnostics)
	}
}

func TestCompileDegradesTriggerSomeDevicesCannotSatisfy(t *testing.T) {
	input := triggerCapabilityInput([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
		{DeviceID: "ups-2", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
	})

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("a partially supported trigger must still compile, got %v", err)
	}
	if !hasPlannerDiagnostic(diagnostics, "TriggerDegradedByDeviceCapability") {
		t.Fatalf("expected TriggerDegradedByDeviceCapability, got %#v", diagnostics)
	}
}

func TestCompileAcceptsTriggerEveryDeviceSatisfies(t *testing.T) {
	input := triggerCapabilityInput([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
		{DeviceID: "ups-2", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
	})

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if hasPlannerDiagnostic(diagnostics, "TriggerDegradedByDeviceCapability") ||
		hasPlannerDiagnostic(diagnostics, "TriggerUnsupportedByAllDevices") {
		t.Fatalf("fully supported triggers must raise no capability diagnostic, got %#v", diagnostics)
	}
}

func TestCompileSkipsTriggerValidationWithoutCapabilityContext(t *testing.T) {
	input := triggerCapabilityInput(nil)
	input.PowerDomains = nil

	if _, _, err := Compile(input, TelemetryInputs{}); err != nil {
		t.Fatalf("no capability context means no capability verdict, got %v", err)
	}
}

func hasPlannerDiagnostic(diagnostics []Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			return true
		}
	}
	return false
}
