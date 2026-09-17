//go:build e2e
// +build e2e

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

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

func simulationSpecs() {
	It("drives real Online/OnBattery/LowBattery transitions from a scripted dummy-ups fixture", func() {
		// Only the static single-state .dev fixture existed before spec.simulation: it can prove
		// the render path and driver connectivity, but it can never actually change state, so
		// nothing could exercise a real OnBattery/LowBattery transition end-to-end (telemetry
		// polling -> UPSDevice.status.phase -> eventually ShutdownFlow trigger eligibility)
		// without hand-patching status, which isn't what happens in production. This drives the
		// transition through NUT's own dummy-loop driver reading a real .seq file delivered via
		// spec.simulation.sequenceConfigMapRef, and asserts on UPSDeviceReconciler's real
		// telemetry poll output, not a mock.
		const simulationNamespace = "power-simulation-e2e"

		// The operand namespace is created by NUTServer's own reconciler (ensureOperandNamespace),
		// asynchronously, on its first reconcile -- it does not exist yet at apply time. The fixture
		// ConfigMap is namespace-scoped and must land in that namespace, so it has to be created
		// after the namespace exists; creating it explicitly here (rather than racing the operator)
		// is both simpler and matches how a real deployment would pre-provision the namespace via a
		// PowerManagementCluster before ever creating a UPSDevice with a namespace-scoped reference.
		deferFixtureCleanup("", simulationNamespace, "cleaning up the scripted-transition namespace")
		By("creating the operand namespace directly (no operator-managed NUTServer exists yet to create it)")
		nsCmd := exec.Command("kubectl", "create", "ns", simulationNamespace)
		_, err := utils.Run(nsCmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the scripted-transition namespace")

		By("creating a ConfigMap-backed .seq fixture, UPSDevice, and NUTServer")
		manifest := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: simulation-e2e-transitions
  namespace: %[1]s
data:
  sequence.seq: |
    device.mfr: nut-operator
    device.model: e2e-simulation
    ups.mfr: nut-operator
    ups.model: e2e-simulation
    ups.status: OL
    battery.charge: 100
    battery.runtime: 3600
    ups.load: 10

    TIMER 40

    device.mfr: nut-operator
    device.model: e2e-simulation
    ups.mfr: nut-operator
    ups.model: e2e-simulation
    ups.status: OB
    battery.charge: 40
    battery.runtime: 600
    ups.load: 10

    TIMER 40

    device.mfr: nut-operator
    device.model: e2e-simulation
    ups.mfr: nut-operator
    ups.model: e2e-simulation
    ups.status: OB LB
    battery.charge: 8
    battery.runtime: 90
    ups.load: 10

    TIMER 40
---
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: simulation-e2e-ups
spec:
  displayName: Simulation E2E Dummy UPS
  driver: dummy-ups
  simulation:
    sequenceConfigMapRef:
      namespace: %[1]s
      name: simulation-e2e-transitions
  telemetry:
    pollInterval: 5s
    alertPollInterval: 5s
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: simulation-e2e-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: simulation-e2e-ups
  image:
    repository: %[2]s
    tag: %[3]s
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
`, simulationNamespace, nutServerRepository, operandImageTag)
		deferFixtureCleanup(manifest, "", "cleaning up the scripted-transition simulation fixture")
		err = applyFixtureManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the scripted-transition simulation fixture")

		By("waiting for the NUTServer to report Ready")
		verifyNUTServerReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "nutserver", "simulation-e2e-nutserver",
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}
		Eventually(verifyNUTServerReady, 3*time.Minute).Should(Succeed())

		upsDevicePhase := func() (string, error) {
			cmd := exec.Command("kubectl", "get", "upsdevice", "simulation-e2e-ups",
				"-o", "jsonpath={.status.phase}")
			return utils.Run(cmd)
		}

		// The fixture loops (dummy-loop mode): OL/OB/LB each held ~40s, then wraps back to OL,
		// a 120s cycle. Each Eventually window below is sized past a full cycle (not just one
		// state's dwell time) so a check that starts moments after its target state's window
		// already closed still survives to catch it on the next lap, rather than racing a single
		// pass. Confirmed empirically (real dummy-ups driver, real upsc polling) that a state
		// with no trailing TIMER after it is only held for one driver poll cycle (~2s) before
		// looping -- the fixture above deliberately gives every block, including the last, its
		// own TIMER so this test isn't racing a near-instant window.
		By("observing the fixture's initial Online state via real telemetry polling")
		verifyOnline := func(g Gomega) {
			phase, err := upsDevicePhase()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(phase).To(Equal("Online"))
		}
		Eventually(verifyOnline, 3*time.Minute, 2*time.Second).Should(Succeed())

		By("observing the scripted transition into OnBattery")
		verifyOnBattery := func(g Gomega) {
			phase, err := upsDevicePhase()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(phase).To(Equal("OnBattery"))
		}
		Eventually(verifyOnBattery, 150*time.Second, 2*time.Second).Should(Succeed())

		// lastStatus is asserted by token rather than by whole string. NUT owns that field's
		// contents and adds to them between releases: 2.8.2's dummy-ups reports "OB LB" for
		// this fixture and 2.8.5 reports "OB LB DISCHRG", which turned an operand upgrade into
		// a red suite even though the operator classified DISCHRG correctly the whole time.
		// What this test is actually about is the normalization the operator owns -- OB plus LB
		// becomes LowBattery, and the charge reading survives the round trip -- so it asserts
		// that and lets NUT add tokens.
		By("observing the scripted transition into LowBattery, with matching telemetry values")
		verifyLowBattery := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "upsdevice", "simulation-e2e-ups",
				"-o", "jsonpath={.status.phase}|{.status.lastStatus}|{.status.batteryChargePercent}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())

			fields := strings.Split(output, "|")
			g.Expect(fields).To(HaveLen(3), "unexpected jsonpath output %q", output)
			g.Expect(fields[0]).To(Equal("LowBattery"))
			g.Expect(fields[2]).To(Equal("8"))
			g.Expect(strings.Fields(fields[1])).To(ContainElements("OB", "LB"))
		}
		Eventually(verifyLowBattery, 150*time.Second, 2*time.Second).Should(Succeed())
	})
}
