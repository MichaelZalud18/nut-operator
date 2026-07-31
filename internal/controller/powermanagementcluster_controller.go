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

// PowerManagementClusterReconciler reconciles a PowerManagementCluster object
type PowerManagementClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters/finalizers,verbs=update

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
	cluster.Status.ObservedGeneration = cluster.Generation
	cluster.Status.Storage.Mode = cluster.Spec.Storage.Mode
	cluster.Status.Storage.Ready = result.accepted
	cluster.Status.Storage.Message = result.message
	setAcceptedCondition(&cluster.Status.Conditions, cluster.Generation, result)
	setReadyCondition(
		&cluster.Status.Conditions,
		cluster.Generation,
		result.accepted,
		"ContractValidated",
		"control plane contract is valid; operand reconciliation is not implemented yet",
	)
	setDegradedCondition(&cluster.Status.Conditions, cluster.Generation, !result.accepted, result.reason, result.message)

	if err := r.Status().Update(ctx, &cluster); err != nil {
		log.Error(err, "failed to update PowerManagementCluster status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PowerManagementClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.PowerManagementCluster{}).
		Named("powermanagementcluster").
		Complete(r)
}
