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

package controller

import (
	"strings"
	"testing"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

func TestValidateUPSDeviceRejectsLocalUSBDriver(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "usbhid-ups",
			Endpoint: &powerv1alpha1.UPSEndpointSpec{
				Host: "ups-rack-a.example.net",
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected local USB driver to be rejected")
	}
	if result.reason != "LocalDriverUnsupported" {
		t.Fatalf("expected LocalDriverUnsupported, got %q", result.reason)
	}
}

func TestValidateUPSDeviceAcceptsNetworkDriver(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "snmp-ups",
			Endpoint: &powerv1alpha1.UPSEndpointSpec{
				Host: "ups-rack-a.example.net",
			},
		},
	}

	result := validateUPSDevice(device)
	if !result.accepted {
		t.Fatalf("expected network UPS driver to be accepted, got %s: %s", result.reason, result.message)
	}
}

func TestValidateUPSDeviceRejectsUnknownDriverOutsideNetworkAllowlist(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "experimental-driver",
			Endpoint: &powerv1alpha1.UPSEndpointSpec{
				Host: "ups-rack-a.example.net",
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected unknown driver to be rejected")
	}
	if result.reason != "DriverUnsupported" {
		t.Fatalf("expected DriverUnsupported, got %q", result.reason)
	}
}

func TestValidateUPSDeviceRejectsFirmwareIdentityWithoutModel(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "snmp-ups",
			Endpoint: &powerv1alpha1.UPSEndpointSpec{
				Host: "ups-rack-a.example.net",
			},
			Identity: powerv1alpha1.UPSDeviceIdentitySpec{
				Firmware: "1.0.0",
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected firmware identity without model to be rejected")
	}
	if result.reason != "IdentityFirmwareRequiresModel" {
		t.Fatalf("expected IdentityFirmwareRequiresModel, got %q", result.reason)
	}
}

func TestValidateUPSDeviceAcceptsDummyUPSSimulation(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "dummy-ups",
			Simulation: &powerv1alpha1.UPSDeviceSimulation{
				SequenceConfigMapRef: powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "ups-1-transitions",
				},
			},
		},
	}

	result := validateUPSDevice(device)
	if !result.accepted {
		t.Fatalf("expected dummy-ups simulation to be accepted, got %s: %s", result.reason, result.message)
	}
}

func TestValidateUPSDeviceRejectsSimulationOnNonDummyDriver(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "snmp-ups",
			Endpoint: &powerv1alpha1.UPSEndpointSpec{
				Host: "ups-rack-a.example.net",
			},
			Simulation: &powerv1alpha1.UPSDeviceSimulation{
				SequenceConfigMapRef: powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "ups-1-transitions",
				},
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected simulation on a non-dummy-ups driver to be rejected")
	}
	if result.reason != "SimulationRequiresDummyUPS" {
		t.Fatalf("expected SimulationRequiresDummyUPS, got %q", result.reason)
	}
}

func TestValidateUPSDeviceRejectsSimulationWithExplicitPort(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "dummy-ups",
			DriverOptions: map[string]string{
				"port": "manual.dev",
			},
			Simulation: &powerv1alpha1.UPSDeviceSimulation{
				SequenceConfigMapRef: powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "ups-1-transitions",
				},
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected simulation combined with an explicit driverOptions.port to be rejected")
	}
	if result.reason != "SimulationPortConflict" {
		t.Fatalf("expected SimulationPortConflict, got %q", result.reason)
	}
}

func TestValidateUPSDeviceRejectsSimulationWithUpstreamNUT(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-tower.example.net",
				UPSName: "ups",
			},
			Simulation: &powerv1alpha1.UPSDeviceSimulation{
				SequenceConfigMapRef: powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "ups-1-transitions",
				},
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected simulation combined with upstreamNUT to be rejected")
	}
	if result.reason != "UpstreamNUTSimulationConflict" {
		t.Fatalf("expected UpstreamNUTSimulationConflict, got %q", result.reason)
	}
}

func TestValidateUPSDeviceRejectsSimulationWithoutConfigMapName(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver:     "dummy-ups",
			Simulation: &powerv1alpha1.UPSDeviceSimulation{},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected simulation without a sequenceConfigMapRef name to be rejected")
	}
	if result.reason != "SimulationSequenceConfigMapRequired" {
		t.Fatalf("expected SimulationSequenceConfigMapRequired, got %q", result.reason)
	}
}

func TestValidateUPSDeviceAcceptsUpstreamNUTWithoutDriver(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-tower.example.net",
				UPSName: "ups",
			},
		},
	}

	result := validateUPSDevice(device)
	if !result.accepted {
		t.Fatalf("expected upstream NUT UPSDevice to be accepted, got %s: %s", result.reason, result.message)
	}
}

