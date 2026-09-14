package planner

import "fmt"

func controlPlaneDiagnostics(input StructuralInputs, plan Plan) []Diagnostic {
	if len(input.ControlPlaneNodes) == 0 {
		return nil
	}
	members := map[string]bool{}
	quorumMembers := 0
	for _, node := range input.ControlPlaneNodes {
		if _, duplicate := members[node.Name]; duplicate || node.Name == "" {
			return []Diagnostic{{Severity: DiagnosticError, Reason: "ControlPlaneMembershipInvalid", Message: "control-plane membership requires unique nonempty node names"}}
		}
		members[node.Name] = node.QuorumMember
	}
	for _, voter := range members {
		if voter {
			quorumMembers++
		}
	}
	releases := map[string][]string{}
	for _, group := range input.GroupNodes {
		releases[group.Group] = group.Releases
	}
	waves := plan.Waves
	if len(waves) == 0 {
		for _, step := range plan.Steps {
			waves = append(waves, Wave{Groups: []string{step.ID}})
		}
	}
	released := map[string]bool{}
	var diagnostics []Diagnostic
	for wi, wave := range waves {
		for _, name := range wave.Groups {
			for _, node := range releases[name] {
				if _, ok := members[node]; ok {
					released[node] = true
				}
			}
		}
		remaining, voters := len(members), quorumMembers
		for node := range released {
			remaining--
			if members[node] {
				voters--
			}
		}
		// Co-wave actions may run in either order; only a single final handoff
		// group can consume the remaining control plane.
		terminal := wi == len(waves)-1 && len(wave.Groups) == 1
		terminalCP := false
		if terminal {
			for _, node := range releases[wave.Groups[0]] {
				if _, ok := members[node]; ok {
					terminalCP = true
				}
			}
		}
		if terminalCP {
			diagnostics = append(diagnostics, terminalChannelDiagnostics(input, wave.Groups[0])...)
		}
		if !terminal && len(released) > 0 && len(members) > 1 && quorumMembers == 0 {
			diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "ControlPlaneQuorumUnknown", Message: "declare control-plane quorum members before releasing an HA control-plane node ahead of terminal handoff"})
			break
		}
		if !terminal && (remaining == 0 || (quorumMembers > 0 && voters < quorumMembers/2+1)) {
			diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "ControlPlaneQuorumLost", Message: fmt.Sprintf("wave %d leaves %d control-plane nodes and %d quorum members before orchestration completes", wi, remaining, voters)})
			break
		}
	}
	return append(diagnostics, controlPlaneLateDiagnostics(input, releases, members)...)
}

func terminalChannelDiagnostics(input StructuralInputs, name string) []Diagnostic {
	counts := map[string]int{}
	for _, group := range input.Groups {
		counts[group.Name] = group.Target.AgentRefCount
	}
	if len(input.Groups) == 0 {
		for _, step := range input.Steps {
			counts[step.ID] = step.Target.AgentRefCount
		}
	}
	if counts[name] > 1 {
		return []Diagnostic{{Severity: DiagnosticError, Reason: "TerminalControlPlaneMultipleChannels", Subject: name, Message: "terminal control-plane handoff requires one NodePowerAgent so every signal can be published in one Secret update"}}
	}
	return nil
}

func controlPlaneLateDiagnostics(input StructuralInputs, releases map[string][]string, members map[string]bool) []Diagnostic {
	var diagnostics []Diagnostic
	for _, group := range input.Groups {
		explicitLate := len(group.Requires) > 0 || len(group.After) > 0
		for _, predecessor := range input.Groups {
			for _, successor := range predecessor.Before {
				if successor == group.Name {
					explicitLate = true
				}
			}
		}
		for _, node := range releases[group.Name] {
			if _, ok := members[node]; ok && !explicitLate {
				diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticWarning, Reason: "ControlPlaneLateDependencyMissing", Subject: group.Name, Message: "control-plane release should declare explicit late dependencies, not rely only on its tier"})
				break
			}
		}
	}
	return diagnostics
}
