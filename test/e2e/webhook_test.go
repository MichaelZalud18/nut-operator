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
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

func managerWebhookSpecs() {
	It("should provisioned cert-manager", func() {
		By("validating that cert-manager has the certificate Secret")
		verifyCertManager := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "secrets", "webhook-server-cert", "-n", namespace)
			_, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
		}
		Eventually(verifyCertManager).Should(Succeed())
	})

	It("should have CA injection for mutating webhooks", func() {
		By("checking CA injection for mutating webhooks")
		verifyCAInjection := func(g Gomega) {
			cmd := exec.Command("kubectl", "get",
				"mutatingwebhookconfigurations.admissionregistration.k8s.io",
				"nut-operator-mutating-webhook-configuration",
				"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
			mwhOutput, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(len(mwhOutput)).To(BeNumerically(">", 10))
		}
		Eventually(verifyCAInjection).Should(Succeed())
	})

	It("should have CA injection for validating webhooks", func() {
		By("checking CA injection for validating webhooks")
		verifyCAInjection := func(g Gomega) {
			cmd := exec.Command("kubectl", "get",
				"validatingwebhookconfigurations.admissionregistration.k8s.io",
				"nut-operator-validating-webhook-configuration",
				"-o", "go-template={{ range .webhooks }}{{ .clientConfig.caBundle }}{{ end }}")
			vwhOutput, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(len(vwhOutput)).To(BeNumerically(">", 10))
		}
		Eventually(verifyCAInjection).Should(Succeed())
	})
}
