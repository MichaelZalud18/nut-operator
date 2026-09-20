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
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/scenario"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmprocess"

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

// TestTalosNodeBootstraps provisions through the Talos API and confirms exactly
// one real Ready node, using shared scenario cleanup and retained diagnostics.
// Host-driven teardown is lifecycle qualification, not actuator shutdown proof.
func TestTalosNodeBootstraps(t *testing.T) {
	ctx, cancel := talosSmokeContext(t)
	defer cancel()
	m, err := NewSafeMachineContext(ctx, Config{
		Memory: "4096", CPUs: "2", ISO: talosISOURL, ISOChecksum: talosISOChecksum,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := m.Config().StateDir
	t.Logf("owned machine state: %s", root)
	workDir := filepath.Join(root, "configuration")
	var controlplaneConfigPath, talosconfigPath, kubeconfigPath string
	var clientset *kubernetes.Clientset
	step := func(name string, budget time.Duration, run func(context.Context) error) scenario.Step {
		return scenario.Step{Name: name, Timeout: budget, Run: func(ctx context.Context, _ *lifecycle.Scope) error {
			t.Log(name)
			return run(ctx)
		}}
	}
	wait := func(ctx context.Context, check func(context.Context) error) error {
		return pollGuest(ctx, 5*time.Second, time.Minute, check, nil)
	}
	_, err = runMachineScenario(ctx, m, []scenario.Step{
		step("Talos maintenance API", 8*time.Minute, func(ctx context.Context) error {
			return wait(ctx, talosMaintenanceAPIReachable)
		}),
		step("configure and install Talos", 2*time.Minute, func(ctx context.Context) error {
			if err := os.Mkdir(workDir, 0700); err != nil {
				return err
			}
			var err error
			controlplaneConfigPath, talosconfigPath, err = genConfig(ctx, "nut-operator-talos-smoke", workDir)
			if err != nil {
				return err
			}
			return applyConfig(ctx, controlplaneConfigPath)
		}),
		step("Talos secure API after install and reboot", 5*time.Minute, func(ctx context.Context) error {
			return wait(ctx, func(ctx context.Context) error { return talosSecureAPIReachable(ctx, talosconfigPath) })
		}),
		step("bootstrap Kubernetes", time.Minute, func(ctx context.Context) error {
			return bootstrap(ctx, talosconfigPath)
		}),
		step("fetch private kubeconfig", 3*time.Minute, func(ctx context.Context) error {
			return wait(ctx, func(ctx context.Context) error {
				var err error
				kubeconfigPath, err = fetchKubeconfig(ctx, talosconfigPath, workDir)
				return err
			})
		}),
		step("construct Kubernetes client", time.Minute, func(_ context.Context) error {
			data, err := os.ReadFile(kubeconfigPath)
			if err != nil {
				return err
			}
			cfg, err := clientcmd.RESTConfigFromKubeConfig(data)
			if err != nil {
				return err
			}
			clientset, err = kubernetes.NewForConfig(cfg)
			return err
		}),
		step("exactly one real Ready node", 5*time.Minute, func(ctx context.Context) error {
			return wait(ctx, func(ctx context.Context) error {
				nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
				if err != nil {
					return fmt.Errorf("listing nodes: %w", err)
				}
				if len(nodes.Items) != 1 {
					return fmt.Errorf("expected exactly one node, got %d", len(nodes.Items))
				}
				if !nodeReady(nodes.Items[0]) {
					return fmt.Errorf("node %q is not Ready yet", nodes.Items[0].Name)
				}
				return nil
			})
		}),
	})
	if err != nil {
		t.Fatalf("Talos boot scenario (retained state %s): %v", root, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("successful cleanup left machine state: %v", err)
	}
	t.Log("confirmed real Node Ready and removal of stopped owned guest state")
}

// TestTalosStartupCancellation cancels after QEMU ownership is captured, before
// provisioning. It proves bounded stop and diagnostic retention on a real guest;
// it does not simulate cancellation inside PEG's non-cooperative Create call.
func TestTalosStartupCancellation(t *testing.T) {
	ctx, cancel := talosSmokeContext(t)
	defer cancel()
	m, err := NewSafeMachineContext(ctx, Config{
		Memory: "4096", CPUs: "2", ISO: talosISOURL, ISOChecksum: talosISOChecksum,
	})
	if err != nil {
		t.Fatal(err)
	}
	root := m.Config().StateDir
	report, err := runMachineScenario(ctx, m, []scenario.Step{{
		Name: "cancel before provisioning", Timeout: time.Second,
		Run: func(context.Context, *lifecycle.Scope) error { cancel(); return ctx.Err() },
	}})
	if !errors.Is(err, context.Canceled) || report.CleanupErr != nil || report.DiagnosticsErr != nil {
		t.Fatalf("cancellation failed (retained state %s): report=%+v err=%v", root, report, err)
	}
	if exited, err := vmprocess.Exited(m); err != nil || !exited {
		t.Fatalf("owned guest still live: exited=%t err=%v", exited, err)
	}
	bundles, err := filepath.Glob(filepath.Join(root, "vm-diagnostics-*", "steps.log"))
	if err != nil || len(bundles) != 1 {
		t.Fatalf("missing retained cancellation evidence: %v %v", bundles, err)
	}
	t.Logf("verified cancelled startup stopped owned QEMU and retained evidence: %s", bundles[0])
	// Workflow cleanup removes retained state after collecting the evidence.
}

func talosSmokeContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if deadline, ok := t.Deadline(); ok {
		bounded, cancel := context.WithDeadline(ctx, deadline.Add(-90*time.Second))
		return bounded, func() { cancel(); stop() }
	}
	return ctx, stop
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
