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

// VM-4's own remaining text, after its third (single-guest) milestone closed: "The two-guest
// topology, real drain/eviction against a live workload Pod, and the network-policy/audit
// assertions remain open." This milestone closes the first two of those three, reusing VM-2's own
// now-proven two-node join (test/hadron/cluster_join_smoke_test.go) instead of duplicating it.
//
// Deliberately scoped like every other VM-3/VM-4 milestone this project has built: one new
// mechanism proven at a time. NodePowerAgent's actuatorPolicy stays Simulate here -- VM-3 already
// separately proved real Actuate/PowerOff halts a guest (hypervisor-confirmed), and combining that
// with a brand-new two-guest drain mechanism in the same run would make a live failure ambiguous
// about which of two new things broke. Real actuation plus a survivor-availability assertion is
// left for a following milestone once this one is itself proven live. Network-policy enforcement
// is also left open, a separate assertion from drain/eviction.
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// twoNodeOutageNamespace hosts the NUTServer/NodePowerAgent/PostgreSQL fixture -- distinct from
// outageFlowNamespace (the single-guest milestone) and from twoNodeWorkloadNamespace below, so
// none of the three ever collide if a future change runs them in the same job.
const twoNodeOutageNamespace = "power-outage-two-node-hadron"

// twoNodeWorkloadNamespace holds only the plain workload Deployment DrainNodes must evict. Kept
// separate from twoNodeOutageNamespace deliberately: kubeactions.Runner's own protectedNamespaces
// self-exclusion (internal/kubeactions/runner.go) never evicts the manager's namespace or any
// NodePowerAgent's own operand namespace -- putting the workload there would make it unevictable
// by construction, silently defeating the one thing this milestone exists to prove.
const twoNodeWorkloadNamespace = "hadron-outage-workload"

