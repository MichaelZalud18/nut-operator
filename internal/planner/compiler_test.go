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
		PowerDomains: []PowerDomainMembership{
			{
				Name:           "domain-a",
				UPSDevices:     []string{"ups-b", "ups-a"},
				Members:        []string{"switch-a", "node-a", "ups-a"},
				Nodes:          []string{"node-b", "node-a"},
				Infrastructure: []string{"switch-b", "switch-a"},
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
	originalDomain := input.PowerDomains[0]
	originalDomain.UPSDevices = append([]string(nil), originalDomain.UPSDevices...)
	originalDomain.Members = append([]string(nil), originalDomain.Members...)
	originalDomain.Nodes = append([]string(nil), originalDomain.Nodes...)
	originalDomain.Infrastructure = append([]string(nil), originalDomain.Infrastructure...)
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
	if !reflect.DeepEqual(input.PowerDomains[0], originalDomain) {
		t.Fatalf("compile mutated power domain membership: got %#v, want %#v", input.PowerDomains[0], originalDomain)
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

func TestCompilePublishesPowerDomainArtifacts(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery", PowerDomains: []string{"rack-a"}}},
		PowerDomains: []PowerDomainMembership{{
			Name:           "rack-a",
			UPSDevices:     []string{"ups-a"},
			Members:        []string{"ups-a", "switch-a", "node-a"},
			Nodes:          []string{"node-a"},
			Infrastructure: []string{"switch-a"},
		}},
		Groups: []Group{{
			Name:   "applications",
			Action: "ScaleWorkload",
		}},
	}

	plan, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	want := []PowerDomainArtifact{{
		Name:           "rack-a",
		UPSDevices:     []string{"ups-a"},
		Members:        []string{"node-a", "switch-a", "ups-a"},
		Nodes:          []string{"node-a"},
		Infrastructure: []string{"switch-a"},
	}}
	if !reflect.DeepEqual(plan.PowerDomains, want) {
		t.Fatalf("expected published power domains %#v, got %#v", want, plan.PowerDomains)
	}
}

func TestCompileLinearStepsPublishesPolicyGraph(t *testing.T) {
	input := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Steps: []Step{
			{ID: "notify", Action: "Notify"},
			{ID: "wait", Action: "Wait", Duration: Duration{Duration: time.Minute}},
			{ID: "notify-complete", Action: "Notify"},
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

// nodeClearanceInput is a flow whose drain and poweroff groups have no declared
// ordering between them: the only thing that can order them is the node they
// share.
func nodeClearanceInput(membership []GroupNodeMembership) StructuralInputs {
	return StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "drain-rack-a", Action: "DrainNodes", Target: Target{NodeSelector: true}},
			{Name: "poweroff-rack-a", Action: "AgentShutdown", Target: Target{AgentRefCount: 1}},
		},
		GroupNodes: membership,
	}
}

func TestCompileDerivesNodeClearanceEdges(t *testing.T) {
	plan, diagnostics, err := Compile(nodeClearanceInput([]GroupNodeMembership{
		{Group: "drain-rack-a", Acts: []string{"node-a", "node-b"}},
		{Group: "poweroff-rack-a", Releases: []string{"node-a", "node-b"}},
	}), TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	edge := findGraphEdge(plan.Graph.Edges, "drain-rack-a", "poweroff-rack-a", GraphEdgeRelationNodeClearance)
	if edge == nil {
		t.Fatalf("expected a node-clearance edge, got %#v", plan.Graph.Edges)
	}
	if edge.Provenance != GraphEdgeProvenanceDerived {
		t.Fatalf("expected derived provenance, got %q", edge.Provenance)
	}
	if !strings.Contains(edge.Explanation, "cannot power off until") {
		t.Fatalf("expected a clearance explanation, got %q", edge.Explanation)
	}

	// The point of the edge: it changes execution order. Without it both groups
	// are ready at once and land in the same wave, draining a node while it is
	// being powered off.
	if got, want := waveGroups(plan.Waves), [][]string{{"drain-rack-a"}, {"poweroff-rack-a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected clearance to split the waves, got %#v", got)
	}
}

func TestCompileWithoutNodeMembershipLeavesGroupsUnordered(t *testing.T) {
	plan, _, err := Compile(nodeClearanceInput(nil), TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if got, want := waveGroups(plan.Waves), [][]string{{"drain-rack-a", "poweroff-rack-a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("expected one unordered wave without node context, got %#v", got)
	}
}

func TestCompileDerivesNoClearanceEdgeForUnsharedNodes(t *testing.T) {
	plan, _, err := Compile(nodeClearanceInput([]GroupNodeMembership{
		{Group: "drain-rack-a", Acts: []string{"node-a"}},
		{Group: "poweroff-rack-a", Releases: []string{"node-z"}},
	}), TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if edge := findGraphEdge(plan.Graph.Edges, "drain-rack-a", "poweroff-rack-a", GraphEdgeRelationNodeClearance); edge != nil {
		t.Fatalf("groups sharing no node must not be ordered, got %#v", edge)
	}
}

// A group that both works on a node and powers it off orders nothing: the edge
// would point at itself.
func TestCompileDerivesNoSelfClearanceEdge(t *testing.T) {
	plan, _, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups:   []Group{{Name: "release-rack-a", Action: "AgentShutdown"}},
		GroupNodes: []GroupNodeMembership{
			{Group: "release-rack-a", Acts: []string{"node-a"}, Releases: []string{"node-a"}},
		},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	for _, edge := range plan.Graph.Edges {
		if edge.Relation == GraphEdgeRelationNodeClearance {
			t.Fatalf("expected no clearance edge for a single group, got %#v", edge)
		}
	}
}

// Membership naming a group that is not in the plan is ignored rather than
// producing an edge to a vertex that does not exist.
func TestCompileIgnoresMembershipForUnknownGroups(t *testing.T) {
	plan, _, err := Compile(nodeClearanceInput([]GroupNodeMembership{
		{Group: "drain-rack-a", Acts: []string{"node-a"}},
		{Group: "poweroff-rack-a", Releases: []string{"node-a"}},
		{Group: "group-from-another-flow", Acts: []string{"node-a"}},
	}), TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	for _, edge := range plan.Graph.Edges {
		if edge.From == "group-from-another-flow" || edge.To == "group-from-another-flow" {
			t.Fatalf("expected unknown group to be ignored, got edge %#v", edge)
		}
	}
}

// Derived clearance edges participate in cycle detection like any other edge:
// two groups that each act on the node the other releases cannot be ordered.
func TestCompileRejectsCyclesCreatedByNodeClearance(t *testing.T) {
	_, diagnostics, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "release-a", Action: "AgentShutdown"},
			{Name: "release-b", Action: "AgentShutdown"},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "release-a", Acts: []string{"node-b"}, Releases: []string{"node-a"}},
			{Group: "release-b", Acts: []string{"node-a"}, Releases: []string{"node-b"}},
		},
	}, TelemetryInputs{})
	if err == nil {
		t.Fatal("expected a clearance cycle to be rejected")
	}
	if !hasDiagnosticReason(diagnostics, "DependencyCycle") {
		t.Fatalf("expected a DependencyCycle diagnostic, got %#v", diagnostics)
	}
}

func TestNodeClearanceIsDeterministicAndFoldsIntoPlanIdentity(t *testing.T) {
	withNodes := nodeClearanceInput([]GroupNodeMembership{
		{Group: "drain-rack-a", Acts: []string{"node-b", "node-a"}},
		{Group: "poweroff-rack-a", Releases: []string{"node-a", "node-b"}},
	})
	shuffled := nodeClearanceInput([]GroupNodeMembership{
		{Group: "poweroff-rack-a", Releases: []string{"node-b", "node-a"}},
		{Group: "drain-rack-a", Acts: []string{"node-a", "node-b"}},
	})

	first, _, err := Compile(withNodes, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	second, _, err := Compile(shuffled, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if first.Hash != second.Hash {
		t.Fatalf("membership ordering must not change plan identity: %q vs %q", first.Hash, second.Hash)
	}

	bare, _, err := Compile(nodeClearanceInput(nil), TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if first.Hash == bare.Hash {
		t.Fatal("node membership must participate in plan identity")
	}
}

// OD-18: tiers count down, so a node at a higher tier is meant to be gone while
// a lower-tier group is still working. If that group runs on that node, the plan
// says something the cluster cannot do.
func TestCompileReportsTierInversion(t *testing.T) {
	groupTier := int32(2)
	_, diagnostics, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "late-workload", Action: "ScaleWorkload", ShutdownTier: &groupTier},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "late-workload", Acts: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-a", Tier: 4}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("inversion is a warning, not a rejection: %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "ShutdownTierInversion") {
		t.Fatalf("expected a ShutdownTierInversion diagnostic, got %#v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason != "ShutdownTierInversion" {
			continue
		}
		if diagnostic.Severity != DiagnosticWarning {
			t.Fatalf("expected a warning, got %q", diagnostic.Severity)
		}
		if !strings.Contains(diagnostic.Message, "node-a") || !strings.Contains(diagnostic.Message, "tier 4") {
			t.Fatalf("expected the message to name the node and its tier, got %q", diagnostic.Message)
		}
	}
}

func TestCompileReportsNoInversionWhenNodeOutlivesItsWorkload(t *testing.T) {
	groupTier := int32(4)
	_, diagnostics, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "early-workload", Action: "ScaleWorkload", ShutdownTier: &groupTier},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "early-workload", Acts: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-a", Tier: 2}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if hasDiagnosticReason(diagnostics, "ShutdownTierInversion") {
		t.Fatalf("a workload stopping before its node is the correct order, got %#v", diagnostics)
	}
}

