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

// UPSCapabilityProfileReconciler reconciles a UPSCapabilityProfile object
type UPSCapabilityProfileReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=upscapabilityprofiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=upscapabilityprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=upscapabilityprofiles/finalizers,verbs=update

// Reconcile validates one declarative UPS capability profile.
func (r *UPSCapabilityProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var profile powerv1alpha1.UPSCapabilityProfile
	if err := r.Get(ctx, req.NamespacedName, &profile); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	base := profile.DeepCopy()

	result := validateUPSCapabilityProfile(&profile)
	profile.Status.ObservedGeneration = profile.Generation
	if result.accepted {
		profile.Status.Phase = powerv1alpha1.UPSCapabilityProfilePhaseAccepted
		profile.Status.ProfileHash = hashJSON(capabilityProfileFromUPSCapabilityProfile(&profile))
	} else {
		profile.Status.Phase = powerv1alpha1.UPSCapabilityProfilePhaseError
		profile.Status.ProfileHash = ""
	}
	setAcceptedCondition(&profile.Status.Conditions, profile.Generation, result)
	setReadyCondition(&profile.Status.Conditions, profile.Generation, result.accepted, result.reason, result.message)
	setDegradedCondition(&profile.Status.Conditions, profile.Generation, !result.accepted, result.reason, result.message)

	if err := r.Status().Patch(ctx, &profile, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update UPSCapabilityProfile status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *UPSCapabilityProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.UPSCapabilityProfile{}).
		Named("upscapabilityprofile").
		Complete(r)
}
