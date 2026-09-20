package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestExternalStorageReadinessRecoversWithoutSpecChanges(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "storage-retry", Generation: 1}, Spec: power.PowerManagementClusterSpec{
		Storage: power.PowerStorageSpec{Mode: power.PowerStorageExternalPostgres, ExternalPostgres: &power.ExternalPostgresStorageSpec{DSNSecretKeyRef: power.SecretKeyReference{Namespace: "power-system", Name: "dsn", Key: "dsn"}}},
	}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(cluster).WithObjects(cluster).Build()
	connector := &fakeAuditConnector{}
	reconciler := &PowerManagementClusterReconciler{Client: kube, Scheme: scheme, StorageConnector: connector}
	for _, ready := range []bool{false, true, false} {
		connector.err = nil
		if !ready {
			connector.err = errors.New("database temporarily unavailable")
		}
		result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)})
		if err != nil {
			t.Fatal(err)
		}
		if result.RequeueAfter <= 0 || result.RequeueAfter > 5*time.Minute || (!ready && result.RequeueAfter > 30*time.Second) {
			t.Fatalf("readiness=%v has no bounded retry: %+v", ready, result)
		}
		var current power.PowerManagementCluster
		if err := kube.Get(context.Background(), client.ObjectKeyFromObject(cluster), &current); err != nil {
			t.Fatal(err)
		}
		if current.Status.Storage.Ready != ready || current.Generation != 1 || current.Status.ObservedGeneration != 1 {
			t.Fatalf("readiness=%v status=%+v generation=%d", ready, current.Status.Storage, current.Generation)
		}
		condition := meta.FindStatusCondition(current.Status.Conditions, "Ready")
		if condition == nil || (condition.Status == metav1.ConditionTrue) != ready || condition.ObservedGeneration != 1 {
			t.Fatalf("readiness=%v Ready condition=%+v", ready, condition)
		}
	}
}

func TestStorageReadinessRetryPreservesOtherModes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		storage power.PowerStorageSpec
		retry   time.Duration
	}{
		{name: "disabled", storage: power.PowerStorageSpec{Mode: power.PowerStorageDisabled}},
		{name: "cnpg", storage: power.PowerStorageSpec{Mode: power.PowerStorageCNPG, CNPG: &power.CNPGStorageSpec{
			ClusterRef: power.NamespacedNameReference{Namespace: "power-system", Name: "audit"},
		}}, retry: 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := power.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "storage-mode", Generation: 1}, Spec: power.PowerManagementClusterSpec{Storage: tc.storage}}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(cluster).WithObjects(cluster).Build()
			reconciler := &PowerManagementClusterReconciler{Client: kube, Scheme: scheme, StorageConnector: &fakeAuditConnector{}}
			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cluster)})
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != tc.retry {
				t.Fatalf("retry=%v, want %v", result.RequeueAfter, tc.retry)
			}
		})
	}
}