// A group that releases a node is not running on it afterward, so the node's own
// tier cannot invert against it.
func TestCompileIgnoresInversionForReleasedNodes(t *testing.T) {
	groupTier := int32(1)
	_, diagnostics, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "release-node", Action: "AgentShutdown", ShutdownTier: &groupTier},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "release-node", Releases: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-a", Tier: 4}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if hasDiagnosticReason(diagnostics, "ShutdownTierInversion") {
		t.Fatalf("releasing a node is not running on it, got %#v", diagnostics)
	}
}

// A mistyped tier label is indistinguishable from a deliberate default unless
// the fallback is stated.
func TestCompileReportsDefaultedShutdownTiers(t *testing.T) {
	explicit := int32(4)
	defaultTier := int32(3)
	_, diagnostics, err := Compile(StructuralInputs{
		Triggers:   []Trigger{{Type: "OnBattery"}},
		TierPolicy: TierPolicy{DefaultTier: &defaultTier},
		Groups: []Group{
			{Name: "declared", Action: "ScaleWorkload", ShutdownTier: &explicit},
			{Name: "defaulted", Action: "ScaleWorkload"},
		},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("defaulting is legitimate configuration, not an error: %v", err)
	}

	var found bool
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason != "ShutdownTierDefaulted" {
			continue
		}
		found = true
		if diagnostic.Subject != "defaulted" {
			t.Fatalf("only the group that fell back should be named, got %q", diagnostic.Subject)
		}
		if diagnostic.Severity != DiagnosticInfo {
			t.Fatalf("expected an informational severity, got %q", diagnostic.Severity)
		}
		if !strings.Contains(diagnostic.Message, "default tier 3") {
			t.Fatalf("expected the message to name the tier applied, got %q", diagnostic.Message)
		}
	}
	if !found {
		t.Fatalf("expected a ShutdownTierDefaulted diagnostic, got %#v", diagnostics)
	}
}

