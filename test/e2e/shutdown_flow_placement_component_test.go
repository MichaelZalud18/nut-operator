//go:build e2e

package e2e

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLogicalFlowSharedNamespaceCoverage(t *testing.T) {
	want := []string{"kube-system", "cert-manager", "local-path-storage"}
	if got := logicalFlowSharedNamespaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("shared relocation namespaces = %v, want %v", got, want)
	}
}

func TestLogicalFlowLocalPathRemainsDrainBlocker(t *testing.T) {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "local-path-provisioner-test", Namespace: "local-path-storage",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "local-path-provisioner-test"}}},
		Spec:   corev1.PodSpec{NodeName: "target"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	want := []string{"local-path-storage/local-path-provisioner-test"}
	if got := logicalFlowDrainBlockers("target", []corev1.Pod{pod}); !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelocated storage controller must block drain: %v", got)
	}
	terminating := pod.DeepCopy()
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	if got := logicalFlowDrainBlockers("target", []corev1.Pod{*terminating}); !reflect.DeepEqual(got, want) {
		t.Fatalf("old terminating storage pod must still block drain: %v", got)
	}
	pod.Spec.NodeName = "survivor"
	if got := logicalFlowDrainBlockers("target", []corev1.Pod{pod}); len(got) != 0 {
		t.Fatalf("relocated storage controller must not block target: %v", got)
	}
	pod.Spec.NodeName, pod.Name = "target", "unexpected-deployment"
	if got := logicalFlowDrainBlockers("target", []corev1.Pod{pod}); len(got) != 1 {
		t.Fatal("shared namespace must not become a blanket drain exception")
	}
}
