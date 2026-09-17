//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	"github.com/MichaelZalud18/nut-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// TEST-2 ends at Simulate. It neither proves guest shutdown nor substitutes injected
// signals for the production trigger/planner/executor path.
func logicalShutdownFlowSpecs() {
	It("executes a logical ShutdownFlow from real dummy-ups telemetry through ordered drain and simulated actuation", Label("TEST-2"), func() {
		var nodes corev1.NodeList
		Expect(logicalFlowGet(&nodes, "nodes", "-l", "!node-role.kubernetes.io/control-plane")).To(Succeed())
		Expect(nodes.Items).To(HaveLen(2), "the shared Kind fixture must have two workers")
		Expect(nodes.Items[0].Spec.Unschedulable).To(BeFalse())
		Expect(nodes.Items[1].Spec.Unschedulable).To(BeFalse())
		var allPods corev1.PodList
		Expect(logicalFlowGet(&allPods, "pods", "-A")).To(Succeed())
		target, other, placementErr := logicalFlowWorkers(nodes.Items, allPods.Items)
		Expect(placementErr).NotTo(HaveOccurred())
		for _, node := range nodes.Items {
			for _, taint := range node.Spec.Taints {
				Expect(taint.Key).NotTo(Equal(flowDrainTaint), "TEST-2 must own its reservation")
			}
		}
		// Reserve the worker, then relocate shared system Deployments before starting
		// the fixture. A second live pod guard still runs just before enforcement.
		cleanup := &logicalFlowCleanup{target: target}
		DeferCleanup(func() error { return cleanup.run(utils.Run) })
		logicalFlowRelocateSystemDeployments(target, other, cleanup)
		for _, ns := range []string{flowOperandNamespace, flowWorkNamespace} {
			cleanup.namespaces = append(cleanup.namespaces, ns)
			_, err := utils.Run(exec.Command("kubectl", "create", "namespace", ns))
			Expect(err).NotTo(HaveOccurred())
		}
		defer func() {
			if CurrentSpecReport().Failed() {
				utils.DumpNamespaceDiagnostics(flowOperandNamespace)
				utils.DumpNamespaceDiagnostics(flowWorkNamespace)
			}
		}()

		By("starting isolated audit storage and workloads using pinned fixture and suite images")
		for _, manifest := range []string{logicalFlowPostgres(other), logicalFlowWorkloads(target, other)} {
			Expect(applyFixtureManifest(manifest)).To(Succeed())
		}
		logicalFlowReadyPod(flowOperandNamespace, "app=test2-postgres")
		logicalFlowReadyPod(flowWorkNamespace, "app=test2-scale")
		logicalFlowReadyPod(flowWorkNamespace, "app=test2-drain")
		untouched := logicalFlowReadyPod(flowWorkNamespace, "app=test2-untouched")
		storage := logicalFlowStorage()
		cleanup.manifests = append(cleanup.manifests, storage)
		Expect(applyFixtureManifest(storage)).To(Succeed())
		Eventually(func(g Gomega) {
			var cluster power.PowerManagementCluster
			g.Expect(logicalFlowGet(&cluster, "powermanagementcluster", "test2-cluster")).To(Succeed())
			g.Expect(cluster.Status.Storage.Ready).To(BeTrue())
		}, 3*time.Minute, 2*time.Second).Should(Succeed())

		By("starting real dummy-ups telemetry and the production-rendered Simulate agent")
		Expect(applyFixtureManifest(logicalFlowSequence(false))).To(Succeed())
		stack := logicalFlowStack(target, other)
		cleanup.manifests = append(cleanup.manifests, stack)
		Expect(applyFixtureManifest(stack)).To(Succeed())
		Eventually(func(g Gomega) {
			var ups power.UPSDevice
			g.Expect(logicalFlowGet(&ups, "upsdevice", "test2-ups")).To(Succeed())
			g.Expect(string(ups.Status.Phase)).To(Equal("Online"))
		}, 2*time.Minute, time.Second).Should(Succeed())
		readyAgent := logicalFlowReadyPod(flowOperandNamespace, "power.zalud.io/nodepoweragent="+flowAgent)
		Expect(logicalFlowToleratesReservation(readyAgent.Spec.Tolerations)).To(BeTrue(), "the rendered agent must tolerate TEST-2's reservation")
		Eventually(func(g Gomega) {
			var agent power.NodePowerAgent
			g.Expect(logicalFlowGet(&agent, "nodepoweragent", flowAgent)).To(Succeed())
			g.Expect(string(agent.Status.Phase)).To(Equal("Ready"))
			g.Expect(agent.Status.SelectedNodes).To(Equal([]string{target}))
		}, 3*time.Minute, time.Second).Should(Succeed())

		By("rejecting Enforce without approval at the real admission boundary")
		unapproved := logicalFlowManifest(target, "Enforce", false)
		cleanup.manifests = append(cleanup.manifests, unapproved)
		cmd := exec.Command("kubectl", "apply", "--dry-run=server", "-f", "-")
		cmd.Stdin = strings.NewReader(unapproved)
		out, err := utils.Run(cmd)
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("metadata.annotations[" + flowApproval + "]"))

		By("observing an eligible DryRun execution with no workload or signal effects")
		Expect(applyFixtureManifest(logicalFlowManifest(target, "DryRun", false))).To(Succeed())
		Eventually(func(g Gomega) {
			flow := logicalFlowRead(g)
			g.Expect(flow.Status.TriggerEvaluation).NotTo(BeNil())
			g.Expect(flow.Status.TriggerEvaluation.Eligible).To(BeFalse())
		}, time.Minute, time.Second).Should(Succeed())
		// ConfigMap changes are watched by the production NUTServer controller and
		// roll the real dummy-ups operand. No telemetry/status field is patched.
		Expect(applyFixtureManifest(logicalFlowSequence(true))).To(Succeed())
		Eventually(func(g Gomega) {
			var ups power.UPSDevice
			g.Expect(logicalFlowGet(&ups, "upsdevice", "test2-ups")).To(Succeed())
			g.Expect(string(ups.Status.Phase)).To(Equal("OnBattery"))
		}, 3*time.Minute, time.Second).Should(Succeed())
		var dryExecution string
		Eventually(func(g Gomega) {
			flow := logicalFlowRead(g)
			g.Expect(flow.Status.TriggerEvaluation).NotTo(BeNil())
			g.Expect(flow.Status.TriggerEvaluation.Eligible).To(BeTrue())
			g.Expect(flow.Status.LastExecution).NotTo(BeNil())
			g.Expect(flow.Status.LastExecution.Phase).To(Equal(power.ShutdownExecutionPhaseCompleted))
			g.Expect(flow.Status.LastExecution.DryRun).To(BeTrue())
			dryExecution = flow.Status.LastExecution.ExecutionID
		}, 3*time.Minute, time.Second).Should(Succeed())
		var deployment appsv1.Deployment
		Expect(logicalFlowGet(&deployment, "deployment", "test2-scale", "-n", flowWorkNamespace)).To(Succeed())
		Expect(*deployment.Spec.Replicas).To(Equal(int32(1)))
		var drain corev1.Pod
		Expect(logicalFlowGet(&drain, "pod", "test2-drain", "-n", flowWorkNamespace)).To(Succeed())
		Expect(drain.DeletionTimestamp).To(BeNil())
		logicalFlowNoSignal(Default, target)
		var node corev1.Node
		Expect(logicalFlowGet(&node, "node", target)).To(Succeed())
		Expect(node.Spec.Unschedulable).To(BeFalse())

		By("approving Enforce and observing scale before drain before release")
		Expect(logicalFlowGet(&allPods, "pods", "-A")).To(Succeed())
		Expect(logicalFlowDrainBlockers(target, allPods.Items)).To(BeEmpty(), "non-fixture workloads appeared on the drain worker")
		Expect(applyFixtureManifest(logicalFlowManifest(target, "Enforce", true))).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(logicalFlowGet(&deployment, "deployment", "test2-scale", "-n", flowWorkNamespace)).To(Succeed())
			g.Expect(*deployment.Spec.Replicas).To(BeZero())
			g.Expect(logicalFlowGet(&drain, "pod", "test2-drain", "-n", flowWorkNamespace)).To(Succeed())
			g.Expect(drain.DeletionTimestamp).To(BeNil(), "drain ran before the scale observation window")
			logicalFlowNoSignal(g, target)
		}, 2*time.Minute, time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			var pods corev1.PodList
			g.Expect(logicalFlowGet(&pods, "pods", "-n", flowWorkNamespace, "--field-selector=spec.nodeName="+target)).To(Succeed())
			g.Expect(pods.Items).To(BeEmpty(), "targeted workloads have not drained")
			g.Expect(logicalFlowGet(&node, "node", target)).To(Succeed())
			g.Expect(node.Spec.Unschedulable).To(BeTrue())
			logicalFlowNoSignal(g, target)
		}, time.Minute, time.Second).Should(Succeed())

		By("correlating the operator-generated signal with the eligible enforced execution")
		var signal nodeagent.ShutdownSignal
		var execution power.ShutdownExecutionStatus
		Eventually(func(g Gomega) {
			flow := logicalFlowRead(g)
			g.Expect(flow.Status.LastExecution).NotTo(BeNil())
			execution = *flow.Status.LastExecution
			g.Expect(execution.ExecutionID).NotTo(Equal(dryExecution))
			g.Expect(execution.Phase).To(Equal(power.ShutdownExecutionPhaseCompleted))
			g.Expect(execution.DryRun).To(BeFalse())
			g.Expect(execution.Rehearsal).To(BeFalse())
			g.Expect(execution.SelectedUPSDevices).To(Equal([]string{"test2-ups"}))
			var secret corev1.Secret
			g.Expect(logicalFlowGet(&secret, "secret", flowAgent+"-node-signals", "-n", flowOperandNamespace)).To(Succeed())
			g.Expect(secret.Data).To(HaveKey(nodeagent.DeliveryChannelMarker))
			g.Expect(secret.Data).To(HaveLen(2), "only the delivery marker and the targeted node key are permitted")
			g.Expect(json.Unmarshal(secret.Data[target+".json"], &signal)).To(Succeed())
			g.Expect(signal.ExecutionID).To(Equal(execution.ExecutionID))
			g.Expect(signal.PlanConfigHash).To(Equal(execution.PlanConfigHash))
			g.Expect(signal.ShutdownFlow).To(Equal(flowName))
			g.Expect(signal.NodeName).To(Equal(target))
			g.Expect(signal.SelectedUPSDevices).To(Equal(execution.SelectedUPSDevices))
		}, 3*time.Minute, time.Second).Should(Succeed())
		agentPod := logicalFlowReadyPod(flowOperandNamespace, "power.zalud.io/nodepoweragent="+flowAgent)
		Eventually(func(g Gomega) {
			logs, err := utils.Run(exec.Command("kubectl", "logs", "-n", flowOperandNamespace, agentPod.Name, "-c", "actuator"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(logs).To(ContainSubstring("simulate actuator accepted shutdown signal executionID=" + execution.ExecutionID))
			g.Expect(logs).NotTo(ContainSubstring("simulate actuator accepted shutdown signal executionID=" + dryExecution))
		}, 2*time.Minute, 2*time.Second).Should(Succeed())
		logicalFlowAssertAudit(execution.ExecutionID, target)
		var survivor corev1.Pod
		Expect(logicalFlowGet(&survivor, "pod", untouched.Name, "-n", flowWorkNamespace)).To(Succeed())
		Expect(survivor.UID).To(Equal(untouched.UID))
		Expect(survivor.DeletionTimestamp).To(BeNil())
		Expect(survivor.Status.Phase).To(Equal(corev1.PodRunning))
		Expect(logicalFlowGet(&node, "node", other)).To(Succeed())
		Expect(node.Spec.Unschedulable).To(BeFalse())

		By("separately rejecting an injected expired signal using the rendered Simulate actuator")
		logicalFlowRejectStale(target, signal)
	})
}

