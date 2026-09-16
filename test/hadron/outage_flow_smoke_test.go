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

// outageFlowNamespace is distinct from upsStackNamespace (used by
// TestHadronOperatorRunsRealUPSStack) so the two tests' fixtures never collide if a future change
// runs them in the same job.
const outageFlowNamespace = "power-outage-hadron"

// postgresImage is the real audit-store PostgreSQL this milestone stands up, pinned by digest like
// every other externally-sourced base image in this repo (images/*/Dockerfile).
const postgresImage = "docker.io/library/postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685"

// TestHadronShutdownFlowProducesRealSignal is VM-4's third milestone: a real ShutdownFlow trigger,
// driven by a real scripted UPS telemetry transition, producing a real operator-written signal --
// the exact thing VM-4's own text says a hand-written signal cannot stand in for ("manual signal
// injection alone is not this end-to-end test").
//
// Chain under test: NUT's own dummy-loop driver reads a real .seq file (the same mechanism
// test/e2e/e2e_test.go's own "drives real Online/OnBattery/LowBattery transitions..." spec already
// proves against Kind) -> UPSDeviceReconciler polls real telemetry and observes OnBattery ->
// ShutdownFlow's trigger evaluator matches it against a real UPSDevice.spec.powerDomains member ->
// the executor compiles and runs a real wave, including a real AgentShutdown step -> the executor
// writes a real signal into the NodePowerAgent's projected Secret -> the real node-actuator
// (already proven in VM-3) observes and accepts it. NodePowerAgent stays Simulate (as in the
// steady-state milestone), so acceptance only ever logs; nothing here can halt the guest.
//
// Deliberately single-guest. Checked, not assumed, before building this: the executor's own
// nodeClearance check (internal/controller/shutdownflow_execution.go) exempts both the manager's
// own namespace (POD_NAMESPACE, set via the Deployment's downward API) and every NodePowerAgent's
// operand namespace from "is this node empty enough to release" -- on this single guest, nothing
// else is running there, so AgentShutdown does not need a prior drain step to succeed. The
// two-guest survivor topology VM-4's own text names remains open for a later milestone that
// actually asserts survivor availability, which a single guest cannot.
//
// The dummy-ups device declares no model, so it matches no product capability profile and Enforce
// mode is blocked by default (internal/controller/upsdevice_identity.go's own unidentified-device
// gate). spec.safety.allowUnidentifiedDevices is the documented, deliberate escape hatch for
// exactly this case (api/v1alpha1/shutdownflow_types.go: "an operator states, in Git, that they
// accept it") -- this milestone is proving trigger evaluation and signal delivery, not capability
// profile matching, which is a separate, already-tested subsystem.
//
// Enforce-mode execution also requires a ready PostgreSQL audit store: SB-11
// (docs/contributing/design/scope-boundaries.md) makes PostgreSQL "a required production
// component," and shutdownflow_controller.go's recordShutdownFlowAudit blocks ExecutionReady on
// it (found live: a run reached TriggerEligible=true and then stalled on
// AuditStoreUnavailable/"shutdown flow execution requires a ready PostgreSQL audit store"). The
// audit spool (spec.storage.auditSpool) only cushions a PostgreSQL outage once a real backend is
// already configured -- resolveAuditSpool rejects pairing it with storage mode Disabled -- so
// there is no way to satisfy this gate without a real, reachable PostgreSQL. This test stands one
// up itself: a real postgres:16-alpine server (pinned by digest, the same convention every
// externally-sourced base image in images/*/Dockerfile already follows), referenced by a real
// PowerManagementCluster with storage mode ExternalPostgres, which the ShutdownFlow's
// managementClusterRef points at.
func TestHadronShutdownFlowProducesRealSignal(t *testing.T) {
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

	m, creds, err := NewSafeMachineContext(ctx, Config{
		Memory:         "4096",
		CPUs:           "2",
		ISO:            hadronISOURL,
		ISOChecksum:    hadronISOChecksum,
		CloudConfig:    func(c Credentials) string { return KairosAutoInstallCloudConfig(c, "/dev/vda") },
		ForwardKubeAPI: true,
	})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("leaving machine state at %s for inspection (stdout/stderr hold the guest console)", m.Config().StateDir)
			if err := SafeStop(m, 30*time.Second); err != nil {
				t.Errorf("stop: %v", err)
			}
			return
		}
		if err := SafeStop(m, 30*time.Second); err != nil {
			t.Errorf("teardown: %v", err)
			return
		}
		if err := m.Clean(); err != nil {
			t.Errorf("removing stopped machine state: %v", err)
		}
	})
	if _, err := m.Create(ctx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Logf("waiting for SSH on 127.0.0.1:%s as %s", creds.Port, creds.User)
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "SSH", func(ctx context.Context) error {
		_, err := guestCommand(ctx, creds, "true")
		return err
	}, nil)

	t.Log("waiting for a Ready k3s node")
	waitForWithDiagnostics(t, ctx, 10*time.Minute, "k3s readiness", func(ctx context.Context) error {
		out, err := guestCommand(ctx, creds, "sudo k3s kubectl get nodes --request-timeout=20s -o json")
		if err != nil {
			return err
		}
		if !hasReadyNode(out) {
			return fmt.Errorf("no Ready node yet:\n%s", out)
		}
		return nil
	}, nil)

	kubeconfig, err := Kubeconfig(ctx, creds)
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	kubeconfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte(kubeconfig), 0600); err != nil {
		t.Fatalf("writing fetched kubeconfig: %v", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("parsing fetched kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building client from fetched kubeconfig: %v", err)
	}
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes through the forwarded API port: %v", err)
	}
	if len(nodes.Items) != 1 {
		t.Fatalf("expected exactly one node through the forwarded API, got %d", len(nodes.Items))
	}
	nodeName := nodes.Items[0].Name
	t.Logf("target node: %s", nodeName)

	t.Log("waiting for the default namespace's own default ServiceAccount")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "default ServiceAccount", func(ctx context.Context) error {
		_, err := clientset.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{})
		return err
	}, nil)

	removeUnneededKubeSystemWorkloads(ctx, t, kubeconfigPath, clientset)

	t.Log("importing the real manager and operand images into the guest's own containerd")
	for _, tarPath := range []string{managerTar, nutServerTar, upsmonTar, actuatorTar, postgresTar} {
		importImageTarball(ctx, t, creds, tarPath)
	}

	restore := preserveFile(t, filepath.Join(repoRoot, "config", "manager", "kustomization.yaml"))
	defer restore()

	t.Log("make install (CRDs)")
	runMake(ctx, t, repoRoot, kubeconfigPath, nil, "install")

	t.Log("make deploy-byo-cert (RBAC, manager Deployment, webhook cert via hack/webhook-cert.sh)")
	runMake(ctx, t, repoRoot, kubeconfigPath, []string{"IMG=" + managerImage}, "deploy-byo-cert")

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
	}, nil)

	t.Log("creating the operand namespace")
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", outageFlowNamespace)

	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	upsmonRepo, upsmonTag := splitImageRef(t, upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, actuatorImage)

	// The .seq fixture's shape (dummy-loop mode, an explicit trailing TIMER on every state so no
	// state's window is only one ~2s driver poll wide) is the same proven-correct mechanism as
	// test/e2e/e2e_test.go's own scripted-transition spec, not a guess at NUT's own scripting
	// syntax. The OB hold duration deliberately differs from e2e's: e2e only watches
	// UPSDevice.status.phase directly, so its ~40s OB window is plenty. This test also has to
	// survive the real ShutdownFlow controller's own watch-latency + 1s eligibility hold before it
	// even starts executing, and a live run caught the fixture wrapping back to OL before that
	// finished inside a 40s window (docs/contributing/audits/hadron-vm-4-operator-2026-09-13.md).
	// OB holds for 600s here -- comfortably longer than any step budget in this test -- so nothing
	// here is racing the fixture's own loop. Tag values are quoted for the same reason as
	// TestHadronOperatorRunsRealUPSStack found live: they are pure-digit timestamps, and an
	// unquoted numeric-looking YAML scalar parses as a JSON number, not the string
	// ImageReference.Tag actually is.
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: hadron-outage-transitions
  namespace: %[1]s
