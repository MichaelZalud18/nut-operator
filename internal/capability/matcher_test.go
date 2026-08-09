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

func TestMatchUsesCapabilityPrecedenceChain(t *testing.T) {
	device := Device{
		ID:           "ups-a",
		Model:        "APC Smart-UPS 1500",
		Firmware:     "1.2.3",
		DriverFamily: "snmp-ups",
	}
	profiles := []Profile{
		profile("universal", "99.0.0", ProfileSourceCRD, ProfileSelector{Universal: true}),
		profile("driver-family", "99.0.0", ProfileSourceCRD, ProfileSelector{DriverFamily: "snmp-ups"}),
		profile("model-glob", "99.0.0", ProfileSourceCRD, ProfileSelector{ModelGlob: "APC Smart-UPS *"}),
		profile("model", "99.0.0", ProfileSourceCRD, ProfileSelector{Model: "APC Smart-UPS 1500"}),
		profile("model-firmware", "1.0.0", ProfileSourceBundled, ProfileSelector{Model: "APC Smart-UPS 1500", Firmware: "1.2.3"}),
	}

	result, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if result.ProfileID != "model-firmware" {
		t.Fatalf("expected exact model+firmware profile to win, got %#v", result)
	}
	if result.Tier != MatchTierExactModelFirmware {
		t.Fatalf("expected tier %q, got %q", MatchTierExactModelFirmware, result.Tier)
	}
}

func TestMatchPrefersCRDSourceWithinTierBeforeVersion(t *testing.T) {
	device := Device{ID: "ups-a", Model: "Vendor Model", Firmware: "1.0.0"}
	profiles := []Profile{
		profile("universal", "1.0.0", ProfileSourceBundled, ProfileSelector{Universal: true}),
		profile("bundled-model", "9.9.9", ProfileSourceBundled, ProfileSelector{Model: "Vendor Model"}),
		profile("crd-model", "1.0.0", ProfileSourceCRD, ProfileSelector{Model: "Vendor Model"}),
	}

	result, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if result.ProfileID != "crd-model" {
		t.Fatalf("expected CRD profile to win within tier before version, got %#v", result)
	}
}

func TestMatchPrefersHighestSemverWithinSource(t *testing.T) {
	device := Device{ID: "ups-a", Model: "Vendor Model"}
	profiles := []Profile{
		profile("universal", "1.0.0", ProfileSourceBundled, ProfileSelector{Universal: true}),
		profile("model-low", "1.2.3", ProfileSourceCRD, ProfileSelector{Model: "Vendor Model"}),
		profile("model-high", "1.3.0", ProfileSourceCRD, ProfileSelector{Model: "Vendor Model"}),
	}

	result, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if result.ProfileID != "model-high" {
		t.Fatalf("expected highest semver profile to win, got %#v", result)
	}
}

func TestMatchFallsBackToUniversalFloorWithWarning(t *testing.T) {
	device := Device{ID: "ups-a", Model: "Unknown Model", DriverFamily: "unknown-driver"}
	profiles := []Profile{
		profile("universal", "1.0.0", ProfileSourceBundled, ProfileSelector{Universal: true}),
	}

	result, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected unidentified-device match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if !result.Unidentified {
		t.Fatalf("expected fallback result, got %#v", result)
	}
	if result.Tier != MatchTierUnidentified {
		t.Fatalf("expected unidentified tier, got %q", result.Tier)
	}
	if !hasDiagnosticReason(diagnostics, "DeviceUnidentified") {
		t.Fatalf("expected DeviceUnidentified warning, got %#v", diagnostics)
	}
}

func TestMatchMissingModelCanUseDriverFamilyProfile(t *testing.T) {
	device := Device{ID: "ups-a", DriverFamily: "snmp-ups"}
	profiles := []Profile{
		profile("driver-family", "1.0.0", ProfileSourceCRD, ProfileSelector{DriverFamily: "snmp-ups"}),
		profile("universal", "1.0.0", ProfileSourceBundled, ProfileSelector{Universal: true}),
	}

	result, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if result.ProfileID != "driver-family" {
		t.Fatalf("expected missing model to use driver-family profile, got %#v", result)
	}
	if result.Tier != MatchTierDriverFamily {
		t.Fatalf("expected driver-family tier, got %q", result.Tier)
	}
	if result.Unidentified {
		t.Fatalf("expected driver-family match not to be marked as fallback, got %#v", result)
	}
}

