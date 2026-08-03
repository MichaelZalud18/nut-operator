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
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
)

func TestRunnerScalesWorkloads(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	replicas := int32(3)
	runner := Runner{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&appsv1.Deployment{ObjectMeta: objectMeta("apps", "web"), Spec: appsv1.DeploymentSpec{Replicas: &replicas}},
		&appsv1.StatefulSet{ObjectMeta: objectMeta("data", "postgres"), Spec: appsv1.StatefulSetSpec{Replicas: &replicas}},
	).Build()}

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
			},
		},
	})
	if err != nil {
		t.Fatalf("RunAction returned error: %v", err)
	}
	if outcome.Outcome != executor.OutcomeSucceeded {
		t.Fatalf("expected succeeded outcome, got %#v", outcome)
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
	runner := Runner{Client: fake.NewClientBuilder().Build()}
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
		Group: executor.Group{
			Name:   "node-a",
			Action: executor.ActionAgentShutdown,
			NodeReleases: []executor.NodeRelease{{
				NodeName:       "node-a",
				NodePowerAgent: "agent-a",
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