// TestHadronOutageFlowTwoNodeDrainsWorkload boots a real two-node k3s cluster (the same join
// test/hadron/cluster_join_smoke_test.go proves), keeps the manager, PostgreSQL, and simulated UPS
// pinned to the server (the survivor), and drives a real ShutdownFlow with a DrainNodes step
// (shutdownTier 2) ahead of an AgentShutdown step (shutdownTier 1, Simulate) targeting the agent
// node -- confirming the agent Node is really cordoned and a real, independently-scheduled
// workload Pod on it is really evicted (Kubernetes eviction API, internal/kubeactions/runner.go's
// drainNodes) before the actuator ever observes a signal, not merely that both steps exist in the
// spec.
func TestHadronOutageFlowTwoNodeDrainsWorkload(t *testing.T) {
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

	serverCreds, agentCreds, kubeconfigPath, clientset, serverNodeName, agentNodeName := bootAndJoinTwoNodeCluster(ctx, t)

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

	// The manager Deployment is plain kubectl/kustomize-applied, not reconciled by any running
	// controller once created, so this patch is a one-time, permanent pin -- nothing will ever
	// revert it the way an operator-owned resource's own reconcile loop would. Keeping the manager
	// off the node this test is about to cordon and drain is exactly VM-2's own "keep manager,
	// PostgreSQL, and simulated UPS on the survivor" criterion, not a workaround for this milestone
	// alone.
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
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", twoNodeOutageNamespace)
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", twoNodeWorkloadNamespace)

	t.Log("applying the real workload Deployment DrainNodes must evict, pinned to the agent node")
	applyWorkloadDeploymentOnNode(ctx, t, kubeconfigPath, clientset, agentNodeName)

	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	upsmonRepo, upsmonTag := splitImageRef(t, upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, actuatorImage)

	// The .seq fixture, timings, and unidentified-device escape hatch are the same proven-correct
	// shape TestHadronShutdownFlowProducesRealSignal already established; only the topology
	// (two real nodes, not one) and the ShutdownFlow's own groups (DrainNodes ahead of
	// AgentShutdown, both targeting the agent specifically) are new. NUTServer's own
	// spec.placement.nodeSelector (internal/controller/nutserver_workload.go) pins its rendered
	// Deployment to the survivor the same way the manager patch above does, keeping the simulated
	// UPS off the node this test is about to drain.
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: hadron-two-node-outage-transitions
  namespace: %[1]s
data:
  sequence.seq: |
    device.mfr: nut-operator
    device.model: hadron-two-node-outage
    ups.mfr: nut-operator
    ups.model: hadron-two-node-outage
    ups.status: OL
    battery.charge: 100
    battery.runtime: 3600
    ups.load: 10

    TIMER 40

    device.mfr: nut-operator
    device.model: hadron-two-node-outage
    ups.mfr: nut-operator
    ups.model: hadron-two-node-outage
    ups.status: OB
    battery.charge: 40
    battery.runtime: 600
    ups.load: 10

    TIMER 600
---
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: hadron-two-node-outage-ups
spec:
  displayName: Hadron VM-4 Two-Node Outage Dummy UPS
  driver: dummy-ups
  powerDomains:
    - hadron-domain
  simulation:
    sequenceConfigMapRef:
      namespace: %[1]s
      name: hadron-two-node-outage-transitions
  telemetry:
    pollInterval: 5s
    alertPollInterval: 5s
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: hadron-two-node-outage-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: hadron-two-node-outage-ups
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
  name: hadron-two-node-outage-agent
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: hadron-two-node-outage-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[5]s
  mode: DryRun
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
    actuatorPolicy: Simulate
    signalTTL: 2m
    requireFreshTelemetry: false
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hadron-two-node-outage-postgres
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hadron-two-node-outage-postgres
  template:
    metadata:
      labels:
        app: hadron-two-node-outage-postgres
    spec:
      nodeSelector:
        kubernetes.io/hostname: %[4]s
      containers:
        - name: postgres
          image: %[10]s
          imagePullPolicy: IfNotPresent
          env:
            - name: POSTGRES_USER
              value: nutoperator
            - name: POSTGRES_PASSWORD
              value: hadron-two-node-outage-test
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
  name: hadron-two-node-outage-postgres
  namespace: %[1]s
spec:
  selector:
    app: hadron-two-node-outage-postgres
  ports:
    - port: 5432
      targetPort: 5432
---
apiVersion: power.zalud.io/v1alpha1
kind: ShutdownFlow
metadata:
  name: hadron-two-node-outage-flow
  annotations:
    power.zalud.io/hadron-two-node-outage-flow-approved: "true"
spec:
  managementClusterRef:
    name: hadron-two-node-outage-cluster
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
          - name: hadron-two-node-outage-agent
      timeout: 2m
  safety:
    requireManualApproval: true
    approvalAnnotation: power.zalud.io/hadron-two-node-outage-flow-approved
    allowUnidentifiedDevices: true
`, twoNodeOutageNamespace,
		nutServerRepo, nutServerTag, serverNodeName,
		agentNodeName,
		upsmonRepo, upsmonTag,
		actuatorRepo, actuatorTag,
		postgresRunImage)

	t.Log("applying the real UPSDevice/NUTServer/NodePowerAgent/ShutdownFlow fixture")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "fixture apply", func(ctx context.Context) error {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "apply", "-f", "-")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		cmd.Stdin = strings.NewReader(manifest)
		if out, err := cmd.CombinedOutput(); err != nil {
			wrapped := fmt.Errorf("kubectl apply: %w\n%s", err, out)
			t.Logf("fixture apply attempt failed: %v", wrapped)
			return wrapped
		}
		return nil
	}, func(ctx context.Context) {
		diagCtx, diagCancel := context.WithTimeout(ctx, 15*time.Second)
		defer diagCancel()
		out := runKubectlOutput(diagCtx, t, kubeconfigPath, "get", "pods", "-A", "-o", "wide")
		t.Logf("diagnostic pod listing while waiting for fixture apply:\n%s", out)
	})

	t.Log("waiting for the NodePowerAgent to report Ready")
	// 2026-09-18 second live run: this timed out with no diagnostic capture at all, leaving no
	// evidence of whether the DaemonSet's own pod ever started, pulled its images, or failed a
	// readiness probe -- added before guessing at a fix, matching every other wait in this package
	// that can plausibly stall.
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "NodePowerAgent Ready", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodepoweragent", "hadron-two-node-outage-agent", "-o", "jsonpath={.status.phase}")
		if phase != "Ready" {
			return fmt.Errorf("NodePowerAgent phase=%q, not Ready yet", phase)
		}
		return nil
	}, func(ctx context.Context) {
		status := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodepoweragent", "hadron-two-node-outage-agent", "-o", "yaml")
		t.Logf("diagnostic NodePowerAgent status:\n%s", status)
		pods := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", twoNodeOutageNamespace, "-o", "wide")
		t.Logf("diagnostic pod listing in %s:\n%s", twoNodeOutageNamespace, pods)
	})

	t.Log("waiting for the real PostgreSQL Deployment to become Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "PostgreSQL Ready", func(ctx context.Context) error {
		dep, err := clientset.AppsV1().Deployments(twoNodeOutageNamespace).Get(ctx, "hadron-two-node-outage-postgres", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if dep.Status.ReadyReplicas < 1 {
			return fmt.Errorf("postgres not ready yet: readyReplicas=%d", dep.Status.ReadyReplicas)
		}
		return nil
	}, nil)

	createPostgresDSNSecretNamed(ctx, t, kubeconfigPath, clientset, twoNodeOutageNamespace, "hadron-two-node-outage-postgres", "hadron-two-node-outage-test")
	applyPowerManagementClusterAndWaitReadyNamed(ctx, t, kubeconfigPath, twoNodeOutageNamespace, "hadron-two-node-outage-cluster", "hadron-two-node-outage-postgres-dsn")

	t.Log("waiting for the real dummy-ups driver to report OnBattery via real telemetry polling")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "UPSDevice OnBattery", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "upsdevice", "hadron-two-node-outage-ups", "-o", "jsonpath={.status.phase}")
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
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "shutdownflow", "hadron-two-node-outage-flow", "-o", "yaml")
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

	agentPodName := waitForExactlyOneRunningAgentPod(ctx, t, clientset, twoNodeOutageNamespace)
	t.Log("waiting for the real actuator to observe a real, operator-written signal, after the real drain")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "actuator observes real signal", func(ctx context.Context) error {
		logCtx, logCancel := context.WithTimeout(ctx, 15*time.Second)
		defer logCancel()
		raw, err := clientset.CoreV1().Pods(twoNodeOutageNamespace).GetLogs(agentPodName, &corev1.PodLogOptions{Container: "actuator"}).DoRaw(logCtx)
		if err != nil {
			return fmt.Errorf("fetching actuator logs: %w", err)
		}
		log := string(raw)
		if !strings.Contains(log, "simulate actuator accepted shutdown signal") {
			return fmt.Errorf("no accepted-signal log line yet:\n%s", log)
		}
		return nil
	}, func(ctx context.Context) {
		diagCtx, diagCancel := context.WithTimeout(ctx, 15*time.Second)
		defer diagCancel()
		out := runKubectlOutput(diagCtx, t, kubeconfigPath, "get", "shutdownflow", "hadron-two-node-outage-flow", "-o", "yaml")
		t.Logf("diagnostic ShutdownFlow state:\n%s", out)
	})
	t.Log("confirmed: the real actuator accepted a real signal only after the real two-node drain completed")

	t.Log("confirming the survivor (server) node stayed Ready and available throughout")
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
	t.Log("confirmed: the survivor node remained Ready and schedulable")

	assertRealDrainAuditRecords(ctx, t, kubeconfigPath, twoNodeOutageNamespace)
}

// bootAndJoinTwoNodeCluster boots a real k3s server and agent joined over a fresh ClusterLink
// segment (the same sequence test/hadron/cluster_join_smoke_test.go's own TestHadronClusterJoin
// already proves live), fetches a host-side kubeconfig from the server, and identifies which of
// the two Ready nodes is which by k3s's own automatic control-plane role label -- Kairos assigns
// each guest an unpredictable random hostname, so node name alone cannot tell survivor from target
// at fixture-authoring time. Factored out of the main test function to keep its own cyclomatic
// complexity bounded; this is boot/join plumbing already proven correct elsewhere, not new
// behavior this milestone itself is qualifying.
func bootAndJoinTwoNodeCluster(ctx context.Context, t *testing.T) (serverCreds, agentCreds Credentials, kubeconfigPath string, clientset *kubernetes.Clientset, serverNodeName, agentNodeName string) {
	t.Helper()

	link, err := NewClusterLink()
	if err != nil {
		t.Fatalf("NewClusterLink: %v", err)
	}
	serverMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatalf("RandomClusterMAC (server): %v", err)
	}
	agentMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatalf("RandomClusterMAC (agent): %v", err)
	}
	serverNIC, err := link.Server(serverMAC)
	if err != nil {
		t.Fatalf("link.Server: %v", err)
	}
	agentNIC, err := link.Client(agentMAC)
	if err != nil {
		t.Fatalf("link.Client: %v", err)
	}

	t.Log("booting the k3s server guest")
	_, serverCreds = bootClusterJoinGuest(ctx, t, "server", Config{
		Memory:      "4096",
		CPUs:        "2",
		ISO:         hadronISOURL,
		ISOChecksum: hadronISOChecksum,
		ClusterNIC:  &serverNIC,
		CloudConfig: func(c Credentials) string {
			return KairosAutoInstallServerCloudConfig(c, "/dev/vda", clusterJoinServerIP)
		},
		ForwardKubeAPI: true,
	})

	t.Log("waiting for SSH on the server guest")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "server SSH", func(ctx context.Context) error {
		_, err := guestCommand(ctx, serverCreds, "true")
		return err
	}, nil)

	t.Log("waiting for a Ready k3s node on the server")
	waitForWithDiagnostics(t, ctx, 10*time.Minute, "server k3s readiness", func(ctx context.Context) error {
		out, err := guestCommand(ctx, serverCreds, "sudo k3s kubectl get nodes --request-timeout=20s -o json")
		if err != nil {
			return err
		}
		if !hasReadyNode(out) {
			return fmt.Errorf("no Ready node yet:\n%s", out)
		}
		return nil
	}, nil)

	t.Log("assigning the server's static ClusterLink address")
	assignClusterLinkAddress(ctx, t, serverCreds, serverMAC, clusterJoinServerIP)

	t.Log("reading the server's own generated node-token")
	token, err := guestCommand(ctx, serverCreds, "sudo cat /var/lib/rancher/k3s/server/node-token")
	if err != nil {
		t.Fatalf("reading node-token: %v", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		t.Fatalf("node-token was empty")
	}

	t.Log("booting the k3s agent guest")
	_, agentCreds = bootClusterJoinGuest(ctx, t, "agent", Config{
		Memory:      "2048",
		CPUs:        "2",
		ISO:         hadronISOURL,
		ISOChecksum: hadronISOChecksum,
		ClusterNIC:  &agentNIC,
		CloudConfig: func(c Credentials) string {
			return KairosAutoInstallAgentCloudConfig(c, "/dev/vda",
				"https://"+clusterJoinServerIP+":6443", token)
		},
	})

	t.Log("waiting for SSH on the agent guest's live installer environment")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "agent SSH", func(ctx context.Context) error {
		_, err := guestCommand(ctx, agentCreds, "true")
		return err
	}, nil)

	t.Log("waiting for the agent guest to reach its installed, rebooted system")
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "agent installed system", func(ctx context.Context) error {
		out, err := guestCommand(ctx, agentCreds, "sudo systemctl status k3s-agent 2>&1; true")
		if err != nil {
			return err
		}
		if strings.Contains(out, "could not be found") {
			return fmt.Errorf("k3s-agent unit does not exist yet (still the live installer environment):\n%s", out)
		}
		return nil
	}, nil)

	t.Log("confirming the agent's SSH session is stable before configuring its network")
	waitForStableSSH(ctx, t, agentCreds, 9, 5*time.Second, 2*time.Minute)

	t.Log("assigning the agent's static ClusterLink address")
	agentIface := assignClusterLinkAddress(ctx, t, agentCreds, agentMAC, clusterJoinAgentIP)
	confirmAgentClusterLinkAddressSurvives(ctx, t, agentCreds, agentIface, clusterJoinAgentIP, 2*time.Minute)

	kubeconfig, err := Kubeconfig(ctx, serverCreds)
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	kubeconfigPath = filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
		t.Fatalf("writing fetched kubeconfig: %v", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("parsing fetched kubeconfig: %v", err)
	}
	clientset, err = kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building client from fetched kubeconfig: %v", err)
	}

	t.Log("fetching kubeconfig and waiting for two distinct Ready nodes from outside both guests")
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "two Ready nodes", func(ctx context.Context) error {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing nodes: %w", err)
		}
		if len(nodes.Items) != 2 {
			return fmt.Errorf("expected exactly two nodes, got %d", len(nodes.Items))
		}
		var server, agent string
		for _, node := range nodes.Items {
			if !nodeReadyCondition(node) {
				return fmt.Errorf("node %q is not Ready yet", node.Name)
			}
			// k3s labels a server (control-plane) node with this role label automatically; a
			// pure k3s-agent join never gets it.
			if _, isServer := node.Labels["node-role.kubernetes.io/control-plane"]; isServer {
				server = node.Name
			} else {
				agent = node.Name
			}
		}
		if server == "" || agent == "" {
			return fmt.Errorf("could not distinguish server/agent node by role label (server=%q agent=%q)", server, agent)
		}
		serverNodeName, agentNodeName = server, agent
		return nil
	}, nil)
	t.Logf("server (survivor) node: %s; agent (drained/target) node: %s", serverNodeName, agentNodeName)

	return serverCreds, agentCreds, kubeconfigPath, clientset, serverNodeName, agentNodeName
}

// applyWorkloadDeploymentOnNode creates the plain, evictable Deployment (no DaemonSet ownership,
// not in any protected namespace) DrainNodes must remove for real. A single replica, no
// PodDisruptionBudget -- this milestone proves the base eviction path works at all; a PDB
// override is internal/kubeactions/runner.go's own already-implemented branch, not re-proven here.
func applyWorkloadDeploymentOnNode(ctx context.Context, t *testing.T, kubeconfigPath string, clientset *kubernetes.Clientset, nodeName string) {
	t.Helper()
	manifest := fmt.Sprintf(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hadron-outage-workload
  namespace: %[1]s
  labels:
    app: hadron-outage-workload
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hadron-outage-workload
  template:
    metadata:
      labels:
        app: hadron-outage-workload
    spec:
      nodeSelector:
        kubernetes.io/hostname: %[2]s
      terminationGracePeriodSeconds: 1
      containers:
        - name: workload
          image: registry.k8s.io/pause:3.9
`, twoNodeWorkloadNamespace, nodeName)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply workload Deployment: %v\n%s", err, out)
	}

	// A typed List, not a `kubectl get -o jsonpath={.items[0]...}` shellout: before the Deployment's
	// own controller has created any Pod yet, that jsonpath template errors on the empty list
	// (`array index out of bounds: index 0, length 0`), which runKubectlOutput turns into an
	// immediate t.Fatalf -- aborting the whole test on a normal, expected transient state instead of
	// retrying it. Confirmed live (2026-09-18, first run of this milestone): the failure landed at
	// this exact check, seconds after the Deployment was applied, with every prior step (real
	// two-node join, controller-manager Ready) already passed.
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "workload Pod Running", func(ctx context.Context) error {
		pods, err := clientset.CoreV1().Pods(twoNodeWorkloadNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=hadron-outage-workload",
		})
		if err != nil {
			return err
		}
		if len(pods.Items) == 0 {
			return fmt.Errorf("workload pod not created yet")
		}
		if phase := pods.Items[0].Status.Phase; phase != corev1.PodRunning {
			return fmt.Errorf("workload pod phase=%q, not Running yet", phase)
		}
		return nil
	}, nil)
}

