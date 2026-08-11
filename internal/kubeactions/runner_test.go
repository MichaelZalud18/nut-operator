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

package kubeactions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
)

func TestRunnerScalesWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	replicas := int32(3)
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&appsv1.Deployment{ObjectMeta: objectMeta("apps", "web"), Spec: appsv1.DeploymentSpec{Replicas: &replicas}},
		&appsv1.StatefulSet{ObjectMeta: objectMeta("data", "postgres"), Spec: appsv1.StatefulSetSpec{Replicas: &replicas}},
		&appsv1.Deployment{ObjectMeta: objectMeta("power-agents", "sidecar"), Spec: appsv1.DeploymentSpec{Replicas: &replicas}},
		&powerv1alpha1.NodePowerAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "fleet-a"},
			Spec:       powerv1alpha1.NodePowerAgentSpec{Namespace: "power-agents"},
		},
	).Build()}

	attemptsBefore := testutil.ToFloat64(metrics.ActuatorActionAttemptsTotal.WithLabelValues(ActionScale, "Enforce", executor.OutcomeSucceeded))
	durationSamplesBefore := histogramSampleCount(t, metrics.ActuatorActionDurationSeconds.WithLabelValues(ActionScale))

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		ShutdownFlow:   "test-flow",
		ExecutionID:    "execution-a",
		PlanConfigHash: "hash-a",
		Group: executor.Group{
			Name:   "applications",
			Action: ActionScale,
			Params: map[string]string{paramReplicas: "1"},
			SelectedTargets: []executor.Target{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
				{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: "data", Name: "postgres"},
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "power-agents", Name: "sidecar"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
	}

	// F-3: RunAction is the single choke point every executor action passes through, real or dry-run --
	// confirm it actually records the actuator metrics rather than just compiling against them.
	if got := testutil.ToFloat64(metrics.ActuatorActionAttemptsTotal.WithLabelValues(ActionScale, "Enforce", executor.OutcomeSucceeded)); got != attemptsBefore+1 {
		t.Fatalf("ActuatorActionAttemptsTotal did not increment for ScaleWorkload/Enforce/Succeeded: before=%v after=%v", attemptsBefore, got)
	}
	if got := histogramSampleCount(t, metrics.ActuatorActionDurationSeconds.WithLabelValues(ActionScale)); got != durationSamplesBefore+1 {
		t.Fatalf("ActuatorActionDurationSeconds did not record a new observation for ScaleWorkload: before=%d after=%d", durationSamplesBefore, got)
	}

	var deployment appsv1.Deployment
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "apps", Name: "web"}, &deployment); err != nil {
		t.Fatalf("get Deployment returned error: %v", err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("expected Deployment replicas to be 1, got %#v", deployment.Spec.Replicas)
	}
	var statefulSet appsv1.StatefulSet
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "data", Name: "postgres"}, &statefulSet); err != nil {
		t.Fatalf("get StatefulSet returned error: %v", err)
	}
	if statefulSet.Spec.Replicas == nil || *statefulSet.Spec.Replicas != 1 {
		t.Fatalf("expected StatefulSet replicas to be 1, got %#v", statefulSet.Spec.Replicas)
	}

	// F-14: the node-agent's own operand namespace must never be scaled by a flow.
	var sidecar appsv1.Deployment
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "power-agents", Name: "sidecar"}, &sidecar); err != nil {
		t.Fatalf("get Deployment returned error: %v", err)
	}
	if sidecar.Spec.Replicas == nil || *sidecar.Spec.Replicas != 3 {
		t.Fatalf("expected node-agent namespace Deployment to be excluded from scaling, got replicas %#v", sidecar.Spec.Replicas)
	}
	excluded, ok := outcome.Details["selfExcluded"].([]string)
	if !ok || len(excluded) != 1 || excluded[0] != "power-agents/sidecar" {
		t.Fatalf("expected selfExcluded to report power-agents/sidecar, got %#v", outcome.Details["selfExcluded"])
	}
}

