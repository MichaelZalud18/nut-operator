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
	ID                 string          `json:"id"`
	Version            string          `json:"version"`
	Source             ProfileSource   `json:"source,omitempty"`
	Selector           ProfileSelector `json:"selector"`
	TelemetryVariables []string        `json:"telemetryVariables,omitempty"`
	ActuationBehaviors []string        `json:"actuationBehaviors,omitempty"`
	Quirks             []string        `json:"quirks,omitempty"`
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
	MatchTierUniversalFloor     MatchTier = "UniversalFloor"
)

// MatchResult is the selected profile plus attribution useful to the resolver.
type MatchResult struct {
	DeviceID       string        `json:"deviceID,omitempty"`
	ProfileID      string        `json:"profileID"`
	ProfileVersion string        `json:"profileVersion"`
	ProfileSource  ProfileSource `json:"profileSource,omitempty"`
	ProfileHash    string        `json:"profileHash"`
	Tier           MatchTier     `json:"tier"`
	Fallback       bool          `json:"fallback,omitempty"`
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