// assertRealDrainAuditRecords extends TestHadronShutdownFlowProducesRealSignal's own
// assertRealAuditRecords with the one new action kind this milestone introduces: a real
// shutdownflow_action_attempts row for the DrainNodes group itself, not only AgentShutdown's.
func assertRealDrainAuditRecords(ctx context.Context, t *testing.T, kubeconfigPath, namespace string) {
	t.Helper()
	t.Log("verifying the real PostgreSQL audit store recorded this execution, including the drain step")

	postgresPod := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", namespace,
		"-l", "app=hadron-two-node-outage-postgres", "-o", "jsonpath={.items[0].metadata.name}")
	if postgresPod == "" {
		t.Fatal("no postgres pod found")
	}

	query := func(sql string) (string, error) {
		queryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(queryCtx, "kubectl", "exec", "-n", namespace, postgresPod, "--",
			"env", "PGPASSWORD=hadron-two-node-outage-test",
			"psql", "-h", "127.0.0.1", "-U", "nutoperator", "-d", "nutoperator", "-tAc", sql)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("psql query %q: %w\n%s", sql, err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}

	var executionID string
	waitForWithDiagnostics(t, ctx, time.Minute, "shutdownflow_executions row", func(ctx context.Context) error {
		id, err := query(`SELECT execution_id FROM power.shutdownflow_executions ` +
			`WHERE shutdownflow = 'hadron-two-node-outage-flow' AND mode = 'Enforce' ` +
			`AND dry_run = false AND approved = true ` +
			`ORDER BY observed_at DESC LIMIT 1`)
		if err != nil {
			return err
		}
		if id == "" {
			return fmt.Errorf("no matching shutdownflow_executions row yet")
		}
		executionID = id
		return nil
	}, nil)
	t.Logf("real execution_id: %s", executionID)

	for _, check := range []struct {
		table string
		sql   string
	}{
		{"shutdownflow_action_attempts (DrainNodes)", fmt.Sprintf(
			`SELECT count(*) FROM power.shutdownflow_action_attempts `+
				`WHERE execution_id = '%s' AND action = 'DrainNodes' AND outcome = 'Succeeded'`, executionID)},
		{"shutdownflow_action_attempts (AgentShutdown)", fmt.Sprintf(
			`SELECT count(*) FROM power.shutdownflow_action_attempts `+
				`WHERE execution_id = '%s' AND action = 'AgentShutdown' AND outcome = 'Succeeded'`, executionID)},
		{"node_release_records", fmt.Sprintf(
			`SELECT count(*) FROM power.node_release_records `+
				`WHERE execution_id = '%s' AND released = true AND approved = true`, executionID)},
		{"node_signal_handoffs", fmt.Sprintf(
			`SELECT count(*) FROM power.node_signal_handoffs `+
				`WHERE execution_id = '%s' AND accepted = true`, executionID)},
	} {
		count, err := query(check.sql)
		if err != nil {
			t.Fatalf("querying %s: %v", check.table, err)
		}
		if count == "0" || count == "" {
			t.Fatalf("expected at least one matching row in %s for execution %s, got %q", check.table, executionID, count)
		}
		t.Logf("confirmed: %s has %s matching row(s)", check.table, count)
	}
	t.Log("confirmed: the real PostgreSQL audit store recorded a successful drain, action attempt, node release, and signal handoff for this execution")
}

