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

// PowerInventoryNodeReconciler reconciles a PowerInventoryNode object
type PowerInventoryNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinventorynodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinventorynodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinventorynodes/finalizers,verbs=update

// Reconcile validates one declarative Kubernetes node inventory record.
func (r *PowerInventoryNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var node powerv1alpha1.PowerInventoryNode
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	base := node.DeepCopy()

	result := validatePowerInventoryNode(&node)
	node.Status.ObservedGeneration = node.Generation
	if result.accepted {
		node.Status.Phase = powerv1alpha1.InventoryResourcePhaseAccepted
	} else {
		node.Status.Phase = powerv1alpha1.InventoryResourcePhaseError
	}
	setAcceptedCondition(&node.Status.Conditions, node.Generation, result)
	setReadyCondition(&node.Status.Conditions, node.Generation, result.accepted, result.reason, result.message)
	setDegradedCondition(&node.Status.Conditions, node.Generation, !result.accepted, result.reason, result.message)

	if err := r.Status().Patch(ctx, &node, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update PowerInventoryNode status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PowerInventoryNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.PowerInventoryNode{}).
		Named("powerinventorynode").
		Complete(r)
}
