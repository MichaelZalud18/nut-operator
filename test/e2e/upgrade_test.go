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

func managerUpgradeSpecs(controllerPodName *string) {
	It("continues reconciling after CRDs and the manager are reapplied over existing resources", func() {
		// F-112: a fresh install is not upgrade coverage. This keeps a real resource present,
		// reapplies the install surfaces, replaces the manager pod, then mutates the existing
		// resource and waits for the new manager to observe its new generation.
		const (
			clusterName      = "upgrade-e2e"
			operandNamespace = "power-upgrade-e2e"
		)
		manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: PowerManagementCluster
metadata:
  name: %[1]s
spec:
  operandNamespace:
    name: %[2]s
    create: true
  storage:
    mode: Disabled
  hooks:
    defaultTimeout: 10s
`, clusterName, operandNamespace)

		waitForClusterReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "powermanagementcluster", clusterName,
				"-o", "jsonpath={.metadata.generation}|{.status.observedGeneration}|{.status.conditions[?(@.type=='Ready')].status}|{.status.storage.ready}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			fields := strings.Split(output, "|")
			g.Expect(fields).To(HaveLen(4), "unexpected PowerManagementCluster status %q", output)
			g.Expect(fields[1]).To(Equal(fields[0]), "controller has not observed the current generation")
			g.Expect(fields[2]).To(Equal("True"))
			g.Expect(fields[3]).To(Equal("true"))
		}

		deferFixtureCleanup(manifest, operandNamespace, "cleaning up the upgrade fixture")
		By("creating a storage-disabled PowerManagementCluster before the replacement")
		err := applyFixtureManifest(manifest)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the upgrade fixture")

		Eventually(waitForClusterReady, 2*time.Minute, time.Second).Should(Succeed())

		By("capturing the running manager pod before replacement")
		_, oldUID, err := currentControllerPodIdentity()
		Expect(err).NotTo(HaveOccurred(), "Failed to read the current manager pod")

		By("reapplying the CRDs and controller deployment over the existing resource")
		cmd := exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to reapply CRDs")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to reapply the controller-manager")

		By("keeping the manager on the image already loaded into Kind")
		cmd = exec.Command("kubectl", "patch", "deployment", "nut-operator-controller-manager",
			"-n", namespace,
			"--type=json",
			"-p", `[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]`,
		)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to restore controller-manager imagePullPolicy for Kind")

		By("forcing a manager replacement and waiting for one new pod to settle")
		cmd = exec.Command("kubectl", "rollout", "restart", "deployment/nut-operator-controller-manager", "-n", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to restart the controller-manager")
		cmd = exec.Command("kubectl", "rollout", "status", "deployment/nut-operator-controller-manager",
			"-n", namespace, "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "The controller-manager replacement never completed")
		Eventually(func(g Gomega) {
			name, uid, err := currentControllerPodIdentity()
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(uid).NotTo(Equal(oldUID), "the manager pod was not replaced")
			*controllerPodName = name
		}, 2*time.Minute, time.Second).Should(Succeed())

		waitForPowerManagementClusterAdmissionReady()

		By("mutating the existing resource after replacement")
		cmd = exec.Command("kubectl", "patch", "powermanagementcluster", clusterName,
			"--type=merge", "-p", `{"spec":{"hooks":{"defaultTimeout":"11s"}}}`)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to patch the upgrade fixture")
		Eventually(waitForClusterReady, 2*time.Minute, time.Second).Should(Succeed())
	})
}
