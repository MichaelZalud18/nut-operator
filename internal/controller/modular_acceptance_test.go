package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Real reconciliation, compilation, execution and HTTPS; fake Kubernetes and
// audit storage. No actuator runs and no external host is represented as a Node.
func TestModularMixedFlowAcceptance(t *testing.T) {
	for _, scenario := range []string{"delivered", "repeated", "failure", "timeout", "disallowed", "dry-run", "rehearsal", "unapproved", "revoked-agent"} {
		t.Run(scenario, func(t *testing.T) {
			r, _, _, _ := asyncFlowFixture(t)
			if err := fake.AddIndex(r.Client, &corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string { return []string{obj.(*corev1.Pod).Spec.NodeName} }); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			var mu sync.Mutex
			var events []string
			var receiverError string
			stopped := false
			effects := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var event struct {
					Data struct {
						DryRun     bool              `json:"dryRun"`
						Extensions map[string]string `json:"extensions"`
					} `json:"data"`
				}
				err := json.NewDecoder(req.Body).Decode(&event)
				mu.Lock()
				if err != nil || req.Header.Get("Authorization") != "Bearer fixture" || event.Data.Extensions["host"] != "external-host" {
					receiverError = "incorrect request authentication or explicit target"
				}
				if event.Data.DryRun != (scenario == "rehearsal") {
					receiverError = "incorrect rehearsal flag"
				}
				events = append(events, "hook")
				mu.Unlock()
				if scenario == "timeout" {
					<-req.Context().Done()
					return
				}
				if scenario == "revoked-agent" {
					var agent power.NodePowerAgent
					if err := r.Get(ctx, client.ObjectKey{Name: "agent"}, &agent); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					agent.Annotations["test/approval"] = "false"
					if err := r.Update(ctx, &agent); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
				}
				if scenario == "failure" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				mu.Lock()
				if !event.Data.DryRun && !stopped {
					stopped = true
					effects++
				}
				mu.Unlock()
				w.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(u.Port())
			if err != nil {
				t.Fatal(err)
			}
			var cluster power.PowerManagementCluster
			if err := r.Get(ctx, client.ObjectKey{Name: "management"}, &cluster); err != nil {
				t.Fatal(err)
			}
			cluster.Spec.Hooks.AllowedEndpoints = []power.PowerHookEndpointAllowlistEntry{{Scheme: "https", Host: u.Hostname(), Port: ptr.To(int32(port)), PathPrefix: "/hosts"}}
			if scenario == "disallowed" {
				cluster.Spec.Hooks.AllowedEndpoints[0].PathPrefix = "/elsewhere"
			}
			if err := r.Update(ctx, &cluster); err != nil {
				t.Fatal(err)
			}
			_, agent, _ := authorizedReleaseFixture(t)
			agent.Spec.Namespace = "power-system"
			agent.Spec.Shutdown.RequireFreshTelemetry = ptr.To(false)
			agent.Status.SelectedNodes = []string{"node-a"}
			agent.Status.ObservedGeneration = agent.Generation
			agent.Status.NodeStatuses = []power.NodePowerAgentNodeStatus{{NodeName: "node-a", PodName: "agent-pod", Ready: true}}
			hook := &power.ShutdownHook{ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "power-system"}, Spec: power.ShutdownHookSpec{Invocation: power.ShutdownHookInvocationSpec{
				Transport: power.ShutdownHookTransportHTTP, Timeout: &metav1.Duration{Duration: time.Second}, HTTP: &power.ShutdownHookHTTPSpec{URL: server.URL + "/hosts/stop", Data: map[string]string{"host": "external-host"}, SecretHeaders: []power.ShutdownHookHTTPSecretHeader{{Name: "Authorization", ValueFrom: power.SecretKeyReference{Namespace: "power-system", Name: "hook-auth", Key: "token"}}}},
			}}}
			if scenario == "timeout" {
				hook.Spec.Invocation.Timeout.Duration = 100 * time.Millisecond
			}
			if scenario == "rehearsal" {
				hook.Spec.DryRun = hook.Spec.Invocation.DeepCopy()
			}
			for _, obj := range []client.Object{
				agent, hook,
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hook-auth", Namespace: "power-system"}, Data: map[string][]byte{"token": []byte("Bearer fixture")}},
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}},
				&power.PowerInventoryNode{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Spec: power.PowerInventoryNodeSpec{NodeName: "node-a", CommunicationPathExempt: ptr.To(true), Roles: power.PowerInventoryNodeRoles{ShutdownTier: ptr.To(int32(2))}}},
				&power.PowerInventoryEdge{ObjectMeta: metav1.ObjectMeta{Name: "feed"}, Spec: power.PowerInventoryEdgeSpec{From: power.PowerInventoryEntityReference{Kind: "UPSDevice", Name: "ups"}, To: power.PowerInventoryEntityReference{Kind: "Node", Name: "node-a"}, Relation: "Feeds", Input: "psu"}},
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent-pod", Namespace: "power-system"}, Spec: corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{Name: "actuator", Env: []corev1.EnvVar{{Name: "POWER_AGENT_MODE", Value: "Actuate"}, {Name: "POWER_ACTUATOR_POLICY", Value: "PowerOff"}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}},
			} {
				if err := r.Create(ctx, obj); err != nil {
					t.Fatal(err)
				}
			}
			r.ExecutorRunner = kubeactions.Runner{Client: r.Client, HTTPClient: server.Client(), ValidateNodeRelease: r.ValidateNodeRelease, SignalWritten: func(_, _, _ string, _ time.Time) { mu.Lock(); defer mu.Unlock(); events = append(events, "signal") }}
			flow := createAsyncFlow(t, r, "mixed")
			flow.Spec.Groups = []power.ShutdownGroup{
				{Name: "external", Action: power.ShutdownStepRunHook, HookRef: &power.NamespacedNameReference{Namespace: hook.Namespace, Name: hook.Name}, Before: []string{"worker"}, ShutdownTier: ptr.To(int32(3))},
				{Name: "worker", Action: power.ShutdownStepAgentShutdown, Target: power.ShutdownStepTarget{AgentRefs: []power.ObjectNameReference{{Name: agent.Name}}}, ShutdownTier: ptr.To(int32(2))},
			}
			if scenario == "dry-run" || scenario == "rehearsal" {
				flow.Spec.Mode = power.ShutdownFlowModeDryRun
			}
			if scenario == "unapproved" {
				flow.Annotations["test/approval"] = "false"
			}
			if err := r.Update(ctx, flow); err != nil {
				t.Fatal(err)
			}
			reconcileAsync(t, r, flow)
			awaitRunDone(t, r, flow.Name)
			current := reconcileAsync(t, r, flow)
			if scenario == "repeated" {
				second := flow.DeepCopy()
				second.ObjectMeta = metav1.ObjectMeta{Name: "mixed-repeat", UID: "mixed-repeat", Generation: 1, Annotations: flow.Annotations}
				second.Status = power.ShutdownFlowStatus{}
				if err := r.Create(ctx, second); err != nil {
					t.Fatal(err)
				}
				reconcileAsync(t, r, second)
				awaitRunDone(t, r, second.Name)
				current = reconcileAsync(t, r, second)
			}
			mu.Lock()
			defer mu.Unlock()
			assertModularMixedFlow(t, r, agent, current, scenario, receiverError, events, effects)
		})
	}
}