// Info never degrades a flow; it is reported, not escalated.
func TestDefaultedTierDiagnosticsDoNotBlockCompilation(t *testing.T) {
	defaultTier := int32(3)
	plan, diagnostics, err := Compile(StructuralInputs{
		Triggers:   []Trigger{{Type: "OnBattery"}},
		TierPolicy: TierPolicy{DefaultTier: &defaultTier},
		Groups:     []Group{{Name: "defaulted", Action: "ScaleWorkload"}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if len(plan.Waves) != 1 {
		t.Fatalf("expected the plan to compile normally, got %#v", plan.Waves)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == DiagnosticError {
			t.Fatalf("informational diagnostics must not reject, got %#v", diagnostic)
		}
	}
}

// OD-18 defaults to blocking: an inverted node is withheld from power-off rather
// than merely reported, because reporting alone still lets the flow cut power to
// a workload it was told to keep running.
func TestCompileBlocksInvertedNodesByDefault(t *testing.T) {
	groupTier := int32(2)
	plan, _, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "late-workload", Action: "ScaleWorkload", ShutdownTier: &groupTier},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "late-workload", Acts: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-a", Tier: 4}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("blocking is not a rejection: %v", err)
	}
	if len(plan.BlockedNodes) != 1 {
		t.Fatalf("expected exactly one blocked node, got %#v", plan.BlockedNodes)
	}
	blocked := plan.BlockedNodes[0]
	if blocked.Name != "node-a" {
		t.Fatalf("expected node-a to be withheld, got %q", blocked.Name)
	}
	if blocked.Reason != "ShutdownTierInversion" {
		t.Fatalf("expected the block to cite the inversion, got %q", blocked.Reason)
	}
	if blocked.NodeTier != 4 {
		t.Fatalf("expected the node's own tier to be recorded, got %d", blocked.NodeTier)
	}
	if len(blocked.Groups) != 1 || blocked.Groups[0] != "late-workload" {
		t.Fatalf("expected the responsible group to be named, got %#v", blocked.Groups)
	}
}

// Allow is how an author accepts going down with the node. The block disappears;
// the warning does not, because opting in accepts a risk rather than retiring it.
func TestCompileHonorsAllowTierInversionPolicy(t *testing.T) {
	groupTier := int32(2)
	plan, diagnostics, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{{
			Name:                "late-workload",
			Action:              "ScaleWorkload",
			ShutdownTier:        &groupTier,
			TierInversionPolicy: TierInversionAllow,
		}},
		GroupNodes: []GroupNodeMembership{
			{Group: "late-workload", Acts: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-a", Tier: 4}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if len(plan.BlockedNodes) != 0 {
		t.Fatalf("Allow means the node powers off on schedule, got %#v", plan.BlockedNodes)
	}
	if !hasDiagnosticReason(diagnostics, "ShutdownTierInversionAllowed") {
		t.Fatalf("expected the accepted inversion to still be reported, got %#v", diagnostics)
	}
	if hasDiagnosticReason(diagnostics, "ShutdownTierInversion") {
		t.Fatalf("an accepted inversion should not also report as blocking, got %#v", diagnostics)
	}
}

// One dissenting group holds the node up. Powering it off to satisfy the group
// that accepted the risk would cut power to the group that did not.
func TestCompileBlocksNodeWhenOnlyOneSharedGroupAllows(t *testing.T) {
	groupTier := int32(2)
	plan, _, err := Compile(StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{
				Name:                "accepts-risk",
				Action:              "ScaleWorkload",
				ShutdownTier:        &groupTier,
				TierInversionPolicy: TierInversionAllow,
			},
			{Name: "needs-power", Action: "ScaleWorkload", ShutdownTier: &groupTier},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "accepts-risk", Acts: []string{"node-a"}},
			{Group: "needs-power", Acts: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-a", Tier: 4}},
	}, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if len(plan.BlockedNodes) != 1 {
		t.Fatalf("the dissenting group should still hold the node, got %#v", plan.BlockedNodes)
	}
	if got := plan.BlockedNodes[0].Groups; len(got) != 1 || got[0] != "needs-power" {
		t.Fatalf("only the group that did not accept the risk explains the block, got %#v", got)
	}
}

// Blocked nodes are derived, so identical inputs must produce identical output
// regardless of the order groups and nodes arrive in.
func TestBlockedNodesAreDeterministic(t *testing.T) {
	groupTier := int32(1)
	inputs := StructuralInputs{
		Triggers: []Trigger{{Type: "OnBattery"}},
		Groups: []Group{
			{Name: "zulu", Action: "ScaleWorkload", ShutdownTier: &groupTier},
			{Name: "alpha", Action: "ScaleWorkload", ShutdownTier: &groupTier},
		},
		GroupNodes: []GroupNodeMembership{
			{Group: "zulu", Acts: []string{"node-b", "node-a"}},
			{Group: "alpha", Acts: []string{"node-a"}},
		},
		NodeTiers: []NodeTier{{Name: "node-b", Tier: 3}, {Name: "node-a", Tier: 2}},
	}
	first, _, err := Compile(inputs, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	reversed := inputs
	reversed.Groups = []Group{inputs.Groups[1], inputs.Groups[0]}
	reversed.GroupNodes = []GroupNodeMembership{inputs.GroupNodes[1], inputs.GroupNodes[0]}
	reversed.NodeTiers = []NodeTier{inputs.NodeTiers[1], inputs.NodeTiers[0]}
	second, _, err := Compile(reversed, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if !reflect.DeepEqual(first.BlockedNodes, second.BlockedNodes) {
		t.Fatalf("blocked nodes differ by input order:\n%#v\n%#v", first.BlockedNodes, second.BlockedNodes)
	}
	if len(first.BlockedNodes) != 2 {
		t.Fatalf("expected both inverted nodes to be blocked, got %#v", first.BlockedNodes)
	}
	if first.BlockedNodes[0].Name != "node-a" || first.BlockedNodes[1].Name != "node-b" {
		t.Fatalf("expected blocked nodes sorted by name, got %#v", first.BlockedNodes)
	}
	if got := first.BlockedNodes[0].Groups; len(got) != 2 || got[0] != "alpha" || got[1] != "zulu" {
		t.Fatalf("expected the groups on node-a sorted by name, got %#v", got)
	}
}

// triggerCapabilityInputWithFallback is triggerCapabilityInput with an OD-9 fallback declared.
func triggerCapabilityInputWithFallback(devices []DeviceCapability, fallback string) StructuralInputs {
	input := triggerCapabilityInput(devices)
	input.Triggers[0].FallbackType = fallback
	return input
}

// The point of OD-9: a declared fallback turns a degraded plan into a covered one, because the
// devices that cannot report runtime still act on the coarser condition.
func TestCompileSubstitutesDeclaredTriggerFallback(t *testing.T) {
	input := triggerCapabilityInputWithFallback([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
		{DeviceID: "ups-2", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
	}, "LowBattery")

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("a covered fallback must compile, got %v", err)
	}
	if !hasPlannerDiagnostic(diagnostics, "TriggerSubstituted") {
		t.Fatalf("expected TriggerSubstituted, got %#v", diagnostics)
	}
	if hasPlannerDiagnostic(diagnostics, "TriggerDegradedByDeviceCapability") {
		t.Fatalf("a declared and satisfiable fallback must not leave the plan degraded, got %#v", diagnostics)
	}
}

// A fallback rescues a plan that would otherwise be rejected outright: every device failing the
// primary is fatal only when nothing coarser covers them.
func TestCompileFallbackRescuesTriggerNoDeviceCanSatisfy(t *testing.T) {
	input := triggerCapabilityInputWithFallback([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
		{DeviceID: "ups-2", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
	}, "LowBattery")

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("a fallback every device can satisfy must compile, got %v", err)
	}
	if !hasPlannerDiagnostic(diagnostics, "TriggerSubstituted") {
		t.Fatalf("expected TriggerSubstituted, got %#v", diagnostics)
	}
}

// Declaring a fallback the uncovered devices also cannot satisfy is worse than declaring none: it
// reads as coverage and provides none, so it is rejected rather than warned about.
func TestCompileRejectsUnsatisfiableTriggerFallback(t *testing.T) {
	input := triggerCapabilityInputWithFallback([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
		{DeviceID: "ups-2", ProfileID: "silent", TelemetryVariables: []string{"battery.charge"}},
	}, "LowBattery")

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err == nil {
		t.Fatal("a fallback the uncovered devices cannot satisfy must not compile")
	}
	if !hasPlannerDiagnostic(diagnostics, "TriggerFallbackUnsatisfiable") {
		t.Fatalf("expected TriggerFallbackUnsatisfiable, got %#v", diagnostics)
	}
}

func TestCompileRejectsTriggerFallbackThatIsNotCoarser(t *testing.T) {
	input := triggerCapabilityInputWithFallback([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
		{DeviceID: "ups-2", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
	}, "ChargeBelow")

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err == nil {
		t.Fatal("ChargeBelow is not a coarser class for RuntimeBelow and must not compile")
	}
	if !hasPlannerDiagnostic(diagnostics, "TriggerFallbackNotCoarser") {
		t.Fatalf("expected TriggerFallbackNotCoarser, got %#v", diagnostics)
	}
}

// The gap is only actionable if the compile output says what to write. Without the name, adopting a
// fallback is a research task performed under outage pressure.
func TestCompileNamesTheFallbackThatWouldCoverAGap(t *testing.T) {
	input := triggerCapabilityInput([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
		{DeviceID: "ups-2", ProfileID: "floor", Unidentified: true, TelemetryVariables: []string{"ups.status"}},
	})

	_, diagnostics, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason != "TriggerDegradedByDeviceCapability" {
			continue
		}
		if !strings.Contains(diagnostic.Message, "fallbackType: LowBattery") {
			t.Fatalf("degrade diagnostic must name the fallback that would cover it, got %q", diagnostic.Message)
		}
		return
	}
	t.Fatalf("expected a degrade diagnostic, got %#v", diagnostics)
}

// feasibilityInput builds a plan with complete telemetry, so the only thing that can move the
// verdict is the runtime-estimate declaration under test.
func feasibilityInput(devices []DeviceCapability) (StructuralInputs, TelemetryInputs) {
	runtime := int64(1800)
	charge := int32(97)
	onBattery := false
	structural := triggerCapabilityInput(devices)
	structural.Triggers = []Trigger{{Type: "OnBattery", PowerDomains: []string{"core"}}}
	return structural, TelemetryInputs{PowerDomains: []PowerDomainSnapshot{{
		Domain:                  "core",
		RuntimeRemainingSeconds: &runtime,
		ChargePercent:           &charge,
		OnBattery:               &onBattery,
	}}}
}

// CR-4's live consequence. Feasibility answers "is there enough runtime to finish", and a firmware
// that reports a fixed estimate cannot answer it -- the number does not fall as the cluster draws on
// the battery, so an OK verdict would be derived from a constant.
func TestFeasibilityIsUnknownWhenADeviceReportsAStaticRuntimeEstimate(t *testing.T) {
	structural, telemetry := feasibilityInput([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"},
			RuntimeEstimate: "Dynamic"},
		{DeviceID: "ups-2", ProfileID: "static", TelemetryVariables: []string{"battery.runtime", "ups.status"},
			RuntimeEstimate: "Static"},
	})

	plan, _, err := Compile(structural, telemetry)
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if plan.Feasibility.Verdict != FeasibilityAdvisoryUnknown {
		t.Fatalf("verdict = %q, want Unknown", plan.Feasibility.Verdict)
	}
	if plan.Feasibility.Reason != "RuntimeEstimateStatic" {
		t.Fatalf("reason = %q, want RuntimeEstimateStatic", plan.Feasibility.Reason)
	}
	// A reason code without a subject tells an operator what happened but not where to look.
	if plan.Feasibility.Detail != "ups-2" {
		t.Fatalf("detail = %q, want the offending device ups-2", plan.Feasibility.Detail)
	}
}

// Unverified must not be read as Static. Every profile shipped today leaves it unset, so treating
// absence as a static estimate would downgrade every existing deployment's feasibility overnight.
func TestFeasibilityStaysOKWhenTheRuntimeEstimateIsUndeclared(t *testing.T) {
	structural, telemetry := feasibilityInput([]DeviceCapability{
		{DeviceID: "ups-1", ProfileID: "product", TelemetryVariables: []string{"battery.runtime", "ups.status"}},
	})

	plan, _, err := Compile(structural, telemetry)
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if plan.Feasibility.Verdict != FeasibilityAdvisoryOK {
		t.Fatalf("verdict = %q, want OK", plan.Feasibility.Verdict)
	}
}

// Incomplete telemetry outranks the estimate question: there is no runtime number to characterize.
func TestFeasibilityReportsIncompleteTelemetryBeforeTheEstimateDeclaration(t *testing.T) {
	structural, telemetry := feasibilityInput([]DeviceCapability{
		{DeviceID: "ups-2", ProfileID: "static", TelemetryVariables: []string{"battery.runtime", "ups.status"},
			RuntimeEstimate: "Static"},
	})
	telemetry.PowerDomains[0].RuntimeRemainingSeconds = nil

	plan, _, err := Compile(structural, telemetry)
	if err != nil {
		t.Fatalf("expected compile to succeed, got %v", err)
	}
	if plan.Feasibility.Reason != "TelemetryIncomplete" {
		t.Fatalf("reason = %q, want TelemetryIncomplete", plan.Feasibility.Reason)
	}
}
