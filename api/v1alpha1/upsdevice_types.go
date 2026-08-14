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
	// displayName is a human-readable name for events and published artifacts.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// identity declares stable capability matching keys when they are known from
	// inventory, a trusted probe, or a vendor-reported NUT model string.
	// +optional
	Identity UPSDeviceIdentitySpec `json:"identity,omitempty"`

	// driver is the Network UPS Tools driver name for a network-capable UPS driver:
	// snmp-ups, netxml-ups, apcupsd-ups, or dummy-ups for tests. It is required
	// unless upstreamNUT is set. Local USB and serial drivers are intentionally
	// unsupported by this API.
	// +optional
	Driver string `json:"driver,omitempty"`

	// endpoint describes the network endpoint reached by the NUT driver. It is not
	// a USB, serial, or host device path. Use upstreamNUT instead for devices that
	// already expose their own NUT data server.
	// +optional
	Endpoint *UPSEndpointSpec `json:"endpoint,omitempty"`

	// upstreamNUT describes a UPS that already exposes a network NUT server. The
	// generated NUTServer relays it through dummy-ups repeater mode.
	// +optional
	UpstreamNUT *UPSUpstreamNUTSpec `json:"upstreamNUT,omitempty"`

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

	// simulation configures scripted state-transition fixture data for the dummy-ups driver, used
	// to exercise OnBattery/LowBattery transitions in tests without real hardware. Only valid when
	// driver is dummy-ups and upstreamNUT is not set.
	// +optional
	Simulation *UPSDeviceSimulation `json:"simulation,omitempty"`
}

// UPSDeviceSimulation configures dummy-ups scripted state-transition simulation (NUT's
// dummy-loop driver mode). Fixture content is declarative and Git-reviewable, sourced from a
// ConfigMap rather than authored directly on the UPSDevice, matching credentialSecretRef's
// reference-not-inline convention.
type UPSDeviceSimulation struct {
	// sequenceConfigMapRef points at a ConfigMap holding a dummy-ups `.seq` scripted
	// transition fixture (NUT's TIMER-directive format: blocks of `variable: value` lines,
	// optionally preceded by a `TIMER <seconds>` line, separated by blank lines, looping once the
	// sequence ends). Must be in the rendered NUTServer operand namespace, matching
	// credentialSecretRef's convention.
	// +kubebuilder:validation:Required
	SequenceConfigMapRef NamespacedNameReference `json:"sequenceConfigMapRef"`

	// sequenceKey is the ConfigMap data key holding the .seq fixture content.
	// +kubebuilder:default=sequence.seq
	// +optional
	SequenceKey string `json:"sequenceKey,omitempty"`
}

// UPSDeviceIdentitySpec declares capability matching keys for one UPS.
type UPSDeviceIdentitySpec struct {
	// model is the provider-reported NUT ups.model value or trusted inventory equivalent.
	// +optional
	Model string `json:"model,omitempty"`

	// firmware narrows matching to a specific provider-reported firmware string.
	// +optional
	Firmware string `json:"firmware,omitempty"`
}

