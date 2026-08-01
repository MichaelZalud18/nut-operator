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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// PowerInventoryEdgeReconciler reconciles a PowerInventoryEdge object
type PowerInventoryEdgeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinventoryedges,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinventoryedges/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinventoryedges/finalizers,verbs=update

// Reconcile validates one declarative inventory relation.
func (r *PowerInventoryEdgeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var edge powerv1alpha1.PowerInventoryEdge
	if err := r.Get(ctx, req.NamespacedName, &edge); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result := validatePowerInventoryEdge(&edge)
	edge.Status.ObservedGeneration = edge.Generation
	if result.accepted {
		edge.Status.Phase = powerv1alpha1.InventoryResourcePhaseAccepted
	} else {
		edge.Status.Phase = powerv1alpha1.InventoryResourcePhaseError
	}
	setAcceptedCondition(&edge.Status.Conditions, edge.Generation, result)
	setReadyCondition(&edge.Status.Conditions, edge.Generation, result.accepted, result.reason, result.message)
	setDegradedCondition(&edge.Status.Conditions, edge.Generation, !result.accepted, result.reason, result.message)

	if err := r.Status().Update(ctx, &edge); err != nil {
		log.Error(err, "failed to update PowerInventoryEdge status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PowerInventoryEdgeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.PowerInventoryEdge{}).
		Named("powerinventoryedge").
		Complete(r)
}
