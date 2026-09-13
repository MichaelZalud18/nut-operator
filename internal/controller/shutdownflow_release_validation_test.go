package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func authorizedReleaseFixture(t *testing.T) (*runtime.Scheme, *power.NodePowerAgent, executor.NodeRelease) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &power.NodePowerAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", UID: "agent-uid", Generation: 3, Annotations: map[string]string{"test/approval": "true"}},
		Spec:       power.NodePowerAgentSpec{Mode: power.NodePowerAgentModeActuate, Shutdown: power.AgentShutdownSpec{ActuatorPolicy: power.ActuatorPolicyPowerOff, ApprovalAnnotation: "test/approval"}},
		Status:     power.NodePowerAgentStatus{SelectedNodes: []string{"node-a", "node-b"}},
	}
	release := executor.NodeRelease{NodeName: "node-a", NodePowerAgent: agent.Name, AgentUID: string(agent.UID), AgentGeneration: agent.Generation, ActuatorPolicy: string(agent.Spec.Shutdown.ActuatorPolicy), SignalSecretNamespace: "power-system", SignalSecretName: "signals", SignalSecretKey: "node-a.json"}
	return scheme, agent, release
}

func TestReleaseAuthorizationUsesFreshAgentState(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		mutate                              func(*power.NodePowerAgent)
		missing, readFailure, cancel, allow bool
	}{
		{name: "approved", allow: true},
		{name: "revoked annotation", mutate: func(a *power.NodePowerAgent) { a.Annotations["test/approval"] = "false" }},
		{name: "missing annotation", mutate: func(a *power.NodePowerAgent) { a.Annotations = nil }},
		{name: "changed approval key", mutate: func(a *power.NodePowerAgent) { a.Spec.Shutdown.ApprovalAnnotation = "test/new" }},
		{name: "mode revoked", mutate: func(a *power.NodePowerAgent) { a.Spec.Mode = power.NodePowerAgentModeDryRun }},
		{name: "policy changed", mutate: func(a *power.NodePowerAgent) { a.Spec.Shutdown.ActuatorPolicy = power.ActuatorPolicySimulate }},
		{name: "recreated agent", mutate: func(a *power.NodePowerAgent) { a.UID = "replacement" }},
		{name: "new specification", mutate: func(a *power.NodePowerAgent) { a.Generation++ }},
		{name: "node removed", mutate: func(a *power.NodePowerAgent) { a.Status.SelectedNodes = nil }},
		{name: "agent deleted", missing: true},
		{name: "API unavailable", readFailure: true},
		{name: "canceled", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme, agent, release := authorizedReleaseFixture(t)
			cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent.DeepCopy()).Build()
			if tc.mutate != nil {
				tc.mutate(agent)
			}
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if !tc.missing {
				builder = builder.WithObjects(agent)
			}
			fresh := builder.WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if tc.readFailure {
					return errors.New("API unavailable")
				}
				return c.Get(ctx, key, obj, opts...)
			}}).Build()
			r := &ShutdownFlowReconciler{Client: cached, APIReader: fresh}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := r.ValidateNodeReleaseAuthorization(ctx, release)
			if (err == nil) != tc.allow {
				t.Fatalf("authorization error=%v, allow=%v", err, tc.allow)
			}
		})
	}
}

func TestReleaseAuthorizationRevokedBetweenSecretWrites(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "update"}[preexisting], func(t *testing.T) {
			scheme, agent, release := authorizedReleaseFixture(t)
			builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent)
			if preexisting {
				builder = builder.WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "signals", Namespace: "power-system"}})
			}
			kube := builder.Build()
			r := &ShutdownFlowReconciler{Client: kube, APIReader: kube}
			runner := kubeactions.Runner{Client: kube, ValidateNodeRelease: r.ValidateNodeReleaseAuthorization,
				SignalWritten: func(string, string, string, time.Time) {
					agent.Annotations["test/approval"] = "false"
					if err := kube.Update(context.Background(), agent); err != nil {
						t.Fatal(err)
					}
				},
			}
			second := release
			second.NodeName, second.SignalSecretKey = "node-b", "node-b.json"
			outcome, err := runner.RunAction(context.Background(), executor.Action{ExecutionID: "run", ShutdownFlow: "flow", PlanConfigHash: "hash", Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: []executor.NodeRelease{release, second}}})
			if err == nil || len(outcome.SignalResults) != 2 || !outcome.SignalResults[0].Published || outcome.SignalResults[1].Published {
				t.Fatalf("revocation not reflected in receipts: %+v, %v", outcome, err)
			}
			var secret corev1.Secret
			if err := kube.Get(context.Background(), client.ObjectKey{Name: "signals", Namespace: "power-system"}, &secret); err != nil {
				t.Fatal(err)
			}
			if len(secret.Data) != 1 || len(secret.Data[release.SignalSecretKey]) == 0 {
				t.Fatalf("unexpected signal writes: %v", secret.Data)
			}
		})
	}
}

func TestReleasePublicationRequiresValidator(t *testing.T) {
	scheme, _, release := authorizedReleaseFixture(t)
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	runner := kubeactions.Runner{Client: kube}
	_, err := runner.RunAction(context.Background(), executor.Action{ExecutionID: "run", ShutdownFlow: "flow", PlanConfigHash: "hash", Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: []executor.NodeRelease{release}}})
	if err == nil {
		t.Fatal("signal publication accepted a missing validator")
	}
	var secrets corev1.SecretList
	if err := kube.List(context.Background(), &secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets.Items) != 0 {
		t.Fatal("signal written without validation")
	}
}

func TestReleaseAuthorizationTalosApproval(t *testing.T) {
	scheme, agent, release := authorizedReleaseFixture(t)
	agent.Spec.Shutdown.ActuatorPolicy = power.ActuatorPolicyTalosShutdown
	release.ActuatorPolicy = string(power.ActuatorPolicyTalosShutdown)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	r := &ShutdownFlowReconciler{Client: kube, APIReader: kube}
	if err := r.ValidateNodeReleaseAuthorization(context.Background(), release); err != nil {
		t.Fatal(err)
	}
	agent.Annotations["test/approval"] = "false"
	if err := kube.Update(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateNodeReleaseAuthorization(context.Background(), release); err == nil {
		t.Fatal("Talos release ignored revoked approval")
	}
}