func TestValidateUPSDeviceRejectsUpstreamNUTDriverConflict(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			Driver: "snmp-ups",
			UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-tower.example.net",
				UPSName: "ups",
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected upstream NUT driver conflict to be rejected")
	}
	if result.reason != "UpstreamNUTDriverConflict" {
		t.Fatalf("expected UpstreamNUTDriverConflict, got %q", result.reason)
	}
}

func TestValidateUPSDeviceRejectsReservedUpstreamNUTDriverOption(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-tower.example.net",
				UPSName: "ups",
			},
			DriverOptions: map[string]string{
				"authconf": "default",
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected reserved upstream NUT driver option to be rejected")
	}
	if result.reason != "UpstreamNUTReservedDriverOption" {
		t.Fatalf("expected UpstreamNUTReservedDriverOption, got %q", result.reason)
	}
}

func TestValidateUPSDeviceRejectsUpstreamNUTSecretAuthWithoutSecret(t *testing.T) {
	device := &powerv1alpha1.UPSDevice{
		Spec: powerv1alpha1.UPSDeviceSpec{
			UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-tower.example.net",
				UPSName: "ups",
				Auth: powerv1alpha1.UPSUpstreamNUTAuthSpec{
					Mode: powerv1alpha1.UPSUpstreamNUTAuthSecret,
				},
			},
		},
	}

	result := validateUPSDevice(device)
	if result.accepted {
		t.Fatal("expected Secret auth without secretKeyRef to be rejected")
	}
	if result.reason != "UpstreamNUTAuthSecretRequired" {
		t.Fatalf("expected UpstreamNUTAuthSecretRequired, got %q", result.reason)
	}
}

func TestValidateShutdownFlowRejectsUnknownGroupDependency(t *testing.T) {
	flow := shutdownFlowWithGroups([]powerv1alpha1.ShutdownGroup{
		{
			Name:   "applications",
			Action: powerv1alpha1.ShutdownStepScaleWorkload,
			Before: []string{"databases"},
		},
	})

	result := validateShutdownFlow(flow)
	if result.accepted {
		t.Fatal("expected unknown dependency to be rejected")
	}
	if result.reason != "UnknownDependency" {
		t.Fatalf("expected UnknownDependency, got %q", result.reason)
	}
}

func TestValidateShutdownFlowRejectsDependencyCycle(t *testing.T) {
	flow := shutdownFlowWithGroups([]powerv1alpha1.ShutdownGroup{
		{
			Name:   "applications",
			Action: powerv1alpha1.ShutdownStepScaleWorkload,
			Before: []string{"databases"},
		},
		{
			Name:   "databases",
			Action: powerv1alpha1.ShutdownStepScaleWorkload,
			Before: []string{"applications"},
		},
	})

	result := validateShutdownFlow(flow)
	if result.accepted {
		t.Fatal("expected dependency cycle to be rejected")
	}
	if result.reason != "DependencyCycle" {
		t.Fatalf("expected DependencyCycle, got %q", result.reason)
	}
}

func TestCompileShutdownGroupsAllowsIndependentConcurrentWave(t *testing.T) {
	appPhase := int32(10)
	flow := shutdownFlowWithGroups([]powerv1alpha1.ShutdownGroup{
		{
			Name:   "frontend-apps",
			Action: powerv1alpha1.ShutdownStepScaleWorkload,
			Phase:  &appPhase,
		},
		{
			Name:   "batch-apps",
			Action: powerv1alpha1.ShutdownStepScaleWorkload,
			Phase:  &appPhase,
		},
		{
			Name:   "databases",
			Action: powerv1alpha1.ShutdownStepScaleWorkload,
			After:  []string{"frontend-apps", "batch-apps"},
		},
	})

	result := validateShutdownFlow(flow)
	if !result.accepted {
		t.Fatalf("expected flow to be accepted, got %s: %s", result.reason, result.message)
	}

	_, waves, _, configHash := compileShutdownFlow(flow)
	if len(waves) != 2 {
		t.Fatalf("expected 2 compiled waves, got %d", len(waves))
	}
	if len(waves[0].Groups) != 2 {
		t.Fatalf("expected first wave to contain 2 independent groups, got %#v", waves[0].Groups)
	}
	if waves[1].Groups[0] != "databases" {
		t.Fatalf("expected databases in second wave, got %#v", waves[1].Groups)
	}
	if configHash == "" {
		t.Fatal("expected compiled flow config hash to be set")
	}
}