data:
  sequence.seq: |
    device.mfr: nut-operator
    device.model: hadron-outage
    ups.mfr: nut-operator
    ups.model: hadron-outage
    ups.status: OL
    battery.charge: 100
    battery.runtime: 3600
    ups.load: 10

    TIMER 40

    device.mfr: nut-operator
    device.model: hadron-outage
    ups.mfr: nut-operator
    ups.model: hadron-outage
    ups.status: OB
    battery.charge: 40
    battery.runtime: 600
    ups.load: 10

    TIMER 600
---
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: hadron-outage-ups
spec:
  displayName: Hadron VM-4 Outage Dummy UPS
  driver: dummy-ups
  powerDomains:
    - hadron-domain
  simulation:
    sequenceConfigMapRef:
      namespace: %[1]s
      name: hadron-outage-transitions
  telemetry:
    pollInterval: 5s
    alertPollInterval: 5s
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: hadron-outage-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: hadron-outage-ups
  image:
    repository: %[2]s
    tag: "%[3]s"
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
---
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: hadron-outage-agent
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: hadron-outage-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[4]s
  mode: DryRun
  images:
    upsmon:
      repository: %[5]s
      tag: "%[6]s"
      pullPolicy: IfNotPresent
    actuator:
      repository: %[7]s
      tag: "%[8]s"
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: Simulate
    signalTTL: 2m
    requireFreshTelemetry: false
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hadron-outage-postgres
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: hadron-outage-postgres
  template:
    metadata:
      labels:
        app: hadron-outage-postgres
    spec:
      containers:
        - name: postgres
          image: %[9]s
          imagePullPolicy: IfNotPresent
          env:
            - name: POSTGRES_USER
              value: nutoperator
            - name: POSTGRES_PASSWORD
              value: hadron-outage-test
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
  name: hadron-outage-postgres
  namespace: %[1]s
