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
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

// applyIdentityStatus records what the device said about itself on this poll.
//
// F-101. The capability profile is selected from spec.identity.model and never from the reported
// value, because GP-5 keeps the failure path on authored input. Nothing compared the two, so a typo
// in the declared model silently bound the device to a different product's profile -- and the
// profile is what decides which telemetry is trusted and which triggers are supported. GP-5 permits
// exactly this correction: derived data verifies authored input and raises a condition, without
// feeding the decision itself.
func applyIdentityStatus(device *powerv1alpha1.UPSDevice, snapshot telemetry.Snapshot) {
	observedAt := metav1.NewTime(snapshot.ObservedAt)
	device.Status.Identity = &powerv1alpha1.UPSDeviceIdentityStatus{
		ReportedModel:        strings.TrimSpace(snapshot.Variables[capability.VariableUPSModel]),
		ReportedManufacturer: strings.TrimSpace(snapshot.Variables[capability.VariableUPSManufacturer]),
		ReportedFirmware:     strings.TrimSpace(snapshot.Variables[capability.VariableUPSFirmware]),
		DeclaredModel:        strings.TrimSpace(device.Spec.Identity.Model),
		ObservedAt:           &observedAt,
	}
}

// identityVerdict is what the IdentityVerified condition publishes.
type identityVerdict struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

// evaluateIdentity compares the declared model against the reported one.
//
// Comparison is exact after trimming, matching what the profile matcher does with the declared
// value: it compares selector.Model to spec.identity.model with ==, so anything this reports as a
// mismatch is a difference the matcher would also see if it could see the device.
//
// Unknown rather than False when either half is absent. A device with no declared model is a
// supported configuration -- it matches by driver family instead -- and reporting it as failing
// verification would put a permanent red condition on every such device.
func evaluateIdentity(identity *powerv1alpha1.UPSDeviceIdentityStatus) identityVerdict {
	declared := ""
	reported := ""
	if identity != nil {
		declared = strings.TrimSpace(identity.DeclaredModel)
		reported = strings.TrimSpace(identity.ReportedModel)
	}

	switch {
	case declared == "":
		return identityVerdict{
			status: metav1.ConditionUnknown,
			reason: "ModelNotDeclared",
			message: "spec.identity.model is not set, so there is nothing to verify and the " +
				"capability profile match cannot reach a product tier",
		}
	case reported == "":
		return identityVerdict{
			status: metav1.ConditionUnknown,
			reason: "ReportedModelUnavailable",
			message: fmt.Sprintf(
				"the device has not reported ups.model, so the declared model %q is unverified",
				declared,
			),
		}
	case reported == declared:
		return identityVerdict{
			status:  metav1.ConditionTrue,
			reason:  "IdentityMatches",
			message: fmt.Sprintf("the device reports ups.model %q, matching spec.identity.model", reported),
		}
	default:
		return identityVerdict{
			status: metav1.ConditionFalse,
			reason: "IdentityMismatch",
			message: fmt.Sprintf(
				"the device reports ups.model %q but spec.identity.model declares %q; the capability "+
					"profile is selected from the declared value, so this device is matched against "+
					"the wrong product's trusted telemetry and supported triggers",
				reported, declared,
			),
		}
	}
}
