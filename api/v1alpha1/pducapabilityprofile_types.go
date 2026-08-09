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

// PDUCapabilityProfileSpec defines the desired state of PDUCapabilityProfile.
//
// A parallel kind to UPSCapabilityProfile rather than an extension of it (OD-25).
// Sharing one kind would keep the CRD count lower at the cost of making its name
// progressively dishonest: every UPS-specific field would become
// optional-and-ignored for PDUs, and a schema whose meaning depends on what
// matched it is not a contract.
//
// Actuation is deliberately absent. Switchable outlets are what a PDU is for, and
// declaring which outlets switch is not the same as switching them — that waits on
// OD-20 to settle which NUT instant commands enter scope, tracked in
// docs/tasks-post-v1.md. Everything here describes a device; nothing commands one.
type PDUCapabilityProfileSpec struct {
	// version is the semantic version of this behavior profile.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// selector declares which PDU devices this profile can match. The precedence
	// chain is the one UPS profiles use: exact model plus firmware, exact model,
	// model glob, driver family, then the universal fallback.
	Selector PDUCapabilityProfileSelector `json:"selector"`

	// outlets describes the outlet layout of matching devices.
	// +optional
	Outlets PDUCapabilityOutletSpec `json:"outlets,omitempty"`

	// telemetry declares authoritative NUT variables exposed by matching devices.
	// +optional
	Telemetry PDUCapabilityTelemetrySpec `json:"telemetry,omitempty"`

	// quirks records behavior-affecting quirks consumed by planner or executor policy.
	// +listType=map
	// +listMapKey=name
	// +optional
	Quirks []CapabilityQuirk `json:"quirks,omitempty"`
}

// PDUCapabilityProfileSelector defines deterministic profile match keys.
type PDUCapabilityProfileSelector struct {
	// model matches an exact provider model string.
	// +optional
	Model string `json:"model,omitempty"`

	// firmware narrows an exact model match to a specific firmware string.
	// +optional
	Firmware string `json:"firmware,omitempty"`

	// modelGlob matches a provider model string using path.Match glob semantics.
	// +optional
	ModelGlob string `json:"modelGlob,omitempty"`

	// driverFamily matches the NUT driver family, such as snmp-ups.
	// +optional
	DriverFamily string `json:"driverFamily,omitempty"`

	// universal marks this profile as the unidentified-device fallback. At most one
	// PDU profile may set it.
	// +kubebuilder:default=false
	// +optional
	Universal *bool `json:"universal,omitempty"`
}

// PDUCapabilityOutletSpec describes an outlet layout without implying control.
type PDUCapabilityOutletSpec struct {
	// count is the number of outlets the device exposes. Unset means undeclared,
	// which is not the same as a device with no outlets; nothing infers one from
	// the other.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Count int32 `json:"count,omitempty"`

	// switchable names the outlets that can be switched independently, using NUT's
	// outlet identifiers. Empty alongside a non-zero count describes a metered-only
	// PDU: it reports per-outlet readings and switches nothing.
	//
	// Declaring an outlet here does not make it switchable by this operator. No PDU
	// actuation is implemented; see docs/tasks-post-v1.md.
	// +optional
	Switchable []string `json:"switchable,omitempty"`
}

// PDUCapabilityTelemetrySpec declares NUT telemetry variables.
type PDUCapabilityTelemetrySpec struct {
	// variables are authoritative NUT variable names exposed by matching devices.
	// +optional
	Variables []string `json:"variables,omitempty"`

	// aliases map a non-standard variable name the device reports (the key) onto
	// the canonical NUT name it should be read as (the value), with the same
	// one-directional, native-wins semantics as UPS profiles (OD-23).
	// +optional
	Aliases map[string]string `json:"aliases,omitempty"`
}

// PDUCapabilityProfileStatus defines the observed state of PDUCapabilityProfile.
type PDUCapabilityProfileStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes profile contract acceptance.
	// +optional
	Phase PDUCapabilityProfilePhase `json:"phase,omitempty"`

	// profileHash identifies the accepted capability profile content.
	// +optional
	ProfileHash string `json:"profileHash,omitempty"`

	// conditions represent the current state of the PDUCapabilityProfile resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PDUCapabilityProfilePhase summarizes profile contract acceptance.
// +kubebuilder:validation:Enum=Accepted;Error
type PDUCapabilityProfilePhase string

const (
	PDUCapabilityProfilePhaseAccepted PDUCapabilityProfilePhase = "Accepted"
	PDUCapabilityProfilePhaseError    PDUCapabilityProfilePhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// PDUCapabilityProfile is the Schema for the pducapabilityprofiles API
type PDUCapabilityProfile struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PDUCapabilityProfile
	// +required
	Spec PDUCapabilityProfileSpec `json:"spec"`

	// status defines the observed state of PDUCapabilityProfile
	// +optional
	Status PDUCapabilityProfileStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PDUCapabilityProfileList contains a list of PDUCapabilityProfile
type PDUCapabilityProfileList struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard list metadata
	// +optional
	metav1.ListMeta `json:"metadata,omitzero"`

	// items is the list of PDUCapabilityProfile
	Items []PDUCapabilityProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PDUCapabilityProfile{}, &PDUCapabilityProfileList{})
		return nil
	})
}
