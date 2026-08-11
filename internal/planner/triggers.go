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

		everyDevice := len(unsupported) == len(targets)
		diagnostics = append(diagnostics,
			evaluateTriggerFallback(trigger, unsupported, scope, required, declared, everyDevice)...)
	}
	return diagnostics
}

// evaluateTriggerFallback applies the OD-9 substitution rules to one trigger's
// uncovered devices.
//
// Substitution is declared, never inferred. The planner will say exactly which
// fallback would cover the gap, but it will not quietly install it: doing so
// changes when nodes begin powering off, and every runtime-feasibility number in
// the compiled plan would then be computed against a trigger that is not the one
// that fires. A plan that reports itself covered while firing at a different time
// is worse than one that reports the gap.
func evaluateTriggerFallback(
	trigger Trigger,
	unsupported []string,
	scope string,
	required []string,
	declared map[string][]string,
	everyDevice bool,
) []Diagnostic {
	devices := strings.Join(unsupported, ", ")
	suggested, hasSubstitute := capability.CoarserTriggerForFallback(capability.TriggerType(trigger.Type))

	if trigger.FallbackType == "" {
		// No fallback declared. A trigger no device can satisfy stays a hard rejection: a plan whose
		// triggers can never fire is not degraded, it is non-functional.
		if everyDevice {
			return []Diagnostic{{
				Severity: DiagnosticError,
				Reason:   "TriggerUnsupportedByAllDevices",
				Subject:  trigger.Type,
				Message: fmt.Sprintf(
					"trigger %q requires NUT %s, which no device in %s declares (%s); this plan can never fire%s",
					trigger.Type, variableList(required), scope, devices, fallbackHint(hasSubstitute, suggested)),
			}}
		}
		return []Diagnostic{{
			Severity: DiagnosticWarning,
			Reason:   "TriggerDegradedByDeviceCapability",
			Subject:  trigger.Type,
			Message: fmt.Sprintf(
				"trigger %q requires NUT %s, which %s in %s does not declare; the trigger still fires for the remaining devices and this plan is degraded for those it cannot cover%s",
				trigger.Type, variableList(required), devices, scope, fallbackHint(hasSubstitute, suggested)),
		}}
	}

	// A fallback that is not strictly coarser is rejected rather than ignored. Accepting one would
	// let a flow declare a substitute needing telemetry the device is already known not to report,
	// which reads as coverage and provides none.
	if !hasSubstitute || trigger.FallbackType != string(suggested) {
		return []Diagnostic{{
			Severity: DiagnosticError,
			Reason:   "TriggerFallbackNotCoarser",
			Subject:  trigger.Type,
			Message: fmt.Sprintf(
				"trigger %q declares fallback %q, which is not a coarser class for it; %s",
				trigger.Type, trigger.FallbackType, coarserAdvice(hasSubstitute, suggested, trigger.Type)),
		}}
	}

	// The fallback has to hold up against the devices that failed the primary, not against the
	// domain as a whole -- those are precisely the devices it exists to cover.
	fallbackRequired := capability.RequiredVariablesForTrigger(suggested)
	var stillUncovered []string
	for _, device := range unsupported {
		variables, known := declared[device]
		if !known {
			continue
		}
		if !coversVariables(variables, fallbackRequired) {
			stillUncovered = append(stillUncovered, device)
		}
	}
	if len(stillUncovered) > 0 {
		sort.Strings(stillUncovered)
		return []Diagnostic{{
			Severity: DiagnosticError,
			Reason:   "TriggerFallbackUnsatisfiable",
			Subject:  trigger.Type,
			Message: fmt.Sprintf(
				"trigger %q declares fallback %q, which requires NUT %s that %s still does not declare; the fallback covers nothing for those devices",
				trigger.Type, trigger.FallbackType, variableList(fallbackRequired), strings.Join(stillUncovered, ", ")),
		}}
	}

	// Informational, not a warning: the gap was declared and is covered, so the plan is not degraded.
	// It is still reported, because substitution means those devices act on a different condition
	// than the one the author wrote, and that belongs in the compile record.
	return []Diagnostic{{
		Severity: DiagnosticInfo,
		Reason:   "TriggerSubstituted",
		Subject:  trigger.Type,
		Message: fmt.Sprintf(
			"trigger %q cannot be satisfied by %s in %s, so the declared fallback %q applies to those devices; they act on %s rather than %s",
			trigger.Type, devices, scope, trigger.FallbackType,
			variableList(fallbackRequired), variableList(required)),
	}}
}

func fallbackHint(hasSubstitute bool, suggested capability.TriggerType) string {
	if !hasSubstitute {
		return ""
	}
	return fmt.Sprintf(". Declaring fallbackType: %s would cover them", suggested)
}

func coarserAdvice(hasSubstitute bool, suggested capability.TriggerType, triggerType string) string {
	if !hasSubstitute {
		return fmt.Sprintf(
			"%q already needs only the telemetry every device reports, so no coarser class exists and fallbackType must be unset",
			triggerType)
	}
	return fmt.Sprintf("the only coarser class is %q", suggested)
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
