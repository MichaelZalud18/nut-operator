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

package telemetry

import (
	"testing"
	"time"
)

func TestNormalizeOnBatteryLowBatterySnapshot(t *testing.T) {
	observedAt := time.Date(2026, 8, 2, 9, 30, 0, 0, time.FixedZone("test", -7*60*60))
	snapshot := Normalize(Sample{
		UPSDevice:  "rack-a-ups",
		NUTServer:  "rack-a",
		NUTName:    "ups",
		ObservedAt: observedAt,
		Variables: map[string]string{
			VariableUPSStatus:      "OB LB DISCHRG",
			VariableBatteryCharge:  "42.5",
			VariableBatteryRuntime: "900",
			VariableUPSLoad:        "38",
		},
	})

	if snapshot.Phase != PhaseLowBattery {
		t.Fatalf("expected low battery phase, got %q", snapshot.Phase)
	}
	if !snapshot.OnBattery || !snapshot.LowBattery || !snapshot.StatusSymbolsContain("DISCHRG") {
		t.Fatalf("expected OB/LB/DISCHRG flags, got %#v", snapshot.StatusSymbols)
	}
	if snapshot.BatteryChargePercent == nil || *snapshot.BatteryChargePercent != 42.5 {
		t.Fatalf("expected battery charge 42.5, got %#v", snapshot.BatteryChargePercent)
	}
	if snapshot.RuntimeSeconds == nil || *snapshot.RuntimeSeconds != 900 {
		t.Fatalf("expected runtime 900, got %#v", snapshot.RuntimeSeconds)
	}
	if snapshot.LoadPercent == nil || *snapshot.LoadPercent != 38 {
		t.Fatalf("expected load 38, got %#v", snapshot.LoadPercent)
	}
	if !snapshot.ObservedAt.Equal(observedAt.UTC()) {
		t.Fatalf("expected UTC observation time, got %s", snapshot.ObservedAt)
	}
}

func TestNormalizeAcceptsUnknownStatusSymbols(t *testing.T) {
	snapshot := Normalize(Sample{
		Variables: map[string]string{
			VariableUPSStatus: "ol vendor-mode",
		},
	})

	if snapshot.Phase != PhaseOnline {
		t.Fatalf("expected online phase, got %q", snapshot.Phase)
	}
	if !snapshot.StatusSymbolsContain("VENDOR-MODE") {
		t.Fatalf("expected unknown symbol to be preserved, got %#v", snapshot.StatusSymbols)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "UnknownStatusSymbol", VariableUPSStatus) {
		t.Fatalf("expected unknown-symbol diagnostic, got %#v", snapshot.Diagnostics)
	}
}

func TestNormalizeReportsMissingStatusAndBadNumbers(t *testing.T) {
	snapshot := Normalize(Sample{
		Variables: map[string]string{
			VariableBatteryCharge:  "full",
			VariableBatteryRuntime: "soon",
		},
	})

	if snapshot.Phase != PhaseStale {
		t.Fatalf("expected stale phase for missing status, got %q", snapshot.Phase)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "StatusMissing", VariableUPSStatus) {
		t.Fatalf("expected missing status diagnostic, got %#v", snapshot.Diagnostics)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "VariableParseFailed", VariableBatteryCharge) {
		t.Fatalf("expected charge parse diagnostic, got %#v", snapshot.Diagnostics)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "VariableParseFailed", VariableBatteryRuntime) {
		t.Fatalf("expected runtime parse diagnostic, got %#v", snapshot.Diagnostics)
	}
}

func TestNormalizeRejectsNonFiniteNumbers(t *testing.T) {
	snapshot := Normalize(Sample{
		Variables: map[string]string{
			VariableUPSStatus:      "OL",
			VariableBatteryCharge:  "NaN",
			VariableBatteryRuntime: "+Inf",
			VariableUPSLoad:        "-Inf",
		},
	})

	if snapshot.BatteryChargePercent != nil {
		t.Fatalf("expected non-finite charge to be rejected, got %#v", snapshot.BatteryChargePercent)
	}
	if snapshot.RuntimeSeconds != nil {
		t.Fatalf("expected non-finite runtime to be rejected, got %#v", snapshot.RuntimeSeconds)
	}
	if snapshot.LoadPercent != nil {
		t.Fatalf("expected non-finite load to be rejected, got %#v", snapshot.LoadPercent)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "VariableParseFailed", VariableBatteryCharge) {
		t.Fatalf("expected charge parse diagnostic, got %#v", snapshot.Diagnostics)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "VariableParseFailed", VariableBatteryRuntime) {
		t.Fatalf("expected runtime parse diagnostic, got %#v", snapshot.Diagnostics)
	}
	if !hasDiagnostic(snapshot.Diagnostics, "VariableParseFailed", VariableUPSLoad) {
		t.Fatalf("expected load parse diagnostic, got %#v", snapshot.Diagnostics)
	}
}

func TestNormalizeDoesNotMutateInputVariables(t *testing.T) {
	variables := map[string]string{VariableUPSStatus: "OL"}
	snapshot := Normalize(Sample{Variables: variables})
	snapshot.Variables[VariableUPSStatus] = "OB"

	if variables[VariableUPSStatus] != "OL" {
		t.Fatalf("expected input variables to remain unchanged, got %q", variables[VariableUPSStatus])
	}
}

func TestAuditSnapshotCopiesNormalizedFields(t *testing.T) {
	snapshot := Normalize(Sample{
		UPSDevice: "rack-a-ups",
		NUTServer: "rack-a",
		NUTName:   "ups",
		Variables: map[string]string{
			VariableUPSStatus:      "OL",
			VariableBatteryRuntime: "1200",
		},
	})

	record := AuditSnapshot("00000000-0000-4000-8000-000000000010", snapshot)

	if record.SnapshotID == "" || record.UPSDevice != "rack-a-ups" || record.UPSStatus != "OL" {
		t.Fatalf("audit snapshot did not copy identity/status: %#v", record)
	}
	if record.RuntimeSeconds == nil || *record.RuntimeSeconds != 1200 {
		t.Fatalf("expected runtime 1200, got %#v", record.RuntimeSeconds)
	}
	record.Variables[VariableUPSStatus] = "OB"
	if snapshot.Variables[VariableUPSStatus] != "OL" {
		t.Fatalf("expected audit variables to be copied")
	}
}

func (s Snapshot) StatusSymbolsContain(symbol string) bool {
	for _, candidate := range s.StatusSymbols {
		if candidate == symbol {
			return true
		}
	}
	return false
}

func hasDiagnostic(diagnostics []Diagnostic, reason, subject string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason && diagnostic.Subject == subject {
			return true
		}
	}
	return false
}
