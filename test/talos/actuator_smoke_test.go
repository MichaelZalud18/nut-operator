//go:build talos_smoke
// +build talos_smoke

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

// VM-7's milestone 2: qualify the shipped TalosShutdown actuator policy, gated on milestone 1's
// deterministic bring-up (talos-vm-7-bootstrap-2026-09-17.md). Reuses test/hadron's own VM-3
// evidence standard -- missing/expired/wrong-node signals and absent approval must leave the guest
// running, and the approved case's halt must be confirmed outside the guest, not from Kubernetes
// NotReady alone -- against a real Talos node instead of a Linux one.
//
// Two things this guest needs that test/hadron's own guest does not:
//
//  1. No SSH and no `ctr images import` equivalent to load a locally built image. registry.go's
//     ephemeral local OCI registry plus genConfig's own --registry-mirror flag stand in: build and
//     push each image from the host, then tell the guest's machine configuration (applied before
//     first boot) to redirect pulls for that registry host to the ephemeral one, reachable from
//     inside the guest via QEMU user-mode networking's own host gateway address (10.0.2.2) with no
//     explicit port forward required.
//  2. A real target address for the Talos API itself. This is a single all-in-one node acting as
//     its own control plane, so `spec.shutdown.talos.endpoints` is that same node's own real
//     network identity as Kubernetes itself reports it (status.addresses' InternalIP) -- not
//     TalosAPIAddr's host-side loopback forward (meaningless from inside a pod's own network
//     namespace) and not guestGatewayHost (that reaches the host, not this guest).
package talos

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

	"github.com/spectrocloud/peg/pkg/machine/types"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/fixture"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/scenario"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/signalfixture"
)

// talosActuatorApprovalAnnotation matches the real, already-decided key
// internal/controller/nodepoweragent_controller_test.go's own envtest case proves, the same one
// test/hadron's DaemonSet milestone uses -- not one invented for this test.
const talosActuatorApprovalAnnotation = "power.zalud.io/approved-for-actuation"

// talosActuatorGuest is one bootstrapped, operator-deployed Talos guest with a real steady-state
// NUTServer/UPSDevice and a talosconfig Secret already in place, but no NodePowerAgent applied yet.
type talosActuatorGuest struct {
	namespace       *fixture.Namespace
	machine         types.Machine
	clientset       *kubernetes.Clientset
	kubeconfigPath  string
	nodeName        string
	nodeInternalIP  string
	talosconfigName string
	talosconfigKey  string
	upsmonImage     string
	actuatorImage   string
}

