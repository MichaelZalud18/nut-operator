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

// NUTServerSpec defines the desired state of NUTServer
type NUTServerSpec struct {
	// managementClusterRef references the owning PowerManagementCluster.
	// +optional
	ManagementClusterRef *ObjectNameReference `json:"managementClusterRef,omitempty"`

	// namespace overrides the operand namespace for this server.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// deviceRefs names UPSDevice resources served by this upsd instance.
	// +optional
	DeviceRefs []ObjectNameReference `json:"deviceRefs,omitempty"`

	// deviceSelector selects UPSDevice resources served by this upsd instance.
	// +optional
	DeviceSelector *metav1.LabelSelector `json:"deviceSelector,omitempty"`

	// replicas is the number of upsd pods for network-backed UPS devices. Pinned to 1: multiple
	// replicas behind one Service split NUT client-login accounting across independent driver
	// instances, which breaks upsd's own tracking of when all clients have disconnected.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// image overrides the default NUT server image.
	// +optional
	Image ImageReference `json:"image,omitempty"`

	// placement controls where the server runs.
	// +optional
	Placement WorkloadPlacementSpec `json:"placement,omitempty"`

	// resources configures NUT server pod requests and limits.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// service configures the upsd Service.
	// +optional
	Service NUTServiceSpec `json:"service,omitempty"`

	// auth configures NUT users and credential management.
	// +optional
	Auth NUTAuthSpec `json:"auth,omitempty"`

	// tls configures NUT protocol TLS.
	// +optional
	TLS NUTTLSSpec `json:"tls,omitempty"`

	// config tunes generated upsd and driver configuration.
	// +optional
	Config NUTServerConfigSpec `json:"config,omitempty"`
}

// NUTServerStatus defines the observed state of NUTServer.
type NUTServerStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes the server lifecycle.
	// +optional
	Phase NUTServerPhase `json:"phase,omitempty"`

	// selectedDevices names UPSDevice resources rendered into this server.
	// +optional
	SelectedDevices []string `json:"selectedDevices,omitempty"`

	// readyReplicas is the number of ready server pods.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// serviceEndpoints reports generated service endpoints.
	// +optional
	ServiceEndpoints []ServiceEndpointStatus `json:"serviceEndpoints,omitempty"`

	// upstreamNUT reports selected upstream NUT devices and their last TCP probe result.
	// +optional
	UpstreamNUT []NUTUpstreamStatus `json:"upstreamNUT,omitempty"`

	// configHash identifies the rendered NUT configuration.
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// managedResources lists resources managed for this server.
	// +optional
	ManagedResources []ManagedResourceStatus `json:"managedResources,omitempty"`

	// conditions represent the current state of the NUTServer resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NUTServiceSpec configures the upsd Service.
