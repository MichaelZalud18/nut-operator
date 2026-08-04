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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// NodePowerAgentSpec defines the desired state of NodePowerAgent
type NodePowerAgentSpec struct {
	// managementClusterRef references the owning PowerManagementCluster.
	// +optional
	ManagementClusterRef *ObjectNameReference `json:"managementClusterRef,omitempty"`

	// namespace overrides the operand namespace for this agent fleet.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// nutServerRefs names NUTServer resources monitored by this agent fleet.
	// +kubebuilder:validation:MinItems=1
	NUTServerRefs []ObjectNameReference `json:"nutServerRefs"`

	// nodeSelector selects nodes managed by this agent fleet.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// mode controls whether the agent only observes, dry-runs, or actuates host shutdown.
	// +kubebuilder:default=DryRun
	// +optional
	Mode NodePowerAgentMode `json:"mode,omitempty"`

	// shutdownFlowRef references the flow compiled into this agent fleet.
	// +optional
	ShutdownFlowRef *ObjectNameReference `json:"shutdownFlowRef,omitempty"`

	// images overrides the default agent images.
	// +optional
	Images NodePowerAgentImages `json:"images,omitempty"`

	// placement controls DaemonSet scheduling.
	// +optional
	Placement WorkloadPlacementSpec `json:"placement,omitempty"`

	// resources configures agent container requests and limits.
	// +optional
	Resources NodePowerAgentResources `json:"resources,omitempty"`

	// upsmon configures generated upsmon.conf values.
	// +optional
	Upsmon UpsmonConfigSpec `json:"upsmon,omitempty"`

	// shutdown configures the handoff signal and actuator behavior.
	// +optional
	Shutdown AgentShutdownSpec `json:"shutdown,omitempty"`

	// hardening overrides pod hardening defaults.
	// +optional
	Hardening PodHardeningSpec `json:"hardening,omitempty"`
}

// NodePowerAgentStatus defines the observed state of NodePowerAgent.
type NodePowerAgentStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes agent readiness.
	// +optional
	Phase NodePowerAgentPhase `json:"phase,omitempty"`

	// selectedNodes lists nodes selected by this fleet at the last reconcile.
	// +optional
	SelectedNodes []string `json:"selectedNodes,omitempty"`

	// desiredNumberScheduled mirrors the generated DaemonSet status.
	// +optional
	DesiredNumberScheduled int32 `json:"desiredNumberScheduled,omitempty"`

	// numberReady mirrors the generated DaemonSet status.
	// +optional
	NumberReady int32 `json:"numberReady,omitempty"`

	// readyNodeCount records selected nodes with an observed ready agent pod.
	// +optional
	ReadyNodeCount int32 `json:"readyNodeCount,omitempty"`

	// unavailableNodeCount records selected nodes without an observed ready agent pod.
	// +optional
	UnavailableNodeCount int32 `json:"unavailableNodeCount,omitempty"`

	// nodeStatuses records per-node agent pod coverage observed from Kubernetes pod readiness.
	// It is the Kubernetes-native node-agent heartbeat used by shutdown execution safety gates.
	// +listType=map
	// +listMapKey=nodeName
	// +optional
	NodeStatuses []NodePowerAgentNodeStatus `json:"nodeStatuses,omitempty"`

	// configHash identifies the rendered agent configuration.
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// managedResources lists resources managed for this agent fleet.
	// +optional
	ManagedResources []ManagedResourceStatus `json:"managedResources,omitempty"`

	// conditions represent the current state of the NodePowerAgent resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodePowerAgentNodeStatus records controller-observed agent coverage for one selected node.
type NodePowerAgentNodeStatus struct {
	// nodeName is the Kubernetes Node name selected by this NodePowerAgent.
	NodeName string `json:"nodeName"`

	// ready is true when a current, ready NodePowerAgent pod is observed on this node.
	Ready bool `json:"ready"`

	// podName is the observed agent pod on this node, when present.
	// +optional
	PodName string `json:"podName,omitempty"`

	// phase is the observed pod phase, when present.
	// +optional
	Phase corev1.PodPhase `json:"phase,omitempty"`

	// reason is a machine-readable coverage reason such as AgentPodReady or AgentPodMissing.
	// +optional
	Reason string `json:"reason,omitempty"`

	// message explains the observed coverage state for operators and audit records.
	// +optional
	Message string `json:"message,omitempty"`

	// lastHeartbeatTime is the latest pod readiness transition observed by the controller.
	// +optional
	LastHeartbeatTime *metav1.Time `json:"lastHeartbeatTime,omitempty"`
}

