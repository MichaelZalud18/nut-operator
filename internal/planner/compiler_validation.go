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
	"slices"
)

func validateStructuralInputs(input StructuralInputs) []Diagnostic {
	return validateStructuralInputsWithIndex(input, newCommunicationIndex(input))
}

func validateStructuralInputsWithIndex(input StructuralInputs, index *communicationIndex) []Diagnostic {
	var diagnostics []Diagnostic
	if input.AbortBehavior != "" && input.AbortBehavior != "HaltAndSurface" {
		diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "AbortBehaviorUnsupported", Message: "v1 supports only HaltAndSurface abort behavior"})
	}
	if input.AbortNotify {
		diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "AbortNotifyUnsupported", Message: "abort-only notifications are unsupported; set abortPolicy.notify to false"})
	}
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
	// Report each duplicate step ID once, even if it occurs more than twice.
	reportedStepIDs := map[string]struct{}{}
	for _, step := range input.Steps {
		if step.ContinueOnError {
			diagnostics = append(diagnostics, Diagnostic{Severity: DiagnosticError, Reason: "ContinueOnErrorUnsupported", Subject: step.ID, Message: "v1 stops after action failure; continueOnError must be false"})
		}
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
		// Concat avoids mutating the input slices' backing arrays when they have spare capacity.
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
	if hasGroupCycle(input, index) {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "DependencyCycle",
			Message:  "shutdown groups contain a dependency cycle",
		})
	}

	return diagnostics
}

func hasGroupCycle(input StructuralInputs, index *communicationIndex) bool {
	edges := graphSuccessors(buildGroupGraphForInputs(input, index))
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

	for _, group := range input.Groups {
		if visit(group.Name) {
			return true
		}
	}
	return false
}

func hasError(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == DiagnosticError {
			return true
		}
	}
	return false
}
