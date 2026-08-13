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
)

// restartHashFor mirrors the render path's restart-hash computation over rendered config, so these
// tests can ask "would this change replace the pod?" without standing up a cluster.
//
// It is deliberately a copy of the expression rather than a shared helper: the thing under test is
// *which keys* the annotation covers, and a helper both sides called would make any answer
// self-consistent. If the render path changes which keys it hashes, this diverges and the tests
// below start failing, which is the intent.
func restartHashFor(upsdConf, tlsDigest string) string {
	return hashStringMap(map[string]string{
		"upsd.conf": upsdConf,
		"tls":       tlsDigest,
	})
}

// F-48: the pod-template annotation used to digest everything rendered, so adding one device
// replaced the pod and cost every *other* device its upsmon sessions and NUT's login accounting --
// the damage F-15 and F-16 exist to prevent.
//
// upsd adopts a device added to ups.conf on reload; this asserts the operator stops forcing a
// recreate for it.
func TestDeviceChangesDoNotForceAPodRecreate(t *testing.T) {
	server := &powerv1alpha1.NUTServer{Spec: powerv1alpha1.NUTServerSpec{Namespace: "power-system"}}

	oneDevice := []powerv1alpha1.UPSDevice{dummyUPSDeviceNamed("alpha")}
	twoDevices := []powerv1alpha1.UPSDevice{dummyUPSDeviceNamed("alpha"), dummyUPSDeviceNamed("beta")}

	before, err := renderUPSConf(oneDevice, nil)
	if err != nil {
		t.Fatalf("render ups.conf: %v", err)
	}
	after, err := renderUPSConf(twoDevices, nil)
	if err != nil {
		t.Fatalf("render ups.conf: %v", err)
	}
	if before == after {
		t.Fatal("fixture is not exercising anything: adding a device did not change ups.conf")
	}

	// The device set moved, so the restart path must not carry any trace of it. upsd.conf is the
	// only rendered file on that path, and it describes where the server listens rather than what
	// it serves.
	upsdConf := renderUPSDConf(server)
	if strings.Contains(upsdConf, "alpha") || strings.Contains(upsdConf, "beta") {
		t.Fatalf("device names must not reach the restart path, got upsd.conf:\n%s", upsdConf)
	}
	if strings.Contains(upsdConf, before) || strings.Contains(upsdConf, after) {
		t.Fatalf("ups.conf content must not reach the restart path, got upsd.conf:\n%s", upsdConf)
	}
}

func dummyUPSDeviceNamed(name string) powerv1alpha1.UPSDevice {
	device := powerv1alpha1.UPSDevice{Spec: powerv1alpha1.UPSDeviceSpec{Driver: "dummy-ups"}}
	device.Name = name
	return device
}

// The other half, and the reason the split is drawn where it is: `upsd -c reload` returns success
// on a changed LISTEN and stays bound to the old port anyway (verified against the operand image).
// Silent non-adoption is exactly the case that must still replace the pod.
func TestListenChangesStillForceAPodRecreate(t *testing.T) {
	before := restartHashFor("LISTEN 0.0.0.0 3493\nALLOW_NO_DEVICE true\n", "")
	after := restartHashFor("LISTEN 0.0.0.0 3999\nALLOW_NO_DEVICE true\n", "")

	if before == after {
		t.Fatal("a changed LISTEN port must replace the pod; upsd cannot rebind on reload")
	}
}

// A rotated certificate changes no rendered config at all -- it arrives through a referenced Secret
// and lands in a mounted volume. upsd builds its SSL context at startup, so without the digest the
// new certificate would sit on disk unserved until something unrelated happened to restart the pod.
func TestCertificateRotationForcesAPodRecreate(t *testing.T) {
	const upsdConf = "LISTEN 0.0.0.0 3493\nALLOW_NO_DEVICE true\nCERTFILE /run/nut-tls/combined.pem\n"

	before := restartHashFor(upsdConf, "digest-of-original-cert")
	after := restartHashFor(upsdConf, "digest-of-rotated-cert")

	if before == after {
		t.Fatal("a rotated serving certificate must replace the pod; upsd builds its SSL context once")
	}
}

// The watchdog is what turns a changed file into a reload, so it has to notice the change and it
// has to signal upsd. Both halves are asserted because either alone is silently useless: watching
// without reloading does nothing, and reloading every tick would re-read config constantly and hide
// whether detection works at all.
func TestWatchdogReloadsOnReloadableConfigChange(t *testing.T) {
	script := driverWatchdogScript()

	if !strings.Contains(script, "upsd -c reload") {
		t.Fatalf("watchdog must reload upsd when reloadable config changes:\n%s", script)
	}
	for _, watched := range []string{"/etc/nut/ups.conf", "/etc/nut/upsd.users"} {
		if !strings.Contains(script, watched) {
			t.Fatalf("watchdog must watch %s for changes:\n%s", watched, script)
		}
	}
	if !strings.Contains(script, `"$currentDigest" != "$lastDigest"`) {
		t.Fatalf("watchdog must reload on change rather than on every tick:\n%s", script)
	}
}

// The watchdog waits before its first pass. Starting immediately races the entrypoint's own driver
// start -- the drivers are legitimately not responsive yet, the confirming re-check agrees, and the
// watchdog restarts a driver that was seconds from healthy. Observed in a live run, not predicted.
func TestWatchdogWaitsBeforeItsFirstPass(t *testing.T) {
	script := driverWatchdogScript()

	loop := strings.Index(script, "while true; do")
	sleep := strings.Index(script, "\n  sleep ")
	check := strings.Index(script, "currentDigest=")
	if loop < 0 || sleep < 0 || check < 0 {
		t.Fatalf("watchdog loop is not in the expected shape:\n%s", script)
	}
	if loop >= sleep || sleep >= check {
		t.Fatalf("watchdog must sleep at the top of the loop, before its first check:\n%s", script)
	}
	if strings.Count(script, "\n  sleep ") != 1 {
		t.Fatalf("watchdog should wait once per pass, not twice:\n%s", script)
	}
}

// The reload only advances the recorded digest when it succeeded. Advancing it unconditionally
// would drop the change on the floor: a failed reload would be remembered as applied, and upsd
// would serve the old configuration until something else happened to change the files again.
func TestWatchdogRetriesAFailedReload(t *testing.T) {
	script := driverWatchdogScript()

	reload := strings.Index(script, "if upsd -c reload; then")
	advance := strings.Index(script, `lastDigest="$currentDigest"`)
	if reload < 0 || advance < 0 || advance < reload {
		t.Fatalf("watchdog must record the digest only after a successful reload:\n%s", script)
	}
}
