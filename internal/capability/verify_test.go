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

func matchWithVariables(variables ...string) MatchResult {
	return MatchResult{
		ProfileID:          "example-ups",
		ProfileVersion:     "1.0.0",
		TelemetryVariables: variables,
	}
}

func observedVariables(names ...string) map[string]string {
	out := make(map[string]string, len(names))
	for _, name := range names {
		out[name] = "1"
	}
	return out
}

func hasDiagnostic(verification Verification, code string) bool {
	for _, diagnostic := range verification.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestVerifyPassesWhenEveryDeclaredVariableIsReported(t *testing.T) {
	verification := Verify(
		matchWithVariables("battery.charge", "ups.status"),
		observedVariables("battery.charge", "ups.status", "ups.mfr"),
	)

	if !verification.Verified {
		t.Fatalf("expected a verified result, got %+v", verification)
	}
	if verification.DriftDetected {
		t.Fatalf("a fully satisfied profile is not drift, got %+v", verification)
	}
	if len(verification.MissingVariables) != 0 {
		t.Fatalf("expected no missing variables, got %v", verification.MissingVariables)
	}
	if !hasDiagnostic(verification, "ProfileVerified") {
		t.Fatalf("expected a ProfileVerified diagnostic, got %+v", verification.Diagnostics)
	}
}

// Drift is the whole point of OD-15: the profile promises telemetry the device
// no longer delivers, which is what a firmware change typically looks like.
func TestVerifyReportsDriftWhenDeclaredTelemetryIsAbsent(t *testing.T) {
	verification := Verify(
		matchWithVariables("battery.charge", "battery.runtime"),
		observedVariables("battery.charge"),
	)

	if verification.Verified {
		t.Fatalf("a profile missing declared telemetry is not verified, got %+v", verification)
	}
	if !verification.DriftDetected {
		t.Fatalf("expected drift, got %+v", verification)
	}
	if !reflect.DeepEqual(verification.MissingVariables, []string{"battery.runtime"}) {
		t.Fatalf("expected battery.runtime missing, got %v", verification.MissingVariables)
	}
	if !hasDiagnostic(verification, "DeclaredVariableMissing") {
		t.Fatalf("expected a DeclaredVariableMissing diagnostic, got %+v", verification.Diagnostics)
	}
}

// A correctly-aliased quirky device must not read as permanently drifted, or
// the alias mechanism would defeat the drift signal it is supposed to coexist
// with.
func TestVerifyResolvesDeclaredVariablesThroughAliases(t *testing.T) {
	match := matchWithVariables("battery.charge")
	match.TelemetryAliases = map[string]string{"vendor.batt.pct": "battery.charge"}

	verification := Verify(match, observedVariables("vendor.batt.pct"))

	if !verification.Verified {
		t.Fatalf("an aliased variable satisfies the declaration, got %+v", verification)
	}
	if verification.DriftDetected {
		t.Fatalf("expected no drift when an alias covers the variable, got %+v", verification)
	}
	if len(verification.UnexpectedVariables) != 0 {
		t.Fatalf("the alias source is declared by the profile, not unexpected: %v", verification.UnexpectedVariables)
	}
}

// Nearly every real UPS reports vendor extensions. If those counted as drift the
// signal would fire on almost every device and be worth nothing.
func TestVerifyRecordsUndeclaredVariablesWithoutCallingThemDrift(t *testing.T) {
	verification := Verify(
		matchWithVariables("battery.charge"),
		observedVariables("battery.charge", "vendor.secret.sauce"),
	)

	if verification.DriftDetected {
		t.Fatalf("extra vendor telemetry is not drift, got %+v", verification)
	}
	if !verification.Verified {
		t.Fatalf("expected still verified, got %+v", verification)
	}
	if !reflect.DeepEqual(verification.UnexpectedVariables, []string{"vendor.secret.sauce"}) {
		t.Fatalf("expected the vendor variable recorded as unexpected, got %v", verification.UnexpectedVariables)
	}
	if !hasDiagnostic(verification, "UndeclaredNonStandardVariable") {
		t.Fatalf("expected an UndeclaredNonStandardVariable diagnostic, got %+v", verification.Diagnostics)
	}
}

// Standard NUT names the profile happens not to list are not interesting: a
// profile declares what it relies on, not an inventory of the protocol.
func TestVerifyIgnoresUndeclaredStandardVariables(t *testing.T) {
	verification := Verify(
		matchWithVariables("battery.charge"),
		observedVariables("battery.charge", "ups.mfr", "ups.model"),
	)

	if len(verification.UnexpectedVariables) != 0 {
		t.Fatalf("standard variables must not be reported as unexpected, got %v", verification.UnexpectedVariables)
	}
}

// An unidentified device has no product claims to be wrong about. Marking it as
// drift would put every unprofiled device into the drift stream.
func TestVerifyNeverReportsDriftForUnidentifiedDevices(t *testing.T) {
	match := matchWithVariables("battery.charge")
	match.Unidentified = true

	verification := Verify(match, observedVariables("ups.status"))

	if verification.DriftDetected {
		t.Fatalf("an unidentified device cannot drift, got %+v", verification)
	}
	if verification.Verified {
		t.Fatalf("an unidentified device is not verified either, got %+v", verification)
	}
	if !hasDiagnostic(verification, "UnidentifiedDevice") {
		t.Fatalf("expected an UnidentifiedDevice diagnostic, got %+v", verification.Diagnostics)
	}
}

// A read that returned nothing proves nothing. Reporting it as verified would
// turn an unreachable device into a clean bill of health.
func TestVerifyDoesNotVerifyAnEmptyRead(t *testing.T) {
	verification := Verify(matchWithVariables(), nil)

	if verification.Verified {
		t.Fatalf("an empty read cannot verify anything, got %+v", verification)
	}
	if !hasDiagnostic(verification, "NoVariablesReported") {
		t.Fatalf("expected a NoVariablesReported diagnostic, got %+v", verification.Diagnostics)
	}
}

func TestVerifyIsDeterministic(t *testing.T) {
	match := matchWithVariables("ups.status", "battery.charge", "battery.runtime")
	observed := observedVariables("battery.charge", "vendor.b", "vendor.a")

	first := Verify(match, observed)
	for i := 0; i < 20; i++ {
		if got := Verify(match, observed); !reflect.DeepEqual(got, first) {
			t.Fatalf("Verify is not deterministic across map iteration orders:\nfirst: %+v\ngot:   %+v", first, got)
		}
	}
	if !reflect.DeepEqual(first.ExpectedVariables, []string{"battery.charge", "battery.runtime", "ups.status"}) {
		t.Fatalf("expected sorted expected-variables, got %v", first.ExpectedVariables)
	}
}
