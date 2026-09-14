package kubeactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestTerminalControlPlaneBatch(t *testing.T) {
	for _, scenario := range []string{"create", "update", "validation failure", "mixed channels", "write failure"} {
		t.Run(scenario, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			writes, validated := 0, 0
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if scenario == "update" {
				builder = builder.WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "signals", Namespace: "system"}, Data: map[string][]byte{"delivery-channel": []byte("marker")}})
			}
			check := func(obj client.Object) error {
				writes++
				if validated != 3 {
					t.Errorf("write after only %d validations", validated)
				}
				secret := obj.(*corev1.Secret)
				for _, key := range []string{"a.json", "b.json", "c.json"} {
					if len(secret.Data[key]) == 0 {
						t.Errorf("missing %s", key)
					}
				}
				if scenario == "write failure" {
					return errors.New("API refused write")
				}
				return nil
			}
			c := builder.WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if err := check(obj); err != nil {
					return err
				}
				return c.Create(ctx, obj, opts...)
			}, Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if err := check(obj); err != nil {
					return err
				}
				return c.Update(ctx, obj, opts...)
			}}).Build()
			r := Runner{Client: c, ValidateNodeRelease: func(_ context.Context, release executor.NodeRelease) error {
				validated++
				if scenario == "validation failure" && release.NodeName == "b" {
					return errors.New("approval revoked")
				}
				return nil
			}}
			var releases []executor.NodeRelease
			for _, name := range []string{"a", "b", "c"} {
				releases = append(releases, executor.NodeRelease{NodeName: name, NodePowerAgent: "agent", TerminalHandoff: true, ControlPlaneNodes: []string{"a", "b", "c"}, SignalSecretNamespace: "system", SignalSecretName: "signals", SignalSecretKey: name + ".json"})
			}
			if scenario == "mixed channels" {
				releases[1].SignalSecretName = "other"
			}
			out, err := r.RunAction(context.Background(), executor.Action{ExecutionID: "run", ShutdownFlow: "flow", PlanConfigHash: "hash", Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: releases}})
			want := scenario == "create" || scenario == "update"
			if (err == nil) != want {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if want {
				if writes != 1 || len(out.SignalResults) != 3 {
					t.Fatalf("writes=%d results=%+v", writes, out.SignalResults)
				}
				for _, result := range out.SignalResults {
					if !result.Published {
						t.Fatal("missing publication receipt")
					}
				}
			}
			if scenario == "validation failure" || scenario == "mixed channels" {
				if writes != 0 {
					t.Fatal("partial terminal publication")
				}
			}
		})
	}
}

func TestSignalPublicationGateCancellation(t *testing.T) {
	if err := signalPublicationGate.Acquire(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	defer signalPublicationGate.Release(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	r := Runner{}
	if err := r.upsertSignalSecret(ctx, executor.Action{}, executor.NodeRelease{}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}