spec:
  selector:
    app: hadron-outage-postgres
  ports:
    - port: 5432
      targetPort: 5432
---
apiVersion: v1
kind: Secret
metadata:
  name: hadron-outage-postgres-dsn
  namespace: %[1]s
stringData:
  dsn: postgres://nutoperator:hadron-outage-test@hadron-outage-postgres.%[1]s.svc.cluster.local:5432/nutoperator?sslmode=disable
---
apiVersion: power.zalud.io/v1alpha1
kind: ShutdownFlow
metadata:
  name: hadron-outage-flow
  annotations:
    power.zalud.io/hadron-outage-flow-approved: "true"
spec:
  managementClusterRef:
    name: hadron-outage-cluster
  mode: Enforce
  triggers:
    - type: OnBattery
      powerDomains:
        - hadron-domain
      for: 1s
  groups:
    - name: shutdown-agent
      action: AgentShutdown
      shutdownTier: 1
      target:
        agentRefs:
          - name: hadron-outage-agent
      timeout: 2m
  safety:
    requireManualApproval: true
    approvalAnnotation: power.zalud.io/hadron-outage-flow-approved
    allowUnidentifiedDevices: true
`, outageFlowNamespace,
		nutServerRepo, nutServerTag, nodeName,
		upsmonRepo, upsmonTag,
		actuatorRepo, actuatorTag,
		postgresRunImage)

	// Every kind here has a mutating webhook (the same reason test/e2e's own signal-delivery
	// fixture retries its apply): failing outright with a connection error until the manager's
	// webhook server is actually serving is expected on the first few attempts, not a fixture
	// problem.
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
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "NodePowerAgent Ready", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodepoweragent", "hadron-outage-agent", "-o", "jsonpath={.status.phase}")
		if phase != "Ready" {
			return fmt.Errorf("NodePowerAgent phase=%q, not Ready yet", phase)
		}
		return nil
	}, nil)

	t.Log("waiting for the real PostgreSQL Deployment to become Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "PostgreSQL Ready", func(ctx context.Context) error {
		dep, err := clientset.AppsV1().Deployments(outageFlowNamespace).Get(ctx, "hadron-outage-postgres", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if dep.Status.ReadyReplicas < 1 {
			return fmt.Errorf("postgres not ready yet: readyReplicas=%d", dep.Status.ReadyReplicas)
		}
		return nil
	}, nil)

	applyPowerManagementClusterAndWaitReady(ctx, t, kubeconfigPath, outageFlowNamespace)

	// Fixture and dwell times copied from test/e2e's own spec: OL held ~40s, then OB. This
	// Eventually window is sized past a full OL dwell plus telemetry-poll latency, not raced
	// against a single poll cycle.
	t.Log("waiting for the real dummy-ups driver to report OnBattery via real telemetry polling")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "UPSDevice OnBattery", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "upsdevice", "hadron-outage-ups", "-o", "jsonpath={.status.phase}")
		if phase != "OnBattery" {
			return fmt.Errorf("UPSDevice phase=%q, not OnBattery yet", phase)
		}
		return nil
	}, nil)
	t.Log("confirmed: real telemetry transitioned the UPSDevice to OnBattery")

	agentPodName := waitForExactlyOneRunningAgentPod(ctx, t, clientset, outageFlowNamespace)
	t.Log("waiting for the real actuator to observe a real, operator-written signal")

	waitForWithDiagnostics(t, ctx, 3*time.Minute, "actuator observes real signal", func(ctx context.Context) error {
		logCtx, logCancel := context.WithTimeout(ctx, 15*time.Second)
		defer logCancel()
		raw, err := clientset.CoreV1().Pods(outageFlowNamespace).GetLogs(agentPodName, &corev1.PodLogOptions{Container: "actuator"}).DoRaw(logCtx)
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
		out := runKubectlOutput(diagCtx, t, kubeconfigPath, "get", "shutdownflow", "hadron-outage-flow", "-o", "yaml")
		t.Logf("diagnostic ShutdownFlow state:\n%s", out)

		// A live run reached phase Aborted with reason AlreadyExecuted -- a dedup guard reporting
		// that a matching execution already has evidence, not the abort's own root cause
		// (internal/controller/shutdownflow_resume.go's markExecutionAlreadyRecorded only ever
		// reports an existing LastExecution status, it never sets Phase itself). The real reason the
		// first, genuine execution attempt aborted has to be in the manager's own log, since nothing
		// in ShutdownFlow.status names it.
		managerPods, listErr := clientset.CoreV1().Pods(operatorNamespace).List(diagCtx, metav1.ListOptions{
			LabelSelector: "control-plane=controller-manager",
		})
		if listErr != nil || len(managerPods.Items) != 1 {
			t.Logf("diagnostic manager log: could not identify manager pod (err=%v, pods=%d)", listErr, len(managerPods.Items))
			return
		}
		logCtx, logCancel := context.WithTimeout(ctx, 15*time.Second)
		defer logCancel()
		raw, logErr := clientset.CoreV1().Pods(operatorNamespace).GetLogs(managerPods.Items[0].Name, &corev1.PodLogOptions{}).DoRaw(logCtx)
		if logErr != nil {
			t.Logf("diagnostic manager log: fetching failed: %v", logErr)
			return
		}
		t.Logf("diagnostic manager log:\n%s", raw)
	})
	t.Log("confirmed: the real actuator accepted a real, operator-written signal -- produced by a real trigger evaluation and execution, not hand-written")
}

// removeUnneededKubeSystemWorkloads deletes the stock k3s Deployments this test does not exercise
// (local-path-provisioner: no PersistentVolumeClaim anywhere in this fixture; metrics-server: no
// HPA or resource-metrics query; traefik: no Ingress). CoreDNS is deliberately left alone --
// PostgreSQL is reached by its Service DNS name, hadron-outage-postgres.<namespace>.svc.cluster.
// local, so this test depends on it.
//
// Found live: the executor's AgentShutdown readiness check (internal/executor/executor.go's
// agentShutdownReadinessError, EX-9) refuses to proceed unless a node's non-exempt, non-DaemonSet
// pods are gone -- exempt is the manager's own namespace and every NodePowerAgent's operand
// namespace (internal/controller/shutdownflow_execution.go's clearanceExemptNamespaces), which
// does not cover kube-system. A single guest was assumed clear enough for this milestone
// (docs/contributing/audits/hadron-vm-4-operator-2026-09-13.md's own design rationale), but k3s's
// stock Deployments are real, non-exempt, non-DaemonSet pods and blocked with reason
// "NodeNotCleared" on a live run. Real drain/eviction against a live workload Pod remains a later
// milestone's work (that same audit doc's own "open, deliberately not attempted here" section) --
// removing components this test never uses is not a stand-in for that, it makes the node
// genuinely, accurately clear rather than working around the check.
func removeUnneededKubeSystemWorkloads(ctx context.Context, t *testing.T, kubeconfigPath string, clientset *kubernetes.Clientset) {
	t.Helper()
	names := []string{"local-path-provisioner", "metrics-server", "traefik"}
	t.Logf("removing unneeded kube-system Deployments so AgentShutdown's node-clearance check can pass: %s", strings.Join(names, ", "))
	for _, name := range names {
		runKubectl(ctx, t, kubeconfigPath, "-n", "kube-system", "delete", "deployment", name, "--ignore-not-found")
	}
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "unneeded kube-system pods gone", func(ctx context.Context) error {
		pods, err := clientset.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing kube-system pods: %w", err)
		}
		for _, pod := range pods.Items {
			for _, name := range names {
				if strings.HasPrefix(pod.Name, name+"-") {
					return fmt.Errorf("pod %q still present", pod.Name)
				}
			}
		}
		return nil
	}, nil)
}

// waitForExactlyOneRunningAgentPod waits for exactly one Running, non-terminating pod matching the
// NodePowerAgent DaemonSet's selector and returns its name. A one-shot List call right after
// NodePowerAgent reports Ready can still catch a DaemonSet rollout mid-transition -- a live run saw
// one Running pod alongside one Terminating pod from an earlier generation of the rendered
// template, both matching the same selector -- so this waits out that window instead of asserting
// on whatever the very first List call happened to return.
func waitForExactlyOneRunningAgentPod(ctx context.Context, t *testing.T, clientset *kubernetes.Clientset, namespace string) string {
	t.Helper()
	var agentPodName string
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "exactly one NodePowerAgent DaemonSet pod", func(ctx context.Context) error {
		agentPods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "power.zalud.io/nodepoweragent=hadron-outage-agent",
		})
		if err != nil {
			return fmt.Errorf("listing NodePowerAgent DaemonSet pods: %w", err)
		}
		var running []corev1.Pod
		for _, pod := range agentPods.Items {
			if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning {
				running = append(running, pod)
			}
		}
		if len(running) != 1 {
			return fmt.Errorf("expected exactly one Running NodePowerAgent DaemonSet pod, got %d Running of %d total", len(running), len(agentPods.Items))
		}
		agentPodName = running[0].Name
		return nil
	}, nil)
	return agentPodName
}

// applyPowerManagementClusterAndWaitReady applies the real PowerManagementCluster and waits for
// its PostgreSQL audit store to report ready. The caller must only invoke this once PostgreSQL
// itself is already confirmed Ready: checked live, for storage mode ExternalPostgres (unlike
// CNPG), Reconcile returns ctrl.Result{} with no RequeueAfter
// (internal/controller/powermanagementcluster_controller.go) whenever the audit-store connection
// fails, and nothing else watches the DSN Secret or anything else that would re-trigger it. A run
// that applied this CR in the same batch as the Postgres Deployment reconciled it exactly once,
// one second after creation -- long before Postgres had started listening -- and the resulting
// AuditStoreNotReady/"connection refused" status then sat frozen at that same lastTransitionTime
// for the rest of the test, because there was never a second reconcile to correct it. No amount of
// retrying the status *read* would have helped: the status itself was never being recomputed.
// Applying the CR only after Postgres already reports Ready makes this reconciler's one and only
// real attempt land against a reachable database.
func applyPowerManagementClusterAndWaitReady(ctx context.Context, t *testing.T, kubeconfigPath, namespace string) {
	t.Helper()
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: PowerManagementCluster
metadata:
  name: hadron-outage-cluster
spec:
  storage:
    mode: ExternalPostgres
    externalPostgres:
      dsnSecretKeyRef:
        namespace: %[1]s
        name: hadron-outage-postgres-dsn
        key: dsn
      requireTLS: false
`, namespace)
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

	// Now that the audit-store connection attempt is guaranteed to land against an already-ready
	// PostgreSQL, this wait exists to confirm ensureAuditStore
	// (internal/controller/powermanagementcluster_controller.go) actually succeeded -- opened a
	// connection, applied the audit schema, and enforced retention -- not merely that the CR was
	// accepted. Each attempt still gets its own short-lived context and logs its own error
	// immediately, the same pattern the fixture apply wait uses
	// (docs/contributing/audits/hadron-vm-4-operator-2026-09-13.md), so a slow final attempt reports
	// its real status instead of a context-cancellation artifact.
	t.Log("waiting for the PowerManagementCluster to report its PostgreSQL audit store ready")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "PowerManagementCluster storage Ready", func(ctx context.Context) error {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "get", "powermanagementcluster", "hadron-outage-cluster", "-o", "jsonpath={.status.storage.ready}")
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
		out := runKubectlOutput(diagCtx, t, kubeconfigPath, "get", "powermanagementcluster", "hadron-outage-cluster", "-o", "yaml")
		t.Logf("diagnostic PowerManagementCluster state:\n%s", out)
	})
}

