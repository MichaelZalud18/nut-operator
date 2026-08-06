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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// UPSCapabilityProbeSpec asks the operator to read what a UPS actually reports
// and draft a capability profile from it.
//
// Probing is advisory reconciliation work (RS-7). A probe never feeds the
// planner, never changes how a device resolves, and never runs on the failure
// path. Its output is a starting point for a human-reviewed profile.
type UPSCapabilityProbeSpec struct {
	// deviceRef names the UPSDevice to probe. The device must already be
	// selected by a ready NUTServer, since the probe reads through the same
	// NUT endpoint telemetry polling uses.
	DeviceRef UPSCapabilityProbeDeviceReference `json:"deviceRef"`

	// profileName is the metadata.name to use in the drafted profile. Defaults
	// to a name derived from the reported manufacturer and model.
	// +optional
	ProfileName string `json:"profileName,omitempty"`

	// refreshInterval re-runs the probe on a schedule. Leave unset for a
	// one-shot probe that only re-runs when the spec changes.
	// +optional
	RefreshInterval *metav1.Duration `json:"refreshInterval,omitempty"`
}

// UPSCapabilityProbeDeviceReference identifies a cluster-scoped UPSDevice.
type UPSCapabilityProbeDeviceReference struct {
	// name is the UPSDevice name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// UPSCapabilityProbeStatus reports what the device said and what profile that
// implies. Readings are deliberately excluded: the draft lists variable names
// only, so a probe can never copy site-specific values into a profile that is
// meant to describe a product model.
type UPSCapabilityProbeStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes probe progress.
	// +optional
	Phase UPSCapabilityProbePhase `json:"phase,omitempty"`

	// probedAt is when the device was last read.
	// +optional
	ProbedAt *metav1.Time `json:"probedAt,omitempty"`

	// identity is the product identity the device reported. Serial numbers are
	// deliberately not recorded: they identify one unit, not a product model.
	// +optional
	Identity UPSCapabilityProbeIdentity `json:"identity,omitzero"`

	// reportedVariables are the NUT variable names the device exposed.
	// +optional
	ReportedVariables []string `json:"reportedVariables,omitempty"`

	// nonStandardVariables are reported names that are not standard NUT names.
	// They are worth a human look and may deserve an alias or a quirk.
	// +optional
	NonStandardVariables []string `json:"nonStandardVariables,omitempty"`

	// suggestedAliases are proposed alias mappings, keyed by the non-standard
	// name the device reported, where a standard reading appears to have arrived
	// under it. Suggestions only, in the same shape spec.telemetry.aliases takes.
	// +optional
	SuggestedAliases map[string]string `json:"suggestedAliases,omitempty"`

	// currentMatch records which profile this device resolves to today, so the
	// gap the probe is filling is visible without a second lookup.
	// +optional
	CurrentMatch UPSCapabilityProbeMatch `json:"currentMatch,omitzero"`

	// draftProfile is a ready-to-apply UPSCapabilityProfile manifest. Review it
	// before applying: the actuation section is intentionally empty.
	// +optional
	DraftProfile string `json:"draftProfile,omitempty"`

	// issueReport is the same findings formatted for pasting into a GitHub
	// issue, so a verified profile can be contributed back to the catalog.
	// +optional
	IssueReport string `json:"issueReport,omitempty"`

	// conditions represent the current state of the UPSCapabilityProbe resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// UPSCapabilityProbeIdentity is the product identity reported by the device.
type UPSCapabilityProbeIdentity struct {
	// manufacturer is the reported ups.mfr value.
	// +optional
	Manufacturer string `json:"manufacturer,omitempty"`

	// model is the reported ups.model value and the profile's match key.
	// +optional
	Model string `json:"model,omitempty"`

	// firmware is the reported ups.firmware value.
	// +optional
	Firmware string `json:"firmware,omitempty"`
}

// UPSCapabilityProbeMatch records the device's current profile resolution.
type UPSCapabilityProbeMatch struct {
	// profileID is the profile the device resolves to today.
	// +optional
	ProfileID string `json:"profileID,omitempty"`

	// tier is the precedence tier that matched.
	// +optional
	Tier string `json:"tier,omitempty"`

	// unidentified is true when the device matched no product profile and fell
	// through to the unidentified-device profile.
	// +optional
	Unidentified bool `json:"unidentified,omitempty"`
}

// UPSCapabilityProbePhase summarizes probe progress.
// +kubebuilder:validation:Enum=Pending;Probed;Failed
type UPSCapabilityProbePhase string

const (
	UPSCapabilityProbePhasePending UPSCapabilityProbePhase = "Pending"
	UPSCapabilityProbePhaseProbed  UPSCapabilityProbePhase = "Probed"
	UPSCapabilityProbePhaseFailed  UPSCapabilityProbePhase = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Device",type=string,JSONPath=".spec.deviceRef.name"
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=".status.identity.model"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Probed",type=date,JSONPath=".status.probedAt"

// UPSCapabilityProbe is the Schema for the upscapabilityprobes API
type UPSCapabilityProbe struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of UPSCapabilityProbe
	// +required
	Spec UPSCapabilityProbeSpec `json:"spec"`

	// status defines the observed state of UPSCapabilityProbe
	// +optional
	Status UPSCapabilityProbeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// UPSCapabilityProbeList contains a list of UPSCapabilityProbe
type UPSCapabilityProbeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []UPSCapabilityProbe `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &UPSCapabilityProbe{}, &UPSCapabilityProbeList{})
		return nil
	})
}
