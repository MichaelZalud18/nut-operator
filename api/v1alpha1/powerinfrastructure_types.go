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

// PowerInfrastructureSpec defines the desired state of PowerInfrastructure
type PowerInfrastructureSpec struct {
	// displayName is a human-readable label for dashboards and events.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// class describes the broad infrastructure role for operator review.
	// +kubebuilder:validation:Enum=PDU;Switch;Router;TransferSwitch;PowerPanel;NetworkDevice;Other
	// +optional
	Class PowerInfrastructureClass `json:"class,omitempty"`

	// description explains why this entity is relevant to power planning.
	// +optional
	Description string `json:"description,omitempty"`
}

// PowerInfrastructureStatus defines the observed state of PowerInfrastructure.
type PowerInfrastructureStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes inventory contract acceptance.
	// +optional
	Phase InventoryResourcePhase `json:"phase,omitempty"`

	// conditions represent the current state of the PowerInfrastructure resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PowerInfrastructureClass describes a power or communication path entity.
// +kubebuilder:validation:Enum=PDU;Switch;Router;TransferSwitch;PowerPanel;NetworkDevice;Other
type PowerInfrastructureClass string

const (
	PowerInfrastructureClassPDU            PowerInfrastructureClass = "PDU"
	PowerInfrastructureClassSwitch         PowerInfrastructureClass = "Switch"
	PowerInfrastructureClassRouter         PowerInfrastructureClass = "Router"
	PowerInfrastructureClassTransferSwitch PowerInfrastructureClass = "TransferSwitch"
	PowerInfrastructureClassPowerPanel     PowerInfrastructureClass = "PowerPanel"
	PowerInfrastructureClassNetworkDevice  PowerInfrastructureClass = "NetworkDevice"
	PowerInfrastructureClassOther          PowerInfrastructureClass = "Other"
)

// InventoryResourcePhase summarizes static inventory object acceptance.
// +kubebuilder:validation:Enum=Accepted;Error
type InventoryResourcePhase string

const (
	InventoryResourcePhaseAccepted InventoryResourcePhase = "Accepted"
	InventoryResourcePhaseError    InventoryResourcePhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// PowerInfrastructure is the Schema for the powerinfrastructures API
type PowerInfrastructure struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PowerInfrastructure
	// +required
	Spec PowerInfrastructureSpec `json:"spec"`

	// status defines the observed state of PowerInfrastructure
	// +optional
	Status PowerInfrastructureStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PowerInfrastructureList contains a list of PowerInfrastructure
type PowerInfrastructureList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PowerInfrastructure `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PowerInfrastructure{}, &PowerInfrastructureList{})
		return nil
	})
}