func TestValidatePowerInventoryNodeRequiresNodeName(t *testing.T) {
	result := validatePowerInventoryNode(&powerv1alpha1.PowerInventoryNode{})
	if result.accepted {
		t.Fatal("expected inventory node without nodeName to be rejected")
	}
	if result.reason != "NodeNameRequired" {
		t.Fatalf("expected NodeNameRequired, got %q", result.reason)
	}
}

func TestValidatePowerInventoryEdgeRequiresFeedInput(t *testing.T) {
	edge := &powerv1alpha1.PowerInventoryEdge{
		Spec: powerv1alpha1.PowerInventoryEdgeSpec{
			From: powerv1alpha1.PowerInventoryEntityReference{
				Kind: powerv1alpha1.PowerInventoryEntityUPSDevice,
				Name: "ups-a",
			},
			To: powerv1alpha1.PowerInventoryEntityReference{
				Kind: powerv1alpha1.PowerInventoryEntityNode,
				Name: "node-a",
			},
			Relation: powerv1alpha1.PowerInventoryEdgeFeeds,
		},
	}

	result := validatePowerInventoryEdge(edge)
	if result.accepted {
		t.Fatal("expected feeds edge without input to be rejected")
	}
	if result.reason != "FeedInputRequired" {
		t.Fatalf("expected FeedInputRequired, got %q", result.reason)
	}
}

func TestValidateUPSCapabilityProfileRejectsAmbiguousSelector(t *testing.T) {
	profile := &powerv1alpha1.UPSCapabilityProfile{
		Spec: powerv1alpha1.UPSCapabilityProfileSpec{
			Version: "1.0.0",
			Selector: powerv1alpha1.UPSCapabilityProfileSelector{
				Model:     "Vendor Model",
				ModelGlob: "Vendor *",
			},
		},
	}

	result := validateUPSCapabilityProfile(profile)
	if result.accepted {
		t.Fatal("expected ambiguous profile selector to be rejected")
	}
	if result.reason != "AmbiguousProfileSelector" {
		t.Fatalf("expected AmbiguousProfileSelector, got %q", result.reason)
	}
}

func TestValidateUPSCapabilityProfileRejectsMultipleMatchTiers(t *testing.T) {
	profile := &powerv1alpha1.UPSCapabilityProfile{
		Spec: powerv1alpha1.UPSCapabilityProfileSpec{
			Version: "1.0.0",
			Selector: powerv1alpha1.UPSCapabilityProfileSelector{
				Model:        "Vendor Model",
				DriverFamily: "snmp-ups",
			},
		},
	}

	result := validateUPSCapabilityProfile(profile)
	if result.accepted {
		t.Fatal("expected profile with multiple match tiers to be rejected")
	}
	if result.reason != "AmbiguousProfileSelector" {
		t.Fatalf("expected AmbiguousProfileSelector, got %q", result.reason)
	}
}

func TestValidateUPSCapabilityProfileRejectsInvalidVersion(t *testing.T) {
	profile := &powerv1alpha1.UPSCapabilityProfile{
		Spec: powerv1alpha1.UPSCapabilityProfileSpec{
			Version: "latest",
			Selector: powerv1alpha1.UPSCapabilityProfileSelector{
				DriverFamily: "snmp-ups",
			},
		},
	}

	result := validateUPSCapabilityProfile(profile)
	if result.accepted {
		t.Fatal("expected profile with invalid version to be rejected")
	}
	if result.reason != "ProfileVersionInvalid" {
		t.Fatalf("expected ProfileVersionInvalid, got %q", result.reason)
	}
}

func TestValidateUPSCapabilityProfileAcceptsUniversalFloor(t *testing.T) {
	universal := true
	profile := &powerv1alpha1.UPSCapabilityProfile{
		Spec: powerv1alpha1.UPSCapabilityProfileSpec{
			Version: "1.0.0",
			Selector: powerv1alpha1.UPSCapabilityProfileSelector{
				Universal: &universal,
			},
		},
	}

	result := validateUPSCapabilityProfile(profile)
	if !result.accepted {
		t.Fatalf("expected the unidentified-device profile to be accepted, got %s: %s", result.reason, result.message)
	}
}

func TestRejectReservedOperandNamespace(t *testing.T) {
	for _, name := range []string{"default", "kube-system", "kube-public", "kube-node-lease"} {
		if err := rejectReservedOperandNamespace(name); err == nil {
			t.Fatalf("expected %q to be rejected as a reserved operand namespace", name)
		}
	}
	if err := rejectReservedOperandNamespace("power-system"); err != nil {
		t.Fatalf("expected power-system to be accepted, got %v", err)
	}
}

func shutdownFlowWithGroups(groups []powerv1alpha1.ShutdownGroup) *powerv1alpha1.ShutdownFlow {
	return &powerv1alpha1.ShutdownFlow{
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Mode: powerv1alpha1.ShutdownFlowModeDryRun,
			Triggers: []powerv1alpha1.ShutdownTrigger{
				{Type: powerv1alpha1.ShutdownTriggerOnBattery},
			},
			Groups: groups,
		},
	}
}

