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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

func managerMetricsSpecs(controllerPodName *string) {
	It("should ensure the metrics endpoint is serving metrics", func() {
		By("creating a ClusterRoleBinding for the service account to allow access to metrics")
		cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
			"--clusterrole=nut-operator-metrics-reader",
			fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
		)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

		By("validating that the metrics service is available")
		cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

		By("getting the service account token")
		token, err := serviceAccountToken()
		Expect(err).NotTo(HaveOccurred())
		Expect(token).NotTo(BeEmpty())

		By("ensuring the controller pod is ready")
		verifyControllerPodReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pod", *controllerPodName, "-n", namespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("True"), "Controller pod not ready")
		}
		Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

		By("verifying that the controller manager is serving the metrics server")
		verifyMetricsServerStarted := func(g Gomega) {
			cmd := exec.Command("kubectl", "logs", *controllerPodName, "-n", namespace)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(ContainSubstring("Serving metrics server"),
				"Metrics server not yet started")
		}
		Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

		By("waiting for the webhook service endpoints to be ready")
		verifyWebhookEndpointsReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "endpointslices.discovery.k8s.io", "-n", namespace,
				"-l", "kubernetes.io/service-name=nut-operator-webhook-service",
				"-o", "jsonpath={range .items[*]}{range .endpoints[*]}{.addresses[*]}{end}{end}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "Webhook endpoints should exist")
			g.Expect(output).ShouldNot(BeEmpty(), "Webhook endpoints not yet ready")
		}
		Eventually(verifyWebhookEndpointsReady, 3*time.Minute, time.Second).Should(Succeed())

		By("verifying the mutating webhook server is ready")
		verifyMutatingWebhookReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "mutatingwebhookconfigurations.admissionregistration.k8s.io",
				"nut-operator-mutating-webhook-configuration",
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "MutatingWebhookConfiguration should exist")
			g.Expect(output).ShouldNot(BeEmpty(), "Mutating webhook CA bundle not yet injected")
		}
		Eventually(verifyMutatingWebhookReady, 3*time.Minute, time.Second).Should(Succeed())

		By("verifying the validating webhook server is ready")
		verifyValidatingWebhookReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "validatingwebhookconfigurations.admissionregistration.k8s.io",
				"nut-operator-validating-webhook-configuration",
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred(), "ValidatingWebhookConfiguration should exist")
			g.Expect(output).ShouldNot(BeEmpty(), "Validating webhook CA bundle not yet injected")
		}
		Eventually(verifyValidatingWebhookReady, 3*time.Minute, time.Second).Should(Succeed())

		By("waiting additional time for webhook server to stabilize")
		time.Sleep(5 * time.Second)

		// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

		By("creating the curl-metrics pod to access the metrics endpoint")
		cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
			"--namespace", namespace,
			"--image=curlimages/curl:8.21.0",
			"--overrides",
			fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:8.21.0",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

		By("waiting for the curl-metrics pod to complete.")
		verifyCurlUp := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
				"-o", "jsonpath={.status.phase}",
				"-n", namespace)
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
		}
		Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

		By("getting the metrics by checking curl-metrics logs")
		verifyMetricsAvailable := func(g Gomega) {
			metricsOutput, err := getMetricsOutput()
			g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
			g.Expect(metricsOutput).NotTo(BeEmpty())
			g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
		}
		Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
	})
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