// Keep the manager in place. Other shared Deployments are moved explicitly before
// the scenario, so worker selection never depends on incidental pod distribution.
func logicalFlowWorkers(nodes []corev1.Node, pods []corev1.Pod) (string, string, error) {
	if len(nodes) != 2 {
		return "", "", fmt.Errorf("expected two workers, got %d", len(nodes))
	}
	managerNode := ""
	for _, pod := range pods {
		if pod.Namespace == namespace && pod.Labels["control-plane"] == "controller-manager" && pod.DeletionTimestamp == nil {
			if managerNode != "" {
				return "", "", fmt.Errorf("manager placement has not converged")
			}
			managerNode = pod.Spec.NodeName
		}
	}
	if managerNode == "" {
		return "", "", fmt.Errorf("manager has no scheduled pod")
	}
	nodes = append([]corev1.Node(nil), nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	for i, node := range nodes {
		if node.Name != managerNode {
			return node.Name, nodes[1-i].Name, nil
		}
	}
	return "", "", fmt.Errorf("no worker separate from manager")
}

func logicalFlowRelocateSystemDeployments(target, survivor string, cleanup *logicalFlowCleanup) {
	By("temporarily placing shared system Deployments on the surviving worker")
	_, err := utils.Run(exec.Command("kubectl", "taint", "node", target, flowDrainTaint+"=true:NoSchedule"))
	Expect(err).NotTo(HaveOccurred())
	for _, ns := range []string{"kube-system", "cert-manager"} {
		var deployments appsv1.DeploymentList
		Expect(logicalFlowGet(&deployments, "deployments", "-n", ns)).To(Succeed())
		for _, deployment := range deployments.Items {
			original := deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"]
			if original == survivor {
				continue
			}
			cleanup.originals = append(cleanup.originals, *deployment.DeepCopy())
			Expect(logicalFlowPatchPlacement(utils.Run, deployment, survivor)).To(Succeed())
			Expect(logicalFlowDeploymentReady(utils.Run, deployment)).To(Succeed())
		}
	}
	Eventually(func(g Gomega) {
		var pods corev1.PodList
		g.Expect(logicalFlowGet(&pods, "pods", "-A")).To(Succeed())
		g.Expect(logicalFlowDrainBlockers(target, pods.Items)).To(BeEmpty(), "system rollouts must finish before the outage")
	}, 3*time.Minute, time.Second).Should(Succeed())
}

func logicalFlowPatchPlacement(run func(*exec.Cmd) (string, error), deployment appsv1.Deployment, hostname any) error {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"uid": deployment.UID},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeSelector": map[string]any{"kubernetes.io/hostname": hostname},
		}}},
	})
	if err != nil {
		return err
	}
	_, err = logicalFlowRunBounded(run, time.Minute, "", "patch", "deployment", deployment.Name, "-n", deployment.Namespace, "--type=merge", "-p", string(patch))
	return err
}