// bootTalosActuatorGuestReadyForNodePowerAgent runs milestone 1's own bring-up flow, deploys the
// real operator on top, and leaves a steady-state UPSDevice/NUTServer fixture and a talosconfig
// Secret in place. Every test in this file applies its own NodePowerAgent on top of this shared,
// already-Ready base.
func bootTalosActuatorGuestReadyForNodePowerAgent(ctx context.Context, t *testing.T) talosActuatorGuest {
	t.Helper()
	repoRoot := repoRootDir(t)
	registryHostPort := startLocalRegistry(ctx, t)
	managerImage := buildAndPushManagerImage(ctx, t, repoRoot, registryHostPort)
	nutServerImage := buildAndPushTalosImage(ctx, t, repoRoot, "images/nut-server/Dockerfile", "nutserver", registryHostPort)
	upsmonImage := buildAndPushTalosImage(ctx, t, repoRoot, "images/upsmon-agent/Dockerfile", "upsmon", registryHostPort)
	actuatorImage := buildAndPushTalosImage(ctx, t, repoRoot, "images/node-actuator/Dockerfile", "actuator", registryHostPort)

	m, err := NewSafeMachineContext(ctx, Config{
		Memory:      "4096",
		CPUs:        "2",
		ISO:         talosISOURL,
		ISOChecksum: talosISOChecksum,
	})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	var scope lifecycle.Scope
	if err := registerMachine(&scope, m); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retaining failed Talos fixture at %s", m.Config().StateDir)
			diagnosticCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := captureMachineDiagnostics(diagnosticCtx, m.Config().StateDir, scenario.Report{}); err != nil {
				t.Errorf("capture guest diagnostics: %v", err)
			}
		}
		if err := scope.Finish(context.Background(), 30*time.Second, t.Failed()); err != nil {
			t.Errorf("owned guest cleanup: %v", err)
		}
	})
	if _, err := m.Create(ctx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Logf("waiting for the Talos maintenance API on %s", TalosAPIAddr)
	waitForWithDiagnostics(t, ctx, 8*time.Minute, "Talos maintenance API", func(ctx context.Context) error {
		return talosMaintenanceAPIReachable(ctx)
	}, nil)

	workDir := t.TempDir()
	t.Log("generating machine configuration with a registry mirror for the locally built images")
	controlplaneConfigPath, talosconfigPath, err := genConfig(ctx, "nut-operator-talos-actuator-smoke", workDir,
		registryMirrorFlag(registryHostPort))
	if err != nil {
		t.Fatalf("gen config: %v", err)
	}

	t.Log("applying machine configuration over the insecure maintenance API")
	if err := applyConfig(ctx, controlplaneConfigPath); err != nil {
		t.Fatalf("apply config: %v", err)
	}

	t.Log("waiting for the Talos secure API after install and reboot")
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "Talos secure API", func(ctx context.Context) error {
		return talosSecureAPIReachable(ctx, talosconfigPath)
	}, nil)

	t.Log("bootstrapping the cluster")
	if err := bootstrap(ctx, talosconfigPath); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	t.Log("fetching kubeconfig")
	var kubeconfigPath string
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "kubeconfig available", func(ctx context.Context) error {
		path, err := fetchKubeconfig(ctx, talosconfigPath, workDir)
		if err != nil {
			return err
		}
		kubeconfigPath = path
		return nil
	}, nil)

	restConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatalf("building rest.Config from fetched kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building client from fetched kubeconfig: %v", err)
	}

	var nodeName, nodeInternalIP string
	t.Log("waiting for the real Node to report Ready and an InternalIP")
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "Node Ready with InternalIP", func(ctx context.Context) error {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing nodes: %w", err)
		}
		if len(nodes.Items) != 1 {
			return fmt.Errorf("expected exactly one node, got %d", len(nodes.Items))
		}
		node := nodes.Items[0]
		if !nodeReady(node) {
			return fmt.Errorf("node %q is not Ready yet", node.Name)
		}
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				nodeName, nodeInternalIP = node.Name, addr.Address
				return nil
			}
		}
		return fmt.Errorf("node %q Ready but has no InternalIP address yet", node.Name)
	}, nil)
	t.Logf("target node: %s (%s)", nodeName, nodeInternalIP)

	t.Log("waiting for the default namespace's own default ServiceAccount")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "default ServiceAccount", func(ctx context.Context) error {
		_, err := clientset.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{})
		return err
	}, nil)

	restore := preserveFile(t, filepath.Join(repoRoot, "config", "manager", "kustomization.yaml"))
	defer restore()

	t.Log("make install (CRDs)")
	runMake(ctx, t, repoRoot, kubeconfigPath, nil, "install")

	t.Log("make deploy-byo-cert (RBAC, manager Deployment, webhook cert via hack/webhook-cert.sh)")
	runMake(ctx, t, repoRoot, kubeconfigPath, []string{"IMG=" + managerImage}, "deploy-byo-cert")

	t.Log("waiting for the real controller-manager Deployment to become Ready")
	// 2026-09-17 first live run: this wait timed out with no evidence of why -- a bare
	// "context deadline exceeded" does not distinguish an image pull failure (the new
	// registry-mirror plumbing this guest alone depends on) from anything else. A diagnose
	// callback that lists the actual pod's container statuses answers that directly next time
	// instead of leaving it to guess.
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
		pods, err := clientset.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("diagnostic pod list failed: %v", err)
			return
		}
		for _, pod := range pods.Items {
			t.Logf("diagnostic pod %s: phase=%s", pod.Name, pod.Status.Phase)
			for _, cs := range pod.Status.ContainerStatuses {
				t.Logf("  container %s: ready=%v restarts=%d state=%+v", cs.Name, cs.Ready, cs.RestartCount, cs.State)
			}
		}
	})

	// The private kubeconfig was generated for this newly provisioned guest.
	// Record that cluster's identity before creating and rechecking the fixture.
	identityCtx, identityCancel := context.WithTimeout(ctx, 15*time.Second)
	cluster, err := clientset.CoreV1().Namespaces().Get(identityCtx, "kube-system", metav1.GetOptions{})
	identityCancel()
	if err != nil {
		t.Fatalf("recording owned cluster identity: %v", err)
	}
	namespace, err := fixture.CreateNamespace(ctx, clientset, cluster.UID, 30*time.Second)
	if err != nil {
		t.Fatalf("creating owned operand namespace: %v", err)
	}
	if err := namespace.Check(ctx); err != nil {
		t.Fatal(err)
	}
	// The whole disposable cluster belongs to scope. Namespace deletion is not
	// attempted after positive actuation has powered off its API server.
	operandNamespace := namespace.Name()

	t.Log("creating the talosconfig Secret the TalosShutdown actuator will mount")
	talosconfigBytes, err := os.ReadFile(talosconfigPath)
	if err != nil {
		t.Fatalf("reading generated talosconfig: %v", err)
	}
	const talosconfigSecretName = "talos-actuator-talosconfig"
	const talosconfigSecretKey = "config"
	secretManifest := fmt.Sprintf("apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\nstringData:\n  %s: |\n%s\n",
		talosconfigSecretName, operandNamespace, talosconfigSecretKey, indentLines(string(talosconfigBytes), "    "))
	applyManifest(ctx, t, kubeconfigPath, secretManifest)

	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	fixtureManifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: talos-actuator-ups
