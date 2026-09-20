package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/readiness"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func readyPod() corev1.Pod {
	return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "guest", UID: "original", Labels: map[string]string{"agent": "test"}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "actuator", Ready: true, ContainerID: "containerd://original",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		}}
}

func TestReadyPodRejectsUnhealthyAndDetectsReplacement(t *testing.T) {
	pod := readyPod()
	original, err := ReadyPod(pod, "actuator")
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*corev1.Pod){
		"not-ready":         func(p *corev1.Pod) { p.Status.Conditions[0].Status = corev1.ConditionFalse },
		"crash-loop":        func(p *corev1.Pod) { p.Status.ContainerStatuses[0].State.Running = nil },
		"container-unready": func(p *corev1.Pod) { p.Status.ContainerStatuses[0].Ready = false },
		"deleted":           func(p *corev1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now },
		"missing-uid":       func(p *corev1.Pod) { p.UID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := pod.DeepCopy()
			change(changed)
			if _, err := ReadyPod(*changed, "actuator"); err == nil {
				t.Fatal("unhealthy pod accepted")
			}
		})
	}
	pod.Status.ContainerStatuses[0].RestartCount++
	restarted, err := ReadyPod(pod, "actuator")
	if err != nil || restarted == original {
		t.Fatal("container restart did not change identity")
	}
	pod.UID = "replacement"
	replaced, err := ReadyPod(pod, "actuator")
	if err != nil || replaced == restarted {
		t.Fatal("pod replacement did not change identity")
	}
}

func TestClusterIdentityAndScopedPodWait(t *testing.T) {
	pod := readyPod()
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-a"}}, &pod)
	if err := CheckIdentity(context.Background(), client, "cluster-a", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := CheckIdentity(context.Background(), client, "cluster-b", time.Second); err == nil {
		t.Fatal("foreign cluster accepted")
	}
	opts := readiness.Options{Timeout: 20 * time.Millisecond, Interval: time.Millisecond}
	id, err := WaitForReadyPod(context.Background(), client.CoreV1().Pods("guest"), "agent=test", "actuator", opts)
	if err != nil || id.UID != pod.UID {
		t.Fatalf("pod observation: %+v, %v", id, err)
	}
	pod.Name = "second"
	pod.UID = "second"
	if _, err := client.CoreV1().Pods("guest").Create(context.Background(), &pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := WaitForReadyPod(context.Background(), client.CoreV1().Pods("guest"), "agent=test", "actuator", opts); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ambiguous pods accepted: %v", err)
	}
}

func TestExplicitConfigAndNodeConditions(t *testing.T) {
	if _, err := Client(nil, time.Second); err == nil {
		t.Fatal("ambient kubeconfig accepted")
	}
	if _, err := Client([]byte("invalid"), time.Second); err == nil {
		t.Fatal("invalid kubeconfig accepted")
	}
	node := corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	if !NodeReady(node) {
		t.Fatal("ready node rejected")
	}
	node.Status.Conditions[0].Status = corev1.ConditionFalse
	if NodeReady(node) {
		t.Fatal("NotReady node accepted")
	}
}
