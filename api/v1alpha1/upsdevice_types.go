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

// UPSDeviceSpec defines the desired state of UPSDevice
type UPSDeviceSpec struct {
	// displayName is a human-readable name for dashboards and events.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// driver is the Network UPS Tools driver name for a network-capable UPS driver,
	// such as snmp-ups, netxml-ups, powerman-pdu, apcupsd-ups, or dummy-ups for
	// tests. Local USB and serial drivers are intentionally unsupported by this API.
	// +kubebuilder:validation:MinLength=1
	Driver string `json:"driver"`

	// endpoint describes the network endpoint reached by the NUT driver. It is not
	// a USB, serial, or host device path.
	// +optional
	Endpoint *UPSEndpointSpec `json:"endpoint,omitempty"`

	// credentialSecretRef points at driver credentials, such as SNMP community or SNMPv3 auth material.
	// +optional
	CredentialSecretRef *NamespacedNameReference `json:"credentialSecretRef,omitempty"`

	// driverOptions is rendered into ups.conf for this device.
	// +optional
	DriverOptions map[string]string `json:"driverOptions,omitempty"`

	// powerDomains names the physical power domains this UPS supplies.
	// +optional
	PowerDomains []string `json:"powerDomains,omitempty"`

	// thresholds defines policy-relevant UPS thresholds.
	// +optional
	Thresholds UPSThresholdsSpec `json:"thresholds,omitempty"`

	// telemetry controls device polling and stale-data behavior.
	// +optional
	Telemetry UPSTelemetrySpec `json:"telemetry,omitempty"`
}

// UPSDeviceStatus defines the observed state of UPSDevice.
type UPSDeviceStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes the current observed device state.
	// +optional
	Phase UPSDevicePhase `json:"phase,omitempty"`

	// nutName is the rendered NUT device name.
	// +optional
	NUTName string `json:"nutName,omitempty"`

	// serverRefs names NUTServer resources currently serving this device.
	// +optional
	ServerRefs []string `json:"serverRefs,omitempty"`

	// lastPollTime is the most recent successful telemetry poll.
	// +optional
	LastPollTime *metav1.Time `json:"lastPollTime,omitempty"`

	// lastStatus is the last observed NUT ups.status value.
	// +optional
	LastStatus string `json:"lastStatus,omitempty"`

	// batteryChargePercent is the last observed battery.charge value.
	// +optional
	BatteryChargePercent *int32 `json:"batteryChargePercent,omitempty"`

	// runtimeSeconds is the last observed battery.runtime value.
	// +optional
	RuntimeSeconds *int64 `json:"runtimeSeconds,omitempty"`

	// loadPercent is the last observed ups.load value.
	// +optional
	LoadPercent *int32 `json:"loadPercent,omitempty"`

	// conditions represent the current state of the UPSDevice resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// UPSEndpointSpec describes a UPS network endpoint.
type UPSEndpointSpec struct {
	// host is the DNS name or IP address reached by the NUT driver.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// port is the driver-specific network port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port *int32 `json:"port,omitempty"`
}

// UPSThresholdsSpec defines policy-relevant safety thresholds.
type UPSThresholdsSpec struct {
	// minRuntimeSeconds is the minimum remaining runtime before the UPS is considered critical.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinRuntimeSeconds *int64 `json:"minRuntimeSeconds,omitempty"`

	// minChargePercent is the minimum battery charge before the UPS is considered critical.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	MinChargePercent *int32 `json:"minChargePercent,omitempty"`

	// onBatteryHoldoff delays action for short transfer events.
	// +optional
	OnBatteryHoldoff *metav1.Duration `json:"onBatteryHoldoff,omitempty"`

	// staleAfter marks telemetry stale after this duration without a successful poll.
	// +optional
	StaleAfter *metav1.Duration `json:"staleAfter,omitempty"`
}

// UPSTelemetrySpec controls device telemetry polling.
type UPSTelemetrySpec struct {
	// pollInterval controls normal-state telemetry polling.
	// +optional
	PollInterval *metav1.Duration `json:"pollInterval,omitempty"`

	// alertPollInterval controls polling while the UPS is on battery or otherwise degraded.
	// +optional
	AlertPollInterval *metav1.Duration `json:"alertPollInterval,omitempty"`
}

// UPSDevicePhase summarizes observed UPS state.
// +kubebuilder:validation:Enum=Unknown;Online;OnBattery;LowBattery;Stale;Unavailable
type UPSDevicePhase string

const (
	UPSDevicePhaseUnknown     UPSDevicePhase = "Unknown"
	UPSDevicePhaseOnline      UPSDevicePhase = "Online"
	UPSDevicePhaseOnBattery   UPSDevicePhase = "OnBattery"
	UPSDevicePhaseLowBattery  UPSDevicePhase = "LowBattery"
	UPSDevicePhaseStale       UPSDevicePhase = "Stale"
	UPSDevicePhaseUnavailable UPSDevicePhase = "Unavailable"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// UPSDevice is the Schema for the upsdevices API
type UPSDevice struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of UPSDevice
	// +required
	Spec UPSDeviceSpec `json:"spec"`

	// status defines the observed state of UPSDevice
	// +optional
	Status UPSDeviceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// UPSDeviceList contains a list of UPSDevice
type UPSDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []UPSDevice `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &UPSDevice{}, &UPSDeviceList{})
		return nil
	})
}
