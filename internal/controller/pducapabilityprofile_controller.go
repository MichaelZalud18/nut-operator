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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
)

// PDUCapabilityProfileReconciler reconciles a PDUCapabilityProfile object.
//
// The parallel kind to UPSCapabilityProfile (OD-25). Scaffolding in v1: the kind
// exists, validates, and matches, and no PDU actuation is implemented.
type PDUCapabilityProfileReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=pducapabilityprofiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=pducapabilityprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=pducapabilityprofiles/finalizers,verbs=update

// Reconcile validates one declarative PDU capability profile.
func (r *PDUCapabilityProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var profile powerv1alpha1.PDUCapabilityProfile
	if err := r.Get(ctx, req.NamespacedName, &profile); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	base := profile.DeepCopy()

	result := validatePDUCapabilityProfile(&profile)
	profile.Status.ObservedGeneration = profile.Generation
	if result.accepted {
		profile.Status.Phase = powerv1alpha1.PDUCapabilityProfilePhaseAccepted
		profile.Status.ProfileHash = hashJSON(pduCapabilityProfileFromCRD(&profile))
	} else {
		profile.Status.Phase = powerv1alpha1.PDUCapabilityProfilePhaseError
		profile.Status.ProfileHash = ""
	}
	setAcceptedCondition(&profile.Status.Conditions, profile.Generation, result)

	// A profile can be individually valid and still leave the set unable to resolve -- two profiles
	// claiming selector.universal, or the same id and version twice. Those checks previously ran
	// only inside MatchPDU, which has no caller while there is no PDU device kind (OD-25), so the
	// conflict was reported nowhere and both profiles read as Accepted.
	//
	// Accepted stays true because the spec really is valid. Ready goes false because the profile
	// cannot be used for resolution while the conflict stands, and that is the distinction the two
	// conditions exist to draw.
	readyReason, readyMessage := result.reason, result.message
	ready := result.accepted
	if result.accepted {
		conflict, err := r.pduProfileSetConflict(ctx, &profile)
		if err != nil {
			return ctrl.Result{}, err
		}
		if conflict != nil {
			ready = false
			readyReason, readyMessage = conflict.Reason, conflict.Message
			profile.Status.Phase = powerv1alpha1.PDUCapabilityProfilePhaseError
		}
	}
	setReadyCondition(&profile.Status.Conditions, profile.Generation, ready, readyReason, readyMessage)
	setDegradedCondition(&profile.Status.Conditions, profile.Generation, !ready, readyReason, readyMessage)

	if err := r.Status().Patch(ctx, &profile, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update PDUCapabilityProfile status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PDUCapabilityProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Every profile is re-evaluated when any profile changes, because the conflicts this reconciler
	// reports are properties of the set rather than of one object. Without the fan-out, whichever
	// profile happened to reconcile first keeps reporting Ready after a second universal profile
	// arrives and makes the set unresolvable -- and, worse, keeps reporting the conflict after the
	// profile that caused it is deleted. Measured on kind before adding it: creating a second
	// universal profile left the first reporting Ready=True.
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.PDUCapabilityProfile{}).
		Watches(&powerv1alpha1.PDUCapabilityProfile{}, handler.EnqueueRequestsFromMapFunc(r.allPDUCapabilityProfiles)).
		Named("pducapabilityprofile").
		Complete(r)
}

// pduProfileSetConflict reports a cross-profile conflict that involves this profile.
//
// Only accepted profiles are considered. A profile the reconciler already rejected is not part of
// any resolvable set, and folding it in would report a duplicate-id conflict against a profile that
// was never going to be used -- blaming the valid profile for the invalid one.
func (r *PDUCapabilityProfileReconciler) pduProfileSetConflict(ctx context.Context, profile *powerv1alpha1.PDUCapabilityProfile) (*capability.Diagnostic, error) {
	var list powerv1alpha1.PDUCapabilityProfileList
	if err := r.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list PDUCapabilityProfile resources: %w", err)
	}

	profiles := make([]capability.PDUProfile, 0, len(list.Items))
	for i := range list.Items {
		obj := &list.Items[i]
		if obj.Name != profile.Name && !validatePDUCapabilityProfile(obj).accepted {
			continue
		}
		profiles = append(profiles, pduCapabilityProfileFromCRD(obj))
	}

	subject := pduCapabilityProfileFromCRD(profile).ID
	for _, diagnostic := range capability.ValidatePDUProfileSet(profiles) {
		if diagnostic.Severity != capability.DiagnosticError {
			continue
		}
		// A set-wide diagnostic names no subject and belongs to every profile in the set; a
		// subject-scoped one belongs only to the profile it names.
		if diagnostic.Subject == "" || diagnostic.Subject == subject {
			return &diagnostic, nil
		}
	}
	return nil, nil
}

// allPDUCapabilityProfiles enqueues every profile, so a set-level conflict is reported on each
// member of the set and cleared from each when it is resolved.
func (r *PDUCapabilityProfileReconciler) allPDUCapabilityProfiles(ctx context.Context, _ client.Object) []reconcile.Request {
	var list powerv1alpha1.PDUCapabilityProfileList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "failed to list PDUCapabilityProfile resources for set re-evaluation")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: list.Items[i].Name},
		})
	}
	return requests
}