// NodePowerAgentMode controls host action authority.
// +kubebuilder:validation:Enum=MonitorOnly;DryRun;Actuate
type NodePowerAgentMode string

const (
	NodePowerAgentModeMonitorOnly NodePowerAgentMode = "MonitorOnly"
	NodePowerAgentModeDryRun      NodePowerAgentMode = "DryRun"
	NodePowerAgentModeActuate     NodePowerAgentMode = "Actuate"
)

// NodePowerAgentImages provides agent image overrides.
type NodePowerAgentImages struct {
	// upsmon is the unprivileged NUT client image.
	// +optional
	Upsmon ImageReference `json:"upsmon,omitempty"`

	// actuator is the host-actuation image. Stub mode runs without host access; real host
	// shutdown is rendered only after explicit actuation approval.
	// +optional
	Actuator ImageReference `json:"actuator,omitempty"`
}

// NodePowerAgentResources configures per-container resources.
type NodePowerAgentResources struct {
	// upsmon configures the NUT client container resources.
	// +optional
	Upsmon corev1.ResourceRequirements `json:"upsmon,omitempty"`

	// actuator configures the actuator container resources.
	// +optional
	Actuator corev1.ResourceRequirements `json:"actuator,omitempty"`
}

// UpsmonConfigSpec configures generated upsmon.conf values.
type UpsmonConfigSpec struct {
	// pollFrequency sets POLLFREQ.
	// +optional
	PollFrequency *metav1.Duration `json:"pollFrequency,omitempty"`

	// alertPollFrequency sets POLLFREQALERT.
	// +optional
	AlertPollFrequency *metav1.Duration `json:"alertPollFrequency,omitempty"`

	// deadTime sets DEADTIME.
	// +optional
	DeadTime *metav1.Duration `json:"deadTime,omitempty"`

	// hostSync sets HOSTSYNC.
	// +optional
	HostSync *metav1.Duration `json:"hostSync,omitempty"`

	// finalDelay sets FINALDELAY.
	// +optional
	FinalDelay *metav1.Duration `json:"finalDelay,omitempty"`
}

// ActuatorPolicy controls the host-action actuator container.
// +kubebuilder:validation:Enum=Disabled;Stub;SystemdPoweroff
type ActuatorPolicy string

const (
	ActuatorPolicyDisabled        ActuatorPolicy = "Disabled"
	ActuatorPolicyStub            ActuatorPolicy = "Stub"
	ActuatorPolicySystemdPoweroff ActuatorPolicy = "SystemdPoweroff"
)

// AgentShutdownSpec configures the local shutdown handoff.
type AgentShutdownSpec struct {
	// actuatorPolicy chooses disabled, stubbed, or real systemd host poweroff.
	// +kubebuilder:default=Stub
	// +optional
	ActuatorPolicy ActuatorPolicy `json:"actuatorPolicy,omitempty"`

	// signalPath is the shared in-pod file path written by upsmon and watched by the actuator.
	// +kubebuilder:default=/run/power-agent/shutdown.json
	// +optional
	SignalPath string `json:"signalPath,omitempty"`

	// signalTTL rejects stale signal files older than this duration.
	// +optional
	SignalTTL *metav1.Duration `json:"signalTTL,omitempty"`

	// requireFreshTelemetry blocks actuation when UPS telemetry is stale.
	// +kubebuilder:default=true
	// +optional
	RequireFreshTelemetry *bool `json:"requireFreshTelemetry,omitempty"`

	// approvalAnnotation must be present on this NodePowerAgent before SystemdPoweroff is rendered.
	// +optional
	ApprovalAnnotation string `json:"approvalAnnotation,omitempty"`
}

// NodePowerAgentPhase summarizes agent fleet readiness.
// +kubebuilder:validation:Enum=Pending;Rendering;Ready;Degraded;Error
type NodePowerAgentPhase string

const (
	NodePowerAgentPhasePending   NodePowerAgentPhase = "Pending"
	NodePowerAgentPhaseRendering NodePowerAgentPhase = "Rendering"
	NodePowerAgentPhaseReady     NodePowerAgentPhase = "Ready"
	NodePowerAgentPhaseDegraded  NodePowerAgentPhase = "Degraded"
	NodePowerAgentPhaseError     NodePowerAgentPhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// NodePowerAgent is the Schema for the nodepoweragents API
type NodePowerAgent struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NodePowerAgent
	// +required
	Spec NodePowerAgentSpec `json:"spec"`

	// status defines the observed state of NodePowerAgent
	// +optional
	Status NodePowerAgentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NodePowerAgentList contains a list of NodePowerAgent
type NodePowerAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NodePowerAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NodePowerAgent{}, &NodePowerAgentList{})
		return nil
	})
}
