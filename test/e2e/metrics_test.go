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
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
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
		manifest, err := json.Marshal(metricsProbePod())
		Expect(err).NotTo(HaveOccurred())
		Expect(applyFixtureManifest(string(manifest))).To(Succeed(), "Failed to create curl-metrics pod")

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
			g.Expect(metricsOutput).To(ContainSubstring("HTTP 200\n"))
			g.Expect(metricsOutput).To(MatchRegexp("(?m)^go_goroutines [0-9]+$"))
		}
		Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
	})
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

const metricsTokenDirectory = "/var/run/secrets/kubernetes.io/serviceaccount"

// Header input stays on stdin: the token never becomes a curl argument or log output.
// The writable shell variables and response live only in memory on the read-only pod.
const metricsProbeScript = `set -eu
token_file=$1
endpoint=$2
attempts=$3
delay=$4
i=0
while [ "$i" -lt "$attempts" ]; do
  token=$(cat "$token_file")
  [ -n "$token" ] || exit 1
  if response=$(printf 'Authorization: Bearer %s\n' "$token" |
    curl --disable --silent --show-error --fail --insecure --max-time 5 \
      --header @- --write-out '\n%{http_code}' "$endpoint"); then
    status=$(printf '%s\n' "$response" | tail -n 1)
    if [ "$status" = 200 ] && metric=$(printf '%s\n' "$response" |
      awk '$1 == "go_goroutines" && NF == 2 && $2 ~ /^[0-9]+$/ { value=$2; found=1 }
           END { if (!found) exit 1; print "go_goroutines " value }'); then
      printf 'HTTP 200\n%s\n' "$metric"
      exit 0
    fi
  fi
  i=$((i + 1))
  if [ "$i" -lt "$attempts" ]; then sleep "$delay"; fi
done
echo 'Metrics endpoint did not return HTTP 200 with go_goroutines' >&2
exit 1
`

func metricsProbePod() corev1.Pod {
	return corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "curl-metrics", Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			ServiceAccountName:           serviceAccountName,
			AutomountServiceAccountToken: ptr.To(false),
			Containers: []corev1.Container{{
				Name: "curl", Image: "curlimages/curl:8.21.0",
				Command: []string{"/bin/sh", "-c"},
				Args: []string{metricsProbeScript, "metrics-probe", metricsTokenDirectory + "/token",
					fmt.Sprintf("https://%s.%s.svc.cluster.local:8443/metrics", metricsServiceName, namespace), "30", "2"},
				SecurityContext: &corev1.SecurityContext{
					ReadOnlyRootFilesystem: ptr.To(true), AllowPrivilegeEscalation: ptr.To(false),
					Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)),
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "metrics-token", MountPath: metricsTokenDirectory, ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{
				Name: "metrics-token",
				VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
						Path: "token", ExpirationSeconds: ptr.To(int64(3600)),
					}}},
				}},
			}},
		},
	}
}
