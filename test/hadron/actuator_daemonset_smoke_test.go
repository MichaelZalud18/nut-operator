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

// VM-3's own text: "finish shipped Linux actuator qualification through the real rendered
// DaemonSet/RBAC ... Prior bare-pod milestones are not full acceptance." actuator_smoke_test.go's
// three tests all create a hand-built corev1.Pod directly (actuatorPodSpec) -- proving the real
// actuator binary and its security context work under a real kernel, but never through the real
// NodePowerAgent-rendered DaemonSet, ServiceAccount, and RBAC an operator user would actually get.
// This file closes that gap: the same signal-validation scenarios actuator_smoke_test.go already
// proved against a bare Pod, now driven against the real rendered DaemonSet.
//
// Unverified against a real guest as of this writing, the same "Testable now; Conditional" status
// every one of this session's own milestones started at. In particular: the exact timing of
// signalStillAuthorized's own revocation pass (internal/controller/nodepoweragent_signals.go)
// against a hand-injected signal with no backing ShutdownFlow execution has no live evidence yet --
// see this file's own per-case comments for what each one is actually expected to prove.
package hadron

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

	"github.com/spectrocloud/peg/pkg/machine/types"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// actuatorDaemonSetNamespace is distinct from every other Hadron smoke test's own operand
// namespace so none of their fixtures can collide if a future change runs them in the same job.
const actuatorDaemonSetNamespace = "power-actuator-daemonset-hadron"

// actuatorDaemonSetApprovalAnnotation matches the key name already proven in
// internal/controller/nodepoweragent_controller_test.go's own "renders approved host poweroff..."
// envtest case -- a real, already-decided annotation key, not one invented for this test.
const actuatorDaemonSetApprovalAnnotation = "power.zalud.io/approved-for-actuation"

// actuatorDaemonSetGuest is one Hadron guest with the real operator, a real steady-state
// NUTServer/UPSDevice, and no NodePowerAgent applied yet -- the shared precondition every test in
// this file needs before it can differ on the one thing it is actually testing: what NodePowerAgent
// fixture (and what signal, if any) it applies on top.
type actuatorDaemonSetGuest struct {
	machine        types.Machine
	clientset      *kubernetes.Clientset
	kubeconfigPath string
	nodeName       string
	upsmonImage    string
	actuatorImage  string
}

// bootActuatorDaemonSetGuestReadyForNodePowerAgent boots one guest, deploys the real operator, and
// applies a real steady-state UPSDevice/NUTServer fixture -- the same minimal, proven shape
// TestHadronOperatorRunsRealUPSStack already deploys (ups_stack_smoke_test.go), reused verbatim
// since nothing about actuator qualification needs OnBattery telemetry or a ShutdownFlow. Every
// test in this file applies its own NodePowerAgent on top of this shared, already-Ready base.
func bootActuatorDaemonSetGuestReadyForNodePowerAgent(ctx context.Context, t *testing.T) actuatorDaemonSetGuest {
	t.Helper()
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
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", actuatorDaemonSetNamespace)

	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: hadron-actuator-ups
spec:
  displayName: Hadron VM-3 Actuator DaemonSet Dummy UPS
  driver: dummy-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: hadron-actuator-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: hadron-actuator-ups
  image:
    repository: %[2]s
    tag: "%[3]s"
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
`, actuatorDaemonSetNamespace, nutServerRepo, nutServerTag)

	t.Log("applying the real UPSDevice/NUTServer fixture")
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
	}, nil)

	return actuatorDaemonSetGuest{
		machine:        m,
		clientset:      clientset,
		kubeconfigPath: kubeconfigPath,
		nodeName:       nodeName,
		upsmonImage:    upsmonImage,
		actuatorImage:  actuatorImage,
	}
}

// applyApprovedActuatorNodePowerAgent applies a real NodePowerAgent CR with ActuatorPolicy:
// PowerOff, Mode: Actuate, and a valid, present approval annotation, then waits for the real
// rendered DaemonSet to report exactly one Running pod. Named consistently with
// nodePowerAgentActuatorPolicyRequiresApproval's own admission gate
// (internal/controller/nodepoweragent_render.go): PowerOff requires spec.mode Actuate,
// spec.shutdown.approvalAnnotation set, and that annotation present and "true" on the object.
func applyApprovedActuatorNodePowerAgent(ctx context.Context, t *testing.T, guest actuatorDaemonSetGuest, name string) string {
	t.Helper()
	upsmonRepo, upsmonTag := splitImageRef(t, guest.upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, guest.actuatorImage)
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: %[1]s
  annotations:
    %[2]s: "true"
spec:
  namespace: %[3]s
  nutServerRefs:
    - name: hadron-actuator-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[4]s
  mode: Actuate
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
    actuatorPolicy: PowerOff
    approvalAnnotation: %[2]s
    signalTTL: 2m
    requireFreshTelemetry: false
`, name, actuatorDaemonSetApprovalAnnotation, actuatorDaemonSetNamespace, guest.nodeName,
		upsmonRepo, upsmonTag, actuatorRepo, actuatorTag)

	t.Logf("applying the real, approved PowerOff NodePowerAgent %q", name)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+guest.kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply NodePowerAgent: %v\n%s", err, out)
	}

	t.Log("waiting for the NodePowerAgent to report Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "NodePowerAgent Ready", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, guest.kubeconfigPath, "get", "nodepoweragent", name, "-o", "jsonpath={.status.phase}")
		if phase != "Ready" {
			return fmt.Errorf("NodePowerAgent phase=%q, not Ready yet", phase)
		}
		return nil
	}, nil)

	return waitForExactlyOneRunningAgentPodNamed(ctx, t, guest.clientset, actuatorDaemonSetNamespace, name)
}

