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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nutServerFinalizer blocks NUTServer deletion until finalizeNUTServer records that teardown
// happened. Owner-reference garbage collection already reliably deletes the rendered child resources
// for a cluster-scoped owner with namespaced dependents (verified, not a gap); what deletion never had
// is any observable record it happened at all -- this operator's whole interface model is status,
// Events, and audit records (GP-7), and a deleted NUTServer previously left none of the three (F-1).
const nutServerFinalizer = "power.zalud.io/nutserver-cleanup"

// NUTServerReconciler reconciles a NUTServer object
type NUTServerReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	UpstreamProber upstreamNUTProber
	Recorder       events.EventRecorder
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices,verbs=get;list;watch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch

// Reconcile validates NUT server configuration and records rendering status.
func (r *NUTServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var server powerv1alpha1.NUTServer
	if err := r.Get(ctx, req.NamespacedName, &server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	base := server.DeepCopy()

	if !server.DeletionTimestamp.IsZero() {
		return r.finalizeNUTServer(ctx, &server)
	}
	if !controllerutil.ContainsFinalizer(&server, nutServerFinalizer) {
		controllerutil.AddFinalizer(&server, nutServerFinalizer)
		if err := r.Update(ctx, &server); err != nil {
			return ctrl.Result{}, err
		}
	}

	result := validateNUTServer(&server)
	reconcileResult := ctrl.Result{}
	server.Status.ObservedGeneration = server.Generation
	if result.accepted {
		rendered, renderErr := r.reconcileNUTServerOperands(ctx, &server)
		if renderErr != nil {
			reconcileResult = ctrl.Result{RequeueAfter: time.Minute}
			server.Status.Phase = powerv1alpha1.NUTServerPhaseError
			server.Status.SelectedDevices = nil
			server.Status.ReadyReplicas = 0
			server.Status.ServiceEndpoints = nil
			server.Status.UpstreamNUT = nil
			server.Status.ConfigHash = ""
			server.Status.ManagedResources = nil
			setAcceptedCondition(&server.Status.Conditions, server.Generation, result)
			setReadyCondition(
				&server.Status.Conditions,
				server.Generation,
				false,
				"OperandRenderingFailed",
				renderErr.Error(),
			)
			setDegradedCondition(&server.Status.Conditions, server.Generation, true, "OperandRenderingFailed", renderErr.Error())
		} else {
			server.Status.Phase = powerv1alpha1.NUTServerPhaseRendering
			server.Status.SelectedDevices = rendered.SelectedDevices
			server.Status.ReadyReplicas = rendered.ReadyReplicas
			server.Status.ServiceEndpoints = rendered.ServiceEndpoints
			server.Status.UpstreamNUT = rendered.UpstreamNUT
			server.Status.ConfigHash = rendered.ConfigHash
			server.Status.ManagedResources = rendered.ManagedResources
			setAcceptedCondition(&server.Status.Conditions, server.Generation, result)
			ready := rendered.ReadyReplicas >= rendered.DesiredReplicas
			reason := "AwaitingReplicas"
			message := "NUT server operands rendered; waiting for ready replicas"
			if ready {
				server.Status.Phase = powerv1alpha1.NUTServerPhaseReady
				reason = "Ready"
				message = "NUT server operands are ready"
			}
			if upstreamStatusDegraded(rendered.UpstreamNUT) {
				ready = false
				server.Status.Phase = powerv1alpha1.NUTServerPhaseDegraded
				reason = "UpstreamNUTUnavailable"
				message = "one or more upstream NUT endpoints failed TCP reachability probing"
			}
			if len(rendered.UpstreamNUT) > 0 {
				reconcileResult = ctrl.Result{RequeueAfter: time.Minute}
			}
			setReadyCondition(&server.Status.Conditions, server.Generation, ready, reason, message)
			setDegradedCondition(&server.Status.Conditions, server.Generation, upstreamStatusDegraded(rendered.UpstreamNUT), reason, message)
		}
	} else {
		server.Status.Phase = powerv1alpha1.NUTServerPhaseError
		server.Status.SelectedDevices = nil
		server.Status.ReadyReplicas = 0
		server.Status.ServiceEndpoints = nil
		server.Status.UpstreamNUT = nil
		server.Status.ConfigHash = ""
		server.Status.ManagedResources = nil
		setAcceptedCondition(&server.Status.Conditions, server.Generation, result)
		setReadyCondition(
			&server.Status.Conditions,
			server.Generation,
			false,
			"ValidationFailed",
			result.message,
		)
		setDegradedCondition(&server.Status.Conditions, server.Generation, true, result.reason, result.message)
	}

	if err := r.Status().Patch(ctx, &server, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update NUTServer status")
		return ctrl.Result{}, err
	}

	return reconcileResult, nil
}

// finalizeNUTServer runs when a NUTServer is being deleted. It does not need to explicitly delete the
// rendered child resources (Deployment, ConfigMap, Secrets, Service, NetworkPolicy, PDB) -- owner
// reference garbage collection already does that correctly for a cluster-scoped owner with namespaced
// dependents. What it adds: a Kubernetes Event recording that teardown happened at all, and a blocking
// point in the deletion path instead of a silent one -- previously a deleted NUTServer left no status,
// Event, or audit trace of ever having existed (F-1).
func (r *NUTServerReconciler) finalizeNUTServer(ctx context.Context, server *powerv1alpha1.NUTServer) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(server, nutServerFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(server, nil, corev1.EventTypeNormal, "OperandTeardown", "Delete",
			"NUTServer deleted; rendered operands are being garbage-collected via owner references")
	}
	controllerutil.RemoveFinalizer(server, nutServerFinalizer)
	if err := r.Update(ctx, server); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func upstreamStatusDegraded(statuses []powerv1alpha1.NUTUpstreamStatus) bool {
	for _, status := range statuses {
		if status.Reachable != nil && !*status.Reachable {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *NUTServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.NUTServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.NetworkPolicy{}).
		// F-43: the render reads UPSDevice specs and the Secrets they reference, and Owns covers
		// neither -- a user-supplied credentialSecretRef target carries no owner reference back
		// here. Without these two watches a driver, port, or credential change silently does
		// nothing until an unrelated reconcile fires.
		Watches(&powerv1alpha1.UPSDevice{}, handler.EnqueueRequestsFromMapFunc(r.nutServerRequestsForUPSDevice),
			builder.WithPredicates(nutServerRenderRelevantPredicate())).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.nutServerRequestsForSecret),
			builder.WithPredicates(secretDataChangedPredicate())).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.nutServerRequestsForConfigMap),
			builder.WithPredicates(configMapDataChangedPredicate())).
		Named("nutserver").
		Complete(r)
}