func logicalFlowDeploymentReady(run func(*exec.Cmd) (string, error), deployment appsv1.Deployment) error {
	_, err := logicalFlowRunBounded(run, 3*time.Minute, "", "rollout", "status", "deployment/"+deployment.Name, "-n", deployment.Namespace, "--timeout=3m")
	return err
}

func logicalFlowToleratesReservation(tolerations []corev1.Toleration) bool {
	taint := &corev1.Taint{Key: flowDrainTaint, Value: "true", Effect: corev1.TaintEffectNoSchedule}
	for _, toleration := range tolerations {
		if toleration.ToleratesTaint(klog.Background(), taint, false) {
			return true
		}
	}
	return false
}

func logicalFlowDrainBlockers(node string, pods []corev1.Pod) []string {
	var blockers []string
	for _, pod := range pods {
		if pod.Spec.NodeName != node || pod.Namespace == flowWorkNamespace || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		// CNI/kube-proxy DaemonSets and mirror pods are excluded by the real drain.
		if pod.Annotations["kubernetes.io/config.mirror"] != "" {
			continue
		}
		daemon := false
		for _, owner := range pod.OwnerReferences {
			if owner.APIVersion == "apps/v1" && owner.Kind == "DaemonSet" {
				daemon = true
			}
		}
		if !daemon {
			blockers = append(blockers, pod.Namespace+"/"+pod.Name)
		}
	}
	return blockers
}