spec:
  displayName: Talos VM-7 Actuator Dummy UPS
  driver: dummy-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: talos-actuator-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: talos-actuator-ups
  image:
    repository: %[2]s
    tag: "%[3]s"
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
`, operandNamespace, nutServerRepo, nutServerTag)

	t.Log("applying the real UPSDevice/NUTServer fixture")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "fixture apply", func(ctx context.Context) error {
		return tryApplyManifest(ctx, kubeconfigPath, fixtureManifest)
	}, nil)

	return talosActuatorGuest{
		namespace:       namespace,
		machine:         m,
		clientset:       clientset,
		kubeconfigPath:  kubeconfigPath,
		nodeName:        nodeName,
		nodeInternalIP:  nodeInternalIP,
		talosconfigName: talosconfigSecretName,
		talosconfigKey:  talosconfigSecretKey,
		upsmonImage:     upsmonImage,
		actuatorImage:   actuatorImage,
	}
}

// applyApprovedTalosActuatorNodePowerAgent applies a real NodePowerAgent CR with ActuatorPolicy:
// TalosShutdown, Mode: Actuate, and a valid, present approval annotation, then waits for the real
// rendered DaemonSet to report exactly one Running pod.
func applyApprovedTalosActuatorNodePowerAgent(ctx context.Context, t *testing.T, guest talosActuatorGuest, name string) string {
	t.Helper()
	if err := guest.namespace.Check(ctx); err != nil {
		t.Fatalf("operand namespace ownership: %v", err)
	}
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
    - name: talos-actuator-nutserver
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
    actuatorPolicy: TalosShutdown
    approvalAnnotation: %[2]s
    signalTTL: 2m
    requireFreshTelemetry: false
    talos:
      talosConfigSecretKeyRef:
        namespace: %[3]s
        name: %[9]s
        key: %[10]s
      endpoints:
        - %[11]s
`, name, talosActuatorApprovalAnnotation, guest.namespace.Name(), guest.nodeName,
		upsmonRepo, upsmonTag, actuatorRepo, actuatorTag,
		guest.talosconfigName, guest.talosconfigKey, guest.nodeInternalIP)

	t.Logf("applying the real, approved TalosShutdown NodePowerAgent %q", name)
	applyManifest(ctx, t, guest.kubeconfigPath, manifest)

	t.Log("waiting for the NodePowerAgent to report Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "NodePowerAgent Ready", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, guest.kubeconfigPath, "get", "nodepoweragent", name, "-o", "jsonpath={.status.phase}")
		if phase != "Ready" {
			return fmt.Errorf("NodePowerAgent phase=%q, not Ready yet", phase)
		}
		return nil
	}, nil)

	return waitForExactlyOneRunningTalosAgentPod(ctx, t, guest.clientset, guest.namespace.Name(), name)
}

