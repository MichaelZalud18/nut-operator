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

package capability

import (
	"fmt"
	"sort"
	"strings"
)

// identityVariables are the NUT variables that describe the product, not the
// unit. ups.serial is deliberately absent: it identifies one physical device,
// and a capability profile describes a model.
const (
	VariableUPSModel        = "ups.model"
	VariableUPSManufacturer = "ups.mfr"
	VariableUPSFirmware     = "ups.firmware"
)

// driverVariablePrefix is the NUT namespace describing the driver rather than the product.
//
// F-102: a profile is a product record, and no bundled profile carries a driver.* name. A draft
// carried ten of them, including driver.parameter.port -- which is the connection setting for one
// installation and belongs in the UPSDevice, not in a profile that a guide invites the user to
// send upstream for the shared catalog. driver.name and driver.version are just as wrong there:
// they record which NUT build read the device, so they change with the operand image while the
// product does not.
const driverVariablePrefix = "driver."

// isDriverVariable reports whether a NUT variable describes the driver rather than the device.
func isDriverVariable(name string) bool {
	return strings.HasPrefix(name, driverVariablePrefix)
}

// standardNUTVariables is the set of NUT variable names treated as standard for
// drafting purposes. It is not exhaustive of every driver extension in
// existence, and it does not need to be: anything outside it is reported as
// non-standard for a human to look at, which is the conservative direction.
var standardNUTVariables = map[string]struct{}{
	"ambient.humidity": {}, "ambient.temperature": {},
	"battery.charge": {}, "battery.charge.low": {}, "battery.charge.restart": {},
	"battery.charge.warning": {}, "battery.current": {}, "battery.date": {},
	"battery.mfr.date": {}, "battery.packs": {}, "battery.runtime": {},
	"battery.runtime.low": {}, "battery.temperature": {}, "battery.type": {},
	"battery.voltage": {}, "battery.voltage.high": {}, "battery.voltage.low": {},
	"battery.voltage.nominal": {},
	"device.mfr":              {}, "device.model": {}, "device.serial": {}, "device.type": {},
	"driver.name": {}, "driver.version": {}, "driver.version.data": {},
	"driver.version.internal": {},
	"input.current":           {}, "input.frequency": {}, "input.frequency.nominal": {},
	"input.sensitivity": {}, "input.transfer.high": {}, "input.transfer.low": {},
	"input.transfer.reason": {}, "input.voltage": {}, "input.voltage.maximum": {},
	"input.voltage.minimum": {}, "input.voltage.nominal": {},
	"outlet.count": {}, "outlet.switchable": {},
	"output.current": {}, "output.current.nominal": {}, "output.frequency": {},
	"output.frequency.nominal": {}, "output.power": {}, "output.power.nominal": {},
	"output.voltage": {}, "output.voltage.nominal": {},
	"ups.beeper.status": {}, "ups.delay.shutdown": {}, "ups.delay.start": {},
	"ups.firmware": {}, "ups.firmware.aux": {}, "ups.id": {}, "ups.load": {},
	"ups.load.high": {}, "ups.mfr": {}, "ups.mfr.date": {}, "ups.model": {},
	"ups.power": {}, "ups.power.nominal": {}, "ups.productid": {}, "ups.realpower": {},
	"ups.realpower.nominal": {}, "ups.serial": {}, "ups.shutdown": {},
	"ups.start.auto": {}, "ups.status": {}, "ups.temperature": {},
	"ups.test.date": {}, "ups.test.interval": {}, "ups.test.result": {},
	"ups.timer.reboot": {}, "ups.timer.shutdown": {}, "ups.timer.start": {},
	"ups.type": {}, "ups.vendorid": {},
}

// aliasCandidateTargets are the canonical variables worth trying to recover
// from a non-standard name. They are the ones the normalizer and trigger
// derivation actually read, so recovering anything else would add noise
// without changing behavior.
var aliasCandidateTargets = []string{
	"battery.charge",
	"battery.charge.low",
	"battery.runtime",
	"ups.load",
	"ups.status",
}

// ProbeObservation is one device read, supplied by the caller. The capability
// package performs no I/O.
type ProbeObservation struct {
	DeviceID  string
	Variables map[string]string
}

// DraftOptions tune the generated profile.
type DraftOptions struct {
	// ProfileName overrides the derived metadata.name.
	ProfileName string
}

// Draft is a proposed capability profile plus the reasoning behind it.
type Draft struct {
	ProfileName          string
	Manufacturer         string
	Model                string
	Firmware             string
	Variables            []string
	NonStandardVariables []string
	SuggestedAliases     map[string]string
	Notes                []string

	// DroppedDriverVariables counts the driver.* names the device reported and the draft left
	// out. Reported rather than silently discarded, so a user comparing the draft against
	// `upsc` output can see the difference was deliberate.
	DroppedDriverVariables int
}

