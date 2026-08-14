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
)

// PDUProfile describes declared capabilities of a NUT-managed PDU.
//
// A parallel kind rather than fields bolted onto Profile (OD-25). Reusing the UPS
// kind would keep the CRD count down and make its name progressively dishonest:
// every UPS-specific field would have to become optional-and-ignored for PDUs, and
// a schema that means different things depending on what matched it stops being a
// contract. Two kinds, each true about its own device class.
//
// What is deliberately absent is actuation. A PDU's whole point is switchable
// outlets, and declaring which outlets switch is not the same as switching them:
// that needs OD-20 to settle which NUT instant commands enter scope at all, and it
// is tracked in docs/tasks-post-v1.md. Outlets here describe shape, never commands.
type PDUProfile struct {
	ID       string          `json:"id"`
	Version  string          `json:"version"`
	Source   ProfileSource   `json:"source,omitempty"`
	Selector ProfileSelector `json:"selector"`
	// Outlets describes the PDU's outlet layout. Declared for planning and review;
	// nothing acts on it in v1.
	Outlets PDUOutlets `json:"outlets,omitempty"`
	// TelemetryVariables are the NUT variables a matching PDU reports.
	TelemetryVariables []string `json:"telemetryVariables,omitempty"`
	// TelemetryAliases map a reported name onto the canonical NUT name, with the
	// same one-directional, native-wins semantics as UPS profiles (OD-23).
	TelemetryAliases map[string]string `json:"telemetryAliases,omitempty"`
	Quirks           []Quirk           `json:"quirks,omitempty"`
}

// PDUOutlets describes an outlet layout without implying control over it.
type PDUOutlets struct {
	// Count is the number of outlets the device exposes. Zero means undeclared,
	// which is different from a device with no outlets — nothing infers one from
	// the other.
	Count int `json:"count,omitempty"`
	// Switchable names the outlets that can be switched independently, using NUT's
	// outlet identifiers. Empty with a non-zero Count means a metered-only PDU:
	// it reports per-outlet data and switches nothing.
	Switchable []string `json:"switchable,omitempty"`
}

// PDUMatchResult is the selected PDU profile plus attribution.
type PDUMatchResult struct {
	DeviceID           string            `json:"deviceID,omitempty"`
	ProfileID          string            `json:"profileID"`
	ProfileVersion     string            `json:"profileVersion"`
	ProfileSource      ProfileSource     `json:"profileSource,omitempty"`
	ProfileHash        string            `json:"profileHash"`
	Tier               MatchTier         `json:"tier"`
	Unidentified       bool              `json:"unidentified,omitempty"`
	Outlets            PDUOutlets        `json:"outlets,omitempty"`
	TelemetryVariables []string          `json:"telemetryVariables,omitempty"`
	TelemetryAliases   map[string]string `json:"telemetryAliases,omitempty"`
	Quirks             []string          `json:"quirks,omitempty"`
}

// MatchPDU selects the best PDU profile for a device.
//
// The precedence chain, semver comparison, and source ranking are the ones the UPS
// matcher uses, called rather than copied — OD-25 names exactly that machinery as
// what must be factored, and two implementations of "which profile wins" would
// drift the first time one of them was tuned.
func MatchPDU(device Device, profiles []PDUProfile) (PDUMatchResult, []Diagnostic, error) {
	normalized := normalizePDUProfiles(profiles)
	diagnostics := validatePDUProfiles(normalized)
	if hasError(diagnostics) {
		return PDUMatchResult{}, diagnostics, ErrRejected
	}

	var best *pduCandidate
	for i := range normalized {
		profile := normalized[i]
		tier, rank, matched := matchTier(device, profile.Selector)
		if !matched {
			continue
		}
		current := pduCandidate{
			profile:  profile,
			tier:     tier,
			tierRank: rank,
			shared: candidate{
				profile: Profile{
					ID:      profile.ID,
					Version: profile.Version,
					Source:  profile.Source,
				},
				tier:       tier,
				tierRank:   rank,
				sourceRank: sourceRank(profile.Source),
				version:    parseSemanticVersion(profile.Version),
			},
		}
		if best == nil || current.shared.betterThan(best.shared) {
			winner := current
			best = &winner
		}
	}

	if best == nil {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticWarning,
			Reason:   "PDUProfileUnmatched",
			Subject:  device.ID,
			Message: fmt.Sprintf(
				"no PDU capability profile matches device %q; declare one or add a universal profile",
				device.ID),
		})
		return PDUMatchResult{DeviceID: device.ID}, diagnostics, nil
	}

	resolvedQuirks, quirkDiagnostics := resolveQuirks(device, best.profile.ID, best.profile.Quirks)
	diagnostics = append(diagnostics, quirkDiagnostics...)

	return PDUMatchResult{
		DeviceID:           device.ID,
		ProfileID:          best.profile.ID,
		ProfileVersion:     best.profile.Version,
		ProfileSource:      best.profile.Source,
		ProfileHash:        stableHash(best.profile),
		Tier:               best.tier,
		Unidentified:       best.tier == MatchTierUnidentified,
		Outlets:            best.profile.Outlets,
		TelemetryVariables: append([]string(nil), best.profile.TelemetryVariables...),
		TelemetryAliases:   copyAliases(best.profile.TelemetryAliases),
		Quirks:             resolvedQuirks,
	}, diagnostics, nil
}