func logicalFlowGet(object any, args ...string) error {
	args = append([]string{"get"}, args...)
	args = append(args, "-o", "json")
	out, err := utils.Run(exec.Command("kubectl", args...))
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), object)
}

func logicalFlowRead(g Gomega) power.ShutdownFlow {
	var flow power.ShutdownFlow
	g.Expect(logicalFlowGet(&flow, "shutdownflow", flowName)).To(Succeed())
	return flow
}

func logicalFlowNoSignal(g Gomega, node string) {
	var secret corev1.Secret
	g.Expect(logicalFlowGet(&secret, "secret", flowAgent+"-node-signals", "-n", flowOperandNamespace)).To(Succeed())
	g.Expect(secret.Data).NotTo(HaveKey(node + ".json"))
}

func logicalFlowReadyPod(ns, selector string) corev1.Pod {
	var pod corev1.Pod
	Eventually(func(g Gomega) {
		var pods corev1.PodList
		g.Expect(logicalFlowGet(&pods, "pods", "-n", ns, "-l", selector)).To(Succeed())
		var ready []corev1.Pod
		for _, candidate := range pods.Items {
			if candidate.DeletionTimestamp != nil {
				continue
			}
			for _, condition := range candidate.Status.Conditions {
				if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
					ready = append(ready, candidate)
				}
			}
		}
		g.Expect(ready).To(HaveLen(1))
		pod = ready[0]
	}, 4*time.Minute, 2*time.Second).Should(Succeed())
	return pod
}