func TestRunnerDrainNodesExcludesNodePowerAgentNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Node{ObjectMeta: objectMeta("", "node-a")},
		&corev1.Pod{
			ObjectMeta: objectMeta("apps", "web-1"),
			Spec:       corev1.PodSpec{NodeName: "node-a"},
		},
		&corev1.Pod{
			ObjectMeta: objectMeta("power-agents", "upsmon-node-a"),
			Spec:       corev1.PodSpec{NodeName: "node-a"},
		},
		&powerv1alpha1.NodePowerAgent{
			ObjectMeta: metav1.ObjectMeta{Name: "fleet-a"},
			Spec:       powerv1alpha1.NodePowerAgentSpec{Namespace: "power-agents"},
		},
	).Build()}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		Group: executor.Group{
			Name:            "nodes",
			Action:          ActionDrainNodes,
			SelectedTargets: []executor.Target{{APIVersion: "v1", Kind: "Node", Name: "node-a"}},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
	}

	evictedCount, ok := outcome.Details["evictedPods"].(int)
	if !ok || evictedCount != 1 {
		t.Fatalf("expected exactly 1 evicted pod (the non-protected one), got %#v", outcome.Details["evictedPods"])
	}

	// F-14: the pod in the node-agent's own operand namespace must survive the drain.
	var agentPod corev1.Pod
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "power-agents", Name: "upsmon-node-a"}, &agentPod); err != nil {
		t.Fatalf("expected node-agent pod to survive drain, get returned error: %v", err)
	}
	excluded, ok := outcome.Details["selfExcludedNamespaces"].([]string)
	if !ok || len(excluded) != 1 || excluded[0] != "power-agents" {
		t.Fatalf("expected selfExcludedNamespaces to report power-agents, got %#v", outcome.Details["selfExcludedNamespaces"])
	}
}

func TestRunnerScaleWorkloadsExcludesManagerNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	replicas := int32(3)
	runner := Runner{
		ManagerNamespace: "nut-operator-system",
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			&appsv1.Deployment{ObjectMeta: objectMeta("nut-operator-system", "controller-manager"), Spec: appsv1.DeploymentSpec{Replicas: &replicas}},
		).Build(),
	}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		Group: executor.Group{
			Name:   "applications",
			Action: ActionScale,
			Params: map[string]string{paramReplicas: "0"},
			SelectedTargets: []executor.Target{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "nut-operator-system", Name: "controller-manager"},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}

	// F-30: a ShutdownFlow must never scale down the controller-manager that is executing it.
	var manager appsv1.Deployment
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "nut-operator-system", Name: "controller-manager"}, &manager); err != nil {
		t.Fatalf("get Deployment returned error: %v", err)
	}
	if manager.Spec.Replicas == nil || *manager.Spec.Replicas != 3 {
		t.Fatalf("expected controller-manager Deployment to be excluded from scaling, got replicas %#v", manager.Spec.Replicas)
	}
	excluded, ok := outcome.Details["selfExcluded"].([]string)
	if !ok || len(excluded) != 1 || excluded[0] != "nut-operator-system/controller-manager" {
		t.Fatalf("expected selfExcluded to report nut-operator-system/controller-manager, got %#v", outcome.Details["selfExcluded"])
	}
}

func TestRunnerDrainNodesExcludesManagerNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	runner := Runner{
		ManagerNamespace: "nut-operator-system",
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			&corev1.Node{ObjectMeta: objectMeta("", "node-a")},
			&corev1.Pod{
				ObjectMeta: objectMeta("apps", "web-1"),
				Spec:       corev1.PodSpec{NodeName: "node-a"},
			},
			&corev1.Pod{
				ObjectMeta: objectMeta("nut-operator-system", "controller-manager-abc"),
				Spec:       corev1.PodSpec{NodeName: "node-a"},
			},
		).Build(),
	}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		Group: executor.Group{
			Name:            "nodes",
			Action:          ActionDrainNodes,
			SelectedTargets: []executor.Target{{APIVersion: "v1", Kind: "Node", Name: "node-a"}},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}

	evictedCount, ok := outcome.Details["evictedPods"].(int)
	if !ok || evictedCount != 1 {
		t.Fatalf("expected exactly 1 evicted pod (the non-protected one), got %#v", outcome.Details["evictedPods"])
	}

	// F-30: the manager's own pod must survive a drain of the node it happens to be scheduled on.
	var managerPod corev1.Pod
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "nut-operator-system", Name: "controller-manager-abc"}, &managerPod); err != nil {
		t.Fatalf("expected controller-manager pod to survive drain, get returned error: %v", err)
	}
	excluded, ok := outcome.Details["selfExcludedNamespaces"].([]string)
	if !ok || len(excluded) != 1 || excluded[0] != "nut-operator-system" {
		t.Fatalf("expected selfExcludedNamespaces to report nut-operator-system, got %#v", outcome.Details["selfExcludedNamespaces"])
	}
}