// waitForExactlyOneRunningAgentPodNamed is waitForExactlyOneRunningAgentPod
// (outage_flow_smoke_test.go) generalized to an arbitrary NodePowerAgent name -- that function
// hardcodes "hadron-outage-agent", which does not apply here.
func waitForExactlyOneRunningAgentPodNamed(ctx context.Context, t *testing.T, clientset *kubernetes.Clientset, namespace, agentName string) string {
	t.Helper()
	var podName string
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "exactly one NodePowerAgent DaemonSet pod", func(ctx context.Context) error {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "power.zalud.io/nodepoweragent=" + agentName,
		})
		if err != nil {
			return fmt.Errorf("listing NodePowerAgent DaemonSet pods: %w", err)
		}
		var running []corev1.Pod
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning {
				running = append(running, pod)
			}
		}
		if len(running) != 1 {
			return fmt.Errorf("expected exactly one Running NodePowerAgent DaemonSet pod, got %d Running of %d total", len(running), len(pods.Items))
		}
		podName = running[0].Name
		return nil
	}, nil)
	return podName
}

// writeRealSignal patches the real signal Secret a NodePowerAgent's own render creates
// (nodePowerAgentSignalSecretName: "<agentName>-node-signals", nodepoweragent_render.go) with
// payload under key "<nodeName>.json" -- the exact name/key the real actuator container polls
// (nodePowerAgentProjectedSignalPath), so this test injects into the same channel a real
// ShutdownFlow execution would write to, not a parallel one of its own invention like
// actuator_smoke_test.go's own hand-created Secret has to, since that test never has a real
// NodePowerAgent whose render already owns a Secret of that name.
//
// Unverified live: internal/controller/nodepoweragent_signals.go's own revokedSignalKeys can
// delete an unauthorized key on the very next reconcile after this patches it in (any case where
// signalStillAuthorized returns false with no matching ShutdownFlow -- an expired/future/malformed/
// missing-fields payload all resolve false immediately). That race resolves to the same outcome
// this test asserts on either way ("guest still running"), so it does not invalidate these cases,
// but it does mean a diagnostic dump of the Secret's own content moments after writing it may
// already show it gone -- expected, not itself a failure.
func writeRealSignal(ctx context.Context, t *testing.T, guest actuatorDaemonSetGuest, agentName string, payload nodeagent.ShutdownSignal) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode signal: %v", err)
	}
	secretName := agentName + "-node-signals"
	runKubectl(ctx, t, guest.kubeconfigPath, "-n", actuatorDaemonSetNamespace, "patch", "secret", secretName,
		"--type=json", "-p", fmt.Sprintf(`[{"op":"add","path":"/data/%s.json","value":"%s"}]`,
			guest.nodeName, base64.StdEncoding.EncodeToString(encoded)))
}

