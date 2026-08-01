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

// PowerInfrastructureReconciler reconciles a PowerInfrastructure object
type PowerInfrastructureReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinfrastructures,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinfrastructures/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinfrastructures/finalizers,verbs=update

// Reconcile validates one declarative power infrastructure entity.
func (r *PowerInfrastructureReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var infrastructure powerv1alpha1.PowerInfrastructure
	if err := r.Get(ctx, req.NamespacedName, &infrastructure); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result := validatePowerInfrastructure(&infrastructure)
	infrastructure.Status.ObservedGeneration = infrastructure.Generation
	if result.accepted {
		infrastructure.Status.Phase = powerv1alpha1.InventoryResourcePhaseAccepted
	} else {
		infrastructure.Status.Phase = powerv1alpha1.InventoryResourcePhaseError
	}
	setAcceptedCondition(&infrastructure.Status.Conditions, infrastructure.Generation, result)
	setReadyCondition(
		&infrastructure.Status.Conditions,
		infrastructure.Generation,
		result.accepted,
		result.reason,
		result.message,
	)
	setDegradedCondition(&infrastructure.Status.Conditions, infrastructure.Generation, !result.accepted, result.reason, result.message)

	if err := r.Status().Update(ctx, &infrastructure); err != nil {
		log.Error(err, "failed to update PowerInfrastructure status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PowerInfrastructureReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.PowerInfrastructure{}).
		Named("powerinfrastructure").
		Complete(r)
}
