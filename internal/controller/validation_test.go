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
	"testing"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
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
