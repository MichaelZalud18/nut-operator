//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func quickstartCommand(parent context.Context, timeout time.Duration, input []byte, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The cert script can leave kubectl running if only its shell is killed.
	// Cancel the owned group immediately, including children that ignore TERM.
	// Cmd.Wait joins cancellation and pipe copying before utils.Run returns.
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	return utils.Run(cmd)
}

func TestQuickstartCommandDeadline(t *testing.T) {
	started := time.Now()
	_, err := quickstartCommand(context.Background(), 50*time.Millisecond, nil, "sleep", "5")
	if err == nil {
		t.Fatal("command outlived its deadline without an error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("command deadline did not bound execution: %s", elapsed)
	}
}

// TestE2E and hack/test-kind.py require private cluster ownership before any spec runs.
// This spec owns its installation so randomized suite order cannot supply missing prerequisites.
var _ = Describe("Two-UPS quickstart install-to-plan", Ordered, Serial, Label("quickstart"), func() {
	It("publishes the documented plan from the BYO-cert install and shipped example", func() {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		// Cleanups run in reverse order: retain sticky cancellation through all
		// command cleanup so interruption cannot start new detached groups.
		DeferCleanup(stop)
		root, err := utils.GetProjectDir()
		Expect(err).NotTo(HaveOccurred())
		example := filepath.Join(root, "docs", "examples", "quickstart")
		bundle := filepath.Join(root, "dist", "install-byo-cert.yaml")
		run := func(args ...string) string {
			out, err := quickstartCommand(ctx, 90*time.Second, nil, "kubectl", args...)
			ExpectWithOffset(1, err).NotTo(HaveOccurred(), out)
			return out
		}
		apply := func(data []byte) {
			out, err := quickstartCommand(ctx, 90*time.Second, data, "kubectl", "apply", "-f", "-")
			ExpectWithOffset(1, err).NotTo(HaveOccurred(), out)
		}
		var topology []byte
		var nodeNames []string
		DeferCleanup(func() {
			// Keep the manager alive while operand finalizers remove their owned resources.
			// Attempt every cleanup even when an earlier command fails; the owned Kind
			// harness remains responsible for final cluster removal on any suite failure.
			var failures []string
			cleanup := func(input []byte, args ...string) {
				out, err := quickstartCommand(ctx, 100*time.Second, input, "kubectl", args...)
				if err != nil {
					failures = append(failures, fmt.Sprintf("kubectl %v: %v: %s", args, err, out))
				}
			}
			for _, file := range []string{"shutdown.yaml", "ups-nut.yaml"} {
				cleanup(nil, "delete", "-f", filepath.Join(example, file), "--ignore-not-found", "--timeout=90s")
			}
			if len(topology) > 0 {
				cleanup(topology, "delete", "-f", "-", "--ignore-not-found", "--timeout=90s")
			}
			cleanup(nil, "delete", "-f", filepath.Join(example, "bootstrap.yaml"), "--ignore-not-found", "--timeout=90s")
			for _, node := range nodeNames {
				cleanup(nil, "label", "node", node, "power.example.com/quickstart-role-")
			}
			cleanup(nil, "delete", "-f", bundle, "--ignore-not-found", "--timeout=90s")
			cleanup(nil, "delete", "namespace", namespace, "--ignore-not-found", "--timeout=90s")
			Expect(failures).To(BeEmpty(), strings.Join(failures, "\n"))
		})

		By("applying the documented installer and provisioning its certificate")
		run("apply", "--server-side", "-f", bundle)
		patch, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []map[string]string{{"name": "manager", "image": managerImage, "imagePullPolicy": "IfNotPresent"}}}}}})
		Expect(err).NotTo(HaveOccurred())
		run("patch", "deployment", "nut-operator-controller-manager", "-n", namespace, "--type=strategic", "-p", string(patch))
		out, err := quickstartCommand(ctx, 90*time.Second, nil, filepath.Join(root, "hack", "webhook-cert.sh"))
		Expect(err).NotTo(HaveOccurred(), out)
		out, err = quickstartCommand(ctx, 310*time.Second, nil, "kubectl", "rollout", "status", "deployment/nut-operator-controller-manager", "-n", namespace, "--timeout=5m")
		Expect(err).NotTo(HaveOccurred(), out)
		Eventually(func(g Gomega) {
			probe := []byte(`{"apiVersion":"power.zalud.io/v1alpha1","kind":"PowerManagementCluster","metadata":{"name":"quickstart-admission-probe"},"spec":{"operandNamespace":{"name":"kube-system","create":true},"storage":{"mode":"Disabled"}}}`)
			out, err := quickstartCommand(ctx, 30*time.Second, probe, "kubectl", "apply", "--dry-run=server", "-f", "-")
			g.Expect(err).To(HaveOccurred())
			g.Expect(out).To(ContainSubstring("spec.operandNamespace.name"))
			g.Expect(out).To(ContainSubstring("reserved"))
		}).WithTimeout(3 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("binding inventory to the disposable cluster's actual Nodes")
		var nodes corev1.NodeList
		Expect(json.Unmarshal([]byte(run("get", "nodes", "-o", "json")), &nodes)).To(Succeed())
		var control string
		var workers []string
		for _, node := range nodes.Items {
			if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
				control = node.Name
			} else {
				workers = append(workers, node.Name)
			}
		}
		Expect(nodes.Items).To(HaveLen(3))
		Expect(control).NotTo(BeEmpty())
		Expect(workers).To(HaveLen(2))
		sort.Strings(workers)
		nodeNames = append([]string{control}, workers...)
		run("label", "node", control, "power.example.com/quickstart-role=control")
		for _, node := range workers {
			run("label", "node", node, "power.example.com/quickstart-role=worker")
		}
		out, err = quickstartCommand(ctx, 10*time.Second, nil, "python3", filepath.Join(example, "render.py"), "--control", control, "--worker-a", workers[0], "--worker-b", workers[1])
		Expect(err).NotTo(HaveOccurred())
		topology = []byte(out)

		By("applying bootstrap with only operand image references adapted for Kind")
		bootstrap, err := os.ReadFile(filepath.Join(example, "bootstrap.yaml"))
		Expect(err).NotTo(HaveOccurred())
		decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(bootstrap), 4096)
		for {
			var obj unstructured.Unstructured
			err := decoder.Decode(&obj)
			if err == io.EOF {
				break
			}
			Expect(err).NotTo(HaveOccurred())
			if obj.GetKind() == "PowerManagementCluster" {
				for operand, repository := range map[string]string{"nutServer": nutServerRepository, "upsmonAgent": upsmonAgentRepository, "actuator": nodeActuatorRepository} {
					Expect(unstructured.SetNestedField(obj.Object, repository, "spec", "images", operand, "repository")).To(Succeed())
					Expect(unstructured.SetNestedField(obj.Object, operandImageTag, "spec", "images", operand, "tag")).To(Succeed())
				}
			}
			data, err := json.Marshal(obj.Object)
			Expect(err).NotTo(HaveOccurred())
			apply(data)
		}
		run("apply", "-f", filepath.Join(example, "ups-nut.yaml"))
		apply(topology)
		run("apply", "-f", filepath.Join(example, "shutdown.yaml"))

		By("checking real NUT telemetry, capability matching and ready agent coverage")
		Eventually(func(g Gomega) {
			for _, name := range []string{"quickstart-ups-a", "quickstart-ups-b"} {
				out, err := quickstartCommand(ctx, 30*time.Second, nil, "kubectl", "get", "upsdevice", name, "-o", "json")
				g.Expect(err).NotTo(HaveOccurred())
				var device power.UPSDevice
				g.Expect(json.Unmarshal([]byte(out), &device)).To(Succeed())
				g.Expect(device.Status.LastPollTime).NotTo(BeNil())
				g.Expect(device.Status.Capability).NotTo(BeNil())
				g.Expect(device.Status.Capability.ProfileID).To(Equal("quickstart-fixture"))
				g.Expect(meta.IsStatusConditionTrue(device.Status.Conditions, "IdentityVerified")).To(BeTrue())
				g.Expect(meta.IsStatusConditionTrue(device.Status.Conditions, "Ready")).To(BeTrue())
			}
			for name, selected := range map[string][]string{"quickstart-control": {control}, "quickstart-workers": workers} {
				out, err := quickstartCommand(ctx, 30*time.Second, nil, "kubectl", "get", "nodepoweragent", name, "-o", "json")
				g.Expect(err).NotTo(HaveOccurred())
				var agent power.NodePowerAgent
				g.Expect(json.Unmarshal([]byte(out), &agent)).To(Succeed())
				g.Expect(agent.Status.SelectedNodes).To(ConsistOf(selected))
				g.Expect(agent.Status.ReadyNodeCount).To(Equal(int32(len(selected))))
				g.Expect(string(agent.Spec.Mode)).To(Equal("DryRun"))
				g.Expect(string(agent.Spec.Shutdown.ActuatorPolicy)).To(Equal("Simulate"))
			}
		}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())

		By("checking the published waves and diagnostics")
		Eventually(func(g Gomega) {
			out, err := quickstartCommand(ctx, 30*time.Second, nil, "kubectl", "get", "shutdownflow", "quickstart", "-o", "json")
			g.Expect(err).NotTo(HaveOccurred())
			var flow power.ShutdownFlow
			g.Expect(json.Unmarshal([]byte(out), &flow)).To(Succeed())
			g.Expect(string(flow.Spec.Mode)).To(Equal("DryRun"))
			g.Expect(meta.IsStatusConditionTrue(flow.Status.Conditions, "Accepted")).To(BeTrue())
			g.Expect(flow.Status.ObservedGeneration).To(Equal(flow.Generation))
			g.Expect(flow.Status.PublishedArtifact).NotTo(BeNil())
			g.Expect(flow.Status.CompiledWaves).To(HaveLen(3))
			for i, groups := range [][]string{{"drain-workers"}, {"stop-workers"}, {"stop-control"}} {
				g.Expect(flow.Status.CompiledWaves[i].Groups).To(Equal(groups))
			}
			g.Expect(flow.Status.EstimatedDuration).To(Equal(&metav1.Duration{Duration: 4 * time.Minute}))
			for _, d := range flow.Status.CompileDiagnostics {
				g.Expect(d.Severity).NotTo(Equal("Error"), fmt.Sprintf("%+v", d))
			}
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
	})
})
