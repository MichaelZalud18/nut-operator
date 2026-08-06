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

// UPSCapabilityProfileSpec defines the desired state of UPSCapabilityProfile
type UPSCapabilityProfileSpec struct {
	// version is the semantic version of this behavior profile.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// selector declares which UPS devices this profile can match.
	Selector UPSCapabilityProfileSelector `json:"selector"`

	// telemetry declares authoritative NUT variables exposed by matching devices.
	// +optional
	Telemetry UPSCapabilityTelemetrySpec `json:"telemetry,omitempty"`

	// actuation declares authoritative UPS control behaviors exposed by matching devices.
	// +optional
	Actuation UPSCapabilityActuationSpec `json:"actuation,omitempty"`

	// quirks records behavior-affecting quirks consumed by planner or executor policy.
	// +optional
	Quirks []string `json:"quirks,omitempty"`
}

// UPSCapabilityProfileStatus defines the observed state of UPSCapabilityProfile.
type UPSCapabilityProfileStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes profile contract acceptance.
	// +optional
	Phase UPSCapabilityProfilePhase `json:"phase,omitempty"`

	// profileHash identifies the accepted capability profile content.
	// +optional
	ProfileHash string `json:"profileHash,omitempty"`

	// conditions represent the current state of the UPSCapabilityProfile resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// UPSCapabilityProfileSelector defines deterministic profile match keys.
type UPSCapabilityProfileSelector struct {
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

	// universal marks this profile as the unidentified-device fallback.
	// +kubebuilder:default=false
	// +optional
	Universal *bool `json:"universal,omitempty"`
}

// UPSCapabilityTelemetrySpec declares NUT telemetry variables.
type UPSCapabilityTelemetrySpec struct {
	// variables are authoritative NUT variable names exposed by matching devices.
	// +optional
	Variables []string `json:"variables,omitempty"`

	// aliases map a non-standard variable name the device reports (the key) onto
	// the canonical NUT name it should be read as (the value). A canonical name
	// the device reports natively always wins over an alias, and every applied
	// alias is recorded in telemetry diagnostics.
	//
	// A map rather than a list of pairs: a reported name can only ever resolve
	// one way, so making a duplicate key unrepresentable is better than
	// validating against it.
	// +optional
	Aliases map[string]string `json:"aliases,omitempty"`
}

// UPSCapabilityActuationSpec declares UPS control behaviors.
type UPSCapabilityActuationSpec struct {
	// behaviors are authoritative actuation behavior identifiers.
	// +optional
	Behaviors []string `json:"behaviors,omitempty"`
}

// UPSCapabilityProfilePhase summarizes profile contract acceptance.
// +kubebuilder:validation:Enum=Accepted;Error
type UPSCapabilityProfilePhase string

const (
	UPSCapabilityProfilePhaseAccepted UPSCapabilityProfilePhase = "Accepted"
	UPSCapabilityProfilePhaseError    UPSCapabilityProfilePhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// UPSCapabilityProfile is the Schema for the upscapabilityprofiles API
type UPSCapabilityProfile struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of UPSCapabilityProfile
	// +required
	Spec UPSCapabilityProfileSpec `json:"spec"`

	// status defines the observed state of UPSCapabilityProfile
	// +optional
	Status UPSCapabilityProfileStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// UPSCapabilityProfileList contains a list of UPSCapabilityProfile
type UPSCapabilityProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []UPSCapabilityProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &UPSCapabilityProfile{}, &UPSCapabilityProfileList{})
		return nil
	})
}