// createPostgresDSNSecretNamed is createPostgresDSNSecret generalized over the Postgres
// Deployment/Service name and password, since this file's own fixture names differ from
// TestHadronShutdownFlowProducesRealSignal's to avoid colliding if both ever run in the same job.
func createPostgresDSNSecretNamed(ctx context.Context, t *testing.T, kubeconfigPath string, clientset *kubernetes.Clientset, namespace, serviceName, password string) {
	t.Helper()
	service, err := clientset.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting the PostgreSQL Service's ClusterIP: %v", err)
	}
	if service.Spec.ClusterIP == "" {
		t.Fatalf("PostgreSQL Service %s/%s has no ClusterIP assigned", namespace, serviceName)
	}
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: %[1]s-dsn
  namespace: %[2]s
stringData:
  dsn: postgres://nutoperator:%[3]s@%[4]s:5432/nutoperator?sslmode=disable
`, serviceName, namespace, password, service.Spec.ClusterIP)
	t.Log("creating the PostgreSQL DSN Secret from the Service's ClusterIP")
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply DSN secret: %v\n%s", err, out)
	}
}

// applyPowerManagementClusterAndWaitReadyNamed is applyPowerManagementClusterAndWaitReady
// generalized over the PowerManagementCluster/DSN-secret name, for the same reason
// createPostgresDSNSecretNamed is.
func applyPowerManagementClusterAndWaitReadyNamed(ctx context.Context, t *testing.T, kubeconfigPath, namespace, clusterName, dsnSecretName string) {
	t.Helper()
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: PowerManagementCluster
metadata:
  name: %[2]s
spec:
  storage:
    mode: ExternalPostgres
    externalPostgres:
      dsnSecretKeyRef:
        namespace: %[1]s
        name: %[3]s
        key: dsn
      requireTLS: false
`, namespace, clusterName, dsnSecretName)
	t.Log("applying the real PowerManagementCluster now that PostgreSQL is reachable")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "PowerManagementCluster apply", func(ctx context.Context) error {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "apply", "-f", "-")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		cmd.Stdin = strings.NewReader(manifest)
		if out, err := cmd.CombinedOutput(); err != nil {
			wrapped := fmt.Errorf("kubectl apply: %w\n%s", err, out)
			t.Logf("PowerManagementCluster apply attempt failed: %v", wrapped)
			return wrapped
		}
		return nil
	}, nil)

	t.Log("waiting for the PowerManagementCluster to report its PostgreSQL audit store ready")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "PowerManagementCluster storage Ready", func(ctx context.Context) error {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "get", "powermanagementcluster", clusterName, "-o", "jsonpath={.status.storage.ready}")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			wrapped := fmt.Errorf("kubectl get powermanagementcluster: %w\n%s", err, out)
			t.Logf("PowerManagementCluster storage-ready attempt failed: %v", wrapped)
			return wrapped
		}
		if ready := strings.TrimSpace(string(out)); ready != "true" {
			return fmt.Errorf("PowerManagementCluster storage.ready=%q, not true yet", ready)
		}
		return nil
	}, func(ctx context.Context) {
		diagCtx, diagCancel := context.WithTimeout(ctx, 15*time.Second)
		defer diagCancel()
		out := runKubectlOutput(diagCtx, t, kubeconfigPath, "get", "powermanagementcluster", clusterName, "-o", "yaml")
		t.Logf("diagnostic PowerManagementCluster state:\n%s", out)
	})
}