func TestRunnerCordonsNodes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Node{ObjectMeta: objectMeta("", "node-a")},
	).Build()}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		Group: executor.Group{
			Name:            "nodes",
			Action:          ActionCordonNodes,
			SelectedTargets: []executor.Target{{APIVersion: "v1", Kind: "Node", Name: "node-a"}},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
	}

	var node corev1.Node
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Name: "node-a"}, &node); err != nil {
		t.Fatalf("get Node returned error: %v", err)
	}
	if !node.Spec.Unschedulable {
		t.Fatal("expected node to be cordoned")
	}
}

func TestRunnerCreatesWorkflowHook(t *testing.T) {
	scheme := runtime.NewScheme()
	fixed := time.Date(2026, 8, 2, 21, 0, 0, 0, time.UTC)
	runner := Runner{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Clock:  func() time.Time { return fixed },
	}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		ShutdownFlow:   "test-flow",
		ExecutionID:    "execution-a",
		PlanConfigHash: "hash-a",
		Group: executor.Group{
			Name:   "storage",
			Action: ActionWorkflow,
			Params: map[string]string{
				paramWorkflowTemplateRef:                "flush-storage",
				paramWorkflowParameterPrefix + "reason": "power-event",
			},
			SelectedTargets: []executor.Target{{APIVersion: "v1", Kind: "Namespace", Name: "storage"}},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
	}

	var workflows unstructured.UnstructuredList
	workflows.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "WorkflowList"})
	if err := runner.Client.List(context.Background(), &workflows, client.InNamespace("storage")); err != nil {
		t.Fatalf("list Workflows returned error: %v", err)
	}
	if len(workflows.Items) != 1 {
		t.Fatalf("expected one workflow, got %d", len(workflows.Items))
	}
	spec, ok := workflows.Items[0].Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("expected workflow spec map, got %#v", workflows.Items[0].Object["spec"])
	}
	templateRef := spec["workflowTemplateRef"].(map[string]any)
	if templateRef["name"] != "flush-storage" {
		t.Fatalf("unexpected workflow template ref: %#v", templateRef)
	}
	if workflows.Items[0].GetLabels()[labelShutdownFlowExecution] != "execution-a" {
		t.Fatalf("workflow labels missing execution ID: %#v", workflows.Items[0].GetLabels())
	}
}

func TestRunnerRequiresAgentShutdownReleases(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	outcome, err := runner.RunAction(context.Background(), executor.Action{
		Group: executor.Group{Name: "node-a", Action: executor.ActionAgentShutdown},
	})
	if err == nil {
		t.Fatal("expected missing releases to block AgentShutdown")
	}
	if outcome.Outcome != executor.OutcomeBlocked {
		t.Fatalf("expected blocked outcome, got %#v", outcome)
	}

	outcome, err = runner.RunAction(context.Background(), executor.Action{
		ExecutionID:    "execution-a",
		ShutdownFlow:   "flow-a",
		PlanConfigHash: "hash-a",
		Group: executor.Group{
			Name:   "node-a",
			Action: executor.ActionAgentShutdown,
			NodeReleases: []executor.NodeRelease{{
				NodeName:              "node-a",
				NodePowerAgent:        "agent-a",
				SignalSecretNamespace: "power-system",
				SignalSecretName:      "agent-a-node-signals",
				SignalSecretKey:       "node-a.json",
			}},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
	}
}

func TestRunnerWritesAgentShutdownSignalSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	fixed := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	runner := Runner{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		Clock:  func() time.Time { return fixed },
	}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		ExecutionID:        "execution-a",
		ShutdownFlow:       "flow-a",
		PlanConfigHash:     "hash-a",
		SelectedUPSDevices: []string{"ups-a"},
		Group: executor.Group{
			Name:   "node-a",
			Action: executor.ActionAgentShutdown,
			NodeReleases: []executor.NodeRelease{{
				NodeName:              "node-a",
				NodePowerAgent:        "agent-a",
				SignalSecretNamespace: "power-system",
				SignalSecretName:      "agent-a-node-signals",
				SignalSecretKey:       "node-a.json",
			}},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
	}

	var secret corev1.Secret
	if err := runner.Client.Get(context.Background(), client.ObjectKey{Namespace: "power-system", Name: "agent-a-node-signals"}, &secret); err != nil {
		t.Fatalf("get signal Secret returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(secret.Data["node-a.json"], &payload); err != nil {
		t.Fatalf("unmarshal signal payload: %v", err)
	}
	if payload["executionID"] != "execution-a" || payload["nodeName"] != "node-a" || payload["dryRun"] != false {
		t.Fatalf("unexpected signal payload: %#v", payload)
	}
	if payload["timestamp"] != fixed.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected timestamp: %#v", payload["timestamp"])
	}
}

func TestLabelValueIsKubernetesSafe(t *testing.T) {
	value := labelValue("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/weird")
	if len(value) > 63 {
		t.Fatalf("label value length = %d, want <= 63", len(value))
	}
	if strings.Contains(value, "/") {
		t.Fatalf("label value was not sanitized: %q", value)
	}
	if value == "" {
		t.Fatal("label value must not be empty")
	}
}

func objectMeta(namespace, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namespace, Name: name}
}

