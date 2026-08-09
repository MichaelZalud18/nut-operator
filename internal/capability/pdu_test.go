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
	"errors"
	"reflect"
	"testing"
)

func pduProfile(id string, selector ProfileSelector) PDUProfile {
	return PDUProfile{ID: id, Version: "1.0.0", Source: ProfileSourceCRD, Selector: selector}
}

// OD-25 requires the PDU kind to reuse the UPS precedence chain rather than carry
// its own. An exact model must beat a glob, which is the chain's first observable
// consequence and would break immediately if the two matchers diverged.
func TestMatchPDUPrefersTheMoreSpecificSelector(t *testing.T) {
	profiles := []PDUProfile{
		pduProfile("glob", ProfileSelector{ModelGlob: "USP-PDU*"}),
		pduProfile("exact", ProfileSelector{Model: "USP-PDU-Pro"}),
		pduProfile("family", ProfileSelector{DriverFamily: "snmp-ups"}),
	}

	result, _, err := MatchPDU(Device{ID: "pdu-a", Model: "USP-PDU-Pro", DriverFamily: "snmp-ups"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if result.ProfileID != "exact" {
		t.Fatalf("expected the exact-model profile to win, got %q at tier %q", result.ProfileID, result.Tier)
	}
	if result.Tier != MatchTierExactModel {
		t.Fatalf("expected the ExactModel tier, got %q", result.Tier)
	}
}

// The universal profile is the unidentified-device fallback (PL-33), and matching
// it must be visible rather than looking like a successful identification.
func TestMatchPDUFallsBackToTheUniversalProfile(t *testing.T) {
	profiles := []PDUProfile{pduProfile("fallback", ProfileSelector{Universal: true})}

	result, _, err := MatchPDU(Device{ID: "pdu-b", Model: "something-unknown"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !result.Unidentified {
		t.Fatalf("expected the universal match to be reported as unidentified, got %#v", result)
	}
}

// An unmatched device is a warning, not a rejection: the operator still knows the
// PDU exists topologically, it just knows nothing about its capabilities.
func TestMatchPDUReportsAnUnmatchedDevice(t *testing.T) {
	result, diagnostics, err := MatchPDU(Device{ID: "pdu-c", Model: "unknown"}, nil)
	if err != nil {
		t.Fatalf("an unmatched device is not a rejection, got %v", err)
	}
	if result.ProfileID != "" {
		t.Fatalf("expected no profile, got %q", result.ProfileID)
	}
	if !hasDiagnosticReason(diagnostics, "PDUProfileUnmatched") {
		t.Fatalf("expected an unmatched diagnostic, got %#v", diagnostics)
	}
}

// Two universal profiles make the fallback ambiguous, and an ambiguous fallback is
// worse than none: which one wins would depend on ordering.
func TestMatchPDURejectsTwoUniversalProfiles(t *testing.T) {
	profiles := []PDUProfile{
		pduProfile("first", ProfileSelector{Universal: true}),
		pduProfile("second", ProfileSelector{Universal: true}),
	}

	_, diagnostics, err := MatchPDU(Device{ID: "pdu-d"}, profiles)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected rejection, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "MultipleUniversalPDUProfiles") {
		t.Fatalf("expected the ambiguity to be named, got %#v", diagnostics)
	}
}

// More switchable outlets than outlets describes hardware the author has not
// counted. Rejected rather than trimmed: silently narrowing a declaration is how a
// device ends up believed less capable than it is with nothing saying so.
func TestMatchPDURejectsMoreSwitchableOutletsThanOutlets(t *testing.T) {
	profile := pduProfile("miscounted", ProfileSelector{Model: "PDU-8"})
	profile.Outlets = PDUOutlets{Count: 2, Switchable: []string{"outlet.1", "outlet.2", "outlet.3"}}

	_, diagnostics, err := MatchPDU(Device{ID: "pdu-e", Model: "PDU-8"}, []PDUProfile{profile})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected rejection, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "PDUOutletCountMismatch") {
		t.Fatalf("expected the mismatch to be named, got %#v", diagnostics)
	}
}

// A metered-only PDU reports per-outlet readings and switches nothing. That is a
// real device class, so a zero-length switchable list must not be an error.
func TestMatchPDUAcceptsAMeteredOnlyDevice(t *testing.T) {
	profile := pduProfile("metered", ProfileSelector{Model: "PDU-M"})
	profile.Outlets = PDUOutlets{Count: 8}

	result, _, err := MatchPDU(Device{ID: "pdu-f", Model: "PDU-M"}, []PDUProfile{profile})
	if err != nil {
		t.Fatalf("a metered-only PDU is valid, got %v", err)
	}
	if result.Outlets.Count != 8 || len(result.Outlets.Switchable) != 0 {
		t.Fatalf("expected 8 outlets and none switchable, got %#v", result.Outlets)
	}
}

// The match feeds plan identity, so the same profile set must produce the same
// hash regardless of the order it arrived in.
func TestMatchPDUIsDeterministic(t *testing.T) {
	build := func(order int) []PDUProfile {
		first := pduProfile("alpha", ProfileSelector{ModelGlob: "PDU-*"})
		first.TelemetryVariables = []string{"outlet.count", "device.mfr"}
		second := pduProfile("zulu", ProfileSelector{DriverFamily: "snmp-ups"})
		if order == 0 {
			return []PDUProfile{first, second}
		}
		return []PDUProfile{second, first}
	}

	left, _, err := MatchPDU(Device{ID: "pdu-g", Model: "PDU-9", DriverFamily: "snmp-ups"}, build(0))
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	right, _, err := MatchPDU(Device{ID: "pdu-g", Model: "PDU-9", DriverFamily: "snmp-ups"}, build(1))
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("match differs by input order:\n%#v\n%#v", left, right)
	}
	if left.ProfileHash == "" {
		t.Fatalf("expected a profile hash, got %#v", left)
	}
}

// A newer version of the same profile wins, which is the SB-9 semver rule the UPS
// matcher owns. Shared machinery, so a regression here is a regression there.
func TestMatchPDUPrefersTheNewerProfileVersion(t *testing.T) {
	older := pduProfile("shared", ProfileSelector{Model: "PDU-V"})
	newer := pduProfile("shared", ProfileSelector{Model: "PDU-V"})
	newer.Version = "2.1.0"

	result, _, err := MatchPDU(Device{ID: "pdu-h", Model: "PDU-V"}, []PDUProfile{older, newer})
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if result.ProfileVersion != "2.1.0" {
		t.Fatalf("expected the newer version to win, got %q", result.ProfileVersion)
	}
}
