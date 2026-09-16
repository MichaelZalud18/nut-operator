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
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/kubeinventory"
	corev1 "k8s.io/api/core/v1"
)

func renderedUPSDriver(device powerv1alpha1.UPSDevice) string {
	if device.Spec.UpstreamNUT != nil {
		return "dummy-ups"
	}
	return device.Spec.Driver
}

func upstreamNUTTarget(device powerv1alpha1.UPSDevice) string {
	upstream := device.Spec.UpstreamNUT
	target := fmt.Sprintf("%s@%s", upstream.UPSName, upstream.Host)
	port := upstreamNUTPort(device)
	if port != 3493 || upstream.Port != nil {
		target = fmt.Sprintf("%s:%d", target, port)
	}
	return target
}

func upstreamNUTPort(device powerv1alpha1.UPSDevice) int32 {
	if device.Spec.UpstreamNUT != nil && device.Spec.UpstreamNUT.Port != nil {
		return *device.Spec.UpstreamNUT.Port
	}
	return 3493
}

func upstreamNUTStrictStart(device powerv1alpha1.UPSDevice) bool {
	if device.Spec.UpstreamNUT == nil || device.Spec.UpstreamNUT.StrictStart == nil {
		return true
	}
	return *device.Spec.UpstreamNUT.StrictStart
}

func upstreamNUTAuthConf(device powerv1alpha1.UPSDevice) string {
	switch kubeinventory.UpstreamAuthMode(device.Spec.UpstreamNUT) {
	case powerv1alpha1.UPSUpstreamNUTAuthDefault:
		return "default"
	case powerv1alpha1.UPSUpstreamNUTAuthSecret:
		return "/etc/nut/upstream-auth/" + nutDeviceName(device) + ".nutauth.conf"
	default:
		return "none"
	}
}

func upstreamNUTAuthProjections(devices []powerv1alpha1.UPSDevice, namespace string) ([]corev1.VolumeProjection, error) {
	projections := make([]corev1.VolumeProjection, 0)
	for _, device := range devices {
		if device.Spec.UpstreamNUT == nil ||
			kubeinventory.UpstreamAuthMode(device.Spec.UpstreamNUT) != powerv1alpha1.UPSUpstreamNUTAuthSecret {
			continue
		}
		ref := device.Spec.UpstreamNUT.Auth.SecretKeyRef
		if ref == nil {
			return nil, fmt.Errorf("UPSDevice %q upstream NUT auth mode Secret requires secretKeyRef", device.Name)
		}
		if ref.Namespace != namespace {
			return nil, fmt.Errorf("UPSDevice %q upstream NUT auth Secret must be in operand namespace %q", device.Name, namespace)
		}
		projections = append(projections, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Items: []corev1.KeyToPath{
					{Key: ref.Key, Path: "upstream-auth/" + nutDeviceName(device) + ".nutauth.conf"},
				},
			},
		})
	}
	return projections, nil
}
