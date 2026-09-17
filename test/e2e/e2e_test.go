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

// namespace where the project is deployed in
const namespace = "nut-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "nut-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "nut-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "nut-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("labeling the namespace so the allow-metrics-traffic NetworkPolicy admits the curl-metrics pod")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace, "metrics=enabled")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with metrics=enabled")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("using the locally loaded Kind image")
		cmd = exec.Command("kubectl", "patch", "deployment", "nut-operator-controller-manager",
			"-n", namespace,
			"--type=json",
			"-p", `[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]`,
		)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to patch controller-manager imagePullPolicy for Kind")

		waitForPowerManagementClusterAdmissionReady()
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		// Cluster-scoped, created imperatively by the metrics spec, and owned by nothing the
		// namespace teardown reaches. Leaving it behind makes the next run of that spec fail with
		// "already exists" on any cluster reused across runs -- which happens whenever a suite
		// failure stops make before cleanup-test-e2e.
		By("deleting the metrics ClusterRoleBinding")
		cmd = exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName,
			"--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		managerUpgradeSpecs(&controllerPodName)

		managerMetricsSpecs(&controllerPodName)

		managerWebhookSpecs()

		// +kubebuilder:scaffold:e2e-webhooks-checks

		signalHandoffSpecs()

		simulationSpecs()

		snmpConformanceSpecs()
	})

	// Both need the operator installed and neither installs it. They live here, inside the
	// container that owns the install and the uninstall, rather than at the top level where
	// they would run against whatever the last container left behind.
	multiNodeSignalTargetingSpecs()
	driverRecoverySpecs()
	driverSoakSpecs()
	podRestartSpecs()
	logicalShutdownFlowSpecs()
	nutStartupSpecs()
})

func currentControllerPodIdentity() (string, string, error) {
	out, err := utils.Run(exec.Command("kubectl", "get", "pods", "-n", namespace,
		"-l", "control-plane=controller-manager",
		"--field-selector=status.phase=Running",
		"-o", `go-template={{ range .items }}{{ if not .metadata.deletionTimestamp }}{{ .metadata.name }}|{{ .metadata.uid }}{{ "\n" }}{{ end }}{{ end }}`))
	if err != nil {
		return "", "", err
	}
	lines := utils.GetNonEmptyLines(out)
	if len(lines) != 1 {
		return "", "", fmt.Errorf("expected exactly one running manager pod, got %d: %q", len(lines), out)
	}
	parts := strings.Split(lines[0], "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("unexpected manager pod identity output %q", lines[0])
	}
	return parts[0], parts[1], nil
}

func waitForPowerManagementClusterAdmissionReady() {
	By("waiting for PowerManagementCluster admission to answer")
	invalid := `apiVersion: power.zalud.io/v1alpha1
kind: PowerManagementCluster
metadata:
  name: admission-readiness-probe
spec:
  operandNamespace:
    name: kube-system
    create: true
  storage:
    mode: Disabled
`
	Eventually(func(g Gomega) {
		cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
		cmd.Stdin = strings.NewReader(invalid)
		out, err := utils.Run(cmd)
		g.Expect(err).To(HaveOccurred(), "the webhook should reject a reserved operand namespace")
		g.Expect(out).To(ContainSubstring("spec.operandNamespace.name"),
			"expected the PowerManagementCluster webhook's own validation message, not a TLS or connectivity error")
		g.Expect(out).To(ContainSubstring("reserved"),
			"expected the PowerManagementCluster webhook's own validation message, not a TLS or connectivity error")
	}, 3*time.Minute, 5*time.Second).Should(Succeed())
}
