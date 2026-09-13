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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// upsStackNamespace is the operand namespace this test's NUTServer/NodePowerAgent target --
// distinct from "default", matching how test/e2e's own dummy-ups fixtures always create a
// dedicated namespace rather than reusing one shared across specs.
const upsStackNamespace = "power-ups-hadron"

// TestHadronOperatorRunsRealUPSStack is VM-4's second milestone: the steady-state, non-outage
// half of its scope. With the real operator now running (the first milestone,
// TestHadronOperatorManagerDeploys), deploy a real dummy-ups-backed UPSDevice, NUTServer, and
// NodePowerAgent -- the same minimal fixture shape test/e2e's own signal-delivery spec already
// proves against Kind (test/e2e/e2e_test.go, "delivers a projected Secret signal to the
// NodePowerAgent actuator...") -- and confirm the real, operator-rendered NodePowerAgent
// DaemonSet reaches Ready on a real guest kernel. Every prior Hadron test (VM-2, VM-3) ran a
// bare, test-authored Pod; this is the first time the real rendered manifest -- the thing VM-3's
// own audit doc named as still open -- runs anywhere in Hadron.
//
// Deliberately stops short of any outage: NodePowerAgent.spec.mode is DryRun and
// actuatorPolicy is Simulate, the same safe configuration test/e2e's own signal-delivery spec
// uses, since nothing here can actually halt the guest. Wiring a ShutdownFlow trigger and driving
// a real Online -> OnBattery -> LowBattery transition through it -- so the operator produces its
// own signal instead of one this test hand-writes, per VM-4's own "manual signal injection alone
// is not this end-to-end test" -- is the next milestone.
func TestHadronOperatorRunsRealUPSStack(t *testing.T) {
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
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", upsStackNamespace)

	// internal/controller/nodepoweragent_render.go's renderImageReference does not default an
	// empty tag to "latest" -- it just omits the ":tag" suffix entirely, leaving the resolved
	// reference as bare "repository" (which containerd would then read as :latest itself). These
	// images were imported under an explicit timestamp tag, not :latest, so repository and tag
	// must both be set explicitly and match exactly what was actually imported.
	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	upsmonRepo, upsmonTag := splitImageRef(t, upsmonImage)
	actuatorRepo, actuatorTag := splitImageRef(t, actuatorImage)

	// Shape matches test/e2e/e2e_test.go's own "delivers a projected Secret signal..." fixture
	// exactly (repository/tag/pullPolicy substituted for this run's own locally-built, locally-
	// imported images) -- a proven-correct manifest, not a guess at the CRD schema. DryRun/
	// Simulate, also matching that spec: nothing here should be able to halt the guest.
	manifest := fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: hadron-ups
spec:
  displayName: Hadron VM-4 Dummy UPS
  driver: dummy-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: hadron-nutserver
spec:
  namespace: %[1]s
  deviceRefs:
    - name: hadron-ups
  image:
    repository: %[2]s
    tag: %[3]s
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
---
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: hadron-agent
spec:
  namespace: %[1]s
  nutServerRefs:
    - name: hadron-nutserver
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[4]s
  mode: DryRun
  images:
    upsmon:
      repository: %[5]s
      tag: %[6]s
      pullPolicy: IfNotPresent
    actuator:
      repository: %[7]s
      tag: %[8]s
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: Simulate
    signalTTL: 2m
    requireFreshTelemetry: false
`, upsStackNamespace,
		nutServerRepo, nutServerTag, nodeName,
		upsmonRepo, upsmonTag,
		actuatorRepo, actuatorTag)

	// Every kind here has a mutating webhook (test/e2e's own comment on this exact fixture): the
	// apply fails with a plain connection error until the manager's webhook server is actually
	// serving, which is not guaranteed the instant the Deployment reports Ready. Retried, not
	// applied once, for the same reason e2e retries it.
	t.Log("applying the real UPSDevice/NUTServer/NodePowerAgent fixture")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "fixture apply", func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		cmd.Stdin = strings.NewReader(manifest)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kubectl apply: %w\n%s", err, out)
		}
		return nil
	}, nil)

	t.Log("waiting for the NodePowerAgent to report Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "NodePowerAgent Ready", func(ctx context.Context) error {
		phase := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodepoweragent", "hadron-agent", "-o", "jsonpath={.status.phase}")
		if phase != "Ready" {
			return fmt.Errorf("NodePowerAgent phase=%q, not Ready yet", phase)
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodepoweragent", "hadron-agent", "-o", "yaml")
		t.Logf("diagnostic NodePowerAgent state:\n%s", out)
	})

	t.Log("confirming the real rendered DaemonSet pod is running")
	pods, err := clientset.CoreV1().Pods(upsStackNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "power.zalud.io/nodepoweragent=hadron-agent",
	})
	if err != nil {
		t.Fatalf("listing NodePowerAgent DaemonSet pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("expected exactly one NodePowerAgent DaemonSet pod, got %d", len(pods.Items))
	}
	t.Logf("confirmed: the real NodePowerAgent DaemonSet pod %s is running on a real guest kernel", pods.Items[0].Name)
}

// splitImageRef splits a fully-qualified image reference this test built itself
// (docker.io/library/<name>:<unix-nanos>, never a digest) into the repository and tag components
// NUTServer/NodePowerAgent's ImageReference type takes as two separate fields.
func splitImageRef(t *testing.T, imageRef string) (repository, tag string) {
	t.Helper()
	idx := strings.LastIndex(imageRef, ":")
	if idx < 0 {
		t.Fatalf("image reference %q has no tag to split", imageRef)
	}
	return imageRef[:idx], imageRef[idx+1:]
}

// importImageTarball imports one docker-save tarball into the guest's own containerd.
func importImageTarball(ctx context.Context, t *testing.T, creds Credentials, tarPath string) {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open image tarball %s: %v", tarPath, err)
	}
	defer func() { _ = f.Close() }()
	if out, err := guestCommandStdin(ctx, creds, "sudo k3s ctr -n k8s.io images import -", f); err != nil {
		t.Fatalf("importing %s into guest containerd: %v\n%s", tarPath, err, out)
	}
}

// runKubectl runs kubectl against kubeconfigPath, failing the test with the combined output on
// error.
func runKubectl(ctx context.Context, t *testing.T, kubeconfigPath string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// runKubectlOutput runs kubectl against kubeconfigPath and returns its trimmed stdout, failing
// the test with the combined output on error.
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