// UPSDeviceCapabilityStatus reports which capability profile this device resolved to.
//
// The resolution happens on every reconcile and used to be invisible: the controller took the
// profile's alias map and discarded the rest, so nothing on the cluster could answer which profile
// a device matched, whether it fell back to the universal floor, or which quirks were in force
// after firmware scoping. A device silently matching nothing behaves like one matching a product
// profile until a trigger needs a variable the floor does not declare.
//
// Every field here is read from the match or computed from current state, per EX-28 -- no judgement
// about whether the resolution is good, which is the reader's to make from the tier.
type UPSDeviceCapabilityStatus struct {
	// profileID is the capability profile this device currently resolves to.
	// +optional
	ProfileID string `json:"profileID,omitempty"`

	// profileVersion is that profile's semantic version.
	// +optional
	ProfileVersion string `json:"profileVersion,omitempty"`

	// profileSource records whether the profile came from the bundled catalog or a CRD.
	// +optional
	ProfileSource string `json:"profileSource,omitempty"`

	// profileHash identifies the exact profile content the match was computed against, so a
	// resolution can be tied to a specific catalog revision.
	// +optional
	ProfileHash string `json:"profileHash,omitempty"`

	// tier is the precedence tier that produced the match, from an exact model and firmware match
	// down to the unidentified floor.
	// +optional
	Tier string `json:"tier,omitempty"`

	// unidentified is true when no profile matched this device and the universal floor was used.
	// +optional
	Unidentified bool `json:"unidentified,omitempty"`

	// quirks names the profile quirks in force for this device after firmware scoping.
	// +optional
	// +listType=atomic
	Quirks []string `json:"quirks,omitempty"`

	// actuationBehaviors names the actuation behaviors the matched profile declares as verified for
	// this device. Empty is the normal and deliberate state: profiles declare nothing until the
	// behavior has been verified against real firmware (F-27), so an empty list means "not
	// verified", never "not capable".
	// +optional
	// +listType=atomic
	ActuationBehaviors []string `json:"actuationBehaviors,omitempty"`

	// reason carries the machine-readable reason the resolution is not a clean product match, or
	// the reason it could not be computed at all. Empty on a clean match.
	// +optional
	Reason string `json:"reason,omitempty"`

	// message explains reason in one sentence.
	// +optional
	Message string `json:"message,omitempty"`
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

	// capability reports the capability profile this device resolves to.
	// +optional
	Capability *UPSDeviceCapabilityStatus `json:"capability,omitempty"`

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

// UPSUpstreamNUTSpec describes a remotely served NUT UPS to relay.
type UPSUpstreamNUTSpec struct {
	// host is the DNS name or IP address of the upstream upsd server.
	// +kubebuilder:validation:MinLength=1
	Host string `json:"host"`

	// port is the upstream upsd TCP port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=3493
	// +optional
	Port *int32 `json:"port,omitempty"`

	// upsName is the upstream UPS name served by the upstream upsd server.
	// +kubebuilder:validation:MinLength=1
	UPSName string `json:"upsName"`

	// auth configures how dummy-ups repeater mode discovers authentication and
	// trust material for the upstream NUT server.
	// +optional
	Auth UPSUpstreamNUTAuthSpec `json:"auth,omitempty"`

	// strictStart preserves dummy-ups repeater mode's strict startup behavior.
	// When false, startup continues even if the upstream server is unavailable.
	// +kubebuilder:default=true
	// +optional
	StrictStart *bool `json:"strictStart,omitempty"`
}

// UPSUpstreamNUTAuthMode selects how upstream NUT auth material is supplied.
// +kubebuilder:validation:Enum=None;Default;Secret
type UPSUpstreamNUTAuthMode string

const (
	// UPSUpstreamNUTAuthNone disables upstream authconf parsing.
	UPSUpstreamNUTAuthNone UPSUpstreamNUTAuthMode = "None"

	// UPSUpstreamNUTAuthDefault asks NUT to use its default authconf discovery.
	UPSUpstreamNUTAuthDefault UPSUpstreamNUTAuthMode = "Default"

	// UPSUpstreamNUTAuthSecret mounts an authconf file from a Kubernetes Secret.
	UPSUpstreamNUTAuthSecret UPSUpstreamNUTAuthMode = "Secret"
)

// UPSUpstreamNUTAuthSpec configures upstream NUT authentication material.
type UPSUpstreamNUTAuthSpec struct {
	// mode selects the upstream authconf source. The default disables authconf
	// parsing so unauthenticated appliances do not depend on image defaults.
	// +kubebuilder:default=None
	// +optional
	Mode UPSUpstreamNUTAuthMode `json:"mode,omitempty"`

	// secretKeyRef points at a Secret key containing a complete nutauth.conf file.
	// The Secret must be in the rendered NUTServer operand namespace.
	// +optional
	SecretKeyRef *SecretKeyReference `json:"secretKeyRef,omitempty"`
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
