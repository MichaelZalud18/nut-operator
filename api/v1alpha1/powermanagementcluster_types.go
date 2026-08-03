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

// PowerManagementClusterSpec defines the desired state of PowerManagementCluster
type PowerManagementClusterSpec struct {
	// operandNamespace defines where generated Deployments, DaemonSets, Services, ConfigMaps, and Secrets live.
	// +optional
	OperandNamespace *OperandNamespaceSpec `json:"operandNamespace,omitempty"`

	// shutdownTiers configures numbered shutdown-tier policy for planner ordering.
	// +optional
	ShutdownTiers PowerShutdownTierPolicySpec `json:"shutdownTiers,omitempty"`

	// storage configures durable audit, telemetry, and flow execution state.
	// +optional
	Storage PowerStorageSpec `json:"storage,omitempty"`

	// images provides default images for generated operands.
	// +optional
	Images PowerImageSet `json:"images,omitempty"`

	// security sets global security defaults for generated operands.
	// +optional
	Security PowerSecuritySpec `json:"security,omitempty"`

	// observability configures metrics, events, and telemetry export defaults.
	// +optional
	Observability PowerObservabilitySpec `json:"observability,omitempty"`
}

const (
	// DefaultShutdownTierLabelKey is the default label key used to read numeric shutdown tiers.
	DefaultShutdownTierLabelKey = "power.zalud.io/shutdown-tier"
)

// PowerShutdownTierPolicySpec configures central numbered shutdown-tier policy.
type PowerShutdownTierPolicySpec struct {
	// labelKey is the Kubernetes label key whose numeric value declares an object's shutdown tier.
	// +optional
	LabelKey string `json:"labelKey,omitempty"`

	// defaultTier is assigned to targets that match no explicit tier. Tiers 0 and 1 are reserved and cannot be the default.
	// +kubebuilder:validation:Minimum=2
	// +optional
	DefaultTier *int32 `json:"defaultTier,omitempty"`

	// tiers documents the known tier numbers and their operator-facing meanings.
	// +listType=map
	// +listMapKey=tier
	// +optional
	Tiers []PowerShutdownTierDefinition `json:"tiers,omitempty"`

	// selectorRules assign tiers by selector before falling back to defaultTier.
	// +listType=map
	// +listMapKey=name
	// +optional
	SelectorRules []PowerShutdownTierSelectorRule `json:"selectorRules,omitempty"`
}

// PowerShutdownTierDefinition names a shutdown tier.
type PowerShutdownTierDefinition struct {
	// tier is the numeric shutdown tier. Higher tiers stop earlier; lower tiers stop later.
	// +kubebuilder:validation:Minimum=0
	Tier int32 `json:"tier"`

	// name is a stable human-readable tier name.
	// +optional
	Name string `json:"name,omitempty"`

	// description explains the tier's purpose.
	// +optional
	Description string `json:"description,omitempty"`
}

// PowerShutdownTierSubjectKind scopes a selector rule.
// +kubebuilder:validation:Enum=Any;Namespace;Workload;Node
type PowerShutdownTierSubjectKind string

const (
	PowerShutdownTierSubjectAny       PowerShutdownTierSubjectKind = "Any"
	PowerShutdownTierSubjectNamespace PowerShutdownTierSubjectKind = "Namespace"
	PowerShutdownTierSubjectWorkload  PowerShutdownTierSubjectKind = "Workload"
	PowerShutdownTierSubjectNode      PowerShutdownTierSubjectKind = "Node"
)

// PowerShutdownTierSelectorRule assigns a tier to objects matched by labels.
type PowerShutdownTierSelectorRule struct {
	// name is unique within the policy.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// subject limits which target kind this rule applies to. Empty means Any.
	// +optional
	Subject PowerShutdownTierSubjectKind `json:"subject,omitempty"`

	// tier is assigned to matching objects. Tier 0 is workload-only and cannot be assigned to nodes.
	// +kubebuilder:validation:Minimum=0
	Tier int32 `json:"tier"`

	// selector matches object labels.
	Selector metav1.LabelSelector `json:"selector"`
}

