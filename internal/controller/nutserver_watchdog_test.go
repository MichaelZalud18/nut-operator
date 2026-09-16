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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// The TLS mounts were indexed as Containers[0] while upsd was the only container. Adding sidecars
// made position an unsafe way to name the container that serves the protocol: mounting the
// certificate into a sidecar would leave upsd serving plaintext while the API reports TLS Required,
// which is F-37 and F-39 arriving a third time by a different route.
func TestTLSMaterialMountsOnUpsdAndNotTheSupervisor(t *testing.T) {
	server := tlsEnabledNUTServer()
	server.Spec.TLS.VerifyClientCertificates = ptrBool(true)
	server.Spec.TLS.ClientCARef = &powerv1alpha1.NamespacedNameReference{Name: "client-ca"}

	deployment := &appsv1.Deployment{}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{
		{Name: driverSupervisorContainerName},
		{Name: nutServerUpsdContainerName},
	}

	applyNUTServerTLSOperand(deployment, server, "ghcr.io/example/nut-server:v1", corev1.PullIfNotPresent)

	containers := deployment.Spec.Template.Spec.Containers
	supervisor, upsd := containers[0], containers[1]

	if !hasVolumeMount(upsd.VolumeMounts, nutServerCombinedTLSVolume, nutServerCombinedCertDir) {
		t.Fatalf("upsd must mount the assembled certificate directory, got %+v", upsd.VolumeMounts)
	}
	if len(supervisor.VolumeMounts) != 0 {
		t.Fatalf("the driver supervisor must not receive TLS material, got %+v", supervisor.VolumeMounts)
	}
}
