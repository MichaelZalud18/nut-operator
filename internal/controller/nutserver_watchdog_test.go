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
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func TestDriverSupervisorScriptHasShellSyntax(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable; the driver supervisor script cannot be syntax-checked here")
	}

	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(driverSupervisorScript())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("driver supervisor script is not valid shell: %v\n%s", err, out)
	}
}

func TestDriverSupervisorUsesPerDeviceForegroundWorkers(t *testing.T) {
	script := driverSupervisorScript()

	if !strings.Contains(script, `upsdrvctl -FF start "$ups"`) {
		t.Fatalf("supervisor must start a foreground worker for each UPS:\n%s", script)
	}
	if strings.Contains(script, "upsdrvctl -FF start\n") {
		t.Fatalf("supervisor must not bundle all drivers into one foreground process:\n%s", script)
	}
	if !strings.Contains(script, "for ups in $configured") {
		t.Fatalf("supervisor must iterate the configured devices:\n%s", script)
	}
}

func TestDriverSupervisorTracksExitedWorkers(t *testing.T) {
	script := driverSupervisorScript()

	for _, expected := range []string{
		"driverExitFile",
		`echo "$rc" > "$exit_file"`,
		"exited with status",
		"reapDriver",
	} {
		if !strings.Contains(script, expected) {
			t.Fatalf("supervisor must track and reap exited driver workers; missing %q in:\n%s", expected, script)
		}
	}
}

func TestDriverSupervisorHandlesAnEmptyUPSConf(t *testing.T) {
	script := driverSupervisorScript()

	if !strings.Contains(script, "upsdrvctl list") {
		t.Fatalf("supervisor must enumerate configured devices through upsdrvctl list:\n%s", script)
	}
	if !strings.Contains(script, "no UPS definitions found") {
		t.Fatalf("supervisor must treat an empty ups.conf as an idle state:\n%s", script)
	}
}

func TestDriverSupervisorKeepsExistingWorkersWhenConfigCannotBeListed(t *testing.T) {
	script := driverSupervisorScript()

	if !strings.Contains(script, "keeping existing workers") {
		t.Fatalf("a temporary invalid ups.conf must not stop currently running workers:\n%s", script)
	}
}

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