// PowerManagementClusterStatus defines the observed state of PowerManagementCluster.
type PowerManagementClusterStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// storage reports durable storage readiness.
	// +optional
	Storage StorageStatus `json:"storage,omitempty"`

	// managedResources lists top-level resources produced for this control plane.
	// +optional
	ManagedResources []ManagedResourceStatus `json:"managedResources,omitempty"`

	// readyServers is the number of NUTServer resources reporting Ready.
	// +optional
	ReadyServers int32 `json:"readyServers,omitempty"`

	// readyAgentFleets is the number of NodePowerAgent resources reporting Ready.
	// +optional
	ReadyAgentFleets int32 `json:"readyAgentFleets,omitempty"`

	// conditions represent the current state of the PowerManagementCluster resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// PowerImageSet provides default operand images.
type PowerImageSet struct {
	// operator is the controller-manager image.
	// +optional
	Operator ImageReference `json:"operator,omitempty"`

	// nutServer is the upsd and driver image.
	// +optional
	NUTServer ImageReference `json:"nutServer,omitempty"`

	// upsmonAgent is the unprivileged NUT client image.
	// +optional
	UpsmonAgent ImageReference `json:"upsmonAgent,omitempty"`

	// actuator is the minimal node-actuation image.
	// +optional
	Actuator ImageReference `json:"actuator,omitempty"`

	// exporter is the metrics exporter image.
	// +optional
	Exporter ImageReference `json:"exporter,omitempty"`
}

// PowerSecurityProfile describes the default operand security posture.
// +kubebuilder:validation:Enum=Restricted;HostActuatorIsolated
type PowerSecurityProfile string

const (
	PowerSecurityRestricted           PowerSecurityProfile = "Restricted"
	PowerSecurityHostActuatorIsolated PowerSecurityProfile = "HostActuatorIsolated"
)

// PowerSecuritySpec sets global security defaults.
type PowerSecuritySpec struct {
	// profile selects the operator's workload hardening baseline.
	// +kubebuilder:default=Restricted
	// +optional
	Profile PowerSecurityProfile `json:"profile,omitempty"`

	// defaultPodHardening applies to generated non-actuator containers.
	// +optional
	DefaultPodHardening PodHardeningSpec `json:"defaultPodHardening,omitempty"`

	// requireExplicitActuation requires each NodePowerAgent to opt into real host shutdown.
	// +kubebuilder:default=true
	// +optional
	RequireExplicitActuation *bool `json:"requireExplicitActuation,omitempty"`

	// allowedActuatorNamespaces restricts where host-actuator DaemonSets may be created.
	// +optional
	AllowedActuatorNamespaces []string `json:"allowedActuatorNamespaces,omitempty"`
}

// PowerObservabilitySpec controls telemetry and metrics defaults.
type PowerObservabilitySpec struct {
	// serviceMonitor enables Prometheus Operator ServiceMonitor rendering.
	// +kubebuilder:default=true
	// +optional
	ServiceMonitor *bool `json:"serviceMonitor,omitempty"`

	// kubernetesEvents enables Kubernetes event emission for power state changes.
	// +kubebuilder:default=true
	// +optional
	KubernetesEvents *bool `json:"kubernetesEvents,omitempty"`

	// telemetryInterval is the default interval for durable UPS telemetry snapshots.
	// +optional
	TelemetryInterval *metav1.Duration `json:"telemetryInterval,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// PowerManagementCluster is the Schema for the powermanagementclusters API
type PowerManagementCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of PowerManagementCluster
	// +required
	Spec PowerManagementClusterSpec `json:"spec"`

	// status defines the observed state of PowerManagementCluster
	// +optional
	Status PowerManagementClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PowerManagementClusterList contains a list of PowerManagementCluster
type PowerManagementClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []PowerManagementCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PowerManagementCluster{}, &PowerManagementClusterList{})
		return nil
	})
}
