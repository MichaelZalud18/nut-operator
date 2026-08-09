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

const (
	BundledUnidentifiedProfileID     = "nut-bundled-unidentified-device"
	BundledUbiquitiUPSTowerProfileID = "ubiquiti-unifi-ups-tower"
	BundledUbiquitiUPS2UProfileID    = "ubiquiti-unifi-ups-2u"
	bundledCapabilityProfileVersion  = "1.0.0"

	// Quirk strings carry their own firmware scope, following the existing
	// firmware-before-1.4.18 precedent. Two earlier quirks claimed a reachable
	// built-in NUT server and no SNMP support at all; both were contradicted by
	// field testing on firmware 1.6.1, so they are replaced by
	// firmware-scoped statements of what was actually observed rather than left
	// as unscoped claims a current device would inherit forever.
	// Quirk names no longer carry firmware scope. They used to -- names like
	// "built-in-nut-server-not-reachable-on-firmware-1.6.1" read correctly and
	// enforced nothing, so a device on newer firmware inherited them forever
	// (F-26). Scope now lives in the Firmware field, where the matcher can act
	// on it, and the names say only what the defect is.
	quirkUbiquitiBuiltInNUTServerUnreachable = "built-in-nut-server-not-reachable"
	quirkUbiquitiSNMPv3Only                  = "snmpv3-only-no-community-string"
	quirkUbiquitiPre1418ProtocolBugs         = "nut-protocol-response-bugs"
	quirkUbiquitiNoConfirmedInstcmds         = "instant-commands-not-confirmed"
	quirkUbiquitiNonstandardLowLevel         = "reports-battery.low-instead-of-battery.charge.low"
	quirkUbiquitiTowerPowerMayVary           = "tower-output-power-and-current-may-be-firmware-or-load-dependent"
	quirkUbiquitiCredentialedReads           = "credentialed-upsc-reads-may-require-client-config"
)

var (
	ubiquitiCommonTelemetryVariables = []string{
		"battery.charge",
		"battery.charge.low",
		"battery.low",
		"battery.runtime",
		"battery.voltage",
		"input.frequency",
		"input.transfer.high",
		"input.transfer.low",
		"input.voltage",
		"input.voltage.nominal",
		"output.current",
		"output.frequency",
		"output.power",
		"output.power.nominal",
		"output.voltage",
		"ups.id",
		"ups.load",
		"ups.mfr",
		"ups.model",
		"ups.serial",
		"ups.status",
		"ups.test.date",
		"ups.test.interval",
		"ups.test.result",
		"ups.type",
	}

	ubiquitiCommonQuirks = []Quirk{
		// Observed on 1.6.1. Scoped to that build rather than to the family: a
		// later firmware that fixes it should stop carrying it, which is the
		// whole point of F-26.
		{
			Name:     quirkUbiquitiBuiltInNUTServerUnreachable,
			Firmware: &QuirkFirmware{Matches: []string{"1.6.1"}},
		},
		{
			Name:     quirkUbiquitiSNMPv3Only,
			Firmware: &QuirkFirmware{Matches: []string{"1.6.1"}},
		},
		// Fixed in 1.4.18, so it applies strictly below that.
		{
			Name:     quirkUbiquitiPre1418ProtocolBugs,
			Firmware: &QuirkFirmware{Below: "1.4.18"},
		},
		// The remainder are unscoped on purpose: they are properties of the
		// model as shipped, with no firmware known to fix them. Inventing a
		// bound here would be asserting a fix nobody has verified.
		{Name: quirkUbiquitiNoConfirmedInstcmds},
		{Name: quirkUbiquitiNonstandardLowLevel},
		{Name: quirkUbiquitiCredentialedReads},
	}

	// These devices report the low-battery level under a non-standard name.
	// The alias is what makes the recorded quirk actionable instead of prose.
	ubiquitiCommonTelemetryAliases = map[string]string{
		"battery.low": "battery.charge.low",
	}
)

// BundledProfiles returns the operator's built-in capability profile catalog.
// Callers receive a deep copy so profile normalization can never mutate the
// shared catalog.
func BundledProfiles() []Profile {
	return copyProfiles([]Profile{
		{
			ID:      BundledUnidentifiedProfileID,
			Version: bundledCapabilityProfileVersion,
			Source:  ProfileSourceBundled,
			Selector: ProfileSelector{
				Universal: true,
			},
			TelemetryVariables: []string{
				"ups.status",
			},
		},
		{
			ID:      BundledUbiquitiUPSTowerProfileID,
			Version: bundledCapabilityProfileVersion,
			Source:  ProfileSourceBundled,
			Selector: ProfileSelector{
				ModelGlob: "TOWER_*VA_*V",
			},
			TelemetryVariables: append(append([]string{}, ubiquitiCommonTelemetryVariables...),
				"ups.temperature",
			),
			TelemetryAliases: ubiquitiCommonTelemetryAliases,
			Quirks: append(append([]Quirk{}, ubiquitiCommonQuirks...),
				Quirk{Name: quirkUbiquitiTowerPowerMayVary},
			),
		},
		{
			ID:      BundledUbiquitiUPS2UProfileID,
			Version: bundledCapabilityProfileVersion,
			Source:  ProfileSourceBundled,
			Selector: ProfileSelector{
				ModelGlob: "2U_*VA_*V",
			},
			TelemetryVariables: ubiquitiCommonTelemetryVariables,
			TelemetryAliases:   ubiquitiCommonTelemetryAliases,
			Quirks:             ubiquitiCommonQuirks,
		},
	})
}

func copyProfiles(profiles []Profile) []Profile {
	copied := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		profile.TelemetryVariables = append([]string(nil), profile.TelemetryVariables...)
		profile.TelemetryAliases = copyAliases(profile.TelemetryAliases)
		profile.ActuationBehaviors = append([]string(nil), profile.ActuationBehaviors...)
		profile.Quirks = copyQuirks(profile.Quirks)
		copied = append(copied, profile)
	}
	return copied
}
