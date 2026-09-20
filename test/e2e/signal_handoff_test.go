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
	"os/exec"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

func signalHandoffSpecs() {
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
		manifest := dummyUPSManifest(agentNamespace, "signal-e2e-nutserver", "signal-e2e-ups", "Signal Handoff E2E Dummy UPS") + fmt.Sprintf(`
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
      kubernetes.io/hostname: %[2]s
  mode: DryRun
  images:
    upsmon:
      repository: %[3]s
      tag: %[5]s
      pullPolicy: IfNotPresent
    actuator:
      repository: %[4]s
      tag: %[5]s
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: Simulate
    signalTTL: 2m
    requireFreshTelemetry: false
`, agentNamespace, nodeName, upsmonAgentRepository, nodeActuatorRepository, operandImageTag)
		deferFixtureCleanup(manifest, agentNamespace, "cleaning up the signal handoff fixture")
		// Retried rather than applied once: every kind in this fixture has a mutating webhook, so
		// the apply fails outright with "connection refused" until the manager's webhook server is
		// serving. Nothing in this spec's own setup waits for that -- it only ever passed because
		// earlier specs in the suite happened to give the manager time, which makes it both
		// unrunnable on its own and a flake waiting for a slow pull.
		applyFixture := func(g Gomega) {
			err := applyFixtureManifest(manifest)
			g.Expect(err).NotTo(HaveOccurred())
		}
		Eventually(applyFixture, 2*time.Minute, 5*time.Second).
			Should(Succeed(), "Failed to create the dummy-ups signal handoff fixture")

		By("waiting for the NodePowerAgent to report Ready")
		verifyAgentReady := func(g Gomega) {
			cmd := exec.Command("kubectl", "get", "nodepoweragent", "signal-e2e-agent",
				"-o", "jsonpath={.status.phase}")
			output, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(output).To(Equal("Ready"))
		}
		Eventually(verifyAgentReady, 3*time.Minute).Should(Succeed())

		By("waiting for the current DaemonSet rollout before measuring live projection")
		var agentPod corev1.Pod
		Eventually(func(g Gomega) {
			var ds appsv1.DaemonSet
			var pods corev1.PodList
			g.Expect(logicalFlowGet(&ds, "daemonset", "signal-e2e-agent-node-power-agent", "-n", agentNamespace)).To(Succeed())
			g.Expect(logicalFlowGet(&pods, "pods", "-n", agentNamespace,
				"-l", "power.zalud.io/nodepoweragent=signal-e2e-agent")).To(Succeed())
			var selectErr error
			agentPod, selectErr = signalHandoffCurrentPod(ds, pods.Items, nodeName)
			g.Expect(selectErr).NotTo(HaveOccurred())
		}, 3*time.Minute, 2*time.Second).Should(Succeed())

		By("writing a shutdown signal directly into the projected signal Secret")
		executionID := fmt.Sprintf("e2e-signal-%d", time.Now().UnixNano())
		signalPayload := map[string]any{
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
			cmd := exec.Command("kubectl", "-n", agentNamespace, "logs", agentPod.Name, "-c", "actuator")
			logs, err := utils.Run(cmd)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(logs).To(ContainSubstring("simulate actuator accepted shutdown signal executionID=" + executionID))
		}
		Eventually(verifySignalObserved, 2*time.Minute, 5*time.Second).Should(Succeed())
		var after corev1.Pod
		Expect(logicalFlowGet(&after, "pod", agentPod.Name, "-n", agentNamespace)).To(Succeed())
		Expect(after.UID).To(Equal(agentPod.UID))
		Expect(after.DeletionTimestamp).To(BeNil())
		Expect(signalHandoffActuator(after)).To(Equal(signalHandoffActuator(agentPod)),
			"a replacement/restarted actuator does not prove live Secret projection")
	})
}

// Ready coverage can include the previous revision during a surge rollout. Only
// start the projection clock after that revision and its terminating pods are gone.
func signalHandoffCurrentPod(ds appsv1.DaemonSet, pods []corev1.Pod, node string) (corev1.Pod, error) {
	if ds.Status.ObservedGeneration != ds.Generation || ds.Status.DesiredNumberScheduled != 1 ||
		ds.Status.UpdatedNumberScheduled != 1 || ds.Status.NumberAvailable != 1 || len(pods) != 1 {
		return corev1.Pod{}, fmt.Errorf("signal fixture rollout has not converged: generation=%d status=%+v pods=%d", ds.Generation, ds.Status, len(pods))
	}
	pod := pods[0]
	owner := metav1.GetControllerOf(&pod)
	hash := ds.Spec.Template.Annotations["power.zalud.io/config-hash"]
	if owner == nil || ds.UID == "" || owner.UID != ds.UID || pod.UID == "" ||
		pod.Spec.NodeName != node || pod.DeletionTimestamp != nil || hash == "" ||
		pod.Annotations["power.zalud.io/config-hash"] != hash {
		return corev1.Pod{}, fmt.Errorf("pod %s is not the current owned signal fixture", pod.Name)
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue && signalHandoffActuator(pod) != "" {
			return pod, nil
		}
	}
	return corev1.Pod{}, fmt.Errorf("pod %s has no Ready running actuator", pod.Name)
}

func signalHandoffActuator(pod corev1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "actuator" && status.ContainerID != "" && status.State.Running != nil {
			return fmt.Sprintf("%s/%d", status.ContainerID, status.RestartCount)
		}
	}
	return ""
}
