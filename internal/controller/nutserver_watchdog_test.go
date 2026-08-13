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

// The fixtures below are captured verbatim from `upsdrvctl status` on the built operand image
// (NUT 2.8.5), tab-separated as it prints them. They are not hand-written approximations: the
// dead-driver row in particular has a trailing empty S_STATUS field, and the never-started row
// carries a negative PF_PID, both of which are shapes an invented fixture would have missed.
const (
	// upsdrvctl prints this banner to stdout, above the header. It is in the fixtures because
	// leaving it out is what hid a live bug: a selector keyed on the absence of RESPONSIVE read
	// this line as a device named "Network" and tried to start it every tick, while every test
	// passed and the real driver still recovered.
	watchdogStatusBanner = "Network UPS Tools upsdrvctl - UPS driver controller 2.8.5 release\n"

	watchdogStatusHeader = "UPSNAME    \t     UPSDRV\tRUNNING\tPF_PID\tS_RESPONSIVE\tS_PID\tS_STATUS\n"

	watchdogStatusHealthy = "wdups      \t  dummy-ups\tRUNNING\t10\tRESPONSIVE\t10\t\"OL\"\n"

	// After kill -9: the PID file survives, so PF_PID is still populated while RUNNING and S_PID
	// report N/A and the status column is empty.
	watchdogStatusKilled = "wdups      \t  dummy-ups\tN/A\t10\tNOT_RESPONSIVE\tN/A\t\n"

	// Configured in ups.conf but never started at all.
	watchdogStatusNeverStarted = "dummy      \t  dummy-ups\tN/A\t-3\tNOT_RESPONSIVE\tN/A\n"

	watchdogStatusSecondHealthy = "eco650     \t  snmp-ups\tRUNNING\t3559207\tRESPONSIVE\t3559207\t\"OL\"\n"
)

// runWatchdogSelector pipes captured status output through the awk the rendered watchdog runs.
//
// Running it rather than inspecting it is the point. Both failure modes here produce a script that
// reads correctly in a string comparison: matching RESPONSIVE as a substring finds every dead
// driver healthy so the watchdog never fires, and matching on absence turns the version banner
// into a device name so it fires every tick at nothing.
func runWatchdogSelector(t *testing.T, status string) []string {
	t.Helper()

	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable; the watchdog selector cannot be exercised here")
	}
	cmd := exec.Command("sh", "-c", driverWatchdogNonResponsiveSelector())
	cmd.Stdin = strings.NewReader(status)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run watchdog selector: %v", err)
	}
	return strings.Fields(string(out))
}

func TestWatchdogSelectorIgnoresHealthyDrivers(t *testing.T) {
	selected := runWatchdogSelector(t, watchdogStatusBanner+watchdogStatusHeader+watchdogStatusHealthy)

	if len(selected) != 0 {
		t.Fatalf("a responsive driver must never be restarted, got %v", selected)
	}
}

func TestWatchdogSelectorFindsDeadAndUnstartedDrivers(t *testing.T) {
	for name, status := range map[string]string{
		"killed":        watchdogStatusBanner + watchdogStatusHeader + watchdogStatusKilled,
		"never-started": watchdogStatusBanner + watchdogStatusHeader + watchdogStatusNeverStarted,
	} {
		t.Run(name, func(t *testing.T) {
			selected := runWatchdogSelector(t, status)
			if len(selected) != 1 {
				t.Fatalf("expected exactly one non-responsive device, got %v", selected)
			}
		})
	}
}

// The mixed case is the one that matters operationally: a server with several devices loses one
// driver, upsd stays up, and readiness stays green because another driver still answers. Nothing
// else in the system notices, which is the whole of F-49.
func TestWatchdogSelectorPicksOnlyTheDeadDriverFromAMixedSet(t *testing.T) {
	selected := runWatchdogSelector(t,
		watchdogStatusBanner+watchdogStatusHeader+watchdogStatusSecondHealthy+watchdogStatusKilled)

	if len(selected) != 1 || selected[0] != "wdups" {
		t.Fatalf("expected only the dead driver wdups to be selected, got %v", selected)
	}
}

