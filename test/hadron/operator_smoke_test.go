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

// operatorNamespace and operatorDeployment match config/default's namespace/namePrefix
// (nut-operator-system / nut-operator-) applied to config/manager's Deployment name
// (controller-manager) -- confirmed by reading the kustomization, not guessed.
const (
	operatorNamespace  = "nut-operator-system"
	operatorDeployment = "nut-operator-controller-manager"
)

// TestHadronOperatorManagerDeploys is VM-4's first milestone: get the real nut-operator manager --
// CRDs, RBAC, and the controller-manager Deployment, deployed through this repository's own
// config/byo-cert overlay and hack/webhook-cert.sh (the no-cert-manager path: "this operator's job
// is to run correctly while the cluster is losing power... any component that has to reconcile,
// issue, or inject something before admission works again is a component that can be unavailable
// at exactly the wrong moment," per that overlay's own comment) -- running for real inside a
// Hadron guest's k3s.
//
// Deliberately narrow: this proves the deployment mechanism itself (image import, make install,
// make deploy-byo-cert, the webhook cert script) works against a real guest kernel and kubelet,
// before wiring any NUTServer/UPSDevice/NodePowerAgent/ShutdownFlow CRs or attempting any part of
// the actual outage flow VM-4 exists to prove. No prior Hadron test has run the operator itself --
// VM-2 and VM-3 both ran bare, test-authored Pods, never the real rendered manifests. A live e2e
// run against Kind (test/e2e) does exercise these same manifests already; what a real guest adds
// is the same real-kernel boundary VM-3's own audit doc argues for the actuator -- a kubelet and
// containerd that are not sharing this project's own build/test-runner kernel.
func TestHadronOperatorManagerDeploys(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	repoRoot := repoRootDir(t)
	imageRef, tarPath := buildManagerImageTarball(ctx, t, repoRoot)

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

	// Same k3s startup race VM-3 found and fixed: a Ready node proves the kubelet came up, not
	// that the controllers populating the default namespace's own default ServiceAccount have run
	// yet, and every namespace this deploy creates needs the same guarantee to hold.
	t.Log("waiting for the default namespace's own default ServiceAccount")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "default ServiceAccount", func(ctx context.Context) error {
		_, err := clientset.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{})
		return err
	}, nil)

	t.Log("importing the real manager image into the guest's own containerd")
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open image tarball: %v", err)
	}
	defer func() { _ = f.Close() }()
	if out, err := guestCommandStdin(ctx, creds, "sudo k3s ctr -n k8s.io images import -", f); err != nil {
		t.Fatalf("importing manager image into guest containerd: %v\n%s", err, out)
	}

	// `make deploy-byo-cert` mutates config/manager/kustomization.yaml in place (kustomize edit
	// set image) -- the same side effect test/e2e's own suite records and restores, since nothing
	// else undoes it. Restored here so this run leaves no visible change in a working tree this
	// test does not own exclusively.
	restore := preserveFile(t, filepath.Join(repoRoot, "config", "manager", "kustomization.yaml"))
	defer restore()

	t.Log("make install (CRDs)")
	runMake(ctx, t, repoRoot, kubeconfigPath, nil, "install")

	t.Log("make deploy-byo-cert (RBAC, manager Deployment, webhook cert via hack/webhook-cert.sh)")
	runMake(ctx, t, repoRoot, kubeconfigPath, []string{"IMG=" + imageRef}, "deploy-byo-cert")

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
		pods, err := clientset.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("diagnostic pod list failed: %v", err)
			return
		}
		for _, p := range pods.Items {
			t.Logf("diagnostic pod %s phase=%s", p.Name, p.Status.Phase)
		}
	})
	t.Log("confirmed: the real nut-operator controller-manager is Running inside the guest")
}

// preserveFile snapshots path's current content and returns a func that restores it -- used
// around make targets that mutate checked-in files as a side effect (kustomize edit set image),
// so this test's own run leaves the working tree exactly as it found it.
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

// runMake runs `make target...` from repoRoot against kubeconfigPath, failing the test with the
// combined output on error.
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

// buildManagerImageTarball builds the real nut-operator manager image via this repository's own
// docker-build target and saves it to a local docker-save tarball. Tagged fully qualified
// (docker.io/library/...) from the start, rather than relying on docker's own implicit
// normalization of an unqualified tag to match up later at import/deploy time -- the same
// reference is used to build, save, import into the guest's containerd, and render into the
// Deployment's image field, so nothing has to agree on a normalization rule it never sees spelled
// out.
func buildManagerImageTarball(ctx context.Context, t *testing.T, repoRoot string) (imageRef, tarPath string) {
	t.Helper()
	imageRef = fmt.Sprintf("docker.io/library/nut-operator-hadron-manager-test:%d", time.Now().UnixNano())
	build := exec.CommandContext(ctx, "make", "docker-build", "IMG="+imageRef)
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("make docker-build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", imageRef).Run() })

	tarPath = filepath.Join(t.TempDir(), "nut-operator-manager.tar")
	save := exec.CommandContext(ctx, "docker", "save", "-o", tarPath, imageRef)
	if out, err := save.CombinedOutput(); err != nil {
		t.Fatalf("docker save nut-operator-manager: %v\n%s", err, out)
	}
	return imageRef, tarPath
}
