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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	storageconfig "github.com/MichaelZalud18/nut-operator/internal/storage"
)

// PowerManagementClusterReconciler reconciles a PowerManagementCluster object
type PowerManagementClusterReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	StorageConnector storageconfig.AuditStoreConnector
	Clock            func() time.Time
}

var cnpgClusterGVK = schema.GroupVersionKind{
	Group:   "postgresql.cnpg.io",
	Version: "v1",
	Kind:    "Cluster",
}

const powerManagementClusterCNPGRefField = ".spec.storage.cnpg.clusterRef"

// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile validates the top-level power management contract and records status.
func (r *PowerManagementClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result := validatePowerManagementCluster(&cluster)
	storageStatus, storageReady, readyReason, readyMessage := r.evaluateStorage(ctx, &cluster, result)
	cluster.Status.ObservedGeneration = cluster.Generation
	cluster.Status.Storage = storageStatus
	setAcceptedCondition(&cluster.Status.Conditions, cluster.Generation, result)
	setReadyCondition(
		&cluster.Status.Conditions,
		cluster.Generation,
		result.accepted && storageReady,
		readyReason,
		readyMessage,
	)
	setDegradedCondition(&cluster.Status.Conditions, cluster.Generation, !result.accepted || !storageReady, readyReason, readyMessage)

	if err := r.Status().Update(ctx, &cluster); err != nil {
		log.Error(err, "failed to update PowerManagementCluster status")
		return ctrl.Result{}, err
	}

	if err := r.recordPowerManagementClusterAuditEvent(ctx, &cluster, result, storageStatus, readyReason, readyMessage); err != nil {
		log.Error(err, "failed to record PowerManagementCluster audit event", "powermanagementcluster", cluster.Name)
	}

	if storageconfig.EffectiveMode(cluster.Spec.Storage) == powerv1alpha1.PowerStorageCNPG {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PowerManagementClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&powerv1alpha1.PowerManagementCluster{},
		powerManagementClusterCNPGRefField,
		indexPowerManagementClusterByCNPGCluster,
	); err != nil {
		return err
	}

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.PowerManagementCluster{})

	if cnpgClusterCRDPresent(mgr.GetRESTMapper()) {
		builder = builder.Watches(
			newCNPGClusterObject(),
			handler.EnqueueRequestsFromMapFunc(r.mapCNPGClusterToPowerManagementClusters),
		)
	} else {
		logf.Log.WithName("powermanagementcluster").Info(
			"CNPG Cluster CRD not found; skipping CNPG watch registration. CNPG is an optional " +
				"storage backend, and watching a GVK the API server can't resolve never syncs, which " +
				"trips controller-runtime's CacheSyncTimeout and crash-loops the whole manager. " +
				"PowerManagementCluster resources using CNPG storage still reconcile via their " +
				"periodic requeue; restart the manager after installing CNPG's CRDs to re-enable " +
				"live watch updates.",
		)
	}

	return builder.
		Named("powermanagementcluster").
		Complete(r)
}

// cnpgClusterCRDPresent reports whether the CNPG Cluster CRD is resolvable via API discovery.
// Registering a Watches() against a GVK the API server doesn't recognize never completes its
// initial cache sync; controller-runtime's default 2-minute CacheSyncTimeout then treats that as a
// fatal controller-start error, which is fatal to the whole manager (see cmd/main.go's
// mgr.Start() handling) and repeats forever under CrashLoopBackOff.
func cnpgClusterCRDPresent(mapper meta.RESTMapper) bool {
	_, err := mapper.RESTMapping(cnpgClusterGVK.GroupKind(), cnpgClusterGVK.Version)
	return err == nil
}

func newCNPGClusterObject() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(cnpgClusterGVK)
	return obj
}

