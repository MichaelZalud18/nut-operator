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

import "testing"

func TestBundledProfilesMatchUbiquitiUPSTypes(t *testing.T) {
	profiles := BundledProfiles()
	cases := map[string]struct {
		device  Device
		profile string
	}{
		"tower-eu": {
			device: Device{
				ID:    "tower",
				Model: "TOWER_1000VA_230V",
			},
			profile: BundledUbiquitiUPSTowerProfileID,
		},
		"tower-us": {
			device: Device{
				ID:    "tower",
				Model: "TOWER_1000VA_120V",
			},
			profile: BundledUbiquitiUPSTowerProfileID,
		},
		"2u-eu": {
			device: Device{
				ID:    "rack",
				Model: "2U_1500VA_230V",
			},
			profile: BundledUbiquitiUPS2UProfileID,
		},
		"2u-us": {
			device: Device{
				ID:    "rack",
				Model: "2U_1440VA_120V",
			},
			profile: BundledUbiquitiUPS2UProfileID,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result, diagnostics, err := Match(tc.device, profiles)
			if err != nil {
				t.Fatalf("expected bundled profile match to succeed, got %v with diagnostics %#v", err, diagnostics)
			}
			if result.ProfileID != tc.profile {
				t.Fatalf("expected profile %q, got %#v", tc.profile, result)
			}
			if result.Tier != MatchTierModelGlob {
				t.Fatalf("expected model glob match, got tier %q", result.Tier)
			}
			if result.Fallback {
				t.Fatalf("expected Ubiquiti profile match not to be fallback, got %#v", result)
			}
		})
	}
}

func TestBundledProfilesIncludeUniversalFloor(t *testing.T) {
	result, diagnostics, err := Match(Device{ID: "unknown", Model: "Unknown Model"}, BundledProfiles())
	if err != nil {
		t.Fatalf("expected bundled universal floor to match, got %v with diagnostics %#v", err, diagnostics)
	}
	if result.ProfileID != BundledUniversalFloorProfileID {
		t.Fatalf("expected bundled universal floor profile, got %#v", result)
	}
	if !result.Fallback {
		t.Fatalf("expected unknown model to fall back, got %#v", result)
	}
}

func TestBundledProfilesReturnDefensiveCopies(t *testing.T) {
	first := BundledProfiles()
	first[0].ID = "mutated"
	first[0].TelemetryVariables[0] = "mutated"

	second := BundledProfiles()
	if second[0].ID == "mutated" {
		t.Fatal("expected bundled profile IDs to be immutable across calls")
	}
	if second[0].TelemetryVariables[0] == "mutated" {
		t.Fatal("expected bundled telemetry variables to be immutable across calls")
	}
}
