//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSignalHandoffCurrentPod(t *testing.T) {
	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{UID: "daemonset", Generation: 2},
		Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"power.zalud.io/config-hash": "current"},
		}}},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberAvailable: 1},
	}
	controller := true
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "current", UID: "pod", Annotations: map[string]string{"power.zalud.io/config-hash": "current"},
			OwnerReferences: []metav1.OwnerReference{{UID: ds.UID, Controller: &controller}}},
		Spec: corev1.PodSpec{NodeName: "selected-node"},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "actuator", ContainerID: "runtime-id", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
	for _, tc := range []struct {
		name  string
		edit  func(*appsv1.DaemonSet, *[]corev1.Pod)
		valid bool
	}{
		{"current", func(*appsv1.DaemonSet, *[]corev1.Pod) {}, true},
		{"ready old and new surge pods", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { *p = append(*p, *pod.DeepCopy()) }, false},
		{"unobserved rollout", func(d *appsv1.DaemonSet, _ *[]corev1.Pod) { d.Status.ObservedGeneration-- }, false},
		{"old revision only", func(d *appsv1.DaemonSet, _ *[]corev1.Pod) { d.Status.UpdatedNumberScheduled = 0 }, false},
		{"old config hash", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { (*p)[0].Annotations["power.zalud.io/config-hash"] = "old" }, false},
		{"terminating pod", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { now := metav1.Now(); (*p)[0].DeletionTimestamp = &now }, false},
		{"foreign owner", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { (*p)[0].OwnerReferences[0].UID = "foreign" }, false},
		{"wrong node", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { (*p)[0].Spec.NodeName = "other" }, false},
		{"not ready", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { (*p)[0].Status.Conditions = nil }, false},
		{"no actuator identity", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { (*p)[0].Status.ContainerStatuses[0].ContainerID = "" }, false},
		{"actuator stopped", func(_ *appsv1.DaemonSet, p *[]corev1.Pod) { (*p)[0].Status.ContainerStatuses[0].State.Running = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ds.DeepCopy()
			pods := []corev1.Pod{*pod.DeepCopy()}
			tc.edit(d, &pods)
			if _, err := signalHandoffCurrentPod(*d, pods, "selected-node"); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	for _, mutate := range []func(*corev1.Pod){
		func(p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount++ },
		func(p *corev1.Pod) { p.Status.ContainerStatuses[0].ContainerID = "replaced" },
		func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Running = nil },
	} {
		after := pod.DeepCopy()
		mutate(after)
		if signalHandoffActuator(pod) == signalHandoffActuator(*after) {
			t.Fatal("actuator process change was accepted as continuity")
		}
	}
}
