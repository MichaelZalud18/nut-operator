//go:build hadron_smoke
// +build hadron_smoke

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

// VM-4's own last remaining item, per TestHadronOutageFlowTwoNodeDrainsWorkload's own doc
// comment: "Real actuation plus a survivor-availability assertion is left for a following
// milestone once this one is itself proven live." That milestone (real drain/eviction, signal
// acceptance, and the full PostgreSQL audit trail) passed completely 2026-10-04
// (docs/contributing/audits/hadron-vm-4-two-node-networking-2026-09-19.md's "Full composed pass").
//
// This is that following milestone: the same real two-node drain/eviction chain, but with
// NodePowerAgent's actuatorPolicy set to the real PowerOff VM-3/VM-9 already separately proved
// halts a guest (QMP-confirmed, not merely "the process disappeared" -- test/hadron/qmp.go),
// now driven through the full ShutdownFlow DrainNodes-then-AgentShutdown pipeline in the
// two-guest topology instead of a bare approved NodePowerAgent. Deliberately a separate test from
// the sibling drain milestone, matching this project's own incremental-scope discipline: a live
// failure here is never ambiguous about whether the already-proven drain/eviction/audit chain
// broke, or whether real actuation specifically did.
//
// Does not re-assert the detailed PostgreSQL audit rows the sibling milestone already proved for
// this exact chain shape -- only what is new here: the agent guest's own QEMU reports a real,
// guest-initiated SHUTDOWN (not host-driven), and the survivor stays Ready and schedulable through
// an actual node going dark, not merely a cordoned one.
package hadron

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// twoNodeActuationNamespace hosts this milestone's own UPSDevice/NUTServer/NodePowerAgent/
// PostgreSQL fixture -- distinct from the sibling drain/network-policy milestones' namespaces so
// none of the three ever collide if a future change runs them in the same job. The evictable
// workload itself reuses applyWorkloadDeploymentOnNode, which hardcodes twoNodeWorkloadNamespace
// (outage_flow_two_node_smoke_test.go) rather than taking a namespace parameter.
const (
	twoNodeActuationNamespace   = "power-outage-two-node-actuation-hadron"
	twoNodeActuationApprovalKey = "power.zalud.io/hadron-two-node-actuation-agent-approved"
)

