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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestLiveReleaseSafetyBeforePublication(t *testing.T) {
	for _, scenario := range []string{"ready", "fresh telemetry", "expired telemetry", "missing timestamp", "future timestamp", "new workload", "unready pod", "missing pod", "wrong node", "old actuator policy", "ambiguous actuator policy", "unobserved generation", "stale telemetry", "list failure", "canceled", "changed destination"} {
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
			case "stale telemetry", "fresh telemetry", "expired telemetry", "missing timestamp", "future timestamp":
				requireFresh = true
				agent.Spec.NUTServerRefs = []power.ObjectNameReference{{Name: "server"}}
				phase := power.UPSDevicePhaseStale
				if scenario != "stale telemetry" {
					phase = power.UPSDevicePhaseOnline
				}
				polled := metav1.NewTime(time.Now().Add(-time.Second))
				device := &power.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: "ups"}, Status: power.UPSDeviceStatus{Phase: phase, LastPollTime: &polled}}
				switch scenario {
				case "expired telemetry":
					polled = metav1.NewTime(time.Now().Add(-time.Hour))
				case "future timestamp":
					polled = metav1.NewTime(time.Now().Add(time.Hour))
				case "missing timestamp":
					device.Status.LastPollTime = nil
				}
				objects = append(objects, &power.NUTServer{ObjectMeta: metav1.ObjectMeta{Name: "server"}, Status: power.NUTServerStatus{SelectedDevices: []string{"ups"}}}, device)
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

func TestNodeReleaseTelemetryAge(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name            string
		age             time.Duration
		phase           power.UPSDevicePhase
		threshold, poll *metav1.Duration
		want            bool
	}{
		{name: "online within default", age: 90*time.Second - time.Nanosecond, phase: power.UPSDevicePhaseOnline, want: true},
		{name: "online at expiry", age: 90 * time.Second, phase: power.UPSDevicePhaseOnline},
		{name: "battery at expiry", age: 30 * time.Second, phase: power.UPSDevicePhaseOnBattery},
		{name: "low battery within default", age: 29 * time.Second, phase: power.UPSDevicePhaseLowBattery, want: true},
		{name: "custom threshold within", age: time.Minute - time.Nanosecond, threshold: &metav1.Duration{Duration: time.Minute}, want: true},
		{name: "custom threshold expiry", age: time.Minute, threshold: &metav1.Duration{Duration: time.Minute}},
		{name: "invalid threshold", threshold: &metav1.Duration{}},
		{name: "negative threshold", threshold: &metav1.Duration{Duration: -time.Second}},
		{name: "custom polling", age: 2 * time.Minute, poll: &metav1.Duration{Duration: time.Minute}, want: true},
		{name: "future", age: -time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polled := metav1.NewTime(now.Add(-tc.age))
			device := &power.UPSDevice{Spec: power.UPSDeviceSpec{Thresholds: power.UPSThresholdsSpec{StaleAfter: tc.threshold}, Telemetry: power.UPSTelemetrySpec{PollInterval: tc.poll}}, Status: power.UPSDeviceStatus{Phase: tc.phase, LastPollTime: &polled}}
			if got := nodeReleaseTelemetryRecent(device, now); got != tc.want {
				t.Fatalf("recent=%v, want %v", got, tc.want)
			}
		})
	}
}
