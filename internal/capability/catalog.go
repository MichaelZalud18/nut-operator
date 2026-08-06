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
	quirkUbiquitiBuiltInNUTServerUnreachable = "built-in-nut-server-not-reachable-on-firmware-1.6.1"
	quirkUbiquitiSNMPv3Only                  = "snmpv3-only-no-community-string-on-firmware-1.6.1"
	quirkUbiquitiPre1418ProtocolBugs         = "firmware-before-1.4.18-had-nut-protocol-response-bugs"
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

	ubiquitiCommonQuirks = []string{
		quirkUbiquitiBuiltInNUTServerUnreachable,
		quirkUbiquitiSNMPv3Only,
		quirkUbiquitiPre1418ProtocolBugs,
		quirkUbiquitiNoConfirmedInstcmds,
		quirkUbiquitiNonstandardLowLevel,
		quirkUbiquitiCredentialedReads,
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
			Quirks: append(append([]string{}, ubiquitiCommonQuirks...),
				quirkUbiquitiTowerPowerMayVary,
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
		profile.Quirks = append([]string(nil), profile.Quirks...)
		copied = append(copied, profile)
	}
	return copied
}
