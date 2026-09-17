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

func snmpConformanceSpecs() {
	It("proves the real snmp-ups driver decodes a simulated UPS-MIB device correctly", func() {
		// dummy-ups (above) proves the operator reacts correctly once a device reports state;
		// it says nothing about whether snmp-ups -- the driver every real network UPS in this
		// project's design actually uses -- talks to real SNMP OIDs correctly. Nothing in this
		// repo previously ran the real snmp-ups driver against anything, real or simulated
		// (every prior "snmp-ups" reference was just a config-string literal in a unit test).
		// This stands up a real snmpsim-command-responder serving a fixture verified against
		// NUT's own upstream ietf-mib.c OID mapping table, points a real NUTServer's snmp-ups
		// driver at it, and asserts on UPSDeviceReconciler's real telemetry poll output.
		const snmpNamespace = "power-snmpconformance-e2e"

		deferFixtureCleanup("", snmpNamespace, "cleaning up the snmp-conformance namespace")
		By("creating the operand namespace directly (no operator-managed NUTServer exists yet to create it)")
		cmd := exec.Command("kubectl", "create", "ns", snmpNamespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the snmp-conformance namespace")

		By("deploying the snmpsim fixture and a snmp-ups-backed UPSDevice/NUTServer")
		manifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: snmpsim-fixture
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: snmpsim-fixture
  template:
    metadata:
      labels:
        app: snmpsim-fixture
    spec:
      containers:
        - name: snmpsim
          image: %[2]s
          ports:
            - containerPort: 161
              protocol: UDP
---
apiVersion: v1
kind: Service
metadata:
  name: snmpsim-fixture
  namespace: %[1]s
spec:
  selector:
    app: snmpsim-fixture
  ports:
    - port: 161
      protocol: UDP
      targetPort: 161
---
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: snmp-conformance-ups
spec:
  displayName: SNMP Conformance E2E UPS
  driver: snmp-ups
  endpoint:
    host: snmpsim-fixture.%[1]s.svc.cluster.local
    port: 161
  driverOptions:
    mibs: ietf
    community: public
    snmp_version: v2c
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: snmp-conformance-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: snmp-conformance-ups
  image:
    repository: %[3]s
    tag: %[4]s
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
`, snmpNamespace, snmpsimFixtureImage, nutServerRepository, operandImageTag)
		deferFixtureCleanup(manifest, "", "cleaning up the snmp-conformance fixture")
		err = applyFixtureManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the snmp-conformance fixture")

		By("waiting for the snmpsim fixture Deployment to become available")
		cmd = exec.Command("kubectl", "-n", snmpNamespace, "rollout", "status", "deployment/snmpsim-fixture", "--timeout=60s")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "snmpsim fixture Deployment never became available")

		By("waiting for the NUTServer to report Ready, proving the snmp-ups driver connected")
		verifyNUTServerReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "nutserver", "snmp-conformance-nutserver",
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}
		Eventually(verifyNUTServerReady, 3*time.Minute).Should(Succeed())

		// Decoded values are asserted exactly, because decoding the fixture's OIDs is what this
		// test exists to prove. lastStatus is asserted by token for the reason in the dummy-ups
		// test above: NUT owns that field and adds to it between releases.
		By("confirming the telemetry poller decoded the fixture's real SNMP values correctly")
		verifyTelemetry := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "upsdevice", "snmp-conformance-ups",
				"-o", "jsonpath={.status.phase}|{.status.lastStatus}|{.status.batteryChargePercent} {.status.loadPercent} {.status.runtimeSeconds}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())

			fields := strings.Split(output, "|")
			g.Expect(fields).To(HaveLen(3), "unexpected jsonpath output %q", output)
			g.Expect(fields[0]).To(Equal("Online"))
			g.Expect(fields[2]).To(Equal("100 10 3600"))
			g.Expect(strings.Fields(fields[1])).To(ContainElement("OL"))
		}
		Eventually(verifyTelemetry, 90*time.Second, 2*time.Second).Should(Succeed())
	})
}
