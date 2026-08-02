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

// Package telemetry normalizes NUT variable snapshots into policy-relevant
// facts without owning the NUT transport, Kubernetes status, or persistence.
package telemetry

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	VariableUPSStatus      = "ups.status"
	VariableBatteryCharge  = "battery.charge"
	VariableBatteryRuntime = "battery.runtime"
	VariableUPSLoad        = "ups.load"

	PhaseUnknown     = "Unknown"
	PhaseOnline      = "Online"
	PhaseOnBattery   = "OnBattery"
	PhaseLowBattery  = "LowBattery"
	PhaseStale       = "Stale"
	PhaseUnavailable = "Unavailable"

	DiagnosticWarning = "Warning"
)

var knownStatusSymbols = map[string]struct{}{
	"ALARM":   {},
	"BOOST":   {},
	"BYPASS":  {},
	"CAL":     {},
	"CHRG":    {},
	"COMM":    {},
	"DISCHRG": {},
	"FSD":     {},
	"LB":      {},
	"NOCOMM":  {},
	"OB":      {},
	"OFF":     {},
	"OL":      {},
	"OVER":    {},
	"RB":      {},
	"TEST":    {},
	"TICK":    {},
	"TOCK":    {},
	"TRIM":    {},
}

// Sample is one raw NUT variable observation.
type Sample struct {
	UPSDevice  string
	NUTServer  string
	NUTName    string
	ObservedAt time.Time
	Variables  map[string]string
}

// Snapshot is the normalized telemetry view used by status, policy, and audit
// adapters. Unknown status symbols are preserved in StatusSymbols and surfaced
// as diagnostics instead of rejecting the snapshot.
type Snapshot struct {
	UPSDevice            string
	NUTServer            string
	NUTName              string
	ObservedAt           time.Time
	Variables            map[string]string
	UPSStatus            string
	StatusSymbols        []string
	Phase                string
	Online               bool
	OnBattery            bool
	LowBattery           bool
	ForcedShutdown       bool
	ReplaceBattery       bool
	NoCommunication      bool
	Alarm                bool
	Overloaded           bool
	BatteryChargePercent *float64
	RuntimeSeconds       *int64
	LoadPercent          *float64
	Diagnostics          []Diagnostic
}

// Diagnostic records non-fatal normalization findings.
type Diagnostic struct {
	Severity string
	Reason   string
	Subject  string
	Message  string
}

// Normalize converts raw NUT variables into a stable snapshot. The NUT
// protocol represents ups.status as space-separated symbols; clients must
// tolerate symbols they do not recognize.
func Normalize(sample Sample) Snapshot {
	var diagnostics []Diagnostic
	variables := copyVariables(sample.Variables)
	status := strings.TrimSpace(variables[VariableUPSStatus])
	symbols, statusDiagnostics := normalizeStatusSymbols(status)
	diagnostics = append(diagnostics, statusDiagnostics...)

	charge, chargeDiagnostics := parseFloatVariable(variables, VariableBatteryCharge)
	diagnostics = append(diagnostics, chargeDiagnostics...)
	runtimeSeconds, runtimeDiagnostics := parseRuntimeSeconds(variables, VariableBatteryRuntime)
	diagnostics = append(diagnostics, runtimeDiagnostics...)
	load, loadDiagnostics := parseFloatVariable(variables, VariableUPSLoad)
	diagnostics = append(diagnostics, loadDiagnostics...)

	flags := statusFlagSet(symbols)
	return Snapshot{
		UPSDevice:            sample.UPSDevice,
		NUTServer:            sample.NUTServer,
		NUTName:              sample.NUTName,
		ObservedAt:           sample.ObservedAt.UTC(),
		Variables:            variables,
		UPSStatus:            status,
		StatusSymbols:        symbols,
		Phase:                phase(flags, status),
		Online:               flags["OL"],
		OnBattery:            flags["OB"],
		LowBattery:           flags["LB"],
		ForcedShutdown:       flags["FSD"],
		ReplaceBattery:       flags["RB"],
		NoCommunication:      flags["NOCOMM"],
		Alarm:                flags["ALARM"],
		Overloaded:           flags["OVER"],
		BatteryChargePercent: charge,
		RuntimeSeconds:       runtimeSeconds,
		LoadPercent:          load,
		Diagnostics:          diagnostics,
	}
}

func normalizeStatusSymbols(status string) ([]string, []Diagnostic) {
	if status == "" {
		return nil, []Diagnostic{{
			Severity: DiagnosticWarning,
			Reason:   "StatusMissing",
			Subject:  VariableUPSStatus,
			Message:  "NUT variable ups.status is missing or empty",
		}}
	}

	seen := map[string]struct{}{}
	var symbols []string
	var diagnostics []Diagnostic
	for _, token := range strings.Fields(status) {
		symbol := strings.ToUpper(token)
		if _, exists := seen[symbol]; exists {
			continue
		}
		seen[symbol] = struct{}{}
		symbols = append(symbols, symbol)
		if _, known := knownStatusSymbols[symbol]; !known {
			diagnostics = append(diagnostics, Diagnostic{
				Severity: DiagnosticWarning,
				Reason:   "UnknownStatusSymbol",
				Subject:  VariableUPSStatus,
				Message:  fmt.Sprintf("NUT status symbol %q is not known to this normalizer", symbol),
			})
		}
	}
	return symbols, diagnostics
}

func statusFlagSet(symbols []string) map[string]bool {
	flags := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		flags[symbol] = true
	}
	return flags
}

func phase(flags map[string]bool, status string) string {
	switch {
	case status == "" || flags["NOCOMM"]:
		return PhaseStale
	case flags["OFF"]:
		return PhaseUnavailable
	case flags["FSD"] || flags["LB"]:
		return PhaseLowBattery
	case flags["OB"]:
		return PhaseOnBattery
	case flags["OL"]:
		return PhaseOnline
	default:
		return PhaseUnknown
	}
}

func parseFloatVariable(variables map[string]string, name string) (*float64, []Diagnostic) {
	raw := strings.TrimSpace(variables[name])
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, []Diagnostic{{
			Severity: DiagnosticWarning,
			Reason:   "VariableParseFailed",
			Subject:  name,
			Message:  fmt.Sprintf("NUT variable %s value %q is not a number", name, raw),
		}}
	}
	return &value, nil
}

func parseRuntimeSeconds(variables map[string]string, name string) (*int64, []Diagnostic) {
	raw := strings.TrimSpace(variables[name])
	if raw == "" {
		return nil, nil
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return &value, nil
	}
	floatValue, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, []Diagnostic{{
			Severity: DiagnosticWarning,
			Reason:   "VariableParseFailed",
			Subject:  name,
			Message:  fmt.Sprintf("NUT variable %s value %q is not a duration in seconds", name, raw),
		}}
	}
	value := int64(floatValue)
	return &value, nil
}

func copyVariables(variables map[string]string) map[string]string {
	if variables == nil {
		return nil
	}
	copied := make(map[string]string, len(variables))
	keys := make([]string, 0, len(variables))
	for key := range variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		copied[key] = variables[key]
	}
	return copied
}
