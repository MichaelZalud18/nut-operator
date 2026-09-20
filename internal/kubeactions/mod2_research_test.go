package kubeactions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The fake receiver has no host actuator. Its durable-state analogue makes a
// repeated "ensure stopped" operation harmless even when event IDs differ.
func TestMOD2ExternalHostHookBoundary(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	hook := &power.ShutdownHook{ObjectMeta: metav1.ObjectMeta{Name: "external-hosts", Namespace: "research"}, Spec: power.ShutdownHookSpec{
		Invocation: power.ShutdownHookInvocationSpec{Transport: power.ShutdownHookTransportHTTP, HTTP: &power.ShutdownHookHTTPSpec{
			URL: "https://hooks.example.test/hosts/stop", Data: map[string]string{"host": "external-a", "operation": "ensure-stopped"},
			SecretHeaders: []power.ShutdownHookHTTPSecretHeader{{Name: "Authorization", ValueFrom: power.SecretKeyReference{Namespace: "research", Name: "auth", Key: "token"}}},
		}},
	}}
	cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "research"}, Spec: power.PowerManagementClusterSpec{Hooks: power.PowerHookPolicySpec{AllowedEndpoints: []power.PowerHookEndpointAllowlistEntry{{Scheme: "https", Host: "hooks.example.test", PathPrefix: "/hosts"}}}}}
	flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "research"}, Spec: power.ShutdownFlowSpec{ManagementClusterRef: &power.ObjectNameReference{Name: cluster.Name}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hook, cluster, flow, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "research"}, Data: map[string][]byte{"token": []byte("Bearer synthetic")}}).Build()
	requests, effects := 0, 0
	stopped := false
	status := http.StatusAccepted
	expire := false
	runner := Runner{Client: c, HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Header.Get("Authorization") != "Bearer synthetic" {
			t.Fatal("missing receiver auth")
		}
		var event struct {
			Data struct {
				DryRun     bool              `json:"dryRun"`
				Extensions map[string]string `json:"extensions"`
			} `json:"data"`
		}
		if err := json.NewDecoder(req.Body).Decode(&event); err != nil {
			t.Fatal(err)
		}
		if event.Data.Extensions["host"] != "external-a" {
			t.Fatal("lost explicit host target")
		}
		if expire {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		if status == http.StatusAccepted && !event.Data.DryRun && !stopped {
			stopped = true
			effects++
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})}}
	action := executor.Action{ShutdownFlow: flow.Name, ExecutionID: "research", PlanConfigHash: "research", Group: executor.Group{Name: "external", Action: ActionRunHook, HookRef: &executor.HookReference{Namespace: hook.Namespace, Name: hook.Name}}}
	action.DryRun = true
	if out, err := runner.RunAction(context.Background(), action); err != nil || out.Outcome != executor.OutcomeSimulated || requests != 0 {
		t.Fatalf("default dry-run: %+v %v requests=%d", out, err, requests)
	}
	hook.Spec.DryRun = hook.Spec.Invocation.DeepCopy()
	if err := c.Update(context.Background(), hook); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunAction(context.Background(), action); err != nil || requests != 1 || effects != 0 {
		t.Fatalf("rehearsal: %v requests=%d effects=%d", err, requests, effects)
	}
	action.DryRun = false
	for i := 0; i < 2; i++ {
		if _, err := runner.RunAction(context.Background(), action); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 3 || effects != 1 {
		t.Fatalf("repeat safety: requests=%d effects=%d", requests, effects)
	}
	cluster.Spec.Hooks.AllowedEndpoints = nil
	if err := c.Update(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunAction(context.Background(), action); err == nil || requests != 3 {
		t.Fatal("disallowed endpoint contacted")
	}
	cluster.Spec.Hooks.AllowedEndpoints = []power.PowerHookEndpointAllowlistEntry{{Scheme: "https", Host: "hooks.example.test", PathPrefix: "/hosts"}}
	if err := c.Update(context.Background(), cluster); err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []bool{false, true} {
		status, expire = http.StatusServiceUnavailable, timeout
		action.Group.Timeout = 10 * time.Millisecond
		input := executor.Input{ExecutionID: "research", ShutdownFlow: flow.Name, Mode: executor.ModeEnforce, Approved: true, PlanConfigHash: "research", InputHash: "research",
			Waves:  []executor.Wave{{Index: 0, Groups: []string{"external"}}, {Index: 1, Groups: []string{"after-hook"}}},
			Groups: []executor.Group{action.Group, {Name: "after-hook", Action: executor.ActionWait}},
		}
		result, err := (executor.Executor{Runner: runner}).Execute(context.Background(), input)
		if err != nil || !result.Degraded || result.Phase != executor.PhaseCompleted || result.Groups != 2 {
			t.Fatalf("advisory continuation timeout=%v: %+v %v", timeout, result, err)
		}
	}
}
