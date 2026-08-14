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
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/capability"
)

func pduProfile(id, version string, universal bool) capability.PDUProfile {
	profile := capability.PDUProfile{ID: id, Version: version}
	profile.Selector.Universal = universal
	if !universal {
		profile.Selector.Model = id
	}
	return profile
}

// Two profiles, each individually valid, that together leave no deterministic answer for any device.
// The per-profile validation the reconciler and webhook run cannot see this: it is a property of the
// set. It was unreachable before, living only inside MatchPDU, which has no caller while there is no
// PDU device kind (OD-25) -- so a cluster could hold both and report both Accepted.
func TestPDUProfileSetRejectsTwoUniversalProfiles(t *testing.T) {
	diagnostics := capability.ValidatePDUProfileSet([]capability.PDUProfile{
		pduProfile("floor-a", "1.0.0", true),
		pduProfile("floor-b", "1.0.0", true),
	})

	if !hasErrorReason(diagnostics, "MultipleUniversalPDUProfiles") {
		t.Fatalf("two universal PDU profiles must be reported, got %+v", diagnostics)
	}
}

func TestPDUProfileSetRejectsTheSameProfileTwice(t *testing.T) {
	diagnostics := capability.ValidatePDUProfileSet([]capability.PDUProfile{
		pduProfile("vendor-pdu", "1.0.0", false),
		pduProfile("vendor-pdu", "1.0.0", false),
	})

	if !hasErrorReason(diagnostics, "DuplicatePDUProfile") {
		t.Fatalf("a duplicated id and version must be reported, got %+v", diagnostics)
	}
}

// The common case must stay quiet, or the conflict report is noise and gets ignored.
func TestPDUProfileSetAcceptsOneUniversalPlusProductProfiles(t *testing.T) {
	diagnostics := capability.ValidatePDUProfileSet([]capability.PDUProfile{
		pduProfile("floor", "1.0.0", true),
		pduProfile("vendor-pdu-a", "1.0.0", false),
		pduProfile("vendor-pdu-b", "2.1.0", false),
	})

	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == capability.DiagnosticError {
			t.Fatalf("a resolvable set must report no error, got %+v", diagnostic)
		}
	}
}

// Distinct versions of one profile are how a catalog revises a device, not a duplicate.
func TestPDUProfileSetAcceptsTwoVersionsOfOneProfile(t *testing.T) {
	diagnostics := capability.ValidatePDUProfileSet([]capability.PDUProfile{
		pduProfile("vendor-pdu", "1.0.0", false),
		pduProfile("vendor-pdu", "1.1.0", false),
	})

	if hasErrorReason(diagnostics, "DuplicatePDUProfile") {
		t.Fatal("two versions of one profile are a revision, not a duplicate")
	}
}

func hasErrorReason(diagnostics []capability.Diagnostic, reason string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == capability.DiagnosticError && diagnostic.Reason == reason {
			return true
		}
	}
	return false
}
