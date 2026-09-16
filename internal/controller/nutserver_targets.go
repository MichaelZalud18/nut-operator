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
	"sort"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *NUTServerReconciler) selectUPSDevices(ctx context.Context, server *powerv1alpha1.NUTServer) ([]powerv1alpha1.UPSDevice, error) {
	return selectUPSDevices(ctx, r.Client, server)
}

func selectUPSDevices(ctx context.Context, c client.Client, server *powerv1alpha1.NUTServer) ([]powerv1alpha1.UPSDevice, error) {
	byName := map[string]powerv1alpha1.UPSDevice{}

	for _, ref := range server.Spec.DeviceRefs {
		var device powerv1alpha1.UPSDevice
		if err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, &device); err != nil {
			return nil, fmt.Errorf("get UPSDevice %q: %w", ref.Name, err)
		}
		byName[device.Name] = device
	}

	if server.Spec.DeviceSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(server.Spec.DeviceSelector)
		if err != nil {
			return nil, fmt.Errorf("parse deviceSelector: %w", err)
		}
		if selector != labels.Nothing() {
			var list powerv1alpha1.UPSDeviceList
			if err := c.List(ctx, &list); err != nil {
				return nil, fmt.Errorf("list UPSDevices: %w", err)
			}
			for _, device := range list.Items {
				if selector.Matches(labels.Set(device.Labels)) {
					byName[device.Name] = device
				}
			}
		}
	}

	devices := make([]powerv1alpha1.UPSDevice, 0, len(byName))
	for _, device := range byName {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].Name < devices[j].Name
	})
	return devices, nil
}