// histogramSampleCount reads the current observation count directly off a histogram's Write() output,
// rather than via testutil.CollectAndCount (which counts distinct label-combination series, not
// observations, and so can't tell two Observe() calls on the same series apart from one). Other tests
// in this file reuse the same action-type labels, so an order-independent, observation-level delta
// check is what actually proves a new Observe() happened.
func histogramSampleCount(t *testing.T, observer prometheus.Observer) uint64 {
	t.Helper()
	histogram, ok := observer.(prometheus.Histogram)
	if !ok {
		t.Fatalf("observer %T does not implement prometheus.Histogram", observer)
	}
	var metric dto.Metric
	if err := histogram.Write(&metric); err != nil {
		t.Fatalf("write histogram metric: %v", err)
	}
	return metric.GetHistogram().GetSampleCount()
}

// Notify emits a Kubernetes Event against the ShutdownFlow. Events are the transport because they
// need no configuration: a Notify backed by an unconfigured webhook is inert, and an inert
// notification during an outage is worse than none -- the flow author believes they will be told.
func TestRunnerNotifyEmitsAnEventOnTheFlow(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	recorder := events.NewFakeRecorder(10)
	runner := Runner{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			&powerv1alpha1.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "conserve-power"}},
		).Build(),
		Recorder: recorder,
	}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		ExecutionID:  "execution-a",
		ShutdownFlow: "conserve-power",
		Group: executor.Group{
			Name:   "announce",
			Action: ActionNotify,
			Params: map[string]string{"message": "shutting down the cluster", "reason": "OutageBegan"},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("outcome = %#v, want Succeeded", outcome)
	}
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, "OutageBegan") || !strings.Contains(event, "shutting down the cluster") {
			t.Fatalf("event = %q, want the declared reason and message", event)
		}
	default:
		t.Fatal("expected an event to be recorded")
	}
}

// A Notify with no recorder must report Blocked rather than succeed. Silently succeeding would tell
// the audit trail a notification was delivered when nothing was sent.
func TestRunnerNotifyWithoutARecorderReportsBlocked(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		ShutdownFlow: "conserve-power",
		Group:        executor.Group{Name: "announce", Action: ActionNotify},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeBlocked {
		t.Fatalf("outcome = %#v, want Blocked", outcome)
	}
}

// Gate was removed from the action enum: nothing defined who opens it, and a shutdown that waits on
// a human is a shutdown that does not finish. It must be rejected, not silently accepted.
func TestRunnerRejectsTheRemovedGateAction(t *testing.T) {
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(runtime.NewScheme()).Build()}

	outcome, err := runner.RunAction(context.Background(), executor.Action{
		ShutdownFlow: "conserve-power",
		Group:        executor.Group{Name: "hold", Action: "Gate"},
	})
	if err == nil {
		t.Fatal("expected a removed action to be rejected")
	}
	if outcome.Outcome != executor.OutcomeBlocked {
		t.Fatalf("outcome = %#v, want Blocked", outcome)
	}
}
