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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// ShutdownFlowReconciler reconciles a ShutdownFlow object
type ShutdownFlowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/finalizers,verbs=update

// Reconcile validates shutdown flow safety and records compiled plan status.
func (r *ShutdownFlowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var flow powerv1alpha1.ShutdownFlow
	if err := r.Get(ctx, req.NamespacedName, &flow); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result := validateShutdownFlow(&flow)
	flow.Status.ObservedGeneration = flow.Generation
	flow.Status.LastEvaluationTime = ptrNow()
	if result.accepted {
		compiled, compiledWaves, estimatedDuration := compileShutdownFlow(&flow)
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseCompiled
		flow.Status.CompiledSteps = compiled
		flow.Status.CompiledWaves = compiledWaves
		flow.Status.EstimatedDuration = estimatedDuration
	} else {
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseError
		flow.Status.CompiledSteps = nil
		flow.Status.CompiledWaves = nil
		flow.Status.EstimatedDuration = nil
	}
	setAcceptedCondition(&flow.Status.Conditions, flow.Generation, result)
	setReadyCondition(
		&flow.Status.Conditions,
		flow.Generation,
		result.accepted,
		"Compiled",
		"shutdown flow compiled for dry-run review",
	)
	setDegradedCondition(&flow.Status.Conditions, flow.Generation, !result.accepted, result.reason, result.message)

	if err := r.Status().Update(ctx, &flow); err != nil {
		log.Error(err, "failed to update ShutdownFlow status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func ptrNow() *metav1.Time {
	now := metav1.Now()
	return &now
}

// SetupWithManager sets up the controller with the Manager.
func (r *ShutdownFlowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.ShutdownFlow{}).
		Named("shutdownflow").
		Complete(r)
}