func TestMatchRejectsInvalidProfiles(t *testing.T) {
	device := Device{ID: "ups-a", Model: "Vendor Model"}
	profiles := []Profile{
		profile("bad-glob", "1.0.0", ProfileSourceCRD, ProfileSelector{ModelGlob: "["}),
	}

	_, diagnostics, err := Match(device, profiles)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("expected ErrRejected, got %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "UnidentifiedProfileRequired") {
		t.Fatalf("expected UnidentifiedProfileRequired diagnostic, got %#v", diagnostics)
	}
	if !hasDiagnosticReason(diagnostics, "InvalidModelGlob") {
		t.Fatalf("expected InvalidModelGlob diagnostic, got %#v", diagnostics)
	}
}

func TestSupportsTriggerDerivesFromTelemetryVariables(t *testing.T) {
	profile := Profile{
		ID:      "runtime-capable",
		Version: "1.0.0",
		TelemetryVariables: []string{
			"battery.runtime",
			"ups.status",
		},
	}

	cases := map[TriggerType]bool{
		TriggerOnBattery:      true,
		TriggerLowBattery:     true,
		TriggerRuntimeBelow:   true,
		TriggerChargeBelow:    false,
		TriggerTelemetryStale: true,
		TriggerType("Custom"): false,
	}
	for trigger, want := range cases {
		if got := SupportsTrigger(profile, trigger); got != want {
			t.Fatalf("SupportsTrigger(%q) = %t, want %t", trigger, got, want)
		}
	}
}

func TestMatchIsDeterministicAndDoesNotMutateProfiles(t *testing.T) {
	device := Device{ID: "ups-a", Model: "Vendor Model"}
	profiles := []Profile{
		{
			ID:      "model",
			Version: "1.0.0",
			Source:  ProfileSourceCRD,
			Selector: ProfileSelector{
				Model: "Vendor Model",
			},
			TelemetryVariables: []string{"ups.status", "battery.runtime"},
			ActuationBehaviors: []string{"outlet.off", "shutdown.return"},
		},
		profile("universal", "1.0.0", ProfileSourceBundled, ProfileSelector{Universal: true}),
	}
	originalTelemetry := append([]string(nil), profiles[0].TelemetryVariables...)
	originalActuation := append([]string(nil), profiles[0].ActuationBehaviors...)

	first, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	second, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected second match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected deterministic matches\nfirst: %#v\nsecond: %#v", first, second)
	}
	if !reflect.DeepEqual(profiles[0].TelemetryVariables, originalTelemetry) {
		t.Fatalf("matcher mutated telemetry variables: got %#v, want %#v", profiles[0].TelemetryVariables, originalTelemetry)
	}
	if !reflect.DeepEqual(profiles[0].ActuationBehaviors, originalActuation) {
		t.Fatalf("matcher mutated actuation behaviors: got %#v, want %#v", profiles[0].ActuationBehaviors, originalActuation)
	}
}

func profile(id, version string, source ProfileSource, selector ProfileSelector) Profile {
	return Profile{
		ID:       id,
		Version:  version,
		Source:   source,
		Selector: selector,
	}
}

func hasDiagnosticReason(diagnostics []Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			return true
		}
	}
	return false
}

func TestMatchResultCarriesProfileContent(t *testing.T) {
	profiles := []Profile{
		{
			ID:                 "floor",
			Version:            "1.0.0",
			Source:             ProfileSourceBundled,
			Selector:           ProfileSelector{Universal: true},
			TelemetryVariables: []string{"ups.status"},
		},
		{
			ID:                 "product",
			Version:            "1.0.0",
			Source:             ProfileSourceBundled,
			Selector:           ProfileSelector{ModelGlob: "2U_*VA_*V"},
			TelemetryVariables: []string{"battery.charge.low", "battery.runtime", "ups.status"},
			TelemetryAliases:   map[string]string{"battery.low": "battery.charge.low"},
			ActuationBehaviors: []string{"shutdown.return"},
			Quirks:             []Quirk{{Name: "example-quirk"}},
		},
	}

	result, _, err := Match(Device{ID: "ups-1", Model: "2U_1500VA_230V"}, profiles)
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if result.ProfileID != "product" {
		t.Fatalf("matched profile = %q, want product", result.ProfileID)
	}
	if len(result.TelemetryVariables) != 3 {
		t.Fatalf("telemetry variables = %#v, want the profile's three", result.TelemetryVariables)
	}
	if len(result.TelemetryAliases) != 1 || result.TelemetryAliases["battery.low"] != "battery.charge.low" {
		t.Fatalf("telemetry aliases = %#v, want the profile's alias", result.TelemetryAliases)
	}
	if len(result.ActuationBehaviors) != 1 || len(result.Quirks) != 1 {
		t.Fatalf("actuation/quirks = %#v/%#v, want the profile's content", result.ActuationBehaviors, result.Quirks)
	}
	if !result.SupportsTriggerType(TriggerRuntimeBelow) {
		t.Fatal("matched profile declares battery.runtime, so RuntimeBelow must be supported")
	}
	if result.SupportsTriggerType(TriggerChargeBelow) {
		t.Fatal("matched profile omits battery.charge, so ChargeBelow must be unsupported")
	}
}