func (r *PowerManagementClusterReconciler) evaluateStorage(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster, result validationResult) (powerv1alpha1.StorageStatus, bool, string, string) {
	backend, err := storageconfig.Resolve(cluster.Spec.Storage)
	mode := storageconfig.EffectiveMode(cluster.Spec.Storage)
	status := powerv1alpha1.StorageStatus{Mode: mode}
	if !result.accepted {
		status.Ready = false
		status.Message = result.message
		return status, false, result.reason, result.message
	}
	if err != nil {
		status.Ready = false
		status.Message = err.Error()
		return status, false, "StorageConfigurationInvalid", status.Message
	}

	switch backend.Source {
	case powerv1alpha1.PowerStorageDisabled:
		status.Ready = true
		status.Message = "storage is disabled; suitable only for development or tests"
		return status, true, "StorageDisabled", status.Message
	case powerv1alpha1.PowerStorageExternalPostgres:
		return r.ensureAuditStore(ctx, cluster, status, "ExternalPostgres storage")
	case powerv1alpha1.PowerStorageCNPG:
		cnpgStatus, cnpgReady, reason, message := r.evaluateCNPGStorage(ctx, backend.CNPG.ClusterRef, status)
		if !cnpgReady {
			return cnpgStatus, cnpgReady, reason, message
		}
		return r.ensureAuditStore(
			ctx,
			cluster,
			cnpgStatus,
			fmt.Sprintf("CNPG cluster %s/%s", backend.CNPG.ClusterRef.Namespace, backend.CNPG.ClusterRef.Name),
		)
	default:
		status.Ready = false
		status.Message = result.message
		return status, false, result.reason, result.message
	}
}

func (r *PowerManagementClusterReconciler) ensureAuditStore(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster, status powerv1alpha1.StorageStatus, source string) (powerv1alpha1.StorageStatus, bool, string, string) {
	store, err := r.storageConnector().OpenAuditStore(ctx, cluster)
	if err != nil {
		status.Ready = false
		status.Message = fmt.Sprintf("%s is configured but audit store is not ready: %v", source, err)
		return status, false, "AuditStoreNotReady", status.Message
	}
	retentionErr := store.EnforceRetention(ctx, r.now())
	closeErr := store.Close()
	if retentionErr != nil || closeErr != nil {
		status.Ready = false
		status.Message = fmt.Sprintf("%s audit store opened but retention or close failed: %v", source, errors.Join(retentionErr, closeErr))
		return status, false, "AuditStoreNotReady", status.Message
	}

	status.Ready = true
	status.Message = source + " is ready; audit schema migration applied and retention policy evaluated"
	return status, true, "AuditStoreReady", status.Message
}

func (r *PowerManagementClusterReconciler) recordPowerManagementClusterAuditEvent(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster, result validationResult, storageStatus powerv1alpha1.StorageStatus, reason, message string) error {
	if cluster == nil || storageStatus.Mode == powerv1alpha1.PowerStorageDisabled || !storageStatus.Ready {
		return nil
	}

	store, err := r.storageConnector().OpenAuditStore(ctx, cluster)
	if err != nil {
		return err
	}

	generation := cluster.Generation
	severity := "Info"
	if !result.accepted {
		severity = "Warning"
	}
	recordErr := store.RecordPowerEvent(ctx, audit.PowerEvent{
		EventID:            uuid.NewString(),
		ObservedAt:         r.now(),
		EventType:          "PowerManagementClusterReconciled",
		Severity:           severity,
		SourceKind:         "PowerManagementCluster",
		SourceName:         cluster.Name,
		ResourceGeneration: &generation,
		Message:            message,
		Details: map[string]any{
			"accepted":     result.accepted,
			"reason":       reason,
			"storageMode":  string(storageStatus.Mode),
			"storageReady": storageStatus.Ready,
		},
	})
	closeErr := store.Close()
	if recordErr != nil || closeErr != nil {
		return errors.Join(recordErr, closeErr)
	}
	return nil
}

