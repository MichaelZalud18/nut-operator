package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestLiveControlPlaneQuorum(t *testing.T) {
	for _, scenario := range []string{"healthy", "unready peer", "missing peer", "pending peer", "reconciled pending peer", "terminal", "failed read", "worker"} {
		t.Run(scenario, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			var objects []client.Object
			for _, name := range []string{"a", "b", "c"} {
				if name == "b" && scenario == "missing peer" {
					continue
				}
				ready := corev1.ConditionTrue
				if name == "b" && scenario == "unready peer" {
					ready = corev1.ConditionFalse
				}
				objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}})
			}
			if scenario == "pending peer" || scenario == "reconciled pending peer" {
				labels := map[string]string{"app.kubernetes.io/managed-by": "nut-operator", "power.zalud.io/nodepoweragent": "agent"}
				data := map[string][]byte{"b.json": []byte(`{"nodeName":"b"}`)}
				if scenario == "reconciled pending peer" {
					delete(labels, "power.zalud.io/nodepoweragent")
					data[nodeagent.DeliveryChannelMarker] = []byte("channel")
				}
				objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "agent-node-signals", Namespace: "system", Labels: labels}, Data: data})
			}
			live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if scenario == "failed read" {
					return errors.New("unavailable")
				}
				return c.List(ctx, list, opts...)
			}}).Build()
			r := &ShutdownFlowReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), APIReader: live}
			release := executor.NodeRelease{NodeName: "a", ControlPlaneNodes: []string{"a", "b", "c"}, QuorumMembers: []string{"a", "b", "c"}, TerminalHandoff: scenario == "terminal"}
			if scenario == "worker" {
				release.NodeName = "worker"
			}
			err := r.validateControlPlaneRelease(context.Background(), release)
			want := scenario == "healthy" || scenario == "terminal" || scenario == "worker"
			if (err == nil) != want {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestQuorumRecheckedBetweenSignalWrites(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	var objects []client.Object
	for _, name := range []string{"a", "b", "c"} {
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
	}
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &ShutdownFlowReconciler{Client: live, APIReader: live}
	runner := kubeactions.Runner{Client: live, ValidateNodeRelease: r.validateControlPlaneRelease}
	var releases []executor.NodeRelease
	for _, name := range []string{"a", "b"} {
		releases = append(releases, executor.NodeRelease{NodeName: name, NodePowerAgent: "agent", ControlPlaneNodes: []string{"a", "b", "c"}, QuorumMembers: []string{"a", "b", "c"}, SignalSecretNamespace: "system", SignalSecretName: "agent-node-signals", SignalSecretKey: name + ".json"})
	}
	outcome, err := runner.RunAction(context.Background(), executor.Action{ExecutionID: "run", ShutdownFlow: "flow", PlanConfigHash: "hash", Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: releases}})
	if err == nil || len(outcome.SignalResults) != 2 || !outcome.SignalResults[0].Published || outcome.SignalResults[1].Published {
		t.Fatalf("outcome=%+v error=%v", outcome, err)
	}
	var secret corev1.Secret
	if err := live.Get(context.Background(), client.ObjectKey{Namespace: "system", Name: "agent-node-signals"}, &secret); err != nil {
		t.Fatal(err)
	}
	if len(secret.Data) != 1 || len(secret.Data["a.json"]) == 0 {
		t.Fatalf("unexpected signal keys: %v", len(secret.Data))
	}
}

func TestConcurrentAgentsCannotSpendTheSameQuorumMargin(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	members := []string{"a", "b", "c", "d", "e"}
	var objects []client.Object
	for _, name := range members {
		ready := corev1.ConditionTrue
		if name == "c" {
			ready = corev1.ConditionFalse
		}
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}}}})
	}
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	r := &ShutdownFlowReconciler{Client: live, APIReader: live}
	results := make(chan error, 2)
	for _, name := range []string{"a", "b"} {
		go func() {
			runner := kubeactions.Runner{Client: live, ValidateNodeRelease: r.validateControlPlaneRelease}
			_, err := runner.RunAction(context.Background(), executor.Action{ExecutionID: "run", ShutdownFlow: "flow", PlanConfigHash: "hash", Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: []executor.NodeRelease{{NodeName: name, NodePowerAgent: name, ControlPlaneNodes: members, QuorumMembers: members, SignalSecretNamespace: "system", SignalSecretName: name + "-node-signals", SignalSecretKey: name + ".json"}}}})
			results <- err
		}()
	}
	succeeded := 0
	for range 2 {
		if <-results == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful concurrent releases=%d, want 1", succeeded)
	}
}
