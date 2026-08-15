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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func holdScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := revocationScheme(t)
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 to scheme: %v", err)
	}
	return scheme
}

func holdFlow(name string, phase powerv1alpha1.ShutdownFlowPhase, last *powerv1alpha1.ShutdownExecutionStatus) *powerv1alpha1.ShutdownFlow {
	return &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: powerv1alpha1.ShutdownFlowStatus{
			Phase:         phase,
			LastExecution: last,
		},
	}
}

func TestFlowIsLiveWhileItsTriggerEpisodeIs(t *testing.T) {
	completed := metav1.NewTime(time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC))
	cases := map[string]struct {
		flow *powerv1alpha1.ShutdownFlow
		live bool
	}{
		"running before any execution record": {
			flow: holdFlow("a", powerv1alpha1.ShutdownFlowPhaseRunning, nil),
			live: true,
		},
		"trigger still eligible": {
			flow: holdFlow("a", powerv1alpha1.ShutdownFlowPhaseCompleted, &powerv1alpha1.ShutdownExecutionStatus{
				ExecutionID: "exec-1", TriggerActive: true, CompletedAt: &completed,
			}),
			live: true,
		},
		"episode over": {
			flow: holdFlow("a", powerv1alpha1.ShutdownFlowPhaseCompleted, &powerv1alpha1.ShutdownExecutionStatus{
				ExecutionID: "exec-1", TriggerActive: false, CompletedAt: &completed,
			}),
			live: false,
		},
		"compiled but never triggered": {
			flow: holdFlow("a", powerv1alpha1.ShutdownFlowPhaseCompiled, nil),
			live: false,
		},
	}
	for name, tc := range cases {
		if got := shutdownFlowIsLive(tc.flow); got != tc.live {
			t.Errorf("%s: shutdownFlowIsLive = %v, want %v", name, got, tc.live)
		}
	}
}

func TestRolloutHoldNamesTheLowestFlowSoTheMessageIsStable(t *testing.T) {
	completed := metav1.NewTime(time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC))
	live := func(name string) *powerv1alpha1.ShutdownFlow {
		return holdFlow(name, powerv1alpha1.ShutdownFlowPhaseCompleted, &powerv1alpha1.ShutdownExecutionStatus{
			ExecutionID: "exec-1", TriggerActive: true, CompletedAt: &completed,
		})
	}
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(revocationScheme(t)).
			WithObjects(live("rack-b"), live("rack-a"), holdFlow("rack-c", powerv1alpha1.ShutdownFlowPhaseCompiled, nil)).
			Build(),
	}

	for i := 0; i < 3; i++ {
		held, err := reconciler.shutdownFlowHoldingRollouts(context.Background())
		if err != nil {
			t.Fatalf("shutdownFlowHoldingRollouts returned error: %v", err)
		}
		if held != "rack-a" {
			t.Fatalf("pass %d reported holder %q, want the lowest-named live flow", i, held)
		}
	}
}

func TestNothingHoldsRolloutsWhenNoFlowIsLive(t *testing.T) {
	completed := metav1.NewTime(time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC))
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(revocationScheme(t)).
			WithObjects(holdFlow("rack-a", powerv1alpha1.ShutdownFlowPhaseCompleted, &powerv1alpha1.ShutdownExecutionStatus{
				ExecutionID: "exec-1", TriggerActive: false, CompletedAt: &completed,
			})).
			Build(),
	}

	held, err := reconciler.shutdownFlowHoldingRollouts(context.Background())
	if err != nil {
		t.Fatalf("shutdownFlowHoldingRollouts returned error: %v", err)
	}
	if held != "" {
		t.Fatalf("a settled flow is holding rollouts: %q", held)
	}
}

func holdTestDaemonSet(priorityClass string) *appsv1.DaemonSet {
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName}}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: revocationNamespace,
			Name:      nodePowerAgentDaemonSetName(agent),
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"keep": "me"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"keep": "me"},
					Annotations: map[string]string{"power.zalud.io/config-hash": "hash-from-before-the-outage"},
				},
				Spec: corev1.PodSpec{PriorityClassName: priorityClass},
			},
		},
	}
}

