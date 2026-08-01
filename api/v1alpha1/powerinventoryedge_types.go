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

// PowerInventoryEdgeSpec defines the desired state of PowerInventoryEdge
type PowerInventoryEdgeSpec struct {
	// from is the source entity for this relation.
	From PowerInventoryEntityReference `json:"from"`

	// to is the target entity for this relation.
	To PowerInventoryEntityReference `json:"to"`

	// relation is the topology relation.
	// +kubebuilder:validation:Enum=Feeds;Carries
	Relation PowerInventoryEdgeRelation `json:"relation"`

	// input identifies the target power input for feeds edges.
	// +optional
	Input string `json:"input,omitempty"`

	// description explains the modeled dependency for operator review.
	// +optional
	Description string `json:"description,omitempty"`
}

// PowerInventoryEdgeStatus defines the observed state of PowerInventoryEdge.
type PowerInventoryEdgeStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes inventory contract acceptance.
	// +optional
	Phase InventoryResourcePhase `json:"phase,omitempty"`

	// conditions represent the current state of the PowerInventoryEdge resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PowerInventoryEntityKind identifies an inventory contract entity kind.
// +kubebuilder:validation:Enum=UPSDevice;Node;PowerInfrastructure
type PowerInventoryEntityKind string

const (
	PowerInventoryEntityUPSDevice           PowerInventoryEntityKind = "UPSDevice"
	PowerInventoryEntityNode                PowerInventoryEntityKind = "Node"
	PowerInventoryEntityPowerInfrastructure PowerInventoryEntityKind = "PowerInfrastructure"
)

// PowerInventoryEntityReference references an inventory entity.
type PowerInventoryEntityReference struct {
	// kind is the referenced inventory entity kind.
	// +kubebuilder:validation:Enum=UPSDevice;Node;PowerInfrastructure
	Kind PowerInventoryEntityKind `json:"kind"`

	// name is the referenced UPSDevice, PowerInfrastructure, or Kubernetes Node name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// PowerInventoryEdgeRelation identifies a structural relation between entities.
// +kubebuilder:validation:Enum=Feeds;Carries
type PowerInventoryEdgeRelation string

const (
	PowerInventoryEdgeFeeds   PowerInventoryEdgeRelation = "Feeds"
	PowerInventoryEdgeCarries PowerInventoryEdgeRelation = "Carries"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// PowerInventoryEdge is the Schema for the powerinventoryedges API
type PowerInventoryEdge struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PowerInventoryEdge
	// +required
	Spec PowerInventoryEdgeSpec `json:"spec"`

	// status defines the observed state of PowerInventoryEdge
	// +optional
	Status PowerInventoryEdgeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PowerInventoryEdgeList contains a list of PowerInventoryEdge
type PowerInventoryEdgeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PowerInventoryEdge `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PowerInventoryEdge{}, &PowerInventoryEdgeList{})
		return nil
	})
}