// waitForActuatorPodLog polls podName's own log for want, the DaemonSet-pod equivalent of
// actuator_smoke_test.go's own waitForActuatorLog (which targets a bare Pod in "default"; this one
// targets a real DaemonSet pod in actuatorDaemonSetNamespace with two containers, so the log fetch
// must name the "actuator" container specifically).
func waitForActuatorPodLog(ctx context.Context, t *testing.T, clientset *kubernetes.Clientset, podName, what, want string) string {
	t.Helper()
	var log string
	// 2026-09-16 first live run: three of five negative-signal subtests timed out at the previous
	// 2-minute budget, each one immediately following another subtest's own writeRealSignal call.
	// The actuator log itself proved why: it kept re-logging the *prior* subtest's own rejection
	// reason for over a minute after the new signal was patched in, because this is a real
	// Kubernetes projected-Secret-volume propagation delay, not a test or actuator bug -- Kubernetes'
	// own documentation states the total delay from a Secret update to it appearing in a mounted
	// volume can be as long as the kubelet sync period (1m default) plus the secret cache TTL (1m
	// default), i.e. up to 2 minutes in the worst case
	// (https://kubernetes.io/docs/concepts/configuration/secret/). Back-to-back subtests in the same
	// pod can land unluckily in that cycle. Widened past the documented worst case, with margin for
	// the actuator's own poll interval and API latency on top.
	waitForWithDiagnostics(t, ctx, 4*time.Minute, what, func(ctx context.Context) error {
		current, err := clientset.CoreV1().Pods(actuatorDaemonSetNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.Status.Phase != corev1.PodRunning && current.Status.Phase != corev1.PodPending {
			return fmt.Errorf("pod left Running/Pending unexpectedly: phase=%s", current.Status.Phase)
		}
		raw, err := clientset.CoreV1().Pods(actuatorDaemonSetNamespace).GetLogs(podName, &corev1.PodLogOptions{Container: "actuator"}).DoRaw(ctx)
		if err != nil {
			return err
		}
		log = string(raw)
		if !strings.Contains(log, want) {
			return fmt.Errorf("no %q yet:\n%s", want, log)
		}
		return nil
	}, func(ctx context.Context) {
		current, err := clientset.CoreV1().Pods(actuatorDaemonSetNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Logf("diagnostic pod fetch failed: %v", err)
			return
		}
		t.Logf("diagnostic pod status: phase=%s\n%+v", current.Status.Phase, current.Status)
	})
	return log
}

// TestHadronActuatorDaemonSetRejectsInvalidSignals re-runs actuator_smoke_test.go's own
// TestHadronActuatorRejectsInvalidSignals negative table -- identical signal payloads and expected
// rejection reasons, since InspectSignal's own validation (cmd/node-actuator's own gate logic) is
// unchanged by which Pod is running it -- but against the real rendered DaemonSet, RBAC, and
// ServiceAccount a real approved PowerOff NodePowerAgent produces, which is the part VM-3's own
// text says prior bare-pod milestones did not prove.
func TestHadronActuatorDaemonSetRejectsInvalidSignals(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootActuatorDaemonSetGuestReadyForNodePowerAgent(ctx, t)
	const agentName = "hadron-actuator-agent"
	podName := applyApprovedActuatorNodePowerAgent(ctx, t, guest, agentName)

	// Identical cases to actuator_smoke_test.go's own TestHadronActuatorRejectsInvalidSignals --
	// InspectSignal's own validation does not depend on which Pod is running it -- shared via
	// invalidActuatorSignalCases rather than duplicated.
	cases := invalidActuatorSignalCases()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeRealSignal(ctx, t, guest, agentName, tc.signal(guest.nodeName))

			log := waitForActuatorPodLog(ctx, t, guest.clientset, podName, "signal rejection log line",
				"halt gate=SignalAccepted result=fail detail=\""+tc.wantReason)
			t.Logf("actuator log:\n%s", log)
			if strings.Contains(log, "halt gate=ModeAuthorized") || strings.Contains(log, "halt gate=SyscallIssued") {
				t.Fatalf("actuator reached actuation gates on a signal that should have been rejected at SignalAccepted:\n%s", log)
			}

			t.Log("confirming the guest is still reachable -- the rejected signal must not have halted it")
			nodes, err := guest.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil || len(nodes.Items) != 1 {
				t.Fatalf("guest unreachable after a signal that should have been rejected, not actuated: err=%v nodes=%d", err, len(nodes.Items))
			}
		})
	}
}