func TestValidateShutdownFlowDeviceIdentificationBlocksEnforce(t *testing.T) {
	bundle := resolver.StructuralBundle{
		Topology: inventory.Topology{
			Domains: []inventory.PowerDomain{{Name: "core", UPSDevices: []string{"ups-1"}}},
		},
		CapabilityMatches: []capability.MatchResult{
			{DeviceID: "ups-1", ProfileID: "nut-bundled-unidentified-device", Unidentified: true},
		},
	}
	flow := enforceFlowTargeting("core")

	result := validateShutdownFlowDeviceIdentification(flow, bundle)
	if result.accepted {
		t.Fatal("Enforce mode must not be accepted against an unidentified UPS")
	}
	if result.reason != "UnidentifiedUPSDevice" {
		t.Fatalf("reason = %q, want UnidentifiedUPSDevice", result.reason)
	}
	if !strings.Contains(result.message, "ups-1") {
		t.Fatalf("the rejection must name the device, got %q", result.message)
	}
}

func TestValidateShutdownFlowDeviceIdentificationAllowsIdentifiedDevices(t *testing.T) {
	bundle := resolver.StructuralBundle{
		Topology: inventory.Topology{
			Domains: []inventory.PowerDomain{{Name: "core", UPSDevices: []string{"ups-1"}}},
		},
		CapabilityMatches: []capability.MatchResult{
			{DeviceID: "ups-1", ProfileID: "vendor-super-1500"},
		},
	}

	if result := validateShutdownFlowDeviceIdentification(enforceFlowTargeting("core"), bundle); !result.accepted {
		t.Fatalf("a matched profile must not block enforcement: %q", result.message)
	}
}

func TestValidateShutdownFlowDeviceIdentificationIgnoresOutOfScopeDevices(t *testing.T) {
	bundle := resolver.StructuralBundle{
		Topology: inventory.Topology{
			Domains: []inventory.PowerDomain{
				{Name: "core", UPSDevices: []string{"ups-1"}},
				{Name: "lab", UPSDevices: []string{"ups-2"}},
			},
		},
		CapabilityMatches: []capability.MatchResult{
			{DeviceID: "ups-1", ProfileID: "vendor-super-1500"},
			{DeviceID: "ups-2", ProfileID: "nut-bundled-unidentified-device", Unidentified: true},
		},
	}

	if result := validateShutdownFlowDeviceIdentification(enforceFlowTargeting("core"), bundle); !result.accepted {
		t.Fatalf("an unidentified device in an untargeted domain must not block: %q", result.message)
	}
}

func TestValidateShutdownFlowDeviceIdentificationHonorsOptIn(t *testing.T) {
	bundle := resolver.StructuralBundle{
		Topology: inventory.Topology{
			Domains: []inventory.PowerDomain{{Name: "core", UPSDevices: []string{"ups-1"}}},
		},
		CapabilityMatches: []capability.MatchResult{
			{DeviceID: "ups-1", ProfileID: "nut-bundled-unidentified-device", Unidentified: true},
		},
	}
	flow := enforceFlowTargeting("core")
	allow := true
	flow.Spec.Safety.AllowUnidentifiedDevices = &allow

	if result := validateShutdownFlowDeviceIdentification(flow, bundle); !result.accepted {
		t.Fatalf("an explicit opt-in must be honored: %q", result.message)
	}
}

func TestValidateShutdownFlowDeviceIdentificationIgnoresDryRun(t *testing.T) {
	bundle := resolver.StructuralBundle{
		CapabilityMatches: []capability.MatchResult{
			{DeviceID: "ups-1", ProfileID: "nut-bundled-unidentified-device", Unidentified: true},
		},
	}
	flow := enforceFlowTargeting("core")
	flow.Spec.Mode = powerv1alpha1.ShutdownFlowModeDryRun

	if result := validateShutdownFlowDeviceIdentification(flow, bundle); !result.accepted {
		t.Fatalf("dry-run review must stay possible for unidentified hardware: %q", result.message)
	}
}

func enforceFlowTargeting(domain string) *powerv1alpha1.ShutdownFlow {
	return &powerv1alpha1.ShutdownFlow{
		Spec: powerv1alpha1.ShutdownFlowSpec{
			Mode: powerv1alpha1.ShutdownFlowModeEnforce,
			Triggers: []powerv1alpha1.ShutdownTrigger{
				{Type: powerv1alpha1.ShutdownTriggerOnBattery, PowerDomains: []string{domain}},
			},
		},
	}
}
