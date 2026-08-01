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
		t.Fatalf("expected universal floor match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if !result.Fallback {
		t.Fatalf("expected fallback result, got %#v", result)
	}
	if result.Tier != MatchTierUniversalFloor {
		t.Fatalf("expected universal floor tier, got %q", result.Tier)
	}
	if !hasDiagnosticReason(diagnostics, "UniversalFloorMatched") {
		t.Fatalf("expected UniversalFloorMatched warning, got %#v", diagnostics)
	}
}

func TestMatchMissingModelUsesUniversalFloor(t *testing.T) {
	device := Device{ID: "ups-a", DriverFamily: "snmp-ups"}
	profiles := []Profile{
		profile("driver-family", "1.0.0", ProfileSourceCRD, ProfileSelector{DriverFamily: "snmp-ups"}),
		profile("universal", "1.0.0", ProfileSourceBundled, ProfileSelector{Universal: true}),
	}

	result, diagnostics, err := Match(device, profiles)
	if err != nil {
		t.Fatalf("expected match to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if result.ProfileID != "universal" {
		t.Fatalf("expected missing model to force universal floor, got %#v", result)
	}
	if !hasDiagnosticReason(diagnostics, "ProviderModelMissing") {
		t.Fatalf("expected ProviderModelMissing warning, got %#v", diagnostics)
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
	if !hasDiagnosticReason(diagnostics, "UniversalFloorRequired") {
		t.Fatalf("expected UniversalFloorRequired diagnostic, got %#v", diagnostics)
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
