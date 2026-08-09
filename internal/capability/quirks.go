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
	"path"
	"sort"
	"strconv"
	"strings"
)

// resolveQuirks reduces a profile's declared quirks to those in force for one
// device's firmware (F-26, OD-22).
//
// The rule a bare string could not express: a quirk fixed in firmware 1.4.18 must
// stop applying to a device running 1.6.1. Before this, every historical defect of
// a model family followed every device of that family forever, and the operator
// had no way to tell a current device from an afflicted one.
//
// Unscoped quirks still apply unconditionally. Most quirks are properties of a
// model rather than of a build, and requiring scope on all of them would push
// authors toward inventing ranges they have not verified.
func resolveQuirks(device Device, profileID string, quirks []Quirk) ([]string, []Diagnostic) {
	if len(quirks) == 0 {
		return nil, nil
	}

	var diagnostics []Diagnostic
	names := make([]string, 0, len(quirks))
	for _, quirk := range quirks {
		if quirk.Name == "" {
			continue
		}
		applies, diagnostic := quirkAppliesToFirmware(device, profileID, quirk)
		if diagnostic != nil {
			diagnostics = append(diagnostics, *diagnostic)
		}
		if applies {
			names = append(names, quirk.Name)
		}
	}
	sort.Strings(names)
	return names, diagnostics
}

// quirkAppliesToFirmware decides one quirk against one device's firmware.
//
// A device that reports no firmware at all keeps every scoped quirk. The scope
// says which builds are affected, and an unknown build cannot be shown to be
// exempt -- assuming the good case would quietly drop a real defect, whereas the
// conservative direction only carries a stale one.
func quirkAppliesToFirmware(device Device, profileID string, quirk Quirk) (bool, *Diagnostic) {
	if quirk.Firmware == nil {
		return true, nil
	}
	scope := quirk.Firmware
	if len(scope.Matches) == 0 && scope.Below == "" {
		return true, nil
	}
	if device.Firmware == "" {
		return true, &Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "QuirkFirmwareUnknown",
			Subject:  device.ID,
			Message: fmt.Sprintf(
				"device %q reports no firmware, so firmware-scoped quirk %q from profile %q is applied conservatively",
				device.ID, quirk.Name, profileID),
		}
	}

	for _, pattern := range scope.Matches {
		if pattern == "" {
			continue
		}
		if matched, err := path.Match(pattern, device.Firmware); err == nil && matched {
			return true, nil
		}
	}

	if scope.Below == "" {
		return false, nil
	}

	deviceVersion, deviceOK := parseDottedVersion(device.Firmware)
	boundVersion, boundOK := parseDottedVersion(scope.Below)
	if !deviceOK || !boundOK {
		// Refusing to guess. Applying a "fixed in X" quirk to firmware that cannot
		// be placed against X would be a coin flip presented as a decision.
		return true, &Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "QuirkFirmwareNotComparable",
			Subject:  device.ID,
			Message: fmt.Sprintf(
				"quirk %q from profile %q is scoped below firmware %q but device %q reports %q, which is not a dotted-numeric version; the quirk is applied conservatively",
				quirk.Name, profileID, scope.Below, device.ID, device.Firmware),
		}
	}
	return compareDottedVersions(deviceVersion, boundVersion) < 0, nil
}

// parseDottedVersion accepts firmware strings that are purely dotted numbers.
//
// Deliberately strict. Accepting "1.6.1-rc2" or "EL-2.3b" by stripping what does
// not parse would invent an ordering the vendor never defined, and the whole point
// of the Below rule is that it can be trusted when it fires.
func parseDottedVersion(raw string) ([]int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}
	parts := strings.Split(trimmed, ".")
	parsed := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return nil, false
		}
		parsed = append(parsed, value)
	}
	return parsed, true
}

// compareDottedVersions orders two parsed versions, treating a missing component
// as zero so 1.4 and 1.4.0 compare equal.
func compareDottedVersions(left, right []int) int {
	longest := len(left)
	if len(right) > longest {
		longest = len(right)
	}
	for i := 0; i < longest; i++ {
		var leftPart, rightPart int
		if i < len(left) {
			leftPart = left[i]
		}
		if i < len(right) {
			rightPart = right[i]
		}
		if leftPart != rightPart {
			if leftPart < rightPart {
				return -1
			}
			return 1
		}
	}
	return 0
}

// validateQuirkDeclarations rejects quirk scopes that cannot be evaluated.
func validateQuirkDeclarations(profileID string, quirks []Quirk) []Diagnostic {
	var diagnostics []Diagnostic
	seen := map[string]struct{}{}
	for _, quirk := range quirks {
		if quirk.Name == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "QuirkNameRequired",
				Subject:  profileID,
				Message:  fmt.Sprintf("capability profile %q declares a quirk with no name", profileID),
			})
			continue
		}
		if _, duplicate := seen[quirk.Name]; duplicate {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateQuirk",
				Subject:  profileID,
				Message:  fmt.Sprintf("capability profile %q declares quirk %q more than once", profileID, quirk.Name),
			})
		}
		seen[quirk.Name] = struct{}{}
		if quirk.Firmware == nil {
			continue
		}
		for _, pattern := range quirk.Firmware.Matches {
			if _, err := path.Match(pattern, "probe"); err != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "InvalidQuirkFirmwareGlob",
					Subject:  profileID,
					Message: fmt.Sprintf(
						"capability profile %q quirk %q declares an invalid firmware glob %q", profileID, quirk.Name, pattern),
				})
			}
		}
		if quirk.Firmware.Below != "" {
			if _, ok := parseDottedVersion(quirk.Firmware.Below); !ok {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "InvalidQuirkFirmwareBound",
					Subject:  profileID,
					Message: fmt.Sprintf(
						"capability profile %q quirk %q declares firmware.below %q, which must be a dotted-numeric version",
						profileID, quirk.Name, quirk.Firmware.Below),
				})
			}
		}
	}
	return diagnostics
}

// copyQuirks defensively copies quirk declarations, including their scopes.
func copyQuirks(quirks []Quirk) []Quirk {
	if len(quirks) == 0 {
		return nil
	}
	copied := make([]Quirk, 0, len(quirks))
	for _, quirk := range quirks {
		next := Quirk{Name: quirk.Name}
		if quirk.Firmware != nil {
			scope := QuirkFirmware{Below: quirk.Firmware.Below}
			scope.Matches = append([]string(nil), quirk.Firmware.Matches...)
			sort.Strings(scope.Matches)
			next.Firmware = &scope
		}
		copied = append(copied, next)
	}
	sort.SliceStable(copied, func(left, right int) bool { return copied[left].Name < copied[right].Name })
	return copied
}