type pduCandidate struct {
	profile  PDUProfile
	tier     MatchTier
	tierRank int
	shared   candidate
}

// ValidatePDUProfileSet reports conflicts that only exist across a set of PDU profiles.
//
// Each profile validates on its own in the admission webhook and the reconciler, and those checks
// cannot see this class of problem: two profiles that are individually perfect can still declare the
// same id and version, or both claim selector.universal, and the set then has no deterministic
// answer for any device. That was unreachable until now -- the checks live inside MatchPDU, which
// has no caller while there is no PDU device kind (OD-25), so a cluster could hold two universal PDU
// profiles with both reporting Accepted and nothing anywhere saying the set was unusable.
//
// Exported separately from MatchPDU precisely so the conflict is reported when the profiles are
// written, rather than at the first resolution attempt, which may be a release away.
func ValidatePDUProfileSet(profiles []PDUProfile) []Diagnostic {
	return validatePDUProfiles(normalizePDUProfiles(profiles))
}

// validatePDUProfiles rejects profile sets that cannot resolve deterministically.
func validatePDUProfiles(profiles []PDUProfile) []Diagnostic {
	var diagnostics []Diagnostic
	seen := map[string]struct{}{}
	universals := 0
	for _, profile := range profiles {
		if profile.ID == "" {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "PDUProfileIDRequired",
				Message:  "every PDU capability profile requires an id",
			})
			continue
		}
		key := profile.ID + ":" + profile.Version
		if _, duplicate := seen[key]; duplicate {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticError,
				Reason:   "DuplicatePDUProfile",
				Subject:  profile.ID,
				Message:  fmt.Sprintf("PDU capability profile %q version %q is declared more than once", profile.ID, profile.Version),
			})
		}
		seen[key] = struct{}{}
		if profile.Selector.Universal {
			universals++
		}
		diagnostics = append(diagnostics, validatePDUOutlets(profile)...)
		diagnostics = append(diagnostics, validateQuirkDeclarations(profile.ID, profile.Quirks)...)
	}
	if universals > 1 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: DiagnosticError,
			Reason:   "MultipleUniversalPDUProfiles",
			Message:  "only one PDU capability profile may declare selector.universal",
		})
	}
	return diagnostics
}

// validatePDUOutlets catches outlet declarations that contradict themselves.
//
// Switchable outlets that exceed the declared count mean the profile author is
// describing hardware they have not counted, and a plan built on that would size
// the device wrong. Reported as an error rather than trimmed: silently narrowing a
// capability declaration is how a device ends up believed to be less capable than
// it is, with nothing saying so.
func validatePDUOutlets(profile PDUProfile) []Diagnostic {
	if profile.Outlets.Count == 0 || len(profile.Outlets.Switchable) == 0 {
		return nil
	}
	if len(profile.Outlets.Switchable) > profile.Outlets.Count {
		return []Diagnostic{{
			Severity: DiagnosticError,
			Reason:   "PDUOutletCountMismatch",
			Subject:  profile.ID,
			Message: fmt.Sprintf(
				"PDU capability profile %q declares %d switchable outlets but only %d outlets",
				profile.ID, len(profile.Outlets.Switchable), profile.Outlets.Count),
		}}
	}
	seen := map[string]struct{}{}
	for _, outlet := range profile.Outlets.Switchable {
		if _, duplicate := seen[outlet]; duplicate {
			return []Diagnostic{{
				Severity: DiagnosticError,
				Reason:   "DuplicatePDUOutlet",
				Subject:  profile.ID,
				Message: fmt.Sprintf(
					"PDU capability profile %q lists outlet %q as switchable more than once",
					profile.ID, outlet),
			}}
		}
		seen[outlet] = struct{}{}
	}
	return nil
}

// normalizePDUProfiles orders profiles and their contents so the same set always
// produces the same match and the same hash.
func normalizePDUProfiles(profiles []PDUProfile) []PDUProfile {
	normalized := make([]PDUProfile, 0, len(profiles))
	for _, profile := range profiles {
		copied := profile
		copied.TelemetryVariables = append([]string(nil), profile.TelemetryVariables...)
		sort.Strings(copied.TelemetryVariables)
		copied.Quirks = copyQuirks(profile.Quirks)
		copied.TelemetryAliases = copyAliases(profile.TelemetryAliases)
		copied.Outlets.Switchable = append([]string(nil), profile.Outlets.Switchable...)
		sort.Strings(copied.Outlets.Switchable)
		normalized = append(normalized, copied)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		if normalized[left].ID != normalized[right].ID {
			return normalized[left].ID < normalized[right].ID
		}
		return normalized[left].Version < normalized[right].Version
	})
	return normalized
}