func logicalFlowAssertAudit(execution, target string) {
	// Both values come from the owned API; reject unexpected characters before SQL interpolation.
	Expect(execution).To(MatchRegexp(`^[a-f0-9-]+$`))
	Expect(target).To(MatchRegexp(`^[a-z0-9.-]+$`))
	query := fmt.Sprintf(`SELECT json_agg(row_to_json(a) ORDER BY a.started_at) FROM (
SELECT a.group_name, a.action, g.selected_targets->0->>'kind' AS target_kind,
g.selected_targets->0->>'namespace' AS target_namespace,
g.selected_targets->0->>'name' AS target_name, a.outcome, a.dry_run,
a.started_at, a.completed_at, a.details FROM power.shutdownflow_action_attempts a
JOIN power.shutdownflow_execution_groups g USING (execution_id, wave_index, group_name)
WHERE a.execution_id = '%s' AND a.action IN ('ScaleWorkload', 'DrainNodes', 'AgentShutdown')
) a`, execution)
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, "test2-postgres", "--",
			"psql", "-U", "test2", "-d", "test2", "-tAc", query))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(logicalFlowAuditEvidence([]byte(out), target)).To(Succeed())
	}, time.Minute, time.Second).Should(Succeed())
}

type logicalFlowAttempt struct {
	Group     string    `json:"group_name"`
	Action    string    `json:"action"`
	Kind      string    `json:"target_kind"`
	Namespace string    `json:"target_namespace"`
	Name      string    `json:"target_name"`
	Outcome   string    `json:"outcome"`
	DryRun    bool      `json:"dry_run"`
	Started   time.Time `json:"started_at"`
	Completed time.Time `json:"completed_at"`
	Details   struct {
		SelectedTargets int `json:"selectedTargetCount"`
		EvictedPods     int `json:"evictedPods"`
		Changed         int `json:"changed"`
	} `json:"details"`
}

func logicalFlowAuditEvidence(data []byte, node string) error {
	var rows []logicalFlowAttempt
	if err := json.Unmarshal(data, &rows); err != nil {
		return err
	}
	if len(rows) != 3 {
		return fmt.Errorf("want three effectful action records, got %d: %s", len(rows), data)
	}
	for i, action := range []string{"ScaleWorkload", "DrainNodes", "AgentShutdown"} {
		row := rows[i]
		if row.Action != action || row.Outcome != "Succeeded" || row.DryRun || row.Started.IsZero() || row.Completed.Before(row.Started) {
			return fmt.Errorf("invalid action evidence: %+v", row)
		}
		if i > 0 && row.Started.Before(rows[i-1].Completed) {
			return fmt.Errorf("actions overlap or are out of order: %s", data)
		}
	}
	if rows[0].Kind != "Deployment" || rows[0].Namespace != flowWorkNamespace || rows[0].Name != "test2-scale" {
		return fmt.Errorf("scale targeted the wrong workload: %+v", rows[0])
	}
	if rows[1].Kind != "Node" || rows[1].Name != node {
		return fmt.Errorf("drain targeted the wrong node: %+v", rows[1])
	}
	if rows[0].Details.SelectedTargets != 1 || rows[0].Details.Changed != 1 || rows[1].Details.SelectedTargets != 1 || rows[1].Details.EvictedPods < 1 {
		return fmt.Errorf("missing targeted scale/drain effects: %s", data)
	}
	return nil
}

func logicalFlowRejectStale(node string, signal nodeagent.ShutdownSignal) {
	var sets appsv1.DaemonSetList
	Expect(logicalFlowGet(&sets, "daemonsets", "-n", flowOperandNamespace, "-l", "power.zalud.io/nodepoweragent="+flowAgent)).To(Succeed())
	Expect(sets.Items).To(HaveLen(1))
	pod, err := logicalFlowStalePod(sets.Items[0], node)
	Expect(err).NotTo(HaveOccurred())
	signal.ExecutionID = "test2-injected-stale"
	signal.Timestamp = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano)
	payload, err := json.Marshal(signal)
	Expect(err).NotTo(HaveOccurred())
	secret := corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "test2-stale-signal", Namespace: flowOperandNamespace},
		Data:       map[string][]byte{node + ".json": payload, nodeagent.DeliveryChannelMarker: []byte("ready")}}
	Expect(applyFixtureManifest(logicalFlowJSONList(secret, pod))).To(Succeed())
	Eventually(func(g Gomega) {
		logs, err := utils.Run(exec.Command("kubectl", "logs", "-n", flowOperandNamespace, pod.Name, "-c", "actuator"))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(logs).To(ContainSubstring("SignalStale"))
		g.Expect(logs).NotTo(ContainSubstring("simulate actuator accepted shutdown signal"))
	}, 2*time.Minute, 2*time.Second).Should(Succeed())
}