// Neither of the two non-device lines may ever be selected. The header's own token is
// S_RESPONSIVE, and the banner's first field is "Network" -- which is a plausible-looking device
// name, so the resulting `upsdrvctl start Network` fails quietly and forever.
//
// This is the regression test for a bug the canned fixtures could not have caught, because the
// original fixtures started at the header. It took running the watchdog against the operand image
// to see it, and it is here so the next person does not have to.
func TestWatchdogSelectorNeverSelectsNonDeviceLines(t *testing.T) {
	selected := runWatchdogSelector(t, watchdogStatusBanner+watchdogStatusHeader+watchdogStatusKilled)

	for _, name := range selected {
		if name == "UPSNAME" {
			t.Error("the watchdog would try to restart a device named UPSNAME")
		}
		if name == "Network" {
			t.Error("the watchdog would try to restart the upsdrvctl version banner as a device")
		}
	}
	if len(selected) != 1 || selected[0] != "wdups" {
		t.Fatalf("expected only the dead device, got %v", selected)
	}
}

// The banner alone, with no devices configured at all, must select nothing.
func TestWatchdogSelectorIgnoresBannerOnlyOutput(t *testing.T) {
	if selected := runWatchdogSelector(t, watchdogStatusBanner+watchdogStatusHeader); len(selected) != 0 {
		t.Fatalf("output with no device rows must select nothing, got %v", selected)
	}
}

func TestWatchdogSelectorHandlesEmptyOutput(t *testing.T) {
	if selected := runWatchdogSelector(t, ""); len(selected) != 0 {
		t.Fatalf("empty status output must select nothing, got %v", selected)
	}
}

// upsdrvctl start is what recovers a dead driver -- verified against the image, where it detects
// the stale PID file, terminates the phantom, and starts a fresh driver on its own. A stop-then-
// start pair would introduce a window where the driver is deliberately down, so the script must
// not grow one.
func TestWatchdogRestartsWithoutStoppingFirst(t *testing.T) {
	script := driverWatchdogScript()

	if !strings.Contains(script, `upsdrvctl start "$ups"`) {
		t.Fatalf("watchdog must recover a driver with upsdrvctl start:\n%s", script)
	}
	if strings.Contains(script, "upsdrvctl stop") {
		t.Fatalf("watchdog must not stop a driver before restarting it:\n%s", script)
	}
}

// upsdrvctl start against a *healthy* driver terminates and replaces it (verified against the
// image). A single transient probe miss must therefore not be enough to act on, or the watchdog
// spends its life restarting working drivers.
func TestWatchdogConfirmsBeforeRestarting(t *testing.T) {
	script := driverWatchdogScript()

	if strings.Count(script, "notResponsive") < 3 {
		t.Fatalf("watchdog must re-check responsiveness before restarting a driver:\n%s", script)
	}
}

// The TLS mounts were indexed as Containers[0] while upsd was the only container. Adding the
// watchdog made position an unsafe way to name the container that serves the protocol: mounting
// the certificate into the sidecar would leave upsd serving plaintext while the API reports TLS
// Required, which is F-37 and F-39 arriving a third time by a different route.
func TestTLSMaterialMountsOnUpsdAndNotTheWatchdog(t *testing.T) {
	server := tlsEnabledNUTServer()
	server.Spec.TLS.VerifyClientCertificates = ptrBool(true)
	server.Spec.TLS.ClientCARef = &powerv1alpha1.NamespacedNameReference{Name: "client-ca"}

	deployment := &appsv1.Deployment{}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{
		{Name: driverWatchdogContainerName},
		{Name: nutServerUpsdContainerName},
	}

	applyNUTServerTLSOperand(deployment, server, "ghcr.io/example/nut-server:v1", corev1.PullIfNotPresent)

	containers := deployment.Spec.Template.Spec.Containers
	watchdog, upsd := containers[0], containers[1]

	if !hasVolumeMount(upsd.VolumeMounts, nutServerCombinedTLSVolume, nutServerCombinedCertDir) {
		t.Fatalf("upsd must mount the assembled certificate directory, got %+v", upsd.VolumeMounts)
	}
	if len(watchdog.VolumeMounts) != 0 {
		t.Fatalf("the watchdog must not receive TLS material, got %+v", watchdog.VolumeMounts)
	}
}
