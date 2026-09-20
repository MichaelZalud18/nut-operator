package planner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

// These fingerprints were captured before ENG-2 from the full serialized plan,
// ordered diagnostics, and error. Existing semantic tests explain individual
// expectations; these guard against incidental changes during internal refactors.
func TestCompileCompatibility(t *testing.T) {
	fixture := loadPlannerFixture(t, "testdata/complex_wave_plan.json")
	for _, tc := range []struct {
		name      string
		input     StructuralInputs
		telemetry TelemetryInputs
		history   HistoryInputs
		want      string
	}{
		{name: "complex estimates", input: fixture.Structural.toPlanner(t), telemetry: fixture.Telemetry, history: fixture.History.toPlanner(t), want: "7212ab69638c42f211fdeae3e87ee8e673301a0e250bd17087130fd0e84e12dc"},
		{name: "carrier release", input: communicationReleaseInput(), want: "a920d17a0be205008e32d363c249d362ea4f391afe78197d3ebbbb78c782cffd"},
		{name: "scoped groups", input: compatibilityScopedInput(false), want: "4841a90cf0334a78637716c8643a3775d1fecf3ad8cd55ab0d2e916a58f44b4e"},
		{name: "scoped linear", input: compatibilityScopedInput(true), want: "c8ebda7085fc7733c11b53749ebcf906d54f0a8f1a073207165262184ddea6e5"},
		{name: "unknown supply", input: compatibilityCommunicationInput("unknown"), want: "40a56ac99b8d00b50a9dea99ba7497f62e03de2b5b22d2910b1e6636ae0c99cd"},
		{name: "transitive cyclic paths", input: compatibilityCommunicationInput("cycle"), want: "3a2c8eadeee904da6b89cc4d4ff039b95307aa21a3dcf161ae4af726fd23e575"},
		{name: "shared service", input: compatibilityCommunicationInput("service"), want: "017ef9f5916dbb8828334b0c7e7d25abc8bee2938d1aed53dd37abbaadcd452e"},
		{name: "duplicate domains", input: compatibilityCommunicationInput("duplicates"), want: "f427a44da83ea0c2b0b5c977c7247a4118eae1ae6fae2a3be26e906884498d8a"},
		{name: "incomplete membership", input: compatibilityCommunicationInput("incomplete"), want: "b2c47b7bc6b32a5c47e9c1fa485d0a78c79157d22da30f0741f2d91acd5196ed"},
		{name: "invalid communication", input: compatibilityCommunicationInput("invalid"), want: "0daaedf1c67e2665c266a410e003e9e48eb6004249a191848a087599b3726aef"},
		{name: "backward linear path", input: compatibilityCommunicationInput("backward"), want: "b14c11876dd0a002ba1259441423820ac8875123d680ad0d1baaf9a1365d2594"},
		{name: "empty execution scope", input: StructuralInputs{ExecutionScope: &ExecutionScope{}}, want: "814b8e7d8fcfd97f60a7a25c9fc64843c7d2bf5974b816e3edfd5232cb3b629f"},
		{name: "empty plan", input: StructuralInputs{}, want: "4775bec07087774d60cb9e6ec0762a828217f793a4d5039c39c31db2e2f8c57b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := compatibilityJSON(t, tc.input)
			plan, diagnostics, err := CompileWithHistory(tc.input, tc.telemetry, tc.history)
			errorText := ""
			if err != nil {
				errorText = err.Error()
			}
			output := compatibilityJSON(t, struct {
				Plan        Plan
				Diagnostics []Diagnostic
				Error       string
			}{plan, diagnostics, errorText})
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(output)))
			if got != tc.want {
				t.Errorf("compatibility fingerprint = %q, want %q; output: %s", got, tc.want, output)
			}
			if after := compatibilityJSON(t, tc.input); before != after {
				t.Fatal("compilation mutated structural inputs")
			}
			// Reverse unordered collections, keeping authored linear step order.
			slices.Reverse(tc.input.Groups)
			slices.Reverse(tc.input.GroupNodes)
			slices.Reverse(tc.input.PowerDomains)
			slices.Reverse(tc.input.CommunicationDependencies)
			slices.Reverse(tc.input.CommunicationServices)
			permutedPlan, permutedDiagnostics, permutedErr := CompileWithHistory(tc.input, tc.telemetry, tc.history)
			if compatibilityJSON(t, plan) != compatibilityJSON(t, permutedPlan) ||
				compatibilityJSON(t, diagnostics) != compatibilityJSON(t, permutedDiagnostics) ||
				fmt.Sprint(err) != fmt.Sprint(permutedErr) {
				t.Fatal("reordering unordered input collections changed compilation output")
			}
		})
	}
}

func compatibilityJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func compatibilityScopedInput(linear bool) StructuralInputs {
	input := partialDomainInput(Trigger{Type: "OnBattery", PowerDomains: []string{"rack-a"}})
	input.PowerDomains[0].Infrastructure = []string{"switch-a"}
	input.PowerDomains[1].Infrastructure = []string{"switch-b"}
	input.CommunicationDependencies = []CommunicationDependency{
		{Dependent: "node-a", Carrier: "switch-a", Source: "path-a"},
		{Dependent: "node-b", Carrier: "switch-b", Source: "path-b"},
	}
	if linear {
		for _, group := range input.Groups {
			input.Steps = append(input.Steps, Step{ID: group.Name, Action: group.Action, Target: group.Target})
		}
		input.Groups = nil
	}
	return input
}

func compatibilityCommunicationInput(scenario string) StructuralInputs {
	input := communicationReleaseInput()
	switch scenario {
	case "unknown":
		input.PowerDomains = nil
	case "cycle":
		input.CommunicationDependencies = append(input.CommunicationDependencies,
			CommunicationDependency{Dependent: "carrier", Carrier: "upstream", Source: "up"},
			CommunicationDependency{Dependent: "upstream", Carrier: "carrier", Source: "down"})
	case "service":
		input.CommunicationServices = []CommunicationServicePath{
			{Service: "OperatorAPI", Entities: []string{"carrier"}}, {Service: "NUT", Exempt: true},
		}
		input.Groups = append(input.Groups, Group{Name: "notify", Action: "Notify"})
	case "duplicates":
		input.PowerDomains = append(input.PowerDomains, input.PowerDomains[0],
			PowerDomainMembership{Name: "other", UPSDevices: []string{"", "ups-b"}, Infrastructure: []string{"carrier", "carrier"}})
		input.CommunicationDependencies = append(input.CommunicationDependencies, input.CommunicationDependencies[0])
	case "incomplete":
		input.GroupNodes = input.GroupNodes[1:]
		input.CommunicationDependencies = append(input.CommunicationDependencies,
			CommunicationDependency{Dependent: "outside", Carrier: "unknown", Source: "outside-path"})
	case "invalid":
		input.CommunicationDependencies = append(input.CommunicationDependencies,
			CommunicationDependency{Dependent: "carrier", Carrier: "carrier"},
			CommunicationDependency{Dependent: "consumer"})
	case "backward":
		for _, group := range input.Groups {
			input.Steps = append(input.Steps, Step{ID: group.Name, Action: group.Action, Target: group.Target})
		}
		slices.Reverse(input.Steps)
		input.Groups = nil
	}
	return input
}