// pullOperandImageTarball pulls a public image reference and re-tags it under this test's own
// fully-qualified tag before saving, the same tagging discipline buildOperandImageTarball uses for
// locally built images -- so nothing downstream (import, splitImageRef, the rendered manifest) has
// to reason about two different tagging conventions depending on where an image came from.
func pullOperandImageTarball(ctx context.Context, t *testing.T, sourceRef, namePrefix string) (imageRef, tarPath string) {
	t.Helper()
	pull := exec.CommandContext(ctx, "docker", "pull", sourceRef)
	if out, err := pull.CombinedOutput(); err != nil {
		t.Fatalf("docker pull %s: %v\n%s", sourceRef, err, out)
	}

	imageRef = fmt.Sprintf("docker.io/library/nut-operator-hadron-%s-test:%d", namePrefix, time.Now().UnixNano())
	tag := exec.CommandContext(ctx, "docker", "tag", sourceRef, imageRef)
	if out, err := tag.CombinedOutput(); err != nil {
		t.Fatalf("docker tag %s: %v\n%s", sourceRef, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", imageRef).Run() })

	tarPath = filepath.Join(t.TempDir(), namePrefix+".tar")
	save := exec.CommandContext(ctx, "docker", "save", "-o", tarPath, imageRef)
	if out, err := save.CombinedOutput(); err != nil {
		t.Fatalf("docker save %s: %v\n%s", namePrefix, err, out)
	}
	return imageRef, tarPath
}
