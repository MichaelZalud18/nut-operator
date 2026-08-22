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

// ensureOperatorInstalled makes a spec's dependency on the operator explicit instead of inherited.
//
// Top-level containers do not share a BeforeAll, and the suite has two that install and uninstall
// the operator between them: the Manager container deploys it, and the BYO-cert container applies
// its own bundle and deletes it again. A spec in a third file therefore runs against whatever those
// two happen to have left behind. Both specs that needed CRDs failed on exactly that -- "the server
// could not find the requested resource (post upsdevices.power.zalud.io)" -- which reads like a
// broken cluster and is really an ordering assumption nobody wrote down.
//
// Idempotent by checking first, so a spec that runs after the Manager container costs nothing, and
// one that runs after an uninstall pays for a deploy rather than failing. It waits for the manager
// to be Available afterwards, because the CRDs existing is not the same as admission working: every
// kind in this suite has a webhook, and applying one before the webhook server serves fails with
// "connection refused".
func ensureOperatorInstalled() {
	if crdsPresent() && managerAvailable() {
		return
	}

	By("installing the operator, which an earlier container has not left in place")
	_, _ = utils.Run(exec.Command("kubectl", "create", "ns", namespace))
	_, err := utils.Run(exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage)))
	Expect(err).NotTo(HaveOccurred(), "Failed to deploy the operator")

	By("waiting for the manager to become Available")
	_, err = utils.Run(exec.Command("kubectl", "-n", namespace, "wait", "--for=condition=Available",
		"deployment/nut-operator-controller-manager", "--timeout=4m"))
	Expect(err).NotTo(HaveOccurred(), "the manager never became Available")

	// Admission is the thing specs actually depend on, and it starts serving after the Deployment
	// reports Available. Proven by use rather than by sleeping: a dry-run apply of the simplest kind
	// in the API group exercises the same webhook path a real apply would.
	By("waiting for admission to accept a request")
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
		cmd.Stdin = strings.NewReader(admissionProbeManifest)
		_, probeErr := utils.Run(cmd)
		g.Expect(probeErr).NotTo(HaveOccurred())
	}, 3*time.Minute, 5*time.Second).Should(Succeed(), "admission never started accepting requests")
}

func crdsPresent() bool {
	_, err := utils.Run(exec.Command("kubectl", "get", "crd", "upsdevices.power.zalud.io"))
	return err == nil
}

func managerAvailable() bool {
	out, err := utils.Run(exec.Command("kubectl", "-n", namespace, "get",
		"deployment/nut-operator-controller-manager",
		"-o", "jsonpath={.status.conditions[?(@.type=='Available')].status}"))
	return err == nil && out == "True"
}

// admissionProbeManifest is the smallest object that reaches a webhook in this API group. Never
// applied for real -- only server-dry-run -- so it needs to be valid, not useful.
const admissionProbeManifest = `apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: admission-probe
spec:
  displayName: admission readiness probe
  driver: snmp-ups
  endpoint:
    address: 198.51.100.1
`
