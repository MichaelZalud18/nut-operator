//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

//go:embed fixtures/ex34_barrier.py
var executionBarrierSource string

// These specs share the shipped manager lifecycle and TEST-2's owned resources.
// Each gets a fresh telemetry/Simulate fixture; none inject readiness or signals.
func executionSafetySpecs() {
	for _, scenario := range []string{"baseline", "flow mode change", "agent specification change"} {
		It("EX-34 checks execution safety after a hook barrier: "+scenario, Label("EX-34"), func() {
			target := executionSafetyFixture()
			flowManifest := executionSafetyFlow(target)
			// Fixture cleanup already owns flowName, even when this creation fails.
			Expect(applyFixtureManifest(flowManifest)).To(Succeed())
			Eventually(func(g Gomega) {
				flow := logicalFlowRead(g)
				g.Expect(flow.Status.TriggerEvaluation).NotTo(BeNil())
				g.Expect(flow.Status.TriggerEvaluation.Eligible).To(BeFalse())
			}, time.Minute, time.Second).Should(Succeed())
			Expect(applyFixtureManifest(logicalFlowSequence(true))).To(Succeed())
			var execution string
			Eventually(func(g Gomega) {
				state := executionBarrierRead(g)
				g.Expect(state).To(HaveLen(1))
				for id, receipt := range state {
					g.Expect(receipt.DryRun).To(BeFalse())
					g.Expect(receipt.Released).To(BeFalse())
					g.Expect(receipt.TimedOut).To(BeFalse())
					execution = id
				}
			}, 3*time.Minute, time.Second).Should(Succeed())
			Expect(execution).To(MatchRegexp(`^[a-f0-9-]+$`))
			logicalFlowNoSignal(Default, target)
			executionSafetyReplicas(Default, 1)
			By("recording the original execution before changing API state")
			_, _ = fmt.Fprintf(GinkgoWriter, "EX-34 scenario=%s originalExecution=%s\n", scenario, execution)
			switch scenario {
			case "flow mode change":
				var before power.ShutdownFlow
				Expect(logicalFlowGet(&before, "shutdownflow", flowName)).To(Succeed())
				out, err := utils.Run(exec.Command("kubectl", "patch", "shutdownflow", flowName, "--type=merge", "-p", `{"spec":{"mode":"DryRun"}}`, "-o", "json"))
				Expect(err).NotTo(HaveOccurred())
				var changed power.ShutdownFlow
				Expect(json.Unmarshal([]byte(out), &changed)).To(Succeed())
				Expect(changed.Spec.Mode).To(Equal(power.ShutdownFlowModeDryRun))
				Expect(changed.Generation).To(BeNumerically(">", before.Generation))
				_, _ = fmt.Fprintf(GinkgoWriter, "API acknowledged flow generation=%d resourceVersion=%s\n", changed.Generation, changed.ResourceVersion)
			case "agent specification change":
				var before power.NodePowerAgent
				Expect(logicalFlowGet(&before, "nodepoweragent", flowAgent)).To(Succeed())
				// An admitted Simulate setting change changes generation while preserving
				// selected nodes. The old execution must not adopt the new generation.
				out, err := utils.Run(exec.Command("kubectl", "patch", "nodepoweragent", flowAgent, "--type=merge", "-p", `{"spec":{"shutdown":{"signalTTL":"3m"}}}`, "-o", "json"))
				Expect(err).NotTo(HaveOccurred())
				var changed power.NodePowerAgent
				Expect(json.Unmarshal([]byte(out), &changed)).To(Succeed())
				Expect(changed.Generation).To(BeNumerically(">", before.Generation))
				Expect(changed.UID).To(Equal(before.UID))
				_, _ = fmt.Fprintf(GinkgoWriter, "API acknowledged agent generation=%d resourceVersion=%s\n", changed.Generation, changed.ResourceVersion)
			}
			By("releasing only the original execution's observed hook barrier")
			_, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, modularReceiverName, "--", "python3", "-c",
				`import sys,urllib.request; urllib.request.urlopen(urllib.request.Request("http://127.0.0.1:8080/release/"+sys.argv[1],data=b""))`, execution))
			Expect(err).NotTo(HaveOccurred())
			Eventually(func(g Gomega) {
				receipt := executionBarrierRead(g)[execution]
				g.Expect(receipt.Released).To(BeTrue())
				g.Expect(receipt.TimedOut).To(BeFalse(), "a timed-out advisory hook cannot synchronize this test")
			}, 10*time.Second, time.Second).Should(Succeed())
			By("checking durable completion and effects for that execution ID")
			executionSafetyAudit(execution, scenario)
			if scenario == "baseline" {
				Eventually(func(g Gomega) {
					executionSafetyReplicas(g, 0)
					var secret corev1.Secret
					g.Expect(logicalFlowGet(&secret, "secret", flowAgent+"-node-signals", "-n", flowOperandNamespace)).To(Succeed())
					var signal nodeagent.ShutdownSignal
					g.Expect(json.Unmarshal(secret.Data[target+".json"], &signal)).To(Succeed())
					g.Expect(signal.ExecutionID).To(Equal(execution))
					g.Expect(signal.NodeName).To(Equal(target))
				}, time.Minute, time.Second).Should(Succeed())
				pod := logicalFlowReadyPod(flowOperandNamespace, "power.zalud.io/nodepoweragent="+flowAgent)
				Eventually(func(g Gomega) {
					logs, err := utils.Run(exec.Command("kubectl", "logs", "-n", flowOperandNamespace, pod.Name, "-c", "actuator"))
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(logs).To(ContainSubstring("simulate actuator accepted shutdown signal executionID=" + execution))
				}, 2*time.Minute, time.Second).Should(Succeed())
			} else {
				executionSafetyReplicas(Default, 1)
				// A new execution has its own authorization. It also encounters its own
				// barrier, which this test never releases. Audit assertions below remain
				// scoped to the original ID even if the controller starts a replacement.
				var secret corev1.Secret
				Expect(logicalFlowGet(&secret, "secret", flowAgent+"-node-signals", "-n", flowOperandNamespace)).To(Succeed())
				if payload, present := secret.Data[target+".json"]; present {
					var signal nodeagent.ShutdownSignal
					Expect(json.Unmarshal(payload, &signal)).To(Succeed())
					Expect(signal.ExecutionID).NotTo(Equal(execution))
				}
			}
		})
	}
}