// BuildDraft turns a device read into a proposed profile. It is deliberately
// conservative: it declares only what the device demonstrably reported, never
// declares actuation behaviors, and marks every inference as a suggestion.
func BuildDraft(observation ProbeObservation, options DraftOptions) Draft {
	draft := Draft{
		Manufacturer: strings.TrimSpace(observation.Variables[VariableUPSManufacturer]),
		Model:        strings.TrimSpace(observation.Variables[VariableUPSModel]),
		Firmware:     strings.TrimSpace(observation.Variables[VariableUPSFirmware]),
	}

	reported := make(map[string]struct{}, len(observation.Variables))
	driverVariables := 0
	for name := range observation.Variables {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if isDriverVariable(name) {
			driverVariables++
			continue
		}
		reported[name] = struct{}{}
		draft.Variables = append(draft.Variables, name)
		if _, standard := standardNUTVariables[name]; !standard {
			draft.NonStandardVariables = append(draft.NonStandardVariables, name)
		}
	}
	draft.DroppedDriverVariables = driverVariables
	sort.Strings(draft.Variables)
	sort.Strings(draft.NonStandardVariables)

	draft.SuggestedAliases = suggestAliases(reported, draft.NonStandardVariables)
	draft.ProfileName = options.ProfileName
	if draft.ProfileName == "" {
		draft.ProfileName = derivedProfileName(draft.Manufacturer, draft.Model)
	}
	draft.Notes = draftNotes(draft, reported)
	return draft
}

// suggestAliases proposes a mapping when a canonical variable is absent but a
// non-standard reported name plausibly carries it. The heuristic is narrow on
// purpose: a suggestion a human must confirm is useful, a wrong alias applied
// silently is the failure this whole mechanism exists to prevent.
func suggestAliases(reported map[string]struct{}, nonStandard []string) map[string]string {
	var suggestions map[string]string

	for _, target := range aliasCandidateTargets {
		if _, present := reported[target]; present {
			continue
		}
		for _, candidate := range nonStandard {
			if _, taken := suggestions[candidate]; taken {
				continue
			}
			if !plausibleAlias(candidate, target) {
				continue
			}
			if suggestions == nil {
				suggestions = map[string]string{}
			}
			suggestions[candidate] = target
			break
		}
	}
	return suggestions
}

// plausibleAlias reports whether a reported name looks like an abbreviated form
// of a canonical name: every dot-separated segment of the reported name appears
// in the canonical name, in order, allowing a known short-form prefix.
func plausibleAlias(reported, canonical string) bool {
	reportedSegments := strings.Split(expandShortForms(reported), ".")
	canonicalSegments := strings.Split(canonical, ".")
	if len(reportedSegments) == 0 || len(reportedSegments) > len(canonicalSegments) {
		return false
	}
	if reportedSegments[0] != canonicalSegments[0] {
		return false
	}

	next := 0
	for _, segment := range reportedSegments {
		matched := false
		for next < len(canonicalSegments) {
			if canonicalSegments[next] == segment {
				matched = true
				next++
				break
			}
			next++
		}
		if !matched {
			return false
		}
	}
	return true
}

// expandShortForms normalizes abbreviations drivers commonly use so the segment
// comparison in plausibleAlias can see through them.
func expandShortForms(name string) string {
	replacements := map[string]string{
		"batt":    "battery",
		"bat":     "battery",
		"pct":     "charge",
		"percent": "charge",
		"runtime": "runtime",
		"stat":    "status",
	}
	segments := strings.Split(name, ".")
	for i, segment := range segments {
		if expanded, exists := replacements[segment]; exists {
			segments[i] = expanded
		}
	}
	return strings.Join(segments, ".")
}

func derivedProfileName(manufacturer, model string) string {
	name := strings.TrimSpace(manufacturer + " " + model)
	if name == "" {
		return "unnamed-ups-profile"
	}
	var builder strings.Builder
	lastDash := false
	for _, char := range strings.ToLower(name) {
		switch {
		case (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9'):
			builder.WriteRune(char)
			lastDash = false
		case !lastDash && builder.Len() > 0:
			builder.WriteRune('-')
			lastDash = true
		}
	}
	derived := strings.Trim(builder.String(), "-")
	if derived == "" {
		return "unnamed-ups-profile"
	}
	return derived
}

func draftNotes(draft Draft, reported map[string]struct{}) []string {
	var notes []string
	if draft.Model == "" {
		notes = append(notes, "The device reported no ups.model value, so the draft has no match selector. "+
			"Fill in spec.selector.model with the model string this device should match, or the profile will never be selected.")
	}
	if _, present := reported["ups.status"]; !present {
		notes = append(notes, "The device did not report ups.status. Every NUT driver is expected to expose it, "+
			"so this probe may have read an incomplete variable set; re-probe before trusting the result.")
	}
	if len(draft.NonStandardVariables) > 0 {
		notes = append(notes, fmt.Sprintf("%d reported variable(s) are not standard NUT names. "+
			"Check whether any carry a standard reading under a different name and belong in spec.telemetry.aliases.",
			len(draft.NonStandardVariables)))
	}
	if draft.DroppedDriverVariables > 0 {
		notes = append(notes, fmt.Sprintf("%d reported driver.* variable(s) were left out. They describe the NUT "+
			"driver rather than the product -- driver.version changes with the operand image, and "+
			"driver.parameter.* is the connection setting for one installation and belongs in the UPSDevice.",
			draft.DroppedDriverVariables))
	}
	notes = append(notes, "The actuation section is intentionally empty. Actuation commands can cut power to "+
		"equipment, so support is declared only after it has been verified against the firmware in use.")
	return notes
}
