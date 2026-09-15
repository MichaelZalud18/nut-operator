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

	t.Log("importing the real manager and operand images into the guest's own containerd")
	for _, tarPath := range []string{managerTar, nutServerTar, upsmonTar, actuatorTar} {
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
apiVersion: power.zalud.io/v1alpha1
kind: ShutdownFlow
metadata:
  name: hadron-outage-flow
  annotations:
    power.zalud.io/hadron-outage-flow-approved: "true"
spec:
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
		actuatorRepo, actuatorTag)

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

	t.Log("waiting for the real actuator to observe a real, operator-written signal")
	agentPods, err := clientset.CoreV1().Pods(outageFlowNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "power.zalud.io/nodepoweragent=hadron-outage-agent",
	})
	if err != nil {
		t.Fatalf("listing NodePowerAgent DaemonSet pods: %v", err)
	}
	if len(agentPods.Items) != 1 {
		t.Fatalf("expected exactly one NodePowerAgent DaemonSet pod, got %d", len(agentPods.Items))
	}
	agentPodName := agentPods.Items[0].Name

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
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "shutdownflow", "hadron-outage-flow", "-o", "yaml")
		t.Logf("diagnostic ShutdownFlow state:\n%s", out)
	})
	t.Log("confirmed: the real actuator accepted a real, operator-written signal -- produced by a real trigger evaluation and execution, not hand-written")
}
