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

// Shared setup/teardown for the two specs that stand up a single dummy-ups-backed NUTServer and
// exercise its driver-outage behavior: driverRecoverySpecs (driver_recovery_test.go) and
// driverSoakSpecs (soak_test.go, F-110's repeated-cycle version of the same claim). Factored out
// rather than duplicated between them -- both specs need identically-shaped fixtures, just under
// different namespaces and cycle counts.

// applyDummyUPSFixture creates namespace and a UPSDevice/NUTServer pair inside it, backed by the
// dummy-ups driver so no real hardware or SNMP simulator is needed.
func applyDummyUPSFixture(namespace, serverName, upsName, displayName string) {
	By("creating the " + namespace + " namespace")
	_, err := utils.Run(exec.Command("kubectl", "create", "ns", namespace))
	Expect(err).NotTo(HaveOccurred())

	By("creating a dummy-ups-backed UPSDevice and NUTServer")
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: %[3]s
spec:
  displayName: %[6]s
  driver: dummy-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: %[4]s
spec:
  namespace: %[1]s
  deviceRefs:
    - name: %[3]s
  image:
    repository: %[2]s
    tag: %[5]s
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
`, namespace, nutServerRepository, upsName, serverName, operandImageTag, displayName)

	applyFixture := func(g Gomega) {
		applyCmd := exec.Command("kubectl", "apply", "-f", "-")
		applyCmd.Stdin = strings.NewReader(manifest)
		_, applyErr := utils.Run(applyCmd)
		g.Expect(applyErr).NotTo(HaveOccurred())
	}
	Eventually(applyFixture, 2*time.Minute, 5*time.Second).Should(Succeed())
}

// waitForNUTServerPodReady polls until exactly one pod matching podSelector reports Ready, and
// returns its name.
func waitForNUTServerPodReady(namespace, podSelector string) string {
	var serverPod string
	By("waiting for the NUT server pod to be Ready")
	Eventually(func(g Gomega) {
		out, getErr := utils.Run(exec.Command("kubectl", "-n", namespace, "get", "pods",
			"-l", podSelector,
			"-o", `jsonpath={range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}`))
		g.Expect(getErr).NotTo(HaveOccurred())

		readyPod := ""
		for _, line := range utils.GetNonEmptyLines(out) {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "True" {
				readyPod = fields[0]
				break
			}
		}
		g.Expect(readyPod).NotTo(BeEmpty(), "no Ready pod matched %s; observed pods: %q", podSelector, out)
		serverPod = readyPod
	}, 4*time.Minute, 5*time.Second).Should(Succeed(), "the NUT server never became Ready")
	return serverPod
}

// teardownDummyUPSFixture dumps diagnostics on failure, then removes the fixture created by
// applyDummyUPSFixture.
func teardownDummyUPSFixture(namespace, serverName, upsName string) {
	if CurrentSpecReport().Failed() {
		By("dumping the " + namespace + " namespace before tearing it down")
		utils.DumpNamespaceDiagnostics(namespace)
	}

	By("removing the " + namespace + " namespace and its cluster-scoped fixture")
	for _, args := range [][]string{
		{"delete", "nutserver", serverName, "--ignore-not-found=true"},
		{"delete", "upsdevice", upsName, "--ignore-not-found=true"},
		{"delete", "ns", namespace, "--ignore-not-found=true", "--wait=false"},
	} {
		_, _ = utils.Run(exec.Command("kubectl", args...))
	}
}
