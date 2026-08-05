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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
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
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
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
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
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

		// +kubebuilder:scaffold:e2e-webhooks-checks

		It("delivers a projected Secret signal to the NodePowerAgent actuator within the configured TTL", func() {
			// Previously blocked on tooling (kind wasn't installed in the dev environment this suite
			// was authored in); envtest has no real kubelet, so this is the only place the actual
			// claim -- "a projected Secret update reaches a running DaemonSet pod within a bounded
			// time" -- can be checked at all. Real dummy-ups UPSDevice/NUTServer/NodePowerAgent stack,
			// real kubelet-synced projected volume, real node-actuator binary.
			const agentNamespace = "power-signal-e2e"

			By("discovering the Kind node name")
			cmd := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[0].metadata.name}")
			nodeName, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(nodeName).NotTo(BeEmpty())

			By("creating a dummy-ups-backed UPSDevice, NUTServer, and NodePowerAgent")
			manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: signal-e2e-ups
spec:
  displayName: Signal Handoff E2E Dummy UPS
  driver: dummy-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: signal-e2e-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: signal-e2e-ups
  image:
    repository: %[2]s
    tag: %[6]s
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
---
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: signal-e2e-agent
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: signal-e2e-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[3]s
  mode: DryRun
  images:
    upsmon:
      repository: %[4]s
      tag: %[6]s
      pullPolicy: IfNotPresent
    actuator:
      repository: %[5]s
      tag: %[6]s
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: Stub
    signalTTL: 2m
    requireFreshTelemetry: false
`, agentNamespace, nutServerRepository, nodeName, upsmonAgentRepository, nodeActuatorRepository, operandImageTag)
			applyCmd := exec.Command("kubectl", "apply", "-f", "-")
			applyCmd.Stdin = strings.NewReader(manifest)
			_, err = utils.Run(applyCmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the dummy-ups signal handoff fixture")

			DeferCleanup(func() {
				By("cleaning up the signal handoff fixture")
				deleteCmd := exec.Command("kubectl", "delete", "-f", "-", "--ignore-not-found", "--wait=false")
				deleteCmd.Stdin = strings.NewReader(manifest)
				_, _ = utils.Run(deleteCmd)
				cmd := exec.Command("kubectl", "delete", "ns", agentNamespace, "--ignore-not-found", "--wait=false")
				_, _ = utils.Run(cmd)
			})

			By("waiting for the NodePowerAgent to report Ready")
			verifyAgentReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nodepoweragent", "signal-e2e-agent",
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Ready"))
			}
			Eventually(verifyAgentReady, 3*time.Minute).Should(Succeed())

			By("finding the NodePowerAgent DaemonSet pod")
			cmd = exec.Command("kubectl", "-n", agentNamespace, "get", "pods",
				"-l", "power.zalud.io/nodepoweragent=signal-e2e-agent",
				"-o", "jsonpath={.items[0].metadata.name}")
			agentPodName, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(agentPodName).NotTo(BeEmpty())

			By("writing a shutdown signal directly into the projected signal Secret")
			executionID := fmt.Sprintf("e2e-signal-%d", time.Now().UnixNano())
			signalPayload := map[string]any{
				"dryRun":             true,
				"executionID":        executionID,
				"nodeName":           nodeName,
				"planConfigHash":     "e2e-signal-handoff",
				"reason":             "E2ESignalHandoffTest",
				"selectedUPSDevices": []string{"signal-e2e-ups"},
				"shutdownFlow":       "signal-e2e-flow",
				"timestamp":          time.Now().UTC().Format(time.RFC3339Nano),
			}
			encodedPayload, err := json.Marshal(signalPayload)
			Expect(err).NotTo(HaveOccurred())
			patch := fmt.Sprintf(`{"data":{%q:%q}}`, nodeName+".json", base64.StdEncoding.EncodeToString(encodedPayload))
			cmd = exec.Command("kubectl", "-n", agentNamespace, "patch", "secret", "signal-e2e-agent-node-signals",
				"--type=merge", "-p", patch)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to write the signal Secret")

			By("confirming the actuator observes the signal within the configured 2m TTL")
			verifySignalObserved := func(g Gomega) {
				cmd := exec.Command("kubectl", "-n", agentNamespace, "logs", agentPodName, "-c", "actuator")
				logs, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(logs).To(ContainSubstring("stub actuator accepted shutdown signal executionID=" + executionID))
			}
			Eventually(verifySignalObserved, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

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
			By("creating the operand namespace directly (no operator-managed NUTServer exists yet to create it)")
			nsCmd := exec.Command("kubectl", "create", "ns", simulationNamespace)
			_, err := utils.Run(nsCmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the scripted-transition namespace")

			DeferCleanup(func() {
				By("cleaning up the scripted-transition namespace")
				cmd := exec.Command("kubectl", "delete", "ns", simulationNamespace, "--ignore-not-found", "--wait=false")
				_, _ = utils.Run(cmd)
			})

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
			applyCmd := exec.Command("kubectl", "apply", "-f", "-")
			applyCmd.Stdin = strings.NewReader(manifest)
			_, err = utils.Run(applyCmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the scripted-transition simulation fixture")

			DeferCleanup(func() {
				By("cleaning up the scripted-transition simulation fixture")
				deleteCmd := exec.Command("kubectl", "delete", "-f", "-", "--ignore-not-found", "--wait=false")
				deleteCmd.Stdin = strings.NewReader(manifest)
				_, _ = utils.Run(deleteCmd)
			})

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

			By("observing the scripted transition into LowBattery, with matching telemetry values")
			verifyLowBattery := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "upsdevice", "simulation-e2e-ups",
					"-o", "jsonpath={.status.phase} {.status.lastStatus} {.status.batteryChargePercent}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("LowBattery OB LB 8"))
			}
			Eventually(verifyLowBattery, 150*time.Second, 2*time.Second).Should(Succeed())
		})

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

			By("creating the operand namespace directly (no operator-managed NUTServer exists yet to create it)")
			cmd := exec.Command("kubectl", "create", "ns", snmpNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the snmp-conformance namespace")

			DeferCleanup(func() {
				By("cleaning up the snmp-conformance namespace")
				cmd := exec.Command("kubectl", "delete", "ns", snmpNamespace, "--ignore-not-found", "--wait=false")
				_, _ = utils.Run(cmd)
			})

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
			applyCmd := exec.Command("kubectl", "apply", "-f", "-")
			applyCmd.Stdin = strings.NewReader(manifest)
			_, err = utils.Run(applyCmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the snmp-conformance fixture")

			DeferCleanup(func() {
				By("cleaning up the snmp-conformance fixture")
				deleteCmd := exec.Command("kubectl", "delete", "-f", "-", "--ignore-not-found", "--wait=false")
				deleteCmd.Stdin = strings.NewReader(manifest)
				_, _ = utils.Run(deleteCmd)
			})

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

			By("confirming the telemetry poller decoded the fixture's real SNMP values correctly")
			verifyTelemetry := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "upsdevice", "snmp-conformance-ups",
					"-o", "jsonpath={.status.phase} {.status.lastStatus} {.status.batteryChargePercent} {.status.loadPercent} {.status.runtimeSeconds}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Online OL 100 10 3600"))
			}
			Eventually(verifyTelemetry, 90*time.Second, 2*time.Second).Should(Succeed())
		})
	})
})

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
