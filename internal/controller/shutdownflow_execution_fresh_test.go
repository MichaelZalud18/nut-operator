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
	"testing"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewExecutionDoesNotRestorePublishedAdaptiveState(t *testing.T) {
	flow := &powerv1alpha1.ShutdownFlow{}
	flow.Status.LastExecution = &powerv1alpha1.ShutdownExecutionStatus{
		Adaptive: &powerv1alpha1.ShutdownExecutionAdaptiveStatus{
			Tier: 2, DeepestTier: 1, PointerStarted: true, TimingMode: string(adaptive.ModeUrgent),
		},
	}
	input := adaptiveInputForFlow(flow, adaptive.PowerObservation{OnBattery: true})
	if input.Pointer != (adaptive.PointerState{}) || input.Timing != (adaptive.TimingState{}) {
		t.Fatalf("new execution restored historical state: pointer=%+v timing=%+v", input.Pointer, input.Timing)
	}
	if !input.Observation.OnBattery {
		t.Fatal("new execution lost its current power observation")
	}
}

func TestRepeatedExecutionKeepsWorkloadScaledDownWithoutCheckpoint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	replicas := int32(3)
	workload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload).Build()
	writer := &fakeAuditStore{}
	e := executor.Executor{Writer: writer, Runner: kubeactions.Runner{Client: c}}
	input := executor.Input{
		ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: executor.ModeEnforce, Approved: true,
		Waves: []executor.Wave{{Groups: []string{"applications"}}},
		Groups: []executor.Group{{
			Name: "applications", Action: kubeactions.ActionScale,
			SelectedTargets: []executor.Target{{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"}},
		}},
	}
	var firstVersion string
	for _, id := range []string{"first-execution", "replacement-execution"} {
		input.ExecutionID = id
		result, err := e.Execute(context.Background(), input)
		if err != nil || result.Phase != executor.PhaseCompleted || result.DryRun || result.ActionAttempts != 1 {
			t.Fatalf("execution %s: %+v %v", id, result, err)
		}
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(workload), workload); err != nil {
			t.Fatal(err)
		}
		if workload.Spec.Replicas == nil || *workload.Spec.Replicas != 0 {
			t.Fatalf("execution %s did not leave workload stopped: %+v", id, workload.Spec.Replicas)
		}
		if firstVersion != "" && workload.ResourceVersion != firstVersion {
			t.Fatal("repeating the shutdown changed an already stopped workload")
		}
		firstVersion = workload.ResourceVersion
	}
	if len(writer.actionAttempts) != 2 || len(writer.executionGroups) != 2 {
		t.Fatal("both action attempts must remain visible in audit history")
	}
}