func waitForExactlyOneRunningTalosAgentPod(ctx context.Context, t *testing.T, clientset *kubernetes.Clientset, namespace, agentName string) string {
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

// writeRealTalosSignal patches the real signal Secret a NodePowerAgent's own render creates, the
// same mechanism and the same live caveat as test/hadron's own writeRealSignal (revocation can
// beat the actuator to an already-unauthorized payload -- see waitForTalosSignalRejectedOrRevoked).
func writeRealTalosSignal(ctx context.Context, t *testing.T, guest talosActuatorGuest, agentName string, payload nodeagent.ShutdownSignal) {
	t.Helper()
	if err := guest.namespace.Check(ctx); err != nil {
		t.Fatalf("operand namespace ownership: %v", err)
	}
	patch, err := signalfixture.SecretPatch(guest.nodeName, payload)
	if err != nil {
		t.Fatalf("encode signal patch: %v", err)
	}
	secretName := agentName + "-node-signals"
	runKubectl(ctx, t, guest.kubeconfigPath, "-n", guest.namespace.Name(), "patch", "secret", secretName,
		"--type=json", "-p", string(patch))
}

// waitForTalosSignalRejectedOrRevoked mirrors test/hadron's own waitForSignalRejectedOrRevoked:
// an invalid signal never authorizing a halt can be proven by either the actuator's own
// InspectSignal gate rejecting it and logging why, or the NodePowerAgent controller's own
// revocation (signalStillAuthorized) deleting the key before the actuator ever reads it -- the same
// mechanism, unchanged by which guest OS the actuator runs on. See
// hadron-vm-3-actuator-daemonset-2026-09-17.md for the live evidence this was built from.
func waitForTalosSignalRejectedOrRevoked(ctx context.Context, t *testing.T, guest talosActuatorGuest, agentName, podName, wantReason string) {
	t.Helper()
	secretName := agentName + "-node-signals"
	signalKey := guest.nodeName + ".json"
	var log string
	waitForWithDiagnostics(t, ctx, 4*time.Minute, "signal rejection or revocation", func(ctx context.Context) error {
		secret, err := guest.clientset.CoreV1().Secrets(guest.namespace.Name()).Get(ctx, secretName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if _, present := secret.Data[signalKey]; !present {
			return nil
		}
		current, err := guest.clientset.CoreV1().Pods(guest.namespace.Name()).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.Status.Phase != corev1.PodRunning && current.Status.Phase != corev1.PodPending {
			return fmt.Errorf("pod left Running/Pending unexpectedly: phase=%s", current.Status.Phase)
		}
		raw, err := guest.clientset.CoreV1().Pods(guest.namespace.Name()).GetLogs(podName, &corev1.PodLogOptions{Container: "actuator"}).DoRaw(ctx)
		if err != nil {
			return err
		}
		log = string(raw)
		want := "halt gate=SignalAccepted result=fail detail=\"" + wantReason
		if !strings.Contains(log, want) {
			return fmt.Errorf("signal key %q still present in the Secret and no %q rejection logged yet", signalKey, want)
		}
		return nil
	}, func(ctx context.Context) {
		current, err := guest.clientset.CoreV1().Pods(guest.namespace.Name()).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Logf("diagnostic pod fetch failed: %v", err)
			return
		}
		t.Logf("diagnostic pod status: phase=%s\n%+v", current.Status.Phase, current.Status)
	})
	if log != "" {
		t.Logf("actuator log:\n%s", log)
	}
	if strings.Contains(log, "halt gate=ModeAuthorized") || strings.Contains(log, "halt gate=SyscallIssued") {
		t.Fatalf("actuator reached actuation gates on a signal that should have been rejected at SignalAccepted:\n%s", log)
	}
}

// TestTalosActuatorRejectsInvalidSignals is VM-7 milestone 2's negative-signal control: the same
// five cases test/hadron's own actuator qualification proved, now through a real TalosShutdown
// NodePowerAgent. None of these may reach actuation; see waitForTalosSignalRejectedOrRevoked's own
// comment for why each is checked against either of two real outcomes rather than one assumed.
func TestTalosActuatorRejectsInvalidSignals(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootTalosActuatorGuestReadyForNodePowerAgent(ctx, t)
	const agentName = "talos-actuator-agent"
	podName := applyApprovedTalosActuatorNodePowerAgent(ctx, t, guest, agentName)

	for i, tc := range signalfixture.Invalid(guest.nodeName, time.Now()) {
		t.Run(tc.Name, func(t *testing.T) {
			// Earlier cases can take minutes. Refresh the clock just before delivery
			// so time-based cases retain their intended rejection reason.
			current := signalfixture.Invalid(guest.nodeName, time.Now())[i]
			writeRealTalosSignal(ctx, t, guest, agentName, current.Payload)
			waitForTalosSignalRejectedOrRevoked(ctx, t, guest, agentName, podName, current.Reason)

			t.Log("confirming the guest is still reachable -- the rejected signal must not have halted it")
			nodes, err := guest.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil || len(nodes.Items) != 1 {
				t.Fatalf("guest unreachable after a signal that should have been rejected, not actuated: err=%v nodes=%d", err, len(nodes.Items))
			}
		})
	}
}

// TestTalosActuatorRejectsAbsentApproval is VM-7 milestone 2's absent-approval control, identical
// in spirit to test/hadron's own: a TalosShutdown NodePowerAgent with no approvalAnnotation set
// must be rejected at admission, never reaching a running DaemonSet at all. Revoked approval is
// deliberately not attempted here either, for the same reason test/hadron's own DaemonSet milestone
// left it open -- no live evidence yet for what that actually does.
func TestTalosActuatorRejectsAbsentApproval(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootTalosActuatorGuestReadyForNodePowerAgent(ctx, t)
	upsmonRepo, upsmonTag := splitImageRef(t, guest.upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, guest.actuatorImage)
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: talos-actuator-agent-unapproved
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: talos-actuator-nutserver
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
    actuatorPolicy: TalosShutdown
    signalTTL: 2m
    requireFreshTelemetry: false
    talos:
      talosConfigSecretKeyRef:
        namespace: %[1]s
        name: %[7]s
        key: %[8]s
      endpoints:
        - %[9]s
`, guest.namespace.Name(), guest.nodeName, upsmonRepo, upsmonTag, actuatorRepo, actuatorTag,
		guest.talosconfigName, guest.talosconfigKey, guest.nodeInternalIP)

	t.Log("applying a TalosShutdown NodePowerAgent with no approvalAnnotation set -- expecting admission to refuse it")
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+guest.kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected admission to reject a TalosShutdown NodePowerAgent with no approvalAnnotation, but apply succeeded:\n%s", out)
	}
	// Confirmed against internal/resourcevalidation/nodepoweragent.go and test/hadron's own
	// 2026-09-17 live evidence for the identical PowerOff case -- apimachinery's field.Required
	// renders as "<path>: Required value: <detail>", not free text.
	if !strings.Contains(string(out), "approvalAnnotation: Required value: required for TalosShutdown actuation") {
		t.Fatalf("apply failed, but not for the expected reason:\n%s", out)
	}
	t.Logf("confirmed: admission rejected the unapproved TalosShutdown NodePowerAgent:\n%s", out)
}

// TestTalosActuatorHaltsOnAcceptedSignal is VM-7 milestone 2's high-severity core: a real, accepted
// signal, a real Talos MachineService.Shutdown call, and hypervisor-confirmed evidence that the
// guest halted itself. Unlike test/hadron's reboot(2) POWER_OFF, which reaches process exit in
// seconds, Talos's own Shutdown sequence cordons and drains the node, stops system services in
// order, unmounts filesystems, and only then powers off -- confirmed against Talos's own
// documentation, not assumed to match the Linux actuator's timing. The budget below is wider to
// match; the underlying evidence check (the guest's own QEMU process exiting on its own, never
// stopped by this test) is unchanged.
func TestTalosActuatorHaltsOnAcceptedSignal(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootTalosActuatorGuestReadyForNodePowerAgent(ctx, t)
	const agentName = "talos-actuator-agent-accepted"
	applyApprovedTalosActuatorNodePowerAgent(ctx, t, guest, agentName)

	writeRealTalosSignal(ctx, t, guest, agentName, nodeagent.ShutdownSignal{
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
	haltCtx, haltCancel := context.WithTimeout(ctx, 6*time.Minute)
	defer haltCancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	exited := false
	for !exited {
		select {
		case <-haltCtx.Done():
			t.Fatalf("guest process never exited on its own within the budget -- the actuator armed but the guest did not actually halt")
		case <-ticker.C:
			if err := process.Signal(syscall.Signal(0)); err != nil {
				exited = true
			}
		}
	}
	t.Log("confirmed: the guest's own QEMU process exited on its own, without this test stopping it, through the real Talos MachineService.Shutdown call")
}

// repoRootDir finds this checkout's own top-level directory, since a test invoked as
// `go test ./test/talos` runs with its working directory set to the package directory, not the
// repo root, regardless of where `go test` itself was run from. Identical to test/hadron's own.
func repoRootDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// preserveFile snapshots path's contents and returns a restore func, identical to test/hadron's
// own -- make deploy-byo-cert mutates config/manager/kustomization.yaml in place.
func preserveFile(t *testing.T, path string) func() {
	t.Helper()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s before mutating it: %v", path, err)
	}
	return func() {
		if err := os.WriteFile(path, original, 0644); err != nil {
			t.Errorf("restoring %s: %v", path, err)
		}
	}
}

// runMake runs `make target...` from repoRoot against kubeconfigPath, identical to test/hadron's
// own.
func runMake(ctx context.Context, t *testing.T, repoRoot, kubeconfigPath string, extraEnv []string, target ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "make", target...)
	cmd.Dir = repoRoot
	cmd.Env = append(append(os.Environ(), "KUBECONFIG="+kubeconfigPath), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make %s: %v\n%s", strings.Join(target, " "), err, out)
	}
}

// runKubectl runs kubectl against kubeconfigPath, failing the test with the combined output on
// error. Identical to test/hadron's own.
func runKubectl(ctx context.Context, t *testing.T, kubeconfigPath string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// runKubectlOutput runs kubectl against kubeconfigPath and returns its trimmed stdout, failing the
// test with the combined output on error. Identical to test/hadron's own.
func runKubectlOutput(ctx context.Context, t *testing.T, kubeconfigPath string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// splitImageRef splits a fully qualified image reference into repository and tag. Identical to
// test/hadron's own.
func splitImageRef(t *testing.T, imageRef string) (repository, tag string) {
	t.Helper()
	idx := strings.LastIndex(imageRef, ":")
	if idx < 0 {
		t.Fatalf("image reference %q has no tag to split", imageRef)
	}
	return imageRef[:idx], imageRef[idx+1:]
}

// applyManifest applies manifest via kubectl, failing the test with the combined output on error.
func applyManifest(ctx context.Context, t *testing.T, kubeconfigPath, manifest string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply: %v\n%s", err, out)
	}
}

// tryApplyManifest is applyManifest's non-fatal form, for use inside a retried poll loop.
func tryApplyManifest(ctx context.Context, kubeconfigPath, manifest string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl apply: %w\n%s", err, out)
	}
	return nil
}

// indentLines prefixes every line of s with prefix, for embedding a multi-line file's content
// under a YAML block scalar (stringData's own "|" literal style).
func indentLines(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

const operatorNamespace = "nut-operator-system"
const operatorDeployment = "nut-operator-controller-manager"
