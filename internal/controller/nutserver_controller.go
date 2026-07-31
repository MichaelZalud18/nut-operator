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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// NUTServerReconciler reconciles a NUTServer object
type NUTServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices,verbs=get;list;watch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch

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
			setReadyCondition(&server.Status.Conditions, server.Generation, ready, reason, message)
			setDegradedCondition(&server.Status.Conditions, server.Generation, false, "AsExpected", "NUT server operands rendered")
		}
	} else {
		server.Status.Phase = powerv1alpha1.NUTServerPhaseError
		server.Status.SelectedDevices = nil
		server.Status.ReadyReplicas = 0
		server.Status.ServiceEndpoints = nil
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

	if err := r.Status().Update(ctx, &server); err != nil {
		log.Error(err, "failed to update NUTServer status")
		return ctrl.Result{}, err
	}

	return reconcileResult, nil
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
		Named("nutserver").
		Complete(r)
}
