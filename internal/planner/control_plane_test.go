package planner

import "testing"

func TestControlPlaneQuorumOrdering(t *testing.T) {
	for _, linear := range []bool{false, true} {
		for _, scenario := range []string{"unsafe", "one early", "terminal", "co-wave", "cumulative"} {
			t.Run(scenario+map[bool]string{true: " steps", false: " groups"}[linear], func(t *testing.T) {
				input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, ControlPlaneNodes: []ControlPlaneNode{{Name: "a", QuorumMember: true}, {Name: "b", QuorumMember: true}, {Name: "c", QuorumMember: true}}, Groups: []Group{{Name: "release", Action: "AgentShutdown"}, {Name: "later", Action: "Notify", After: []string{"release"}}}, GroupNodes: []GroupNodeMembership{{Group: "release", Releases: []string{"a", "b", "c"}}}}
				want := false
				switch scenario {
				case "one early":
					input.GroupNodes[0].Releases = []string{"a"}
					want = true
				case "terminal":
					input.Groups[0].After = []string{"later"}
					input.Groups[1].After = nil
					want = true
				case "co-wave":
					input.Groups[1].After = nil
				case "cumulative":
					input.GroupNodes[0].Releases = []string{"a"}
					input.Groups = append(input.Groups, Group{Name: "release-two", Action: "AgentShutdown", After: []string{"release"}, Before: []string{"later"}})
					input.GroupNodes = append(input.GroupNodes, GroupNodeMembership{Group: "release-two", Releases: []string{"b"}})
				}
				if linear {
					if scenario == "co-wave" {
						return
					}
					order := []string{"release", "later"}
					if scenario == "terminal" {
						order = []string{"later", "release"}
					}
					if scenario == "cumulative" {
						order = []string{"release", "release-two", "later"}
					}
					for _, name := range order {
						action := "AgentShutdown"
						if name == "later" {
							action = "Notify"
						}
						input.Steps = append(input.Steps, Step{ID: name, Action: action})
					}
					input.Groups = nil
				}
				_, diagnostics, err := Compile(input, TelemetryInputs{})
				if (err == nil) != want {
					t.Fatalf("err=%v diagnostics=%+v", err, diagnostics)
				}
				if !want {
					found := false
					for _, d := range diagnostics {
						found = found || d.Reason == "ControlPlaneQuorumLost"
					}
					if !found {
						t.Fatalf("wrong rejection: %+v", diagnostics)
					}
				}
			})
		}
	}
}

func TestControlPlaneMembershipAffectsPlanIdentity(t *testing.T) {
	input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, Steps: []Step{{ID: "notify", Action: "Notify"}}, ControlPlaneNodes: []ControlPlaneNode{{Name: "b", QuorumMember: true}, {Name: "a", QuorumMember: true}}}
	a, _, err := Compile(input, TelemetryInputs{})
	if err != nil {
		t.Fatal(err)
	}
	input.ControlPlaneNodes[0], input.ControlPlaneNodes[1] = input.ControlPlaneNodes[1], input.ControlPlaneNodes[0]
	b, _, err := Compile(input, TelemetryInputs{})
	if err != nil || a.Hash != b.Hash {
		t.Fatalf("unstable hash %v", err)
	}
	input.ControlPlaneNodes[0].QuorumMember = false
	c, _, err := Compile(input, TelemetryInputs{})
	if err != nil || c.Hash == a.Hash {
		t.Fatalf("membership missing from hash %v", err)
	}
}

func TestControlPlaneDeclarationGuards(t *testing.T) {
	for _, scenario := range []string{"unknown quorum", "multiple channels", "missing dependency", "explicit before"} {
		t.Run(scenario, func(t *testing.T) {
			input := StructuralInputs{Triggers: []Trigger{{Type: "OnBattery"}}, ControlPlaneNodes: []ControlPlaneNode{{Name: "a", QuorumMember: true}, {Name: "b", QuorumMember: true}, {Name: "c", QuorumMember: true}}, Groups: []Group{{Name: "release", Action: "AgentShutdown", Target: Target{AgentRefCount: 1}, After: []string{"work"}}, {Name: "work", Action: "Notify"}}, GroupNodes: []GroupNodeMembership{{Group: "release", Releases: []string{"a"}}}}
			wantReason := ""
			switch scenario {
			case "unknown quorum":
				for i := range input.ControlPlaneNodes {
					input.ControlPlaneNodes[i].QuorumMember = false
				}
				input.Groups[0].After = nil
				input.Groups[0].Before = []string{"work"}
				wantReason = "ControlPlaneQuorumUnknown"
			case "multiple channels":
				input.Groups[0].Target.AgentRefCount = 2
				wantReason = "TerminalControlPlaneMultipleChannels"
			case "missing dependency":
				input.Groups = input.Groups[:1]
				wantReason = "ControlPlaneLateDependencyMissing"
				input.Groups[0].After = nil
			case "explicit before":
				input.Groups[0].After = nil
				input.Groups[1].Before = []string{"release"}
			}
			_, diagnostics, err := Compile(input, TelemetryInputs{})
			if scenario == "explicit before" {
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range diagnostics {
					if d.Reason == "ControlPlaneLateDependencyMissing" {
						t.Fatal("explicit predecessor ignored")
					}
				}
				return
			}
			found := false
			for _, d := range diagnostics {
				if d.Reason == wantReason {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v err=%v", wantReason, diagnostics, err)
			}
		})
	}
}