func TestHeldDaemonSetWriteLeavesTheLiveSpecUntouched(t *testing.T) {
	scheme := holdScheme(t)
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName, Generation: 1}}
	existing := holdTestDaemonSet("system-node-critical")
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing, agent).Build(),
		Scheme: scheme,
	}

	returned, err := reconciler.ensureNodePowerAgentDaemonSet(context.Background(), agent, revocationNamespace, "rack-a", nodePowerAgentDaemonSetSpec{
		ConfigMapName:      "cfg",
		SecretName:         "sec",
		ServiceAccountName: "sa",
		ConfigHash:         "hash-that-would-roll-every-pod",
		UpsmonImage:        "upsmon:new",
		ActuatorImage:      "actuator:new",
		SignalSecretName:   "signals",
	})
	if err != nil {
		t.Fatalf("ensureNodePowerAgentDaemonSet returned error: %v", err)
	}
	if got := returned.Spec.Template.Annotations["power.zalud.io/config-hash"]; got != "hash-from-before-the-outage" {
		t.Errorf("config hash is %q; the spec write landed mid-flow and will roll every agent pod", got)
	}

	var stored appsv1.DaemonSet
	if err := reconciler.Get(context.Background(), client.ObjectKeyFromObject(existing), &stored); err != nil {
		t.Fatalf("get stored DaemonSet: %v", err)
	}
	if got := stored.Spec.Template.Annotations["power.zalud.io/config-hash"]; got != "hash-from-before-the-outage" {
		t.Errorf("stored config hash is %q; the write was not actually held", got)
	}
}

// A missing DaemonSet is created even while held: no agent at all is worse than an agent that
// restarts at an awkward moment, and deferring creation would strand an agent added mid-outage.
func TestHeldWriteStillCreatesAMissingDaemonSet(t *testing.T) {
	scheme := holdScheme(t)
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName, Generation: 1}}
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build(),
		Scheme: scheme,
	}

	created, err := reconciler.ensureNodePowerAgentDaemonSet(context.Background(), agent, revocationNamespace, "rack-a", nodePowerAgentDaemonSetSpec{
		ConfigMapName:      "cfg",
		SecretName:         "sec",
		ServiceAccountName: "sa",
		ConfigHash:         "hash",
		UpsmonImage:        "upsmon:1",
		ActuatorImage:      "actuator:1",
		SignalSecretName:   "signals",
	})
	if err != nil {
		t.Fatalf("ensureNodePowerAgentDaemonSet returned error: %v", err)
	}
	if created.Spec.Template.Annotations["power.zalud.io/config-hash"] != "hash" {
		t.Fatal("a DaemonSet that did not exist yet was not created while a flow held rollouts")
	}
}

func TestUnheldWriteLandsNormally(t *testing.T) {
	scheme := holdScheme(t)
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName, Generation: 1}}
	existing := holdTestDaemonSet("system-node-critical")
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing, agent).Build(),
		Scheme: scheme,
	}

	updated, err := reconciler.ensureNodePowerAgentDaemonSet(context.Background(), agent, revocationNamespace, "", nodePowerAgentDaemonSetSpec{
		ConfigMapName:      "cfg",
		SecretName:         "sec",
		ServiceAccountName: "sa",
		ConfigHash:         "hash-after-the-outage",
		UpsmonImage:        "upsmon:new",
		ActuatorImage:      "actuator:new",
		SignalSecretName:   "signals",
	})
	if err != nil {
		t.Fatalf("ensureNodePowerAgentDaemonSet returned error: %v", err)
	}
	if got := updated.Spec.Template.Annotations["power.zalud.io/config-hash"]; got != "hash-after-the-outage" {
		t.Fatalf("config hash is %q; the deferred write never lands once the flow settles", got)
	}
}
