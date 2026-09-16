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
	"context"
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func dummyUPSDefinitionFileName(device powerv1alpha1.UPSDevice) (string, bool, error) {
	if device.Spec.Driver != "dummy-ups" {
		return "", false, nil
	}
	if device.Spec.UpstreamNUT != nil {
		return "", false, nil
	}
	if explicitPort := device.Spec.DriverOptions["port"]; explicitPort != "" {
		return "", false, nil
	}
	if hasSimulationSequence(device) {
		return "", false, nil
	}
	filename := nutDeviceName(device) + ".dev"
	if err := validateNUTConfigToken(filename); err != nil {
		return "", false, fmt.Errorf("invalid dummy UPS definition filename for UPSDevice %q: %w", device.Name, err)
	}
	return filename, true, nil
}

// hasSimulationSequence reports whether a UPSDevice requests dummy-ups scripted
// state-transition simulation via a referenced .seq fixture ConfigMap.
func hasSimulationSequence(device powerv1alpha1.UPSDevice) bool {
	return device.Spec.Simulation != nil && device.Spec.Simulation.SequenceConfigMapRef.Name != ""
}

// dummyUPSSimulationFileName names the dummy-ups `.seq` scripted-transition fixture file for a
// device requesting spec.simulation, mirroring dummyUPSDefinitionFileName's static `.dev` case.
// The `.seq` extension alone selects NUT's dummy-loop driver mode; renderUPSConf also writes an
// explicit `mode = dummy-loop` driver option for clarity.
func dummyUPSSimulationFileName(device powerv1alpha1.UPSDevice) (string, bool, error) {
	if device.Spec.Driver != "dummy-ups" {
		return "", false, nil
	}
	if device.Spec.UpstreamNUT != nil {
		return "", false, nil
	}
	if explicitPort := device.Spec.DriverOptions["port"]; explicitPort != "" {
		return "", false, nil
	}
	if !hasSimulationSequence(device) {
		return "", false, nil
	}
	filename := nutDeviceName(device) + ".seq"
	if err := validateNUTConfigToken(filename); err != nil {
		return "", false, fmt.Errorf("invalid dummy UPS simulation filename for UPSDevice %q: %w", device.Name, err)
	}
	return filename, true, nil
}

// resolveUPSDeviceSimulationFixtures fetches spec.simulation.sequenceConfigMapRef for each
// selected device so renderNUTServerConfig can render a dummy-ups `.seq` scripted-transition
// fixture instead of the static `.dev` file, letting tests drive real OnBattery/LowBattery
// transitions without hand-patching cluster state. Same-namespace-only, matching
// resolveUPSDeviceCredentials.
func resolveUPSDeviceSimulationFixtures(ctx context.Context, c client.Client, namespace string, devices []powerv1alpha1.UPSDevice) (map[string]string, error) {
	fixtures := make(map[string]string)
	for _, device := range devices {
		if !hasSimulationSequence(device) {
			continue
		}
		ref := device.Spec.Simulation.SequenceConfigMapRef
		if ref.Namespace != namespace {
			return nil, fmt.Errorf("UPSDevice %q simulation.sequenceConfigMapRef must be in operand namespace %q", device.Name, namespace)
		}
		key := device.Spec.Simulation.SequenceKey
		if key == "" {
			key = "sequence.seq"
		}
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &cm); err != nil {
			return nil, fmt.Errorf("get simulation.sequenceConfigMapRef ConfigMap for UPSDevice %q: %w", device.Name, err)
		}
		content, ok := cm.Data[key]
		if !ok {
			return nil, fmt.Errorf("UPSDevice %q simulation.sequenceConfigMapRef ConfigMap %q missing key %q", device.Name, ref.Name, key)
		}
		fixtures[device.Name] = content
	}
	return fixtures, nil
}

func renderDummyUPSDefinition(device powerv1alpha1.UPSDevice) (string, error) {
	name := nutDeviceName(device)
	displayName := device.Spec.DisplayName
	if displayName == "" {
		displayName = name
	}
	if err := validateNUTConfigValue(displayName); err != nil {
		return "", fmt.Errorf("invalid dummy UPS display name for UPSDevice %q: %w", device.Name, err)
	}
	return fmt.Sprintf(`device.mfr: nut-operator
device.model: %s
device.serial: %s
ups.mfr: nut-operator
ups.model: %s
ups.status: OL
battery.charge: 100
battery.runtime: 3600
ups.load: 10
`, displayName, name, displayName), nil
}
