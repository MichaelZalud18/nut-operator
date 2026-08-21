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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

// F-101: the capability profile is selected from spec.identity.model alone, so a typo bound a
// device to another product's profile with nothing to say so -- and that profile decides which
// telemetry is trusted and which triggers are supported.
func TestEvaluateIdentityReportsAMismatch(t *testing.T) {
	verdict := evaluateIdentity(&powerv1alpha1.UPSDeviceIdentityStatus{
		DeclaredModel: "Smart-UPS 1500",
		ReportedModel: "Smart-UPS 750",
	})

	if verdict.status != metav1.ConditionFalse {
		t.Fatalf("status = %q, want False", verdict.status)
	}
	if verdict.reason != "IdentityMismatch" {
		t.Errorf("reason = %q, want IdentityMismatch", verdict.reason)
	}
	// Both strings belong in the message: a reader seeing only one cannot tell which half is wrong.
	for _, model := range []string{"Smart-UPS 1500", "Smart-UPS 750"} {
		if !strings.Contains(verdict.message, model) {
			t.Errorf("message does not name %q: %s", model, verdict.message)
		}
	}
}

func TestEvaluateIdentityVerifiesAgreement(t *testing.T) {
	verdict := evaluateIdentity(&powerv1alpha1.UPSDeviceIdentityStatus{
		DeclaredModel: "Smart-UPS 1500",
		ReportedModel: "Smart-UPS 1500",
	})

	if verdict.status != metav1.ConditionTrue {
		t.Fatalf("status = %q, want True", verdict.status)
	}
	if verdict.reason != "IdentityMatches" {
		t.Errorf("reason = %q, want IdentityMatches", verdict.reason)
	}
}

// Unknown, not False. A device with no declared model matches by driver family instead, which is a
// supported configuration -- reporting it as failing verification would paint every such device red
// permanently, and a condition that is always red is a condition nobody reads.
func TestEvaluateIdentityIsUnknownWhenThereIsNothingToCompare(t *testing.T) {
	for name, identity := range map[string]*powerv1alpha1.UPSDeviceIdentityStatus{
		"no status at all": nil,
		"nothing declared": {ReportedModel: "Smart-UPS 1500"},
		"nothing reported": {DeclaredModel: "Smart-UPS 1500"},
		"neither half set": {},
		"whitespace only":  {DeclaredModel: "   ", ReportedModel: "Smart-UPS 1500"},
	} {
		verdict := evaluateIdentity(identity)
		if verdict.status != metav1.ConditionUnknown {
			t.Errorf("%s: status = %q, want Unknown", name, verdict.status)
		}
		if verdict.reason == "" {
			t.Errorf("%s: an Unknown verdict must still say why", name)
		}
	}
}

// The matcher compares selector.Model to spec.identity.model with ==, so this check must not be
// looser than the matcher: a difference this forgives is a difference the matcher would not.
func TestEvaluateIdentityIsCaseSensitive(t *testing.T) {
	verdict := evaluateIdentity(&powerv1alpha1.UPSDeviceIdentityStatus{
		DeclaredModel: "smart-ups 1500",
		ReportedModel: "Smart-UPS 1500",
	})

	if verdict.status != metav1.ConditionFalse {
		t.Errorf("status = %q, want False -- the profile matcher would not match these either", verdict.status)
	}
}

func TestApplyIdentityStatusRecordsWhatTheDeviceReported(t *testing.T) {
	observedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Identity: powerv1alpha1.UPSDeviceIdentitySpec{Model: "Smart-UPS 1500"},
		},
	}

	applyIdentityStatus(device, telemetry.Snapshot{
		ObservedAt: observedAt,
		Variables: map[string]string{
			// Trailing whitespace is normal in NUT variable values and must not read as a mismatch.
			"ups.model":    "Smart-UPS 1500  ",
			"ups.mfr":      "APC",
			"ups.firmware": "UPS 09.3",
		},
	})

	identity := device.Status.Identity
	if identity == nil {
		t.Fatal("a successful poll recorded no identity status")
	}
	if identity.ReportedModel != "Smart-UPS 1500" {
		t.Errorf("reported model = %q, want the trimmed value", identity.ReportedModel)
	}
	if identity.ReportedManufacturer != "APC" || identity.ReportedFirmware != "UPS 09.3" {
		t.Errorf("manufacturer/firmware not recorded: %#v", identity)
	}
	if identity.DeclaredModel != "Smart-UPS 1500" {
		t.Errorf("declared model = %q, want the spec value echoed beside the reported one", identity.DeclaredModel)
	}
	if identity.ObservedAt == nil || !identity.ObservedAt.Time.Equal(observedAt) {
		t.Errorf("observedAt = %v, want the poll time", identity.ObservedAt)
	}

	if verdict := evaluateIdentity(identity); verdict.status != metav1.ConditionTrue {
		t.Errorf("trailing whitespace read as a mismatch: %s", verdict.message)
	}
}
