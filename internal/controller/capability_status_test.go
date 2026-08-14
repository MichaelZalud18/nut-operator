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

package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/capability"
)

// The whole point of publishing the resolution: a device that matched nothing must be
// distinguishable from one that matched its product profile. Before this they looked identical from
// the cluster, and the difference only surfaced later, when a trigger needed a variable the
// universal floor does not declare.
func TestCapabilityStatusDistinguishesAFallbackFromAProductMatch(t *testing.T) {
	product := capabilityStatusFromMatch(capability.MatchResult{
		ProfileID:      "ubiquiti-unifi-ups-tower",
		ProfileVersion: "1.0.0",
		Tier:           capability.MatchTierExactModel,
	}, nil, nil)

	fallback := capabilityStatusFromMatch(capability.MatchResult{
		ProfileID:      "universal-floor",
		ProfileVersion: "1.0.0",
		Tier:           capability.MatchTierUnidentified,
		Unidentified:   true,
	}, []capability.Diagnostic{{
		Severity: capability.DiagnosticWarning,
		Reason:   "DeviceUnidentified",
		Message:  `device "ups-a" matched no product capability profile and is unidentified`,
	}}, nil)

	if product.Unidentified {
		t.Error("a product match must not be reported as unidentified")
	}
	if product.Reason != "" {
		t.Errorf("a clean product match carries no reason, got %q", product.Reason)
	}
	if !fallback.Unidentified {
		t.Error("a device that matched nothing must say so")
	}
	if fallback.Reason != "DeviceUnidentified" {
		t.Errorf("the fallback must carry the matcher's own reason, got %q", fallback.Reason)
	}
	if product.Tier == fallback.Tier {
		t.Error("the tier is what a reader judges the match by; the two cases must differ")
	}
}

// A failed resolution still polls telemetry, just without alias resolution. Publishing nothing would
// present that partial failure as a clean match.
func TestCapabilityStatusReportsAFailedResolutionRatherThanNothing(t *testing.T) {
	status := capabilityStatusFromMatch(capability.MatchResult{}, nil, errors.New("list UPSCapabilityProfile resources: connection refused"))

	if status == nil {
		t.Fatal("a failed match must publish a status, not nil")
	}
	if status.Reason != "CapabilityMatchFailed" {
		t.Errorf("expected CapabilityMatchFailed, got %q", status.Reason)
	}
	if !strings.Contains(status.Message, "connection refused") {
		t.Errorf("the message must carry the underlying error, got %q", status.Message)
	}
	if status.ProfileID != "" {
		t.Error("a failed match must not publish a profile identity it does not have")
	}
}

// The quirks that survive firmware scoping are the ones actually in force, and the matcher is the
// only thing that knows which those are. Publishing them is what makes that work observable.
func TestCapabilityStatusPublishesTheQuirksInForce(t *testing.T) {
	status := capabilityStatusFromMatch(capability.MatchResult{
		ProfileID: "ubiquiti-unifi-ups-tower",
		Tier:      capability.MatchTierExactModelFirmware,
		Quirks:    []string{"battery-low-nonstandard-name", "firmware-before-1.4.18"},
	}, nil, nil)

	if len(status.Quirks) != 2 {
		t.Fatalf("expected both scoped quirks, got %v", status.Quirks)
	}
}

// EX-28: publish what is, not what it means. A judgement about whether the match is good belongs to
// the reader, who has the tier to judge it by.
func TestCapabilityStatusPublishesNoHealthJudgement(t *testing.T) {
	status := capabilityStatusFromMatch(capability.MatchResult{
		ProfileID:    "universal-floor",
		Tier:         capability.MatchTierUnidentified,
		Unidentified: true,
	}, nil, nil)

	for _, adjective := range []string{"healthy", "degraded", "poor", "bad", "good", "acceptable"} {
		if strings.Contains(strings.ToLower(status.Reason+" "+status.Message), adjective) {
			t.Errorf("status must not carry a health judgement, found %q", adjective)
		}
	}
}

// The status must not alias the matcher's slice, or a later normalization pass inside the matcher
// would be visible through a published status field.
func TestCapabilityStatusCopiesQuirks(t *testing.T) {
	quirks := []string{"one", "two"}
	status := capabilityStatusFromMatch(capability.MatchResult{Quirks: quirks}, nil, nil)

	quirks[0] = "mutated"
	if status.Quirks[0] != "one" {
		t.Error("published quirks must be a copy, not an alias of the match's slice")
	}
}

// F-27 needs a device to be able to say which actuation behaviors its profile currently claims, and
// until now it could not. Empty is the normal state and means "not verified", never "not capable" --
// which is why it is published as a list rather than inferred from silence.
func TestCapabilityStatusPublishesDeclaredActuationBehaviors(t *testing.T) {
	status := capabilityStatusFromMatch(capability.MatchResult{
		ProfileID:          "some-verified-pdu-backed-ups",
		ActuationBehaviors: []string{"load.off", "shutdown.return"},
	}, nil, nil)

	if len(status.ActuationBehaviors) != 2 {
		t.Fatalf("declared actuation behaviors must be published, got %v", status.ActuationBehaviors)
	}

	empty := capabilityStatusFromMatch(capability.MatchResult{ProfileID: "ubiquiti-unifi-ups-tower"}, nil, nil)
	if len(empty.ActuationBehaviors) != 0 {
		t.Errorf("a profile declaring none must publish none, got %v", empty.ActuationBehaviors)
	}
}
