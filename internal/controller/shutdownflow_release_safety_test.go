package controller

import (
	"context"
	"errors"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestLiveReleaseSafetyBeforePublication(t *testing.T) {
	for _, scenario := range []string{"ready", "fresh telemetry", "new workload", "unready pod", "missing pod", "wrong node", "old actuator policy", "ambiguous actuator policy", "unobserved generation", "stale telemetry", "list failure", "canceled", "changed destination"} {
		t.Run(scenario, func(t *testing.T) {
			scheme, agent, release := authorizedReleaseFixture(t)
			agent.Status.ObservedGeneration = agent.Generation
			agent.Status.NodeStatuses = []power.NodePowerAgentNodeStatus{{NodeName: "node-a", PodName: "agent-pod", Ready: true}}
			requireFresh := false
			agent.Spec.Shutdown.RequireFreshTelemetry = &requireFresh
			release.SignalSecretName = nodePowerAgentSignalSecretName(agent)
			release.AgentReady, release.TelemetryFresh, release.Cleared = true, true, true
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-pod", Namespace: "power-system"},
				Spec:   corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Name: "actuator", Env: []corev1.EnvVar{{Name: "POWER_AGENT_MODE", Value: "Actuate"}, {Name: "POWER_ACTUATOR_POLICY", Value: "PowerOff"}}}}},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}},
			}
			cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent.DeepCopy(), pod.DeepCopy()).Build()
			objects := []client.Object{agent, pod}
			switch scenario {
			case "new workload":
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "arrived-after-planning", Namespace: "apps"}, Spec: corev1.PodSpec{NodeName: "node-a"}})
			case "unready pod":
				pod.Status.Conditions[0].Status = corev1.ConditionFalse
			case "wrong node":
				pod.Spec.NodeName = "node-b"
			case "missing pod":
				pod.Name = "other-pod"
			case "ambiguous actuator policy":
				pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: "POWER_ACTUATOR_POLICY", Value: "PowerOff"})
			case "old actuator policy":
				pod.Spec.Containers[0].Env[1].Value = "Simulate"
			case "unobserved generation":
				agent.Status.ObservedGeneration--
			case "stale telemetry", "fresh telemetry":
				requireFresh = true
				agent.Spec.NUTServerRefs = []power.ObjectNameReference{{Name: "server"}}
				phase := power.UPSDevicePhaseStale
				if scenario == "fresh telemetry" {
					phase = power.UPSDevicePhaseOnline
				}
				objects = append(objects, &power.NUTServer{ObjectMeta: metav1.ObjectMeta{Name: "server"}, Status: power.NUTServerStatus{SelectedDevices: []string{"ups"}}}, &power.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: "ups"}, Status: power.UPSDeviceStatus{Phase: phase}})
			case "changed destination":
				release.SignalSecretName = "old-channel"
			}
			fresh := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string { return []string{obj.(*corev1.Pod).Spec.NodeName} }).WithInterceptorFuncs(interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if scenario == "list failure" {
					return errors.New("API unavailable")
				}
				return c.List(ctx, list, opts...)
			}}).Build()
			r := &ShutdownFlowReconciler{Client: cached, APIReader: fresh}
			runner := kubeactions.Runner{Client: cached, ValidateNodeRelease: r.ValidateNodeRelease}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			_, err := runner.RunAction(ctx, executor.Action{ExecutionID: "run", ShutdownFlow: "flow", PlanConfigHash: "hash", Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: []executor.NodeRelease{release}}})
			allowed := scenario == "ready" || scenario == "fresh telemetry"
			if (err == nil) != allowed {
				t.Fatalf("scenario=%s error=%v", scenario, err)
			}
			var secrets corev1.SecretList
			if err := cached.List(context.Background(), &secrets); err != nil {
				t.Fatal(err)
			}
			want := 0
			if allowed {
				want = 1
			}
			if len(secrets.Items) != want {
				t.Fatalf("published %d Secrets, want %d", len(secrets.Items), want)
			}
		})
	}
}
