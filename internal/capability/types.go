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

// Package capability provides deterministic UPS capability profile matching.
// It performs no probing and no I/O; runtime probes are advisory resolver work.
package capability

// Device is the resolved matching key set supplied by inventory providers.
type Device struct {
	ID           string `json:"id,omitempty"`
	Model        string `json:"model,omitempty"`
	Firmware     string `json:"firmware,omitempty"`
	DriverFamily string `json:"driverFamily,omitempty"`
}

// ProfileSource records where a profile came from.
type ProfileSource string

const (
	ProfileSourceCRD     ProfileSource = "CRD"
	ProfileSourceBundled ProfileSource = "Bundled"
)

// Profile describes declared UPS capabilities. Declarations are authoritative;
// probes can detect drift but never rewrite this structure.
type Profile struct {
	ID                 string            `json:"id"`
	Version            string            `json:"version"`
	Source             ProfileSource     `json:"source,omitempty"`
	Selector           ProfileSelector   `json:"selector"`
	TelemetryVariables []string          `json:"telemetryVariables,omitempty"`
	TelemetryAliases   map[string]string `json:"telemetryAliases,omitempty"`
	ActuationBehaviors []string          `json:"actuationBehaviors,omitempty"`
	Quirks             []Quirk           `json:"quirks,omitempty"`
}

// Quirk is a behavior-affecting device defect, optionally scoped to the firmware
// that actually has it (OD-22).
//
// A bare name cannot expire. The bundled catalog carried scope in the string --
// "built-in-nut-server-not-reachable-on-firmware-1.6.1" -- which reads correctly
// and enforces nothing, so a device on newer firmware inherited every historical
// defect of its model family forever (F-26). Scope has to be a field for anything
// to act on it.
type Quirk struct {
	Name string `json:"name"`
	// Firmware limits the quirk to the firmware that has it. Nil means the quirk
	// applies to every device the profile matches, which stays the default: most
	// quirks are properties of a model, not of a build.
	Firmware *QuirkFirmware `json:"firmware,omitempty"`
}

// QuirkFirmware scopes a quirk to firmware versions.
type QuirkFirmware struct {
	// Matches are glob patterns compared against the device's firmware string.
	// Globs rather than ranges because vendor firmware strings are not generally
	// orderable -- "1.6.1" looks like a version and "EL-2.3b" does not, and
	// pretending otherwise mis-scopes silently.
	Matches []string `json:"matches,omitempty"`
	// Below applies the quirk to firmware strictly below this dotted-numeric
	// version. Only dotted-numeric firmware can be compared; anything else is
	// reported rather than guessed at, because the alternative is applying a
	// "fixed in 1.4.18" quirk to a device whose firmware string cannot be placed.
	Below string `json:"below,omitempty"`
}

// ProfileSelector defines the deterministic match inputs for one profile.
type ProfileSelector struct {
	Model        string `json:"model,omitempty"`
	Firmware     string `json:"firmware,omitempty"`
	ModelGlob    string `json:"modelGlob,omitempty"`
	DriverFamily string `json:"driverFamily,omitempty"`
	Universal    bool   `json:"universal,omitempty"`
}

// MatchTier names the profile precedence tier that won.
type MatchTier string

const (
	MatchTierExactModelFirmware MatchTier = "ExactModelFirmware"
	MatchTierExactModel         MatchTier = "ExactModel"
	MatchTierModelGlob          MatchTier = "ModelGlob"
	MatchTierDriverFamily       MatchTier = "DriverFamily"
	MatchTierUnidentified       MatchTier = "Unidentified"
)

// MatchResult is the selected profile plus attribution useful to the resolver.
// It carries the matched profile's declared content, not just its identity:
// downstream stages consume capabilities without re-reading the profile set,
// and the content participates in the structural hash so a profile edit changes
// plan identity.
type MatchResult struct {
	DeviceID           string            `json:"deviceID,omitempty"`
	ProfileID          string            `json:"profileID"`
	ProfileVersion     string            `json:"profileVersion"`
	ProfileSource      ProfileSource     `json:"profileSource,omitempty"`
	ProfileHash        string            `json:"profileHash"`
	Tier               MatchTier         `json:"tier"`
	Unidentified       bool              `json:"unidentified,omitempty"`
	TelemetryVariables []string          `json:"telemetryVariables,omitempty"`
	TelemetryAliases   map[string]string `json:"telemetryAliases,omitempty"`
	ActuationBehaviors []string          `json:"actuationBehaviors,omitempty"`
	// Quirks are the quirk names that apply to this device after firmware scoping.
	// Names rather than structures: scope is resolved here, so nothing downstream
	// has to re-decide whether a quirk is in force.
	Quirks []string `json:"quirks,omitempty"`
}

// SupportsTriggerType reports whether the matched profile's declared telemetry
// satisfies a trigger class, per CR-2: profiles declare variables, the operator
// owns what those variables imply.
func (r MatchResult) SupportsTriggerType(trigger TriggerType) bool {
	return satisfiesTrigger(r.TelemetryVariables, trigger)
}

// TriggerType is the trigger class used for derived capability checks.
type TriggerType string

const (
	TriggerOnBattery      TriggerType = "OnBattery"
	TriggerLowBattery     TriggerType = "LowBattery"
	TriggerRuntimeBelow   TriggerType = "RuntimeBelow"
	TriggerChargeBelow    TriggerType = "ChargeBelow"
	TriggerTelemetryStale TriggerType = "TelemetryStale"
)

// Diagnostic is a machine-readable warning or rejection from capability logic.
type Diagnostic struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}