func executionSafetyFixture() string {
	var nodes corev1.NodeList
	Expect(logicalFlowGet(&nodes, "nodes", "-l", "!node-role.kubernetes.io/control-plane")).To(Succeed())
	var pods corev1.PodList
	Expect(logicalFlowGet(&pods, "pods", "-A")).To(Succeed())
	target, survivor, err := logicalFlowWorkers(nodes.Items, pods.Items)
	Expect(err).NotTo(HaveOccurred())
	for _, node := range nodes.Items {
		Expect(node.Spec.Unschedulable).To(BeFalse())
		for _, taint := range node.Spec.Taints {
			Expect(taint.Key).NotTo(Equal(flowDrainTaint))
		}
	}
	cleanup := &logicalFlowCleanup{target: target}
	DeferCleanup(func() error { return cleanup.run(utils.Run) })
	logicalFlowRelocateSystemDeployments(target, survivor, cleanup)
	for _, ns := range []string{flowOperandNamespace, flowWorkNamespace} {
		cleanup.namespaces = append(cleanup.namespaces, ns)
		_, err := utils.Run(exec.Command("kubectl", "create", "namespace", ns))
		Expect(err).NotTo(HaveOccurred())
	}
	DeferCleanup(func() {
		if CurrentSpecReport().Failed() {
			utils.DumpNamespaceDiagnostics(flowOperandNamespace)
			utils.DumpNamespaceDiagnostics(flowWorkNamespace)
		}
	})
	// Workloads stay on the survivor so clearance of the target needs no drain.
	for _, manifest := range []string{logicalFlowPostgres(survivor), logicalFlowWorkloads(survivor, survivor), executionBarrierManifest(survivor)} {
		Expect(applyFixtureManifest(manifest)).To(Succeed())
	}
	logicalFlowReadyPod(flowOperandNamespace, "app=test2-postgres")
	Eventually(func() error {
		_, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, "test2-postgres", "--",
			"pg_isready", "-h", "test2-postgres."+flowOperandNamespace+".svc", "-U", "test2"))
		return err
	}, time.Minute, time.Second).Should(Succeed(), "PostgreSQL Service routing must be ready before storage configuration")
	logicalFlowReadyPod(flowOperandNamespace, "app="+modularReceiverName)
	logicalFlowReadyPod(flowWorkNamespace, "app=test2-scale")
	storage := logicalFlowStorage()
	cleanup.manifests = append(cleanup.manifests, storage)
	Expect(applyFixtureManifest(storage)).To(Succeed())
	Eventually(func(g Gomega) {
		var cluster power.PowerManagementCluster
		g.Expect(logicalFlowGet(&cluster, "powermanagementcluster", "test2-cluster")).To(Succeed())
		g.Expect(cluster.Status.Storage.Ready).To(BeTrue())
	}, 3*time.Minute, time.Second).Should(Succeed())
	Expect(applyFixtureManifest(logicalFlowSequence(false))).To(Succeed())
	stack := logicalFlowStack(target, survivor)
	cleanup.manifests = append(cleanup.manifests, stack)
	Expect(applyFixtureManifest(stack)).To(Succeed())
	Eventually(func(g Gomega) {
		var ups power.UPSDevice
		g.Expect(logicalFlowGet(&ups, "upsdevice", "test2-ups")).To(Succeed())
		g.Expect(ups.Status.Phase).To(Equal(power.UPSDevicePhaseOnline))
		var agent power.NodePowerAgent
		g.Expect(logicalFlowGet(&agent, "nodepoweragent", flowAgent)).To(Succeed())
		g.Expect(string(agent.Status.Phase)).To(Equal("Ready"))
		g.Expect(agent.Status.SelectedNodes).To(Equal([]string{target}))
		g.Expect(agent.Status.ObservedGeneration).To(Equal(agent.Generation))
	}, 4*time.Minute, time.Second).Should(Succeed())
	cleanup.manifests = append(cleanup.manifests, executionSafetyFlow(target))
	return target
}

