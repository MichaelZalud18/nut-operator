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

	"github.com/MichaelZalud18/nut-operator/internal/capability"
)

// validateTriggerCapabilities implements PL-19: every declared trigger is
// checked against the capability profiles resolved for the devices it targets.
//
// The some/all split is the whole point. A trigger no device can ever satisfy
// produces a plan that cannot fire, which is a broken plan, not a degraded one,
// so it is rejected at compile time rather than discovered during an outage. A
// trigger only some devices can satisfy still works for the rest, so it warns
// and the plan compiles.
//
// An unidentified device does not by itself escalate a
// warning into a rejection: the floor is the terminal tier of the matching
// chain, not an error condition.
func validateTriggerCapabilities(input StructuralInputs) []Diagnostic {
	if len(input.DeviceCapabilities) == 0 || len(input.Triggers) == 0 {
		return nil
	}

	declared := make(map[string][]string, len(input.DeviceCapabilities))
	allDevices := make([]string, 0, len(input.DeviceCapabilities))
	for _, device := range input.DeviceCapabilities {
		if device.DeviceID == "" {
			continue
		}
		declared[device.DeviceID] = device.TelemetryVariables
		allDevices = append(allDevices, device.DeviceID)
	}
	sort.Strings(allDevices)

	domains := make(map[string][]string, len(input.PowerDomains))
	for _, domain := range input.PowerDomains {
		domains[domain.Name] = domain.UPSDevices
	}

	var diagnostics []Diagnostic
	for _, trigger := range input.Triggers {
		targets, scope := triggerTargetDevices(trigger, domains, allDevices)
		if len(targets) == 0 {
			continue
		}

		required := capability.RequiredVariablesForTrigger(capability.TriggerType(trigger.Type))
		if len(required) == 0 {
			continue
		}

		var unsupported []string
		for _, device := range targets {
			variables, known := declared[device]
			if !known {
				continue
			}
			if !coversVariables(variables, required) {
				unsupported = append(unsupported, device)
			}
		}
		if len(unsupported) == 0 {
			continue
		}
		sort.Strings(unsupported)

		if len(unsupported) == len(targets) {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "TriggerUnsupportedByAllDevices",
				Subject:  trigger.Type,
				Message: fmt.Sprintf("trigger %q requires NUT %s, which no device in %s declares (%s); this plan can never fire",
					trigger.Type, variableList(required), scope, strings.Join(unsupported, ", ")),
			})
			continue
		}

		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "TriggerDegradedByDeviceCapability",
			Subject:  trigger.Type,
			Message: fmt.Sprintf("trigger %q requires NUT %s, which %s in %s does not declare; the trigger still fires for the remaining devices and this plan is degraded for those it cannot cover",
				trigger.Type, variableList(required), strings.Join(unsupported, ", "), scope),
		})
	}
	return diagnostics
}

// triggerTargetDevices resolves which devices a trigger is validated against,
// and names that scope for diagnostics. Explicit device references win over
// domain references, matching how the trigger evaluator selects devices.
func triggerTargetDevices(trigger Trigger, domains map[string][]string, allDevices []string) ([]string, string) {
	if len(trigger.UPSDevices) > 0 {
		devices := append([]string(nil), trigger.UPSDevices...)
		sort.Strings(devices)
		return devices, "the devices it names"
	}

	if len(trigger.PowerDomains) > 0 {
		seen := map[string]struct{}{}
		var devices []string
		for _, domain := range trigger.PowerDomains {
			for _, device := range domains[domain] {
				if _, exists := seen[device]; exists {
					continue
				}
				seen[device] = struct{}{}
				devices = append(devices, device)
			}
		}
		sort.Strings(devices)
		names := append([]string(nil), trigger.PowerDomains...)
		sort.Strings(names)
		return devices, fmt.Sprintf("power domain %s", strings.Join(names, ", "))
	}

	return allDevices, "any power domain"
}

func coversVariables(declared, required []string) bool {
	available := make(map[string]struct{}, len(declared))
	for _, variable := range declared {
		available[variable] = struct{}{}
	}
	for _, variable := range required {
		if _, exists := available[variable]; !exists {
			return false
		}
	}
	return true
}

func variableList(variables []string) string {
	if len(variables) == 1 {
		return variables[0]
	}
	return strings.Join(variables, " and ")
}
