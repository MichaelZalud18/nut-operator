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

package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestHandoffAuditUsesActualSecretWrites(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		failAt                                         int
		preexisting, cancelAfterFirst, missingEvidence bool
		published                                      int
	}{
		{name: "all published", failAt: -1, published: 3},
		{name: "failed create", failAt: 0},
		{name: "failed first update", failAt: 0, preexisting: true},
		{name: "partial update failure", failAt: 1, published: 1},
		{name: "cancel after first", failAt: -1, cancelAfterFirst: true, published: 1},
		{name: "success without receipts", failAt: -1, missingEvidence: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			issuedAt := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			writes := 0
			writeError := func() error {
				index := writes
				writes++
				if index == tc.failAt {
					return errors.New("fixture write failure")
				}
				return ctx.Err()
			}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.preexisting {
				builder = builder.WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "signals"}})
			}
			kube := builder.WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if err := writeError(); err != nil {
						return err
					}
					return c.Create(ctx, obj, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					if err := writeError(); err != nil {
						return err
					}
					return c.Update(ctx, obj, opts...)
				},
			}).Build()
			var runner executor.ActionRunner = kubeactions.Runner{Client: kube, ValidateNodeRelease: func(context.Context, executor.NodeRelease) error { return nil }, Clock: func() time.Time { return issuedAt }, SignalWritten: func(string, string, string, time.Time) {
				if tc.cancelAfterFirst {
					cancel()
				}
			}}
			if tc.missingEvidence {
				runner = succeedingControllerActionRunner{}
			}
			releases := make([]executor.NodeRelease, 3)
			for i := range releases {
				releases[i] = executor.NodeRelease{NodeName: fmt.Sprintf("node-%d", i), NodePowerAgent: "agents", SignalSecretNamespace: "power-system", SignalSecretName: "signals", SignalSecretKey: fmt.Sprintf("node-%d.json", i), AgentReady: true, TelemetryFresh: true, Cleared: true}
			}
			writer := &fakeAuditStore{}
			e := executor.Executor{Writer: writer, Runner: runner, Clock: time.Now, NewID: func() string { return "publication-test" }}
			_, err := e.Execute(ctx, executor.Input{ExecutionID: "publication-test", ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: executor.ModeEnforce, Approved: true,
				Waves: []executor.Wave{{Index: 0, Groups: []string{"nodes"}}}, Groups: []executor.Group{{Name: "nodes", Action: executor.ActionAgentShutdown, NodeReleases: releases}}})
			wantError := tc.failAt >= 0 || tc.cancelAfterFirst
			if (err != nil) != wantError {
				t.Fatalf("execution error=%v, wantError=%v", err, wantError)
			}
			if len(writer.nodeReleases) != 3 || len(writer.nodeSignalHandoffs) != 3 {
				t.Fatalf("missing per-node evidence: %d releases, %d handoffs", len(writer.nodeReleases), len(writer.nodeSignalHandoffs))
			}
			for i := range releases {
				want := i < tc.published
				if want && (writer.nodeSignalHandoffs[i].SignalPayload["timestamp"] != issuedAt.Format(time.RFC3339Nano) || !writer.nodeSignalHandoffs[i].StaleAfter.Equal(issuedAt.Add(2*time.Minute))) {
					t.Errorf("node %d: audit lost issued signal time: %#v", i, writer.nodeSignalHandoffs[i])
				}
				if writer.nodeReleases[i].Released != want || writer.nodeSignalHandoffs[i].Accepted != want {
					t.Errorf("node %d: expected published=%v; release=%#v, handoff=%#v", i, want, writer.nodeReleases[i], writer.nodeSignalHandoffs[i])
				}
			}
			var secret corev1.Secret
			_ = kube.Get(context.Background(), client.ObjectKey{Namespace: "power-system", Name: "signals"}, &secret)
			if len(secret.Data) != tc.published {
				t.Fatalf("stored signal count=%d, want %d", len(secret.Data), tc.published)
			}
		})
	}
}