func (r *PowerManagementClusterReconciler) storageConnector() storageconfig.AuditStoreConnector {
	if r.StorageConnector != nil {
		return r.StorageConnector
	}
	return storageconfig.NewKubernetesConnector(r.Client, storageconfig.ConnectorOptions{})
}

func (r *PowerManagementClusterReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

func storageMode(spec powerv1alpha1.PowerStorageSpec) powerv1alpha1.PowerStorageMode {
	return storageconfig.EffectiveMode(spec)
}

func (r *PowerManagementClusterReconciler) evaluateCNPGStorage(ctx context.Context, ref powerv1alpha1.NamespacedNameReference, status powerv1alpha1.StorageStatus) (powerv1alpha1.StorageStatus, bool, string, string) {
	cnpgCluster := &unstructured.Unstructured{}
	cnpgCluster.SetGroupVersionKind(cnpgClusterGVK)
	if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, cnpgCluster); err != nil {
		status.Ready = false
		if apierrors.IsNotFound(err) {
			status.Message = fmt.Sprintf("CNPG cluster %s/%s was not found", ref.Namespace, ref.Name)
			return status, false, "CNPGClusterNotFound", status.Message
		}
		status.Message = fmt.Sprintf("Could not read CNPG cluster %s/%s: %v", ref.Namespace, ref.Name, err)
		return status, false, "CNPGClusterReadFailed", status.Message
	}

	desiredInstances, _, _ := unstructured.NestedInt64(cnpgCluster.Object, "spec", "instances")
	if desiredInstances < 1 {
		desiredInstances = 1
	}
	readyInstances, _, _ := unstructured.NestedInt64(cnpgCluster.Object, "status", "readyInstances")
	phase, _, _ := unstructured.NestedString(cnpgCluster.Object, "status", "phase")
	if readyInstances < desiredInstances {
		status.Ready = false
		status.Message = fmt.Sprintf("CNPG cluster %s/%s reports %d/%d ready instances; phase=%q", ref.Namespace, ref.Name, readyInstances, desiredInstances, phase)
		return status, false, "CNPGClusterNotReady", status.Message
	}
	if phase != "" && !strings.Contains(strings.ToLower(phase), "healthy") {
		status.Ready = false
		status.Message = fmt.Sprintf("CNPG cluster %s/%s reports phase=%q", ref.Namespace, ref.Name, phase)
		return status, false, "CNPGClusterNotReady", status.Message
	}

	status.Ready = true
	status.Message = fmt.Sprintf("CNPG cluster %s/%s reports %d/%d ready instances", ref.Namespace, ref.Name, readyInstances, desiredInstances)
	return status, true, "CNPGClusterReady", status.Message
}

func indexPowerManagementClusterByCNPGCluster(obj client.Object) []string {
	cluster, ok := obj.(*powerv1alpha1.PowerManagementCluster)
	if !ok {
		return nil
	}
	if storageMode(cluster.Spec.Storage) != powerv1alpha1.PowerStorageCNPG || cluster.Spec.Storage.CNPG == nil {
		return nil
	}
	ref := cluster.Spec.Storage.CNPG.ClusterRef
	if ref.Namespace == "" || ref.Name == "" {
		return nil
	}
	return []string{cnpgReferenceKey(ref.Namespace, ref.Name)}
}

func (r *PowerManagementClusterReconciler) mapCNPGClusterToPowerManagementClusters(ctx context.Context, obj client.Object) []reconcile.Request {
	var clusters powerv1alpha1.PowerManagementClusterList
	if err := r.List(ctx, &clusters, client.MatchingFields{
		powerManagementClusterCNPGRefField: cnpgReferenceKey(obj.GetNamespace(), obj.GetName()),
	}); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(clusters.Items))
	for _, cluster := range clusters.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: cluster.Namespace,
				Name:      cluster.Name,
			},
		})
	}
	return requests
}

func cnpgReferenceKey(namespace, name string) string {
	return namespace + "/" + name
}
