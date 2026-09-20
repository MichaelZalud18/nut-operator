package scenario_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/diagnostics"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/fixture"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/scenario"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/workspace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// This is a framework-only composition contract. No adapter or existing scenario
// imports it, and the client is fake: it does not qualify guest/API integration.
func TestFailedScenarioCollectsBeforeCleanupAndRetainsWorkspace(t *testing.T) {
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "owned-cluster"}})
	client.PrependReactor("create", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		ns := a.(ktesting.CreateAction).GetObject().(*corev1.Namespace)
		ns.Name = "vm-fixture-composition"
		ns.UID = "owned-namespace"
		ns.ResourceVersion = "1"
		return false, nil, nil
	})
	source, parent, artifacts := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "fixture.yaml"), []byte("test fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var private string
	var ns *fixture.Namespace
	var bundle diagnostics.Bundle
	failure := errors.New("scenario assertion failed")
	report, err := scenario.Run(context.Background(), scenario.Plan{
		Timeout: time.Second, CleanupTimeout: time.Second, DiagnosticTimeout: time.Second,
		Steps: []scenario.Step{
			{Name: "prepare", Timeout: time.Second, Run: func(ctx context.Context, s *lifecycle.Scope) error {
				// Register callbacks first, covering partially completed setup.
				if err := s.Add("fixture", func(ctx context.Context) error {
					if ns != nil {
						return ns.Delete(ctx)
					}
					return nil
				}, func(context.Context) error {
					if private != "" {
						return os.RemoveAll(private)
					}
					return nil
				}); err != nil {
					return err
				}
				var err error
				private, err = workspace.Clone(ctx, source, parent)
				if err != nil {
					return err
				}
				ns, err = fixture.CreateNamespace(ctx, client, "owned-cluster", time.Second)
				return err
			}},
			{Name: "assert", Timeout: time.Second, Run: func(ctx context.Context, _ *lifecycle.Scope) error {
				if err := ns.Check(ctx); err != nil {
					return err
				}
				return failure
			}},
		},
		OnFailure: func(ctx context.Context, _ scenario.Report) error {
			var err error
			bundle, err = diagnostics.Capture(ctx, diagnostics.Options{Parent: artifacts, Timeout: time.Second, MaxBytes: 1024}, []diagnostics.Collector{{Name: "namespace", Timeout: time.Second, Collect: func(ctx context.Context, w io.Writer) error {
				if err := ns.Check(ctx); err != nil {
					return err
				} // Must still exist before stop.
				_, err := io.WriteString(w, ns.Name())
				return err
			}}})
			return err
		},
	})
	if !errors.Is(err, failure) || report.CleanupErr != nil || report.DiagnosticsErr != nil {
		t.Fatalf("%+v %v", report, err)
	}
	if _, err := client.CoreV1().Namespaces().Get(context.Background(), ns.Name(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("fixture remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(private, "fixture.yaml")); err != nil {
		t.Fatalf("failure workspace lost: %v", err)
	}
	data, err := os.ReadFile(bundle.Entries[0].Path)
	if err != nil || string(data) != ns.Name() {
		t.Fatalf("diagnostics lost: %q %v", data, err)
	}
}