func executionSafetyFlow(node string) string {
	var flow power.ShutdownFlow
	Expect(yaml.Unmarshal([]byte(logicalFlowManifest(node, "Enforce", true)), &flow)).To(Succeed())
	scale := flow.Spec.Groups[0]
	scale.ShutdownTier = ptr.To(int32(1))
	scale.After = []string{"release"}
	flow.Spec.Groups = []power.ShutdownGroup{
		{Name: "barrier", Action: power.ShutdownStepRunHook, ShutdownTier: ptr.To(int32(3)), Before: []string{"release"},
			HookRef: &power.NamespacedNameReference{Namespace: flowOperandNamespace, Name: "external-stop"}, Timeout: &metav1.Duration{Duration: 3 * time.Minute}},
		{Name: "release", Action: power.ShutdownStepAgentShutdown, ShutdownTier: ptr.To(int32(2)), After: []string{"barrier"},
			Target: power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: flowAgent}}}, Timeout: &metav1.Duration{Duration: time.Minute}},
		scale,
	}
	data, err := json.Marshal(flow)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

func executionBarrierManifest(survivor string) string {
	var list struct {
		Items []json.RawMessage `json:"items"`
	}
	Expect(json.Unmarshal([]byte(modularReceiverManifest(survivor)), &list)).To(Succeed())
	var objects []any
	for _, item := range list.Items {
		var meta metav1.TypeMeta
		Expect(json.Unmarshal(item, &meta)).To(Succeed())
		switch meta.Kind {
		case "ConfigMap":
			var cm corev1.ConfigMap
			Expect(json.Unmarshal(item, &cm)).To(Succeed())
			cm.Data["receiver.py"] = executionBarrierSource
			objects = append(objects, cm)
		case "ShutdownHook":
			var hook power.ShutdownHook
			Expect(json.Unmarshal(item, &hook)).To(Succeed())
			if hook.Name != "external-stop" {
				continue
			}
			hook.Spec.Invocation.HTTP.URL = modularReceiverURL() + "/hooks/barrier"
			hook.Spec.Invocation.Timeout = &metav1.Duration{Duration: 3 * time.Minute}
			hook.Spec.DryRun = nil
			objects = append(objects, hook)
		default:
			objects = append(objects, item)
		}
	}
	return logicalFlowJSONList(objects...)
}

