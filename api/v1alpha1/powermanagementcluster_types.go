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
