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

package v1alpha1

import (
	"context"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var powerinventorynodelog = logf.Log.WithName("powerinventorynode-resource")

// SetupPowerInventoryNodeWebhookWithManager registers the webhook for PowerInventoryNode in the manager.
func SetupPowerInventoryNodeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.PowerInventoryNode{}).
		WithValidator(&PowerInventoryNodeCustomValidator{}).
		WithDefaulter(&PowerInventoryNodeCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-powerinventorynode,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powerinventorynodes,verbs=create;update,versions=v1alpha1,name=mpowerinventorynode-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerInventoryNodeCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind PowerInventoryNode when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PowerInventoryNodeCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind PowerInventoryNode.
func (d *PowerInventoryNodeCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.PowerInventoryNode) error {
	powerinventorynodelog.Info("Defaulting for PowerInventoryNode", "name", obj.GetName())

	if obj.Spec.PowerPlanningExempt == nil {
		obj.Spec.PowerPlanningExempt = ptrBool(false)
	}
	if obj.Spec.CommunicationPathExempt == nil {
		obj.Spec.CommunicationPathExempt = ptrBool(false)
	}
	if obj.Spec.Roles.ControlPlane == nil {
		obj.Spec.Roles.ControlPlane = ptrBool(false)
	}
	if obj.Spec.Roles.ControlPlaneQuorumMember == nil {
		obj.Spec.Roles.ControlPlaneQuorumMember = ptrBool(false)
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-powerinventorynode,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powerinventorynodes,verbs=create;update,versions=v1alpha1,name=vpowerinventorynode-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerInventoryNodeCustomValidator struct is responsible for validating the PowerInventoryNode resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PowerInventoryNodeCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type PowerInventoryNode.
func (v *PowerInventoryNodeCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.PowerInventoryNode) (admission.Warnings, error) {
	powerinventorynodelog.Info("Validation for PowerInventoryNode upon creation", "name", obj.GetName())

	return nil, validatePowerInventoryNodeAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type PowerInventoryNode.
func (v *PowerInventoryNodeCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.PowerInventoryNode) (admission.Warnings, error) {
	powerinventorynodelog.Info("Validation for PowerInventoryNode upon update", "name", newObj.GetName())

	return nil, validatePowerInventoryNodeAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type PowerInventoryNode.
func (v *PowerInventoryNodeCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.PowerInventoryNode) (admission.Warnings, error) {
	powerinventorynodelog.Info("Validation for PowerInventoryNode upon deletion", "name", obj.GetName())

	return nil, nil
}

func validatePowerInventoryNodeAdmission(obj *powerv1alpha1.PowerInventoryNode) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")
	nodeNamePath := specPath.Child("nodeName")

	if obj.Spec.NodeName == "" {
		errs = append(errs, field.Required(nodeNamePath, "required as the canonical Kubernetes Node name"))
	} else {
		errs = append(errs, validateDNSSubdomain(nodeNamePath, obj.Spec.NodeName)...)
	}
	if obj.Spec.Roles.LastDitchRole != "" && containsControlCharacter(obj.Spec.Roles.LastDitchRole) {
		errs = append(errs, field.Invalid(specPath.Child("roles").Child("lastDitchRole"), obj.Spec.Roles.LastDitchRole, "must not contain control characters"))
	}
	if obj.Spec.Roles.ShutdownTier != nil && *obj.Spec.Roles.ShutdownTier < 1 {
		errs = append(errs, field.Invalid(specPath.Child("roles").Child("shutdownTier"), *obj.Spec.Roles.ShutdownTier, "nodes must use tier 1 or greater; tier 0 is workload-only"))
	}
	return newInvalidAdmissionError("PowerInventoryNode", obj, errs)
}