func TestMatchWarnsWhenMissingModelCostsAProductMatch(t *testing.T) {
	profiles := []Profile{{
		ID:                 "floor",
		Version:            "1.0.0",
		Source:             ProfileSourceBundled,
		Selector:           ProfileSelector{Universal: true},
		TelemetryVariables: []string{"ups.status"},
	}}

	result, diagnostics, err := Match(Device{ID: "ups-1", DriverFamily: "snmp-ups"}, profiles)
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if !result.Unidentified {
		t.Fatal("with only a floor profile available, a model-less device must fall back")
	}
	if !hasDiagnosticReason(diagnostics, "ProviderModelMissing") {
		t.Fatalf("expected a ProviderModelMissing warning, got %#v", diagnostics)
	}
}

func TestMatchStaysQuietWhenDriverFamilyProfileMatches(t *testing.T) {
	profiles := []Profile{
		{
			ID:                 "floor",
			Version:            "1.0.0",
			Source:             ProfileSourceBundled,
			Selector:           ProfileSelector{Universal: true},
			TelemetryVariables: []string{"ups.status"},
		},
		{
			ID:                 "snmp-family",
			Version:            "1.0.0",
			Source:             ProfileSourceCRD,
			Selector:           ProfileSelector{DriverFamily: "snmp-ups"},
			TelemetryVariables: []string{"battery.runtime", "ups.status"},
		},
	}

	result, diagnostics, err := Match(Device{ID: "ups-1", DriverFamily: "snmp-ups"}, profiles)
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if result.Unidentified || result.ProfileID != "snmp-family" {
		t.Fatalf("expected the driver-family profile to win, got %q fallback=%v", result.ProfileID, result.Unidentified)
	}
	if hasDiagnosticReason(diagnostics, "ProviderModelMissing") {
		t.Fatalf("a device that matched a real profile lost nothing to its missing model, got %#v", diagnostics)
	}
}

func TestValidateProfileAliasesRejectsAmbiguity(t *testing.T) {
	floor := Profile{
		ID:                 "floor",
		Version:            "1.0.0",
		Selector:           ProfileSelector{Universal: true},
		TelemetryVariables: []string{"ups.status"},
	}

	// A duplicate source is not tested because the map shape makes it
	// unrepresentable: a key can only appear once.
	for name, testCase := range map[string]struct {
		aliases map[string]string
		reason  string
	}{
		"self alias": {
			aliases: map[string]string{"battery.low": "battery.low"},
			reason:  "InvalidTelemetryAlias",
		},
		"missing target": {
			aliases: map[string]string{"battery.low": ""},
			reason:  "InvalidTelemetryAlias",
		},
		"colliding target": {
			aliases: map[string]string{
				"battery.low": "battery.charge.low",
				"batt.low":    "battery.charge.low",
			},
			reason: "CollidingTelemetryAlias",
		},
	} {
		t.Run(name, func(t *testing.T) {
			profile := floor
			profile.TelemetryAliases = testCase.aliases
			_, diagnostics, err := Match(Device{ID: "ups-1"}, []Profile{profile})
			if err == nil {
				t.Fatal("expected an ambiguous alias set to be rejected")
			}
			if !hasDiagnosticReason(diagnostics, testCase.reason) {
				t.Fatalf("expected reason %q, got %#v", testCase.reason, diagnostics)
			}
		})
	}
}

func TestValidateProfileAliasesWarnsOnUndeclaredTarget(t *testing.T) {
	profile := Profile{
		ID:                 "floor",
		Version:            "1.0.0",
		Selector:           ProfileSelector{Universal: true},
		TelemetryVariables: []string{"ups.status"},
		TelemetryAliases:   map[string]string{"batt.low": "battery.charge.low"},
	}

	_, diagnostics, err := Match(Device{ID: "ups-1"}, []Profile{profile})
	if err != nil {
		t.Fatalf("an undeclared alias target must warn, not reject: %v", err)
	}
	if !hasDiagnosticReason(diagnostics, "AliasTargetNotDeclared") {
		t.Fatalf("expected an AliasTargetNotDeclared warning, got %#v", diagnostics)
	}
}
