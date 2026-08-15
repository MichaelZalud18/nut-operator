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

	// inventory configures how declarative inventory snapshots are treated.
	// +optional
	Inventory PowerInventorySpec `json:"inventory,omitempty"`

	// storage configures durable audit, telemetry, and flow execution state.
	// +optional
	Storage PowerStorageSpec `json:"storage,omitempty"`

	// images provides default images for generated operands.
	// +optional
	Images PowerImageSet `json:"images,omitempty"`

	// security sets global security defaults for generated operands.
	// +optional
	Security PowerSecuritySpec `json:"security,omitempty"`

	// hooks configures global policy for ShutdownHook delivery.
	// +optional
	Hooks PowerHookPolicySpec `json:"hooks,omitempty"`

	// observability configures metrics, events, and telemetry export defaults.
	// +optional
	Observability PowerObservabilitySpec `json:"observability,omitempty"`
}

const (
	// DefaultShutdownTierLabelKey is the default label key used to read numeric shutdown tiers.
	DefaultShutdownTierLabelKey = "power.zalud.io/shutdown-tier"
)

// PowerInventorySpec configures treatment of declarative inventory snapshots.
type PowerInventorySpec struct {
	// snapshotAgeLevels raise the reported severity as an inventory snapshot ages
	// (IN-16). Age never blocks planning: a provider outage must degrade rather
	// than stop a shutdown (IN-15), so the worst an old snapshot can do is say so
	// loudly. The levels exist because "last good snapshot" must not be able to
	// quietly mean "eight months ago".
	//
	// Thresholds are evaluated highest-first, so the oldest matching entry wins.
	// An empty list applies the defaults: Info at one hour, Warning at six.
	// +listType=map
	// +listMapKey=level
	// +optional
	SnapshotAgeLevels []PowerSnapshotAgeLevel `json:"snapshotAgeLevels,omitempty"`
}

// PowerSnapshotAgeLevel raises one severity once a snapshot reaches an age.
type PowerSnapshotAgeLevel struct {
	// level is the severity reported once a snapshot is older than after. Info is
	// recorded and published without changing conditions; Warning additionally
	// degrades flows compiled from the snapshot.
	Level PowerNotificationLevel `json:"level"`

	// after is the snapshot age at which this level begins to apply.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h))+$`
	After metav1.Duration `json:"after"`
}

// PowerNotificationLevel is the severity attached to an advisory condition.
//
// There is no rejecting level on purpose. Escalation here reports harder; it
// never stops a shutdown from planning.
// +kubebuilder:validation:Enum=Info;Warning
type PowerNotificationLevel string

const (
	// PowerNotificationInfo is recorded and published but changes no condition.
	PowerNotificationInfo PowerNotificationLevel = "Info"
	// PowerNotificationWarning degrades the resources that consumed the snapshot.
	PowerNotificationWarning PowerNotificationLevel = "Warning"
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

// PowerHookPolicySpec configures global ShutdownHook delivery policy.
type PowerHookPolicySpec struct {
	// defaultTimeout is used when a hook invocation does not declare its own timeout.
	// +kubebuilder:default="10s"
	// +optional
	DefaultTimeout *metav1.Duration `json:"defaultTimeout,omitempty"`

	// allowedEndpoints is the HTTP endpoint allowlist for ShutdownHook invocation.
	// HTTP hooks that do not match an entry are refused before delivery.
	// +optional
	AllowedEndpoints []PowerHookEndpointAllowlistEntry `json:"allowedEndpoints,omitempty"`
}

// PowerHookEndpointAllowlistEntry allows one HTTP hook endpoint shape.
type PowerHookEndpointAllowlistEntry struct {
	// scheme is the allowed URL scheme.
	// +kubebuilder:validation:Enum=http;https
	Scheme string `json:"scheme"`

	// host is the exact DNS name or IP literal allowed for hook delivery.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// port is the allowed TCP port. Empty means the scheme default port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port *int32 `json:"port,omitempty"`

	// pathPrefix limits delivery to paths under this prefix. Empty allows any path.
	// +optional
	PathPrefix string `json:"pathPrefix,omitempty"`
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

	// publishCadence bounds how long the operator may publish nothing before republishing
	// unchanged state, so a subscriber can tell "nothing is happening" from "the publisher died"
	// (EX-29). Both look like silence otherwise.
	//
	// Cluster-wide rather than per-flow (OD-30): the cadence describes the publisher's liveness,
	// and there is one publisher. A subscriber watching several flows needs one interval to
	// reason about, and letting each flow set its own would make the reconcile rate a workload
	// author's decision. Per-flow variation that is actually justified already exists as the
	// idle/active split below, which follows what a flow is doing rather than what it declared.
	// +optional
	PublishCadence *PublishCadenceSpec `json:"publishCadence,omitempty"`
}

// PublishCadenceSpec sets the EX-29 heartbeat intervals.
type PublishCadenceSpec struct {
	// idle is the republish interval when no flow is mid-outage. Defaults to 60s.
	// +optional
	Idle *metav1.Duration `json:"idle,omitempty"`

	// active is the republish interval while a flow is mid-outage — descending, or holding a
	// matched trigger. Defaults to 10s. Must be faster than idle: the whole point of the split is
	// that a subscriber tracking progress needs fresher state than one confirming liveness.
	// +optional
	Active *metav1.Duration `json:"active,omitempty"`
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
