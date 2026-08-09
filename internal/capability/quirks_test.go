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
	"reflect"
	"testing"
)

// scopedProfile builds a minimal valid profile set. The universal entry is not
// decoration: the matcher requires an unidentified-device fallback to exist, so a
// set without one is rejected before quirk scoping is ever reached.
func scopedProfile(quirks ...Quirk) []Profile {
	return []Profile{
		{
			ID:       "scoped",
			Version:  "1.0.0",
			Source:   ProfileSourceBundled,
			Selector: ProfileSelector{Model: "X"},
			Quirks:   quirks,
		},
		{
			ID:       "fallback",
			Version:  "1.0.0",
			Source:   ProfileSourceBundled,
			Selector: ProfileSelector{Universal: true},
		},
	}
}

// F-26, the whole point: a defect fixed in firmware 1.4.18 must stop following a
// device that has moved past it. Before scoping, every historical quirk of a model
// family applied to every device of that family permanently.
func TestQuirksExpireOnFixedFirmware(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "protocol-bugs",
		Firmware: &QuirkFirmware{Below: "1.4.18"},
	})

	afflicted, _, err := Match(Device{ID: "old", Model: "X", Firmware: "1.4.17"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(afflicted.Quirks, []string{"protocol-bugs"}) {
		t.Fatalf("expected the quirk on 1.4.17, got %#v", afflicted.Quirks)
	}

	fixed, _, err := Match(Device{ID: "new", Model: "X", Firmware: "1.6.1"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if len(fixed.Quirks) != 0 {
		t.Fatalf("expected 1.6.1 to have outgrown the quirk, got %#v", fixed.Quirks)
	}
}

// The boundary is exclusive: "fixed in 1.4.18" means 1.4.18 itself is fixed.
func TestQuirkBelowBoundIsExclusive(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "protocol-bugs",
		Firmware: &QuirkFirmware{Below: "1.4.18"},
	})

	result, _, err := Match(Device{ID: "boundary", Model: "X", Firmware: "1.4.18"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if len(result.Quirks) != 0 {
		t.Fatalf("the fix release itself is not afflicted, got %#v", result.Quirks)
	}
}

// Component counts differ in the wild; 1.4 and 1.4.0 are the same firmware.
func TestQuirkVersionComparisonPadsMissingComponents(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "protocol-bugs",
		Firmware: &QuirkFirmware{Below: "1.4.18"},
	})

	result, _, err := Match(Device{ID: "short", Model: "X", Firmware: "1.4"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(result.Quirks, []string{"protocol-bugs"}) {
		t.Fatalf("expected 1.4 to sort below 1.4.18, got %#v", result.Quirks)
	}
}

// Glob scoping covers defects observed on specific builds with no known fix
// release, which is most of what the bundled catalog actually knows.
func TestQuirkGlobScopeMatchesOnlyTheAffectedBuilds(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "snmpv3-only",
		Firmware: &QuirkFirmware{Matches: []string{"1.6.*"}},
	})

	hit, _, err := Match(Device{ID: "hit", Model: "X", Firmware: "1.6.1"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(hit.Quirks, []string{"snmpv3-only"}) {
		t.Fatalf("expected the quirk on 1.6.1, got %#v", hit.Quirks)
	}

	miss, _, err := Match(Device{ID: "miss", Model: "X", Firmware: "1.7.0"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if len(miss.Quirks) != 0 {
		t.Fatalf("expected 1.7.0 to be out of scope, got %#v", miss.Quirks)
	}
}

// Most quirks are properties of a model rather than of a build, so an unscoped
// quirk must keep applying regardless of firmware.
func TestUnscopedQuirksAlwaysApply(t *testing.T) {
	profiles := scopedProfile(Quirk{Name: "model-defect"})

	result, _, err := Match(Device{ID: "any", Model: "X", Firmware: "9.9.9"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(result.Quirks, []string{"model-defect"}) {
		t.Fatalf("expected the unscoped quirk to apply, got %#v", result.Quirks)
	}
}

// A device that reports no firmware cannot be shown to be exempt. Keeping the
// quirk carries a stale defect; dropping it would hide a real one.
func TestQuirksApplyConservativelyWhenFirmwareIsUnknown(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "protocol-bugs",
		Firmware: &QuirkFirmware{Below: "1.4.18"},
	})

	result, diagnostics, err := Match(Device{ID: "silent", Model: "X"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(result.Quirks, []string{"protocol-bugs"}) {
		t.Fatalf("expected the quirk to be kept, got %#v", result.Quirks)
	}
	if !hasDiagnosticReason(diagnostics, "QuirkFirmwareUnknown") {
		t.Fatalf("expected the conservative choice to be reported, got %#v", diagnostics)
	}
}

// Vendor firmware strings are not generally orderable. Placing "EL-2.3b" against
// "1.4.18" would be a coin flip presented as a decision, so it is reported.
func TestQuirksReportFirmwareThatCannotBeCompared(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "protocol-bugs",
		Firmware: &QuirkFirmware{Below: "1.4.18"},
	})

	result, diagnostics, err := Match(Device{ID: "vendor", Model: "X", Firmware: "EL-2.3b"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !reflect.DeepEqual(result.Quirks, []string{"protocol-bugs"}) {
		t.Fatalf("expected the quirk to be kept, got %#v", result.Quirks)
	}
	if !hasDiagnosticReason(diagnostics, "QuirkFirmwareNotComparable") {
		t.Fatalf("expected the incomparable firmware to be reported, got %#v", diagnostics)
	}
}

// A scope the matcher cannot evaluate is worse than no scope: it looks like
// protection and silently is not. Caught at validation instead.
func TestInvalidQuirkScopesAreRejected(t *testing.T) {
	profiles := scopedProfile(Quirk{
		Name:     "bad-bound",
		Firmware: &QuirkFirmware{Below: "not-a-version"},
	})

	_, diagnostics, err := Match(Device{ID: "x", Model: "X", Firmware: "1.0.0"}, profiles)
	if err == nil {
		t.Fatalf("expected rejection, got diagnostics %#v", diagnostics)
	}
	if !hasDiagnosticReason(diagnostics, "InvalidQuirkFirmwareBound") {
		t.Fatalf("expected the bad bound to be named, got %#v", diagnostics)
	}
}

// The bundled catalog is the thing F-26 was found in, so it is asserted directly:
// a current-firmware device must not inherit the 1.6.1-era defects.
func TestBundledUbiquitiQuirksExpireOnNewerFirmware(t *testing.T) {
	profiles := BundledProfiles()

	afflicted, _, err := Match(Device{ID: "old", Model: "TOWER_1500VA_120V", Firmware: "1.6.1"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if !containsString(afflicted.Quirks, "snmpv3-only-no-community-string") {
		t.Fatalf("expected the 1.6.1 quirk on 1.6.1, got %#v", afflicted.Quirks)
	}

	current, _, err := Match(Device{ID: "new", Model: "TOWER_1500VA_120V", Firmware: "2.0.0"}, profiles)
	if err != nil {
		t.Fatalf("expected a match, got %v", err)
	}
	if containsString(current.Quirks, "snmpv3-only-no-community-string") {
		t.Fatalf("firmware 2.0.0 should have outgrown the 1.6.1 quirk, got %#v", current.Quirks)
	}
	if containsString(current.Quirks, "nut-protocol-response-bugs") {
		t.Fatalf("firmware 2.0.0 is past the 1.4.18 fix, got %#v", current.Quirks)
	}
	if !containsString(current.Quirks, "instant-commands-not-confirmed") {
		t.Fatalf("unscoped model quirks must survive, got %#v", current.Quirks)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
