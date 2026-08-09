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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
)

const (
	DiagnosticError   = "Error"
	DiagnosticWarning = "Warning"
)

var ErrRejected = errors.New("capability matcher rejected profile inputs")

// Match selects one profile using the documented precedence chain. CRD profiles
// win over bundled profiles within the same tier, then highest semver, then a
// lexicographic tiebreak.
func Match(device Device, profiles []Profile) (MatchResult, []Diagnostic, error) {
	normalized := normalizeProfiles(profiles)
	diagnostics := validateProfiles(normalized)
	if hasError(diagnostics) {
		return MatchResult{}, diagnostics, ErrRejected
	}

	candidates := matchingCandidates(device, normalized)
	if len(candidates) == 0 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "UnidentifiedProfileMissing",
			Subject:  device.ID,
			Message:  "no capability profile matched and no unidentified-device profile is available",
		})
		return MatchResult{}, diagnostics, ErrRejected
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].betterThan(candidates[j])
	})

	best := candidates[0]
	// Quirk scope is resolved here, against the device that actually matched, so
	// downstream consumers get the quirks in force rather than the model family's
	// full history (F-26).
	resolvedQuirks, quirkDiagnostics := resolveQuirks(device, best.profile.ID, best.profile.Quirks)
	diagnostics = append(diagnostics, quirkDiagnostics...)
	result := MatchResult{
		DeviceID:           device.ID,
		ProfileID:          best.profile.ID,
		ProfileVersion:     best.profile.Version,
		ProfileSource:      best.profile.Source,
		ProfileHash:        stableHash(best.profile),
		Tier:               best.tier,
		Unidentified:       best.tier == MatchTierUnidentified,
		TelemetryVariables: append([]string(nil), best.profile.TelemetryVariables...),
		TelemetryAliases:   copyAliases(best.profile.TelemetryAliases),
		ActuationBehaviors: append([]string(nil), best.profile.ActuationBehaviors...),
		Quirks:             resolvedQuirks,
	}
	if result.Unidentified {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "DeviceUnidentified",
			Subject:  device.ID,
			Message:  fmt.Sprintf("device %q matched no product capability profile and is unidentified", device.ID),
		})
		// Only report a missing model when it actually cost the device a
		// match. The three most specific tiers all key off the model, so a
		// model-less device that floored had no way to reach a product
		// profile -- but one that matched by driver family lost nothing, and
		// warning there would be noise rather than the honest signal RS-19
		// asks for.
		if device.Model == "" {
			message := fmt.Sprintf("device %q declares no model key, so the model, model+firmware, and model-glob match tiers were unreachable and it is unidentified; set spec.identity.model to match a product profile", device.ID)
			if device.DriverFamily == "" {
				message = fmt.Sprintf("device %q has no model or driver-family key; the device cannot be identified", device.ID)
			}
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticWarning,
				Reason:   "ProviderModelMissing",
				Subject:  device.ID,
				Message:  message,
			})
		}
	}

	return result, diagnostics, nil
}

// SupportsTrigger derives trigger support from declared telemetry variables.
func SupportsTrigger(profile Profile, trigger TriggerType) bool {
	return satisfiesTrigger(profile.TelemetryVariables, trigger)
}

// satisfiesTrigger reports whether a declared variable set covers a trigger
// class. Aliases are deliberately not consulted here: an alias renames a
// variable the device already reports, and the profile is expected to declare
// the canonical name it resolves to.
func satisfiesTrigger(declared []string, trigger TriggerType) bool {
	required := RequiredVariablesForTrigger(trigger)
	if len(required) == 0 {
		return true
	}
	variables := map[string]struct{}{}
	for _, variable := range declared {
		variables[variable] = struct{}{}
	}
	for _, variable := range required {
		if _, exists := variables[variable]; !exists {
			return false
		}
	}
	return true
}

// RequiredVariablesForTrigger returns the profile variables that imply support
// for a trigger class.
func RequiredVariablesForTrigger(trigger TriggerType) []string {
	switch trigger {
	case TriggerOnBattery, TriggerLowBattery:
		return []string{"ups.status"}
	case TriggerRuntimeBelow:
		return []string{"battery.runtime"}
	case TriggerChargeBelow:
		return []string{"battery.charge"}
	case TriggerTelemetryStale:
		return nil
	default:
		return []string{"__unsupported_trigger__"}
	}
}

