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
	"strings"
	"testing"
)

func TestBuildDraftDeclaresOnlyWhatTheDeviceReported(t *testing.T) {
	draft := BuildDraft(ProbeObservation{
		DeviceID: "ups-1",
		Variables: map[string]string{
			"ups.mfr":         "Vendor",
			"ups.model":       "SUPER 1500",
			"ups.firmware":    "2.1",
			"ups.status":      "OL",
			"battery.charge":  "97",
			"battery.runtime": "3600",
		},
	}, DraftOptions{})

	if draft.Model != "SUPER 1500" || draft.Manufacturer != "Vendor" || draft.Firmware != "2.1" {
		t.Fatalf("identity not read from the device: %#v", draft)
	}
	if draft.ProfileName != "vendor-super-1500" {
		t.Fatalf("derived profile name = %q, want vendor-super-1500", draft.ProfileName)
	}
	if len(draft.Variables) != 6 {
		t.Fatalf("variables = %#v, want all six reported names", draft.Variables)
	}
	if len(draft.NonStandardVariables) != 0 {
		t.Fatalf("all reported names are standard, got non-standard %#v", draft.NonStandardVariables)
	}

	yaml := draft.ProfileYAML()
	if !strings.Contains(yaml, "model: SUPER 1500") {
		t.Fatalf("draft is missing its selector:\n%s", yaml)
	}
	if !strings.Contains(yaml, "behaviors: []") {
		t.Fatalf("actuation must be drafted empty:\n%s", yaml)
	}
	// A profile describes a product, so a probe must never copy readings into it.
	for _, reading := range []string{"OL", "97", "3600"} {
		if strings.Contains(yaml, reading) {
			t.Fatalf("draft leaked the reading %q into a product profile:\n%s", reading, yaml)
		}
	}
}

func TestBuildDraftSuggestsAliasForNonStandardName(t *testing.T) {
	draft := BuildDraft(ProbeObservation{
		DeviceID: "ups-1",
		Variables: map[string]string{
			"ups.model":   "SUPER 1500",
			"ups.status":  "OL",
			"battery.low": "20",
		},
	}, DraftOptions{})

	if len(draft.SuggestedAliases) != 1 {
		t.Fatalf("suggested aliases = %#v, want one battery.low mapping", draft.SuggestedAliases)
	}
	if draft.SuggestedAliases["battery.low"] != "battery.charge.low" {
		t.Fatalf("alias = %#v, want battery.low -> battery.charge.low", draft.SuggestedAliases)
	}
	if !strings.Contains(draft.ProfileYAML(), "SUGGESTED, NOT VERIFIED") {
		t.Fatal("suggested aliases must be marked unverified in the draft")
	}
}

func TestBuildDraftDoesNotSuggestAliasWhenCanonicalIsPresent(t *testing.T) {
	draft := BuildDraft(ProbeObservation{
		DeviceID: "ups-1",
		Variables: map[string]string{
			"ups.model":          "SUPER 1500",
			"ups.status":         "OL",
			"battery.low":        "20",
			"battery.charge.low": "20",
		},
	}, DraftOptions{})

	if len(draft.SuggestedAliases) != 0 {
		t.Fatalf("the device already reports the canonical name, got %#v", draft.SuggestedAliases)
	}
}

func TestBuildDraftFlagsMissingModel(t *testing.T) {
	draft := BuildDraft(ProbeObservation{
		DeviceID:  "ups-1",
		Variables: map[string]string{"ups.status": "OL"},
	}, DraftOptions{})

	if draft.ProfileName != "unnamed-ups-profile" {
		t.Fatalf("profile name = %q, want the unnamed placeholder", draft.ProfileName)
	}
	if !hasNoteContaining(draft.Notes, "no ups.model") {
		t.Fatalf("a model-less draft must say so, got %#v", draft.Notes)
	}
	if !strings.Contains(draft.ProfileYAML(), "REQUIRED") {
		t.Fatal("a draft with no selector must say the selector is required")
	}
}

func TestBuildDraftReportsNonStandardVariables(t *testing.T) {
	draft := BuildDraft(ProbeObservation{
		DeviceID: "ups-1",
		Variables: map[string]string{
			"ups.model":       "SUPER 1500",
			"ups.status":      "OL",
			"vendor.magic":    "1",
			"battery.runtime": "3600",
		},
	}, DraftOptions{})

	if len(draft.NonStandardVariables) != 1 || draft.NonStandardVariables[0] != "vendor.magic" {
		t.Fatalf("non-standard variables = %#v, want vendor.magic", draft.NonStandardVariables)
	}
	// vendor.magic shares no root with any canonical target, so nothing should
	// be invented for it.
	if len(draft.SuggestedAliases) != 0 {
		t.Fatalf("an unrecognizable name must not produce a guess, got %#v", draft.SuggestedAliases)
	}
}

func TestBuildDraftIsDeterministic(t *testing.T) {
	observation := ProbeObservation{
		DeviceID: "ups-1",
		Variables: map[string]string{
			"ups.model":       "SUPER 1500",
			"ups.status":      "OL",
			"battery.runtime": "3600",
			"battery.charge":  "97",
			"input.voltage":   "120",
			"batt.pct":        "97",
		},
	}

	first := BuildDraft(observation, DraftOptions{}).ProfileYAML()
	for i := 0; i < 20; i++ {
		if got := BuildDraft(observation, DraftOptions{}).ProfileYAML(); got != first {
			t.Fatalf("draft is not deterministic across map iteration order:\n%s\n---\n%s", first, got)
		}
	}
}

func TestIssueReportOmitsSerialAndCarriesChecklist(t *testing.T) {
	draft := BuildDraft(ProbeObservation{
		DeviceID: "ups-1",
		Variables: map[string]string{
			"ups.model":  "SUPER 1500",
			"ups.mfr":    "Vendor",
			"ups.serial": "SN-0001-SITE-A",
			"ups.status": "OL",
		},
	}, DraftOptions{})

	report := draft.IssueReport()
	if strings.Contains(report, "SN-0001-SITE-A") {
		t.Fatalf("issue report leaked a unit serial:\n%s", report)
	}
	if !strings.Contains(report, "ups.serial") {
		t.Fatal("the report should still record that ups.serial is a reported variable name")
	}
	if !strings.Contains(report, "- [ ]") {
		t.Fatal("the report should carry a verification checklist")
	}
}

func TestQuoteYAMLScalarProtectsAmbiguousModels(t *testing.T) {
	for input, want := range map[string]string{
		"SUPER 1500":  "SUPER 1500",
		"1500:VA":     `"1500:VA"`,
		"no":          `"no"`,
		"":            `""`,
		"model #7":    `"model #7"`,
		" spaced":     `" spaced"`,
		`quote"inner`: `"quote\"inner"`,
	} {
		if got := quoteYAMLScalar(input); got != want {
			t.Fatalf("quoteYAMLScalar(%q) = %s, want %s", input, got, want)
		}
	}
}

func hasNoteContaining(notes []string, fragment string) bool {
	for _, note := range notes {
		if strings.Contains(note, fragment) {
			return true
		}
	}
	return false
}
