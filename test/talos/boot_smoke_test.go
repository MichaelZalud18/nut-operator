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

// Separate build tag from the rest of the package (`talos_smoke`, not `talos`): this needs real
// KVM, downloads a real ISO, and performs a full unattended install and reboot, taking minutes
// rather than seconds -- the same reasoning test/hadron's own hadron/hadron_smoke split documents.
// Only talos-vm-boot-smoke.yml's workflow_dispatch job builds with this tag.
package talos

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Pinned against the release's own sha256sum.txt sidecar (cross-checked, not merely copied): the
// same provenance standard test/hadron's own pinned Kairos artifact and this repository's other
// externally-sourced pins (images/*/Dockerfile's digest-pinned base images) already hold to.
const (
	talosISOURL      = "https://github.com/siderolabs/talos/releases/download/v1.12.9/metal-amd64.iso"
	talosISOChecksum = "7f6e8ee537cf19dd873333578d5e117725a781108b0a6934323a869d2cc93666" // pragma: allowlist secret -- a public release checksum, not a credential
)

// TestTalosNodeBootstraps is VM-7's first milestone: pin the Talos artifact, generate and apply
// machine configuration through this package's own guest adapter (talosctl.go), reach the Talos
// API from the host, bootstrap one Kubernetes node, fetch kubeconfig, and verify actual Node Ready
// from the host -- then prove clean owned teardown. Deliberately not TalosShutdown qualification:
// VM-7's own text gates that on this bring-up being deterministic first.
//
// Unverified against a real guest as of this writing (docs/contributing/audits/
// vm-test-research-2026-09-15.md#vm-7 names this milestone "Testable now; Conditional" for exactly
// this reason) -- expect at least one live run to find something this design got wrong, the same
// way every one of test/hadron's own milestones did on its first attempt.
func TestTalosNodeBootstraps(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	m, err := NewSafeMachineContext(ctx, Config{
		Memory:      "4096",
		CPUs:        "2",
		ISO:         talosISOURL,
		ISOChecksum: talosISOChecksum,
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

	t.Logf("waiting for the Talos maintenance API on %s", TalosAPIAddr)
	// 2026-09-16 first live run: the metal ISO's isolinux menu mirrors to the captured serial
	// console (SeaBIOS's own display-to-serial mirroring when no VGA display is attached), but the
	// Talos kernel itself does not -- its default boot entry has no console=ttyS0, so nothing
	// further appears on that console even on a successful boot. That run's own guest never opened
	// port 50000 within a 3-minute budget; CD-ROM-backed ISO boot under nested KVM plus Talos's own
	// first-boot initialization is the likely cause, not a wiring defect (no connection-refused
	// evidence, only deadline-exceeded). Widened budget, still comfortably inside the 26m go test
	// deadline alongside the other three waits (5m + 3m + 5m).
	waitForWithDiagnostics(t, ctx, 8*time.Minute, "Talos maintenance API", func(ctx context.Context) error {
		return talosMaintenanceAPIReachable(ctx)
	}, nil)

	workDir := t.TempDir()
	t.Log("generating machine configuration")
	controlplaneConfigPath, talosconfigPath, err := genConfig(ctx, "nut-operator-talos-smoke", workDir)
	if err != nil {
		t.Fatalf("gen config: %v", err)
	}

	t.Log("applying machine configuration over the insecure maintenance API")
	if err := applyConfig(ctx, controlplaneConfigPath); err != nil {
		t.Fatalf("apply config: %v", err)
	}

	// The applied config triggers an install-to-disk and reboot, the same install-then-reboot
	// shape test/hadron's own Kairos flow already has a proven wait for -- this is a second,
	// independent wait rather than assumed to follow immediately once apply-config returns.
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

	kubeconfig, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		t.Fatalf("reading fetched kubeconfig: %v", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parsing fetched kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building client from fetched kubeconfig: %v", err)
	}

	t.Log("waiting for the real Node to report Ready from outside the guest")
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "Node Ready", func(ctx context.Context) error {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing nodes through the forwarded Kubernetes API: %w", err)
		}
		if len(nodes.Items) != 1 {
			return fmt.Errorf("expected exactly one node, got %d", len(nodes.Items))
		}
		if !nodeReady(nodes.Items[0]) {
			return fmt.Errorf("node %q is not Ready yet", nodes.Items[0].Name)
		}
		return nil
	}, nil)
	t.Log("reached the guest's Kubernetes API from outside it and confirmed the real Node Ready")
}

// nodeReady matches the actual Ready condition, not the substring also present in NotReady --
// the same distinction test/hadron's own hasReadyNode makes.
func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// waitForWithDiagnostics polls with an optional diagnose callback invoked roughly every minute
// while still waiting. Identical to test/hadron's own equivalent.
func waitForWithDiagnostics(t *testing.T, parent context.Context, timeout time.Duration, what string, check func(context.Context) error, diagnose func(context.Context)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := pollGuest(ctx, 5*time.Second, time.Minute, check, diagnose); err != nil {
		t.Fatalf("waiting for %s (budget %s): %v", what, timeout, err)
	}
}