type executionBarrierReceipt struct{ DryRun, Released, TimedOut bool }

func executionBarrierRead(g Gomega) map[string]executionBarrierReceipt {
	out, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, modularReceiverName, "--", "python3", "-c", `import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/state").read().decode())`))
	g.Expect(err).NotTo(HaveOccurred())
	var state map[string]executionBarrierReceipt
	g.Expect(json.Unmarshal([]byte(out), &state)).To(Succeed())
	return state
}

func executionSafetyReplicas(g Gomega, replicas int32) {
	var deployment appsv1.Deployment
	g.Expect(logicalFlowGet(&deployment, "deployment", "test2-scale", "-n", flowWorkNamespace)).To(Succeed())
	g.Expect(*deployment.Spec.Replicas).To(Equal(replicas))
}

func executionSafetyAudit(execution, scenario string) {
	Expect(execution).To(MatchRegexp(`^[a-f0-9-]+$`))
	query := fmt.Sprintf(`SELECT json_build_object('phase',e.phase,'reason',e.reason,'completed',e.completed_at IS NOT NULL,
 'published',(SELECT count(*) FROM power.node_signal_handoffs h WHERE h.execution_id=e.execution_id AND h.accepted),
 'effects',(SELECT count(*) FROM power.shutdownflow_action_attempts a WHERE a.execution_id=e.execution_id AND a.group_name IN ('release','scale') AND NOT a.dry_run AND a.outcome='Succeeded'),
 'dryActions',(SELECT count(*) FROM power.shutdownflow_action_attempts a WHERE a.execution_id=e.execution_id AND a.group_name IN ('release','scale') AND a.dry_run),
 'attempts',(SELECT coalesce(json_agg(row_to_json(a)), '[]'::json) FROM power.shutdownflow_action_attempts a WHERE a.execution_id=e.execution_id))
 FROM power.shutdownflow_executions e WHERE e.execution_id='%s'`, execution)
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, "test2-postgres", "--", "psql", "-U", "test2", "-d", "test2", "-tAc", query))
		g.Expect(err).NotTo(HaveOccurred())
		var result struct {
			Phase, Reason                  string
			Completed                      bool
			Published, Effects, DryActions int
			Attempts                       json.RawMessage
		}
		g.Expect(json.Unmarshal([]byte(strings.TrimSpace(out)), &result)).To(Succeed())
		g.Expect(result.Completed).To(BeTrue())
		if scenario == "baseline" {
			g.Expect(result.Phase).To(Equal("Completed"))
			g.Expect(result.Published).To(Equal(1))
			g.Expect(result.Effects).To(Equal(2))
		} else {
			g.Expect(result.Published).To(BeZero())
			g.Expect(result.Effects).To(BeZero())
			if scenario == "agent specification change" {
				g.Expect(result.Phase).To(Equal("Aborted"))
				g.Expect(string(result.Attempts)).To(ContainSubstring("changed"))
			} else {
				g.Expect(result.Phase).To(BeElementOf("Aborted", "Completed"))
				if result.Phase == "Aborted" {
					g.Expect(result.Reason).To(ContainSubstring("context canceled"))
				} else {
					g.Expect(result.DryActions).To(Equal(2), "the live mode gate must suppress both later actions")
				}
			}
		}
	}, time.Minute, time.Second).Should(Succeed())
}