type candidate struct {
	profile    Profile
	tier       MatchTier
	tierRank   int
	sourceRank int
	version    semanticVersion
}

func matchingCandidates(device Device, profiles []Profile) []candidate {
	var candidates []candidate
	for _, profile := range profiles {
		tier, rank, matched := matchTier(device, profile.Selector)
		if !matched {
			continue
		}
		candidates = append(candidates, candidate{
			profile:    profile,
			tier:       tier,
			tierRank:   rank,
			sourceRank: sourceRank(profile.Source),
			version:    parseSemanticVersion(profile.Version),
		})
	}
	return candidates
}

func matchTier(device Device, selector ProfileSelector) (MatchTier, int, bool) {
	if device.Model != "" && selector.Model != "" && selector.Firmware != "" &&
		selector.Model == device.Model && selector.Firmware == device.Firmware {
		return MatchTierExactModelFirmware, 0, true
	}
	if device.Model != "" && selector.Model != "" && selector.Firmware == "" && selector.Model == device.Model {
		return MatchTierExactModel, 1, true
	}
	if device.Model != "" && selector.ModelGlob != "" {
		if matched, _ := path.Match(selector.ModelGlob, device.Model); matched {
			return MatchTierModelGlob, 2, true
		}
	}
	if device.DriverFamily != "" && selector.DriverFamily != "" && selector.DriverFamily == device.DriverFamily {
		return MatchTierDriverFamily, 3, true
	}
	if selector.Universal {
		return MatchTierUnidentified, 4, true
	}
	return "", 0, false
}

func (left candidate) betterThan(right candidate) bool {
	switch {
	case left.tierRank != right.tierRank:
		return left.tierRank < right.tierRank
	case left.sourceRank != right.sourceRank:
		return left.sourceRank > right.sourceRank
	case left.version.compare(right.version) != 0:
		return left.version.compare(right.version) > 0
	default:
		return left.profile.ID+":"+left.profile.Version < right.profile.ID+":"+right.profile.Version
	}
}

func validateProfiles(profiles []Profile) []Diagnostic {
	var diagnostics []Diagnostic
	seenIDs := map[string]struct{}{}
	hasUniversal := false
	for _, profile := range profiles {
		if profile.ID == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ProfileIDRequired",
				Message:  "every capability profile requires an id",
			})
		}
		key := profile.ID + ":" + profile.Version + ":" + string(profile.Source)
		if _, exists := seenIDs[key]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicateProfile",
				Subject:  profile.ID,
				Message:  fmt.Sprintf("capability profile %q version %q from source %q is duplicated", profile.ID, profile.Version, profile.Source),
			})
		}
		seenIDs[key] = struct{}{}
		if profile.Version == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "ProfileVersionRequired",
				Subject:  profile.ID,
				Message:  fmt.Sprintf("capability profile %q requires a semantic version", profile.ID),
			})
		}
		if profile.Selector.ModelGlob != "" {
			if _, err := path.Match(profile.Selector.ModelGlob, "probe"); err != nil {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: DiagnosticError,
					Reason:   "InvalidModelGlob",
					Subject:  profile.ID,
					Message:  fmt.Sprintf("capability profile %q has invalid model glob %q", profile.ID, profile.Selector.ModelGlob),
				})
			}
		}
		diagnostics = append(diagnostics, validateProfileAliases(profile)...)
		diagnostics = append(diagnostics, validateQuirkDeclarations(profile.ID, profile.Quirks)...)
		if profile.Selector.Universal {
			hasUniversal = true
		}
	}
	if !hasUniversal {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "UnidentifiedProfileRequired",
			Message:  "at least one unidentified-device capability profile is required",
		})
	}
	return diagnostics
}