// TestHadronOutageFlowTwoNodeActuatesRealPowerOff boots a real two-node k3s cluster, drives the
// same real DrainNodes-then-AgentShutdown ShutdownFlow the sibling milestone proved, but with
// NodePowerAgent's actuatorPolicy set to PowerOff -- confirming the agent guest's own QEMU reports
// a real, guest-initiated SHUTDOWN only after the real drain/eviction completes, and that the
// survivor node stays Ready and schedulable through an actual node going dark.
func TestHadronOutageFlowTwoNodeActuatesRealPowerOff(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	repoRoot := repoRootDir(t)
	managerImage, managerTar := buildManagerImageTarball(ctx, t, repoRoot)
	nutServerImage, nutServerTar := buildOperandImageTarball(ctx, t, repoRoot, "images/nut-server/Dockerfile", "nutserver")
	upsmonImage, upsmonTar := buildOperandImageTarball(ctx, t, repoRoot, "images/upsmon-agent/Dockerfile", "upsmon")
	actuatorImage, actuatorTar := buildOperandImageTarball(ctx, t, repoRoot, "images/node-actuator/Dockerfile", "actuator")
	postgresRunImage, postgresTar := pullOperandImageTarball(ctx, t, postgresImage, "postgres")
	allTars := []string{managerTar, nutServerTar, upsmonTar, actuatorTar, postgresTar}

	serverCreds, agentCreds, kubeconfigPath, clientset, serverNodeName, agentNodeName, agentMachine := bootAndJoinTwoNodeCluster(ctx, t)

	t.Log("waiting for the default namespace's own default ServiceAccount")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "default ServiceAccount", func(ctx context.Context) error {
		_, err := clientset.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{})
		return err
	}, nil)

	t.Log("importing the real manager and operand images into both guests' own containerd")
	for _, tarPath := range allTars {
		importImageTarball(ctx, t, serverCreds, tarPath)
		importImageTarball(ctx, t, agentCreds, tarPath)
	}

	restore := preserveFile(t, filepath.Join(repoRoot, "config", "manager", "kustomization.yaml"))
	defer restore()

	t.Log("make install (CRDs)")
	runMake(ctx, t, repoRoot, kubeconfigPath, nil, "install")

	t.Log("make deploy-byo-cert (RBAC, manager Deployment, webhook cert via hack/webhook-cert.sh)")
	runMake(ctx, t, repoRoot, kubeconfigPath, []string{"IMG=" + managerImage}, "deploy-byo-cert")

	t.Log("pinning the manager Deployment to the survivor (server) node")
	runKubectl(ctx, t, kubeconfigPath, "-n", operatorNamespace, "patch", "deployment", operatorDeployment,
		"--type=strategic", "-p", fmt.Sprintf(`{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":%q}}}}}`, serverNodeName))

	t.Log("waiting for the real controller-manager Deployment to become Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "controller-manager Ready", func(ctx context.Context) error {
		dep, err := clientset.AppsV1().Deployments(operatorNamespace).Get(ctx, operatorDeployment, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if dep.Status.ReadyReplicas < 1 {
			return fmt.Errorf("controller-manager not ready yet: readyReplicas=%d", dep.Status.ReadyReplicas)
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", operatorNamespace, "-o", "wide")
		t.Logf("diagnostic controller-manager pod listing:\n%s", out)
	})

	t.Log("creating the operand and workload namespaces")
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", twoNodeActuationNamespace)
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", twoNodeWorkloadNamespace)

	t.Log("applying the real workload Deployment DrainNodes must evict, pinned to the agent node")
	applyWorkloadDeploymentOnNode(ctx, t, kubeconfigPath, clientset, agentNodeName)

	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	upsmonRepo, upsmonTag := splitImageRef(t, upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, actuatorImage)

	// Same proven-correct .seq fixture and ShutdownFlow group shape as the sibling drain
	// milestone; the only things that change are actuatorPolicy (PowerOff, not Simulate),
	// NodePowerAgent's own mode (Actuate, not DryRun), and the approvalAnnotation PowerOff
	// actuation requires at admission (test/hadron/actuator_daemonset_smoke_test.go's own approved
	// fixture established this exact shape).
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: hadron-two-node-actuation-transitions
  namespace: %[1]s
data:
  sequence.seq: |
    device.mfr: nut-operator
    device.model: hadron-two-node-actuation
    ups.mfr: nut-operator
    ups.model: hadron-two-node-actuation
    ups.status: OL
    battery.charge: 100
    battery.runtime: 3600
    ups.load: 10

    TIMER 40

    device.mfr: nut-operator
    device.model: hadron-two-node-actuation
    ups.mfr: nut-operator
    ups.model: hadron-two-node-actuation
    ups.status: OB
    battery.charge: 40
    battery.runtime: 600
    ups.load: 10

    TIMER 600
---
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: hadron-two-node-actuation-ups
spec:
  displayName: Hadron VM-4 Two-Node Actuation Dummy UPS
  driver: dummy-ups
  powerDomains:
    - hadron-domain
  simulation:
    sequenceConfigMapRef:
      namespace: %[1]s
      name: hadron-two-node-actuation-transitions
  telemetry:
    pollInterval: 5s
    alertPollInterval: 5s
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: hadron-two-node-actuation-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: hadron-two-node-actuation-ups
  image:
    repository: %[2]s
    tag: "%[3]s"
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
  placement:
    nodeSelector:
      kubernetes.io/hostname: %[4]s
---
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: hadron-two-node-actuation-agent
  annotations:
    %[10]s: "true"
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: hadron-two-node-actuation-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[5]s
  mode: Actuate
  images:
    upsmon:
      repository: %[6]s
      tag: "%[7]s"
      pullPolicy: IfNotPresent
    actuator:
      repository: %[8]s
      tag: "%[9]s"
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: PowerOff
    approvalAnnotation: %[10]s
    signalTTL: 2m
    requireFreshTelemetry: false
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hadron-two-node-actuation-postgres
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hadron-two-node-actuation-postgres
  template:
    metadata:
      labels:
        app: hadron-two-node-actuation-postgres
    spec:
      nodeSelector:
        kubernetes.io/hostname: %[4]s
      containers:
        - name: postgres
          image: %[11]s
          imagePullPolicy: IfNotPresent
          env:
            - name: POSTGRES_USER
              value: nutoperator
            - name: POSTGRES_PASSWORD
              value: hadron-two-node-actuation-test
            - name: POSTGRES_DB
              value: nutoperator
          ports:
            - containerPort: 5432
          readinessProbe:
            exec:
              command: ["pg_isready", "-U", "nutoperator"]
            initialDelaySeconds: 2
            periodSeconds: 2
---
apiVersion: v1
kind: Service
metadata:
  name: hadron-two-node-actuation-postgres
  namespace: %[1]s
spec:
  selector:
    app: hadron-two-node-actuation-postgres
  ports:
    - port: 5432
      targetPort: 5432
---
apiVersion: power.zalud.io/v1alpha1
kind: ShutdownFlow
metadata:
  name: hadron-two-node-actuation-flow
  annotations:
    power.zalud.io/hadron-two-node-actuation-flow-approved: "true"
spec:
  managementClusterRef:
    name: hadron-two-node-actuation-cluster
  mode: Enforce
  triggers:
    - type: OnBattery
      powerDomains:
        - hadron-domain
      for: 1s
  groups:
    - name: drain-agent-node
      action: DrainNodes
      shutdownTier: 2
      target:
        nodeSelector:
          matchLabels:
            kubernetes.io/hostname: %[5]s
      before: [shutdown-agent]
      timeout: 2m
    - name: shutdown-agent
      action: AgentShutdown
      shutdownTier: 1
      target:
        agentRefs:
          - name: hadron-two-node-actuation-agent
      timeout: 2m
  safety:
    requireManualApproval: true
    approvalAnnotation: power.zalud.io/hadron-two-node-actuation-flow-approved
    allowUnidentifiedDevices: true
`, twoNodeActuationNamespace,
		nutServerRepo, nutServerTag, serverNodeName,
		agentNodeName,
		upsmonRepo, upsmonTag,
		actuatorRepo, actuatorTag,
		twoNodeActuationApprovalKey,
		postgresRunImage)

	t.Log("applying the real UPSDevice/NUTServer/NodePowerAgent/ShutdownFlow fixture")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "fixture apply", func(ctx context.Context) error {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "apply", "-f", "-")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		cmd.Stdin = strings.NewReader(manifest)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl apply: %w\n%s", err, out)
		}
		return nil
	}, nil)

	t.Log("waiting for the NodePowerAgent to report Ready")
	// NodePowerAgent is cluster-scoped (config/crd/bases/power.zalud.io_nodepoweragents.yaml);
	// spec.namespace only controls where its own rendered operand resources land, not this get's
	// own scope. 6-minute budget matches the sibling drain milestone's own widened margin for
	// cross-node routing/NetworkPolicy catch-up.
	waitForWithDiagnostics(t, ctx, 6*time.Minute, "NodePowerAgent Ready", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodepoweragent", "hadron-two-node-actuation-agent", "-o", "jsonpath={.status.phase}")
		if phase != "Ready" {
			return fmt.Errorf("NodePowerAgent phase=%q, not Ready yet", phase)
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", twoNodeActuationNamespace, "-o", "wide")
		t.Logf("diagnostic pod listing in %s:\n%s", twoNodeActuationNamespace, out)
	})

	t.Log("waiting for the real PostgreSQL Deployment to become Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "PostgreSQL Ready", func(ctx context.Context) error {
		dep, err := clientset.AppsV1().Deployments(twoNodeActuationNamespace).Get(ctx, "hadron-two-node-actuation-postgres", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if dep.Status.ReadyReplicas < 1 {
			return fmt.Errorf("postgres not ready yet: readyReplicas=%d", dep.Status.ReadyReplicas)
		}
		return nil
	}, nil)

	createPostgresDSNSecretNamed(ctx, t, kubeconfigPath, clientset, twoNodeActuationNamespace, "hadron-two-node-actuation-postgres", "hadron-two-node-actuation-test")
	applyPowerManagementClusterAndWaitReadyNamed(ctx, t, kubeconfigPath, twoNodeActuationNamespace, "hadron-two-node-actuation-cluster", "hadron-two-node-actuation-postgres-dsn")

	t.Log("waiting for the real dummy-ups driver to report OnBattery via real telemetry polling")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "UPSDevice OnBattery", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "upsdevice", "hadron-two-node-actuation-ups", "-o", "jsonpath={.status.phase}")
		if phase != "OnBattery" {
			return fmt.Errorf("UPSDevice phase=%q, not OnBattery yet", phase)
		}
		return nil
	}, nil)
	t.Log("confirmed: real telemetry transitioned the UPSDevice to OnBattery")

	t.Log("waiting for the agent Node to be really cordoned by the real DrainNodes step")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "agent Node cordoned", func(ctx context.Context) error {
		node, err := clientset.CoreV1().Nodes().Get(ctx, agentNodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !node.Spec.Unschedulable {
			return fmt.Errorf("agent Node %q is not cordoned yet", agentNodeName)
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "shutdownflow", "hadron-two-node-actuation-flow", "-o", "yaml")
		t.Logf("diagnostic ShutdownFlow state:\n%s", out)
	})
	t.Log("confirmed: the agent Node is really cordoned")

	t.Log("waiting for the real workload Pod on the agent node to be really evicted")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "workload Pod evicted", func(ctx context.Context) error {
		pods, err := clientset.CoreV1().Pods(twoNodeWorkloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=hadron-outage-workload",
		})
		if err != nil {
			return fmt.Errorf("listing workload pods: %w", err)
		}
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning {
				return fmt.Errorf("workload pod %q is still Running, not evicted yet", pod.Name)
			}
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", twoNodeWorkloadNamespace, "-o", "wide")
		t.Logf("diagnostic workload pod listing:\n%s", out)
	})
	t.Log("confirmed: the real workload Pod on the agent node was really evicted by DrainNodes")

	t.Log("waiting for QEMU's own QMP socket to report a real guest-initiated SHUTDOWN on the agent -- process disappearance alone cannot distinguish a genuine halt from a crash or an external kill (VM-9)")
	socketPath, err := qmpSocketPath(agentMachine)
	if err != nil {
		t.Fatalf("getting agent machine QMP socket path: %v", err)
	}
	haltCtx, haltCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer haltCancel()
	if err := waitForQMPShutdown(haltCtx, socketPath); err != nil {
		t.Fatalf("agent guest did not report a real SHUTDOWN within the budget -- the actuator armed but the guest did not actually halt, or halted before the real drain/eviction completed: %v", err)
	}
	t.Log("confirmed: QEMU's own QMP socket reported a real guest-initiated SHUTDOWN on the agent, only after the real two-node drain/eviction completed")

	t.Log("confirming the survivor (server) node stayed Ready and available throughout an actual agent halt")
	survivorNode, err := clientset.CoreV1().Nodes().Get(ctx, serverNodeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting survivor Node: %v", err)
	}
	if !nodeReadyCondition(*survivorNode) {
		t.Fatalf("survivor Node %q is not Ready", serverNodeName)
	}
	if survivorNode.Spec.Unschedulable {
		t.Fatalf("survivor Node %q was unexpectedly cordoned", serverNodeName)
	}
	t.Log("confirmed: the survivor node remained Ready and schedulable through a real agent power-off")
}