// TestHadronActuatorDaemonSetRejectsAbsentApproval is VM-3's "absent approval" requirement: a
// PowerOff NodePowerAgent with no approvalAnnotation set at all must be rejected at admission --
// internal/controller/validation.go's own nodePowerAgentActuatorPolicyRequiresApproval gate --
// never reaching a running DaemonSet in the first place. Deliberately does not attempt "revoked"
// approval: whether removing an already-approved annotation is itself rejected by the same
// admission gate (making revocation a ratchet, not a runtime actuator behavior) or takes effect
// some other way has no live evidence yet, and guessing at it risks asserting the wrong mechanism
// entirely rather than leaving it honestly open.
func TestHadronActuatorDaemonSetRejectsAbsentApproval(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootActuatorDaemonSetGuestReadyForNodePowerAgent(ctx, t)
	upsmonRepo, upsmonTag := splitImageRef(t, guest.upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, guest.actuatorImage)
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: hadron-actuator-agent-unapproved
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: hadron-actuator-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[2]s
  mode: Actuate
  images:
    upsmon:
      repository: %[3]s
      tag: "%[4]s"
      pullPolicy: IfNotPresent
    actuator:
      repository: %[5]s
      tag: "%[6]s"
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: PowerOff
    signalTTL: 2m
    requireFreshTelemetry: false
`, actuatorDaemonSetNamespace, guest.nodeName, upsmonRepo, upsmonTag, actuatorRepo, actuatorTag)

	t.Log("applying a PowerOff NodePowerAgent with no approvalAnnotation set -- expecting admission to refuse it")
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+guest.kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected admission to reject a PowerOff NodePowerAgent with no approvalAnnotation, but apply succeeded:\n%s", out)
	}
	// 2026-09-16 first live run: admission did reject the request, but this assertion's expected
	// substring was stale -- internal/resourcevalidation/nodepoweragent.go actually raises
	// field.Required(specPath.Child("approvalAnnotation"), "required for "+policy+" actuation"),
	// which apimachinery renders as "<path>: Required value: <detail>", confirmed against that
	// source and the live error text below, not guessed.
	if !strings.Contains(string(out), "approvalAnnotation: Required value: required for PowerOff actuation") {
		t.Fatalf("apply failed, but not for the expected reason:\n%s", out)
	}
	t.Logf("confirmed: admission rejected the unapproved PowerOff NodePowerAgent:\n%s", out)
}

// TestHadronActuatorDaemonSetHaltsOnAcceptedSignal is VM-3's actual high-severity core, driven
// through the real rendered DaemonSet this time: a real, accepted signal delivered to the real
// operator-managed Secret, a real reboot(2), and hypervisor-confirmed evidence that the guest
// halted itself. See actuator_smoke_test.go's own TestHadronActuatorHaltsOnAcceptedSignal for why
// the pod's own log is not a pass condition here (a small idle guest reaches reboot(2) faster than
// its log line reliably survives the container-stdout -> containerd -> kubelet -> guest-API-server
// pipe) -- the same reasoning applies unchanged to a DaemonSet pod.
func TestHadronActuatorDaemonSetHaltsOnAcceptedSignal(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootActuatorDaemonSetGuestReadyForNodePowerAgent(ctx, t)
	const agentName = "hadron-actuator-agent-accepted"
	podName := applyApprovedActuatorNodePowerAgent(ctx, t, guest, agentName)

	writeRealSignal(ctx, t, guest, agentName, nodeagent.ShutdownSignal{
		ExecutionID:    "exec-accepted",
		NodeName:       guest.nodeName,
		PlanConfigHash: "test-hash",
		ShutdownFlow:   "test-flow",
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
	})

	t.Log("waiting for the guest's own QEMU process to exit on its own -- this test never stops it itself")
	process, err := machineProcess(guest.machine)
	if err != nil {
		t.Fatalf("getting machine process handle: %v", err)
	}
	defer func() { _ = process.Release() }()
	haltCtx, haltCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer haltCancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	exited := false
	for !exited {
		select {
		case <-haltCtx.Done():
			t.Fatalf("guest process never exited on its own within the budget -- the actuator armed but the guest did not actually halt (a container outside the host PID namespace can call reboot(2) successfully and leave the machine running); podName=%s", podName)
		case <-ticker.C:
			if err := process.Signal(syscall.Signal(0)); err != nil {
				exited = true
			}
		}
	}
	t.Log("confirmed: the guest's own QEMU process exited on its own, without this test stopping it, through the real rendered DaemonSet")
}
