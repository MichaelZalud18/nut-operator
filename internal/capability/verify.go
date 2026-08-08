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
	"sort"
	"strings"
)

// trimVariableName normalizes a NUT variable name the same way BuildDraft does,
// so a name with incidental whitespace is not reported as both missing and
// unexpected.
func trimVariableName(name string) string {
	return strings.TrimSpace(name)
}

// Verification is the evidence produced by checking a matched profile against
// what a device actually reported (OD-15). It answers one question: does the
// profile this device resolves to still describe the device in front of us?
//
// This is pure: it takes a match and a device read and returns a comparison. It
// performs no I/O and makes no decisions about what to do with the result.
type Verification struct {
	// Verified means the matched profile is a real product profile and every
	// telemetry variable it declares was actually readable from the device.
	Verified bool

	// DriftDetected means the profile declares telemetry the device did not
	// provide. That is the actionable signal: the profile makes a claim about
	// this device that is no longer true, usually after a firmware change.
	DriftDetected bool

	// ExpectedVariables is what the matched profile declares it relies on.
	ExpectedVariables []string

	// MissingVariables are declared variables the device did not report, under
	// either their canonical name or a profile-declared alias.
	MissingVariables []string

	// UnexpectedVariables are non-standard names the device reported that the
	// profile neither declares nor aliases — candidates for a new alias or
	// quirk. These are recorded as evidence but deliberately do not set
	// DriftDetected: nearly every real UPS reports vendor extensions, so
	// treating them as drift would make the signal fire constantly and mean
	// nothing. A profile is wrong when it promises something absent, not when
	// the device offers something extra.
	UnexpectedVariables []string

	// Diagnostics explain the outcome in the same shape the rest of the system
	// uses, so a verification reads like any other diagnostic-bearing result.
	Diagnostics []VerificationDiagnostic
}

// VerificationDiagnostic is a single human-readable finding about a comparison.
type VerificationDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

const (
	VerificationSeverityInfo    = "Info"
	VerificationSeverityWarning = "Warning"
)

// Verify compares a device read against the profile the device matched.
//
// An unidentified device is reported as unverified but never as drifted: the
// unidentified profile makes no product claims, so there is nothing it could be
// wrong about. Recording that as drift would put every unprofiled device into
// the drift stream and bury the real signal.
func Verify(match MatchResult, observed map[string]string) Verification {
	verification := Verification{
		ExpectedVariables: append([]string(nil), match.TelemetryVariables...),
	}
	sort.Strings(verification.ExpectedVariables)

	reported := make(map[string]struct{}, len(observed))
	for name := range observed {
		if trimmed := trimVariableName(name); trimmed != "" {
			reported[trimmed] = struct{}{}
		}
	}

	// An alias means "this device delivers the canonical reading under another
	// name", so a declared variable satisfied through its alias is present, not
	// missing. Resolving aliases here is what keeps a correctly-aliased quirky
	// device from being reported as permanently drifted.
	aliasTargets := make(map[string]struct{}, len(match.TelemetryAliases))
	for reportedName, canonical := range match.TelemetryAliases {
		if _, present := reported[trimVariableName(reportedName)]; !present {
			continue
		}
		if canonical := trimVariableName(canonical); canonical != "" {
			aliasTargets[canonical] = struct{}{}
		}
	}

	for _, expected := range verification.ExpectedVariables {
		if _, present := reported[expected]; present {
			continue
		}
		if _, aliased := aliasTargets[expected]; aliased {
			continue
		}
		verification.MissingVariables = append(verification.MissingVariables, expected)
	}

	declared := make(map[string]struct{}, len(verification.ExpectedVariables))
	for _, expected := range verification.ExpectedVariables {
		declared[expected] = struct{}{}
	}
	for name := range reported {
		if _, standard := standardNUTVariables[name]; standard {
			continue
		}
		if _, isDeclared := declared[name]; isDeclared {
			continue
		}
		if _, isAliasSource := match.TelemetryAliases[name]; isAliasSource {
			continue
		}
		verification.UnexpectedVariables = append(verification.UnexpectedVariables, name)
	}
	sort.Strings(verification.UnexpectedVariables)

	verification.DriftDetected = !match.Unidentified && len(verification.MissingVariables) > 0
	verification.Verified = !match.Unidentified &&
		len(reported) > 0 &&
		len(verification.MissingVariables) == 0

	verification.Diagnostics = verificationDiagnostics(match, verification, len(reported))
	return verification
}

func verificationDiagnostics(match MatchResult, verification Verification, reportedCount int) []VerificationDiagnostic {
	var diagnostics []VerificationDiagnostic

	switch {
	case reportedCount == 0:
		diagnostics = append(diagnostics, VerificationDiagnostic{
			Severity: VerificationSeverityWarning,
			Code:     "NoVariablesReported",
			Message:  "device returned no NUT variables, so nothing about the profile could be confirmed",
		})
	case match.Unidentified:
		diagnostics = append(diagnostics, VerificationDiagnostic{
			Severity: VerificationSeverityInfo,
			Code:     "UnidentifiedDevice",
			Message:  "device matched no product profile, so there are no declared variables to verify against",
		})
	}

	if len(verification.MissingVariables) > 0 {
		diagnostics = append(diagnostics, VerificationDiagnostic{
			Severity: VerificationSeverityWarning,
			Code:     "DeclaredVariableMissing",
			Message: "profile " + match.ProfileID + " declares telemetry the device did not report: " +
				joinVariables(verification.MissingVariables),
		})
	}
	if len(verification.UnexpectedVariables) > 0 {
		diagnostics = append(diagnostics, VerificationDiagnostic{
			Severity: VerificationSeverityInfo,
			Code:     "UndeclaredNonStandardVariable",
			Message: "device reported non-standard telemetry the profile does not declare or alias: " +
				joinVariables(verification.UnexpectedVariables),
		})
	}
	if verification.Verified {
		diagnostics = append(diagnostics, VerificationDiagnostic{
			Severity: VerificationSeverityInfo,
			Code:     "ProfileVerified",
			Message:  "every telemetry variable profile " + match.ProfileID + " declares was readable from the device",
		})
	}
	return diagnostics
}

func joinVariables(names []string) string {
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}