func assertModularMixedFlow(t *testing.T, r *ShutdownFlowReconciler, agent *power.NodePowerAgent, current *power.ShutdownFlow, scenario, receiverError string, events []string, effects int) {
	t.Helper()
	if receiverError != "" {
		t.Fatal(receiverError)
	}
	want := "hook signal"
	switch scenario {
	case "dry-run", "unapproved":
		want = ""
	case "rehearsal", "revoked-agent":
		want = "hook"
	case "disallowed":
		want = "signal"
	case "repeated":
		want = "hook signal hook signal"
	}
	got := strings.Join(events, " ")
	if got != want {
		t.Fatalf("events=%q want=%q; status=%+v", got, want, current.Status)
	}
	if scenario != "unapproved" && current.Status.LastExecution == nil {
		t.Fatalf("no execution evidence: %+v", current.Status)
	}
	if scenario == "repeated" && effects != 1 {
		t.Fatalf("repeated operation had %d effects", effects)
	}
	if scenario == "failure" || scenario == "timeout" || scenario == "disallowed" {
		condition := meta.FindStatusCondition(current.Status.Conditions, "Degraded")
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "ShutdownHookFailed" {
			t.Fatalf("missing hook failure evidence: %+v", condition)
		}
	}
	if strings.Contains(want, "signal") && current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted {
		t.Fatalf("flow did not complete: %+v", current.Status.LastExecution)
	}
	var signals corev1.Secret
	err := r.Get(context.Background(), client.ObjectKey{Namespace: "power-system", Name: nodePowerAgentSignalSecretName(agent)}, &signals)
	if strings.Contains(want, "signal") && (err != nil || len(signals.Data["node-a.json"]) == 0 || len(signals.Data["external-host.json"]) != 0) {
		t.Fatalf("wrong signal destination: %+v %v", signals.Data, err)
	}
	if !strings.Contains(want, "signal") && !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected signal Secret: %+v %v", signals.Data, err)
	}
}