// validateProfileAliases enforces what the map shape cannot. A map key already
// guarantees a reported name resolves exactly one way, so the only remaining
// ambiguity is two reported names collapsing onto one canonical name.
func validateProfileAliases(profile Profile) []Diagnostic {
	var diagnostics []Diagnostic
	declared := make(map[string]struct{}, len(profile.TelemetryVariables))
	for _, variable := range profile.TelemetryVariables {
		declared[variable] = struct{}{}
	}

	sources := make([]string, 0, len(profile.TelemetryAliases))
	for source := range profile.TelemetryAliases {
		sources = append(sources, source)
	}
	sort.Strings(sources)

	claimed := map[string]string{}
	for _, source := range sources {
		target := profile.TelemetryAliases[source]
		switch {
		case source == "" || target == "":
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "InvalidTelemetryAlias",
				Subject:  profile.ID,
				Message:  fmt.Sprintf("capability profile %q has a telemetry alias with an empty name", profile.ID),
			})
			continue
		case source == target:
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "InvalidTelemetryAlias",
				Subject:  profile.ID,
				Message:  fmt.Sprintf("capability profile %q aliases %q to itself", profile.ID, source),
			})
			continue
		}

		if previous, exists := claimed[target]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "CollidingTelemetryAlias",
				Subject:  profile.ID,
				Message: fmt.Sprintf("capability profile %q aliases both %q and %q onto %q; a canonical name accepts one source",
					profile.ID, previous, source, target),
			})
		}
		claimed[target] = source

		if _, exists := declared[target]; !exists {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticWarning,
				Reason:   "AliasTargetNotDeclared",
				Subject:  profile.ID,
				Message: fmt.Sprintf("capability profile %q aliases %q onto %q but does not declare %q as a telemetry variable, so trigger support will not see it",
					profile.ID, source, target, target),
			})
		}
	}
	return diagnostics
}

// copyAliases deep-copies an alias map so profile normalization can never
// mutate a caller's catalog. A nil map stays nil, which keeps hashes and
// round-trips stable for profiles that declare no aliases.
func copyAliases(aliases map[string]string) map[string]string {
	if aliases == nil {
		return nil
	}
	copied := make(map[string]string, len(aliases))
	for source, target := range aliases {
		copied[source] = target
	}
	return copied
}

func normalizeProfiles(profiles []Profile) []Profile {
	normalized := append([]Profile(nil), profiles...)
	for i := range normalized {
		normalized[i].TelemetryVariables = append([]string(nil), normalized[i].TelemetryVariables...)
		normalized[i].TelemetryAliases = copyAliases(normalized[i].TelemetryAliases)
		normalized[i].ActuationBehaviors = append([]string(nil), normalized[i].ActuationBehaviors...)
		normalized[i].Quirks = copyQuirks(normalized[i].Quirks)
		sort.Strings(normalized[i].TelemetryVariables)
		sort.Strings(normalized[i].ActuationBehaviors)
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		left, right := normalized[i], normalized[j]
		switch {
		case left.ID != right.ID:
			return left.ID < right.ID
		case left.Version != right.Version:
			return left.Version < right.Version
		default:
			return left.Source < right.Source
		}
	})
	return normalized
}

func sourceRank(source ProfileSource) int {
	switch source {
	case ProfileSourceCRD:
		return 2
	case ProfileSourceBundled:
		return 1
	default:
		return 0
	}
}

type semanticVersion struct {
	major int
	minor int
	patch int
	raw   string
	valid bool
}

func parseSemanticVersion(raw string) semanticVersion {
	version := semanticVersion{raw: raw}
	trimmed := strings.TrimPrefix(raw, "v")
	core := strings.SplitN(trimmed, "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return version
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return version
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return version
	}

	version.major = major
	version.minor = minor
	version.patch = patch
	version.valid = true
	return version
}

func (left semanticVersion) compare(right semanticVersion) int {
	if left.valid && !right.valid {
		return 1
	}
	if !left.valid && right.valid {
		return -1
	}
	if !left.valid && !right.valid {
		return strings.Compare(left.raw, right.raw)
	}
	switch {
	case left.major != right.major:
		return left.major - right.major
	case left.minor != right.minor:
		return left.minor - right.minor
	default:
		return left.patch - right.patch
	}
}

func hasError(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == DiagnosticError {
			return true
		}
	}
	return false
}

func stableHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("capability input could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
