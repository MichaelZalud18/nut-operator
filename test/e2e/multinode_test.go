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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

// Signal targeting is the claim a single-node cluster cannot test (F-109).
//
// The existing handoff spec pins its agent to one node with a nodeSelector, so it proves a signal
// reaches an actuator. It cannot prove the signal reached the *right* actuator, because on one node
// "delivered to the intended node" and "delivered to the only node there is" are the same
// observation. A signal broadcast to every node would pass it unchanged — and broadcasting a halt is
// the specific accident this design exists to prevent, since the operator's whole job is stopping
// some nodes and not others.
//
// So this runs an agent with no nodeSelector across every node, writes a signal keyed to exactly one
// of them, and asserts both halves: that node's actuator accepts it, and every other node's actuator
// does not. The negative half is the reason the spec exists.
// multiNodeSignalTargetingSpecs is called from inside the Manager container rather than registered at the top
// level, because it needs the operator installed and only that container owns installing
// it. Registered standalone, these specs ran against whatever the Manager and BYO-cert
// containers had last left behind, which was sometimes no CRDs at all.
func multiNodeSignalTargetingSpecs() {
	Describe("Multi-node signal targeting", Ordered, func() {
		const (
			namespace = "power-fanout-e2e"
			agentName = "fanout-e2e-agent"
		)

		var (
			// podsByNode maps node name to the agent pod running there. Nodes are addressed only as
			// "the targeted node" and "the others" below; which specific node is picked is an accident
			// of iteration order and means nothing.
			podsByNode  map[string]string
			targetNode  string
			targetPod   string
			nodeCount   int
			executionID string
		)

		BeforeAll(func() {
			By("counting the cluster's nodes")
			cmd := exec.Command("kubectl", "get", "nodes", "-o", "jsonpath={.items[*].metadata.name}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			nodeCount = len(strings.Fields(out))
			Expect(nodeCount).To(BeNumerically(">=", 2),
				"this spec distinguishes the targeted node from the others, which needs more than one. "+
					"`make setup-test-e2e` builds a three-node cluster; see test/e2e/kind-config.yaml (F-109)")

			By("creating the fanout namespace")
			_, err = utils.Run(exec.Command("kubectl", "create", "ns", namespace))
			Expect(err).NotTo(HaveOccurred())

			By("creating a dummy-ups-backed stack whose agent selects no node in particular")
			manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: fanout-e2e-ups
spec:
  displayName: Fanout E2E Dummy UPS
  driver: dummy-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: fanout-e2e-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: fanout-e2e-ups
  image:
    repository: %[2]s
    tag: %[5]s
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
---
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: %[6]s
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: fanout-e2e-nutserver
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
`, namespace, nutServerRepository, upsmonAgentRepository, nodeActuatorRepository, operandImageTag, agentName)

			// Retried for the same reason the handoff spec retries: every kind here has a mutating
			// webhook, so an apply lands "connection refused" until the manager's webhook server serves.
			applyFixture := func(g Gomega) {
				applyCmd := exec.Command("kubectl", "apply", "-f", "-")
				applyCmd.Stdin = strings.NewReader(manifest)
				_, applyErr := utils.Run(applyCmd)
				g.Expect(applyErr).NotTo(HaveOccurred())
			}
			Eventually(applyFixture, 2*time.Minute, 5*time.Second).Should(Succeed())
		})

		AfterAll(func() {
			By("removing the fanout namespace and its cluster-scoped fixture")
			for _, args := range [][]string{
				{"delete", "nodepoweragent", agentName, "--ignore-not-found=true"},
				{"delete", "nutserver", "fanout-e2e-nutserver", "--ignore-not-found=true"},
				{"delete", "upsdevice", "fanout-e2e-ups", "--ignore-not-found=true"},
				{"delete", "ns", namespace, "--ignore-not-found=true", "--wait=false"},
			} {
				_, _ = utils.Run(exec.Command("kubectl", args...))
			}
		})

		It("runs one agent pod on every node", func() {
			// The DaemonSet tolerates every NoSchedule and NoExecute taint, so "every node" includes the
			// control plane. A count short of the node total means placement silently lost a node, and
			// an operator that misses a node at shutdown leaves it running on a dead battery.
			byNode := func(g Gomega) {
				cmd := exec.Command("kubectl", "-n", namespace, "get", "pods",
					"-l", "power.zalud.io/nodepoweragent="+agentName,
					"-o", "jsonpath={range .items[*]}{.spec.nodeName}{\" \"}{.metadata.name}{\"\\n\"}{end}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				found := map[string]string{}
				for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
					fields := strings.Fields(line)
					if len(fields) == 2 {
						found[fields[0]] = fields[1]
					}
				}
				g.Expect(found).To(HaveLen(nodeCount), "expected one agent pod per node")
				podsByNode = found
			}
			Eventually(byNode, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("waiting for every agent pod to be Ready")
			cmd := exec.Command("kubectl", "-n", namespace, "wait", "--for=condition=Ready",
				"pod", "-l", "power.zalud.io/nodepoweragent="+agentName, "--timeout=4m")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "not every agent pod became Ready")
		})

		It("delivers a signal to the node it names and to no other", func() {
			Expect(podsByNode).NotTo(BeEmpty(), "the placement spec must run first")
			for node, pod := range podsByNode {
				targetNode, targetPod = node, pod
				break
			}

			By("writing a shutdown signal naming exactly one node")
			executionID = fmt.Sprintf("e2e-fanout-%d", time.Now().UnixNano())
			payload, err := json.Marshal(map[string]any{
				"executionID":        executionID,
				"nodeName":           targetNode,
				"planConfigHash":     "e2e-fanout",
				"reason":             "E2EFanoutTargetingTest",
				"selectedUPSDevices": []string{"fanout-e2e-ups"},
				"shutdownFlow":       "fanout-e2e-flow",
				"timestamp":          time.Now().UTC().Format(time.RFC3339Nano),
			})
			Expect(err).NotTo(HaveOccurred())

			patch := fmt.Sprintf(`{"data":{%q:%q}}`,
				targetNode+".json", base64.StdEncoding.EncodeToString(payload))
			_, err = utils.Run(exec.Command("kubectl", "-n", namespace, "patch", "secret",
				agentName+"-node-signals", "--type=merge", "-p", patch))
			Expect(err).NotTo(HaveOccurred(), "Failed to write the signal Secret")

			By("confirming the named node's actuator accepts it")
			accepted := func(g Gomega) {
				logs, logErr := utils.Run(exec.Command("kubectl", "-n", namespace, "logs", targetPod, "-c", "actuator"))
				g.Expect(logErr).NotTo(HaveOccurred())
				g.Expect(logs).To(ContainSubstring("simulate actuator accepted shutdown signal executionID=" + executionID))
			}
			Eventually(accepted, 3*time.Minute, 5*time.Second).Should(Succeed())

			// Checked after the positive half has already passed, so the signal has demonstrably been
			// delivered and read somewhere. Asserting silence first would pass while the Secret was
			// still propagating and prove only that nothing had happened yet.
			By("confirming no other node's actuator accepted it")
			for node, pod := range podsByNode {
				if node == targetNode {
					continue
				}
				logs, logErr := utils.Run(exec.Command("kubectl", "-n", namespace, "logs", pod, "-c", "actuator"))
				Expect(logErr).NotTo(HaveOccurred())
				Expect(logs).NotTo(ContainSubstring(executionID),
					"an agent on a node the signal did not name acted on it, so signals are reaching "+
						"every node rather than the one addressed")
			}
		})
	})
}
