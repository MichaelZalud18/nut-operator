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

// PowerInventoryNodeSpec defines the desired state of PowerInventoryNode
type PowerInventoryNodeSpec struct {
	// nodeName is the canonical Kubernetes Node metadata.name this inventory record describes.
	// +kubebuilder:validation:MinLength=1
	NodeName string `json:"nodeName"`

	// powerPlanningExempt excludes this node from the UPS feeds orphan rule.
	// +kubebuilder:default=false
	// +optional
	PowerPlanningExempt *bool `json:"powerPlanningExempt,omitempty"`

	// communicationPathExempt suppresses missing carries-path warnings for this node.
	// +kubebuilder:default=false
	// +optional
	CommunicationPathExempt *bool `json:"communicationPathExempt,omitempty"`

	// roles declares planner-relevant node roles.
	// +optional
	Roles PowerInventoryNodeRoles `json:"roles,omitempty"`
}

// PowerInventoryNodeStatus defines the observed state of PowerInventoryNode.
type PowerInventoryNodeStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes inventory contract acceptance.
	// +optional
	Phase InventoryResourcePhase `json:"phase,omitempty"`

	// conditions represent the current state of the PowerInventoryNode resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PowerInventoryNodeRoles declares node roles consumed by planner rules.
type PowerInventoryNodeRoles struct {
	// controlPlane marks this node as hosting Kubernetes control-plane responsibilities.
	// +kubebuilder:default=false
	// +optional
	ControlPlane *bool `json:"controlPlane,omitempty"`

	// controlPlaneQuorumMember marks this node as counting toward control-plane quorum.
	// +kubebuilder:default=false
	// +optional
	ControlPlaneQuorumMember *bool `json:"controlPlaneQuorumMember,omitempty"`

	// shutdownTier explicitly assigns this node to a numbered shutdown tier. Nodes cannot use tier 0.
	// +kubebuilder:validation:Minimum=1
	// +optional
	ShutdownTier *int32 `json:"shutdownTier,omitempty"`

	// lastDitchRole is an explicit late-shutdown role interpreted by planner policy.
	// When shutdownTier is omitted, any non-empty lastDitchRole maps this node to tier 1.
	// +optional
	LastDitchRole string `json:"lastDitchRole,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// PowerInventoryNode is the Schema for the powerinventorynodes API
type PowerInventoryNode struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PowerInventoryNode
	// +required
	Spec PowerInventoryNodeSpec `json:"spec"`

	// status defines the observed state of PowerInventoryNode
	// +optional
	Status PowerInventoryNodeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PowerInventoryNodeList contains a list of PowerInventoryNode
type PowerInventoryNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PowerInventoryNode `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PowerInventoryNode{}, &PowerInventoryNodeList{})
		return nil
	})
}