type NUTServiceSpec struct {
	// type is the Kubernetes Service type.
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	// +optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// port is the upsd service port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=3493
	// +optional
	Port *int32 `json:"port,omitempty"`

	// annotations are copied to the generated Service.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// NUTUpstreamStatus reports the rendered relay target for one upstream NUT device.
type NUTUpstreamStatus struct {
	// upsDevice is the selected UPSDevice resource name.
	// +optional
	UPSDevice string `json:"upsDevice,omitempty"`

	// nutName is the local NUT name exposed by this NUTServer.
	// +optional
	NUTName string `json:"nutName,omitempty"`

	// host is the upstream upsd host.
	// +optional
	Host string `json:"host,omitempty"`

	// port is the upstream upsd TCP port.
	// +optional
	Port int32 `json:"port,omitempty"`

	// upsName is the upstream UPS name on the upstream upsd server.
	// +optional
	UPSName string `json:"upsName,omitempty"`

	// authMode is the authconf mode rendered for dummy-ups repeater mode.
	// +optional
	AuthMode UPSUpstreamNUTAuthMode `json:"authMode,omitempty"`

	// strictStart reports whether upstream availability is fatal at driver startup.
	// +optional
	StrictStart bool `json:"strictStart,omitempty"`

	// reachable is the result of the most recent TCP reachability probe.
	// +optional
	Reachable *bool `json:"reachable,omitempty"`

	// lastProbeTime is when reachable was last observed.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// message summarizes the most recent upstream probe.
	// +optional
	Message string `json:"message,omitempty"`
}

// NUTAuthMode configures NUT credential rendering.
// +kubebuilder:validation:Enum=OperatorManaged;ExistingSecret
type NUTAuthMode string

const (
	NUTAuthOperatorManaged NUTAuthMode = "OperatorManaged"
	NUTAuthExistingSecret  NUTAuthMode = "ExistingSecret"
)

// NUTAuthSpec configures NUT users and credential management.
type NUTAuthSpec struct {
	// mode selects whether the operator generates NUT users or consumes an existing Secret.
	// +kubebuilder:default=OperatorManaged
	// +optional
	Mode NUTAuthMode `json:"mode,omitempty"`

	// existingSecretRef points at an upsd.users-compatible Secret when mode is ExistingSecret.
	// +optional
	ExistingSecretRef *NamespacedNameReference `json:"existingSecretRef,omitempty"`

	// perNodeUsers requests distinct upsmon secondary credentials for each node.
	// +kubebuilder:default=true
	// +optional
	PerNodeUsers *bool `json:"perNodeUsers,omitempty"`
}

// NUTTLSMode configures NUT protocol TLS.
// +kubebuilder:validation:Enum=Disabled;Opportunistic;Required
type NUTTLSMode string

const (
	NUTTLSDisabled      NUTTLSMode = "Disabled"
	NUTTLSOpportunistic NUTTLSMode = "Opportunistic"
	NUTTLSRequired      NUTTLSMode = "Required"
)

// NUTTLSSpec configures TLS for NUT clients and servers.
type NUTTLSSpec struct {
	// mode selects TLS enforcement.
	// +kubebuilder:default=Required
	// +optional
	Mode NUTTLSMode `json:"mode,omitempty"`

	// serverCertificateRef points at the server certificate Secret. The Secret must be
	// kubernetes.io/tls shaped: data["tls.crt"] and data["tls.key"]. upsd's CERTFILE wants a
	// single PEM holding the chain followed by the key, so the operand assembles that file at
	// startup rather than requiring a pre-combined Secret key.
	// +optional
	ServerCertificateRef *NamespacedNameReference `json:"serverCertificateRef,omitempty"`

	// serverCARef points at the CA bundle that NUT clients use to verify this server's
	// certificate. It is rendered as CERTPATH in upsmon.conf on every NodePowerAgent that
	// monitors this server. Without it upsmon can encrypt but cannot authenticate the server,
	// so CERTVERIFY stays off. The Secret must carry data["ca.crt"].
	// +optional
	ServerCARef *NamespacedNameReference `json:"serverCARef,omitempty"`

	// clientCARef points at the CA Secret upsd uses to validate client certificates. It is
	// rendered as CERTPATH in upsd.conf and only consulted when verifyClientCertificates is
	// true. The Secret must carry data["ca.crt"].
	// +optional
	ClientCARef *NamespacedNameReference `json:"clientCARef,omitempty"`

	// verifyClientCertificates renders CERTREQUEST into upsd.conf, requiring every NUT client to
	// present a certificate signed by clientCARef.
	//
	// This defaults to false because the operator does not issue client certificates to upsmon.
	// Turning it on locks out every NodePowerAgent unless the agent image is NUT 2.8.6 or newer
	// (the first release where upsmon.conf accepts CERTFILE under OpenSSL) and an operator-external
	// mechanism supplies each agent a client certificate. CERTREQUEST is also a no-op on OpenSSL
	// upsd builds older than 2.8.6, so enabling it there silently grants no protection.
	// +kubebuilder:default=false
	// +optional
	VerifyClientCertificates *bool `json:"verifyClientCertificates,omitempty"`

	// disableWeakProtocols renders DISABLE_WEAK_SSL into upsd.conf, raising the minimum
	// negotiated version from TLS 1.0 to TLS 1.2.
	// +kubebuilder:default=true
	// +optional
	DisableWeakProtocols *bool `json:"disableWeakProtocols,omitempty"`
}

// NUTServerConfigSpec tunes generated NUT configuration.
type NUTServerConfigSpec struct {
	// listenAddress is the address rendered into upsd.conf.
	// +kubebuilder:default="0.0.0.0"
	// +optional
	ListenAddress string `json:"listenAddress,omitempty"`

	// maxStartDelay configures maxstartdelay in ups.conf.
	// +optional
	MaxStartDelay *metav1.Duration `json:"maxStartDelay,omitempty"`

	// pollInterval sets the default driver poll interval.
	// +optional
	PollInterval *metav1.Duration `json:"pollInterval,omitempty"`
}

// NUTServerPhase summarizes server readiness.
// +kubebuilder:validation:Enum=Pending;Rendering;Ready;Degraded;Error
type NUTServerPhase string

const (
	NUTServerPhasePending   NUTServerPhase = "Pending"
	NUTServerPhaseRendering NUTServerPhase = "Rendering"
	NUTServerPhaseReady     NUTServerPhase = "Ready"
	NUTServerPhaseDegraded  NUTServerPhase = "Degraded"
	NUTServerPhaseError     NUTServerPhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// NUTServer is the Schema for the nutservers API
type NUTServer struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of NUTServer
	// +required
	Spec NUTServerSpec `json:"spec"`

	// status defines the observed state of NUTServer
	// +optional
	Status NUTServerStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// NUTServerList contains a list of NUTServer
type NUTServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []NUTServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &NUTServer{}, &NUTServerList{})
		return nil
	})
}
