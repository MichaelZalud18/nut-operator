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
var powerinventoryedgelog = logf.Log.WithName("powerinventoryedge-resource")

// SetupPowerInventoryEdgeWebhookWithManager registers the webhook for PowerInventoryEdge in the manager.
func SetupPowerInventoryEdgeWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.PowerInventoryEdge{}).
		WithValidator(&PowerInventoryEdgeCustomValidator{}).
		WithDefaulter(&PowerInventoryEdgeCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-powerinventoryedge,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powerinventoryedges,verbs=create;update,versions=v1alpha1,name=mpowerinventoryedge-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerInventoryEdgeCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind PowerInventoryEdge when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PowerInventoryEdgeCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind PowerInventoryEdge.
func (d *PowerInventoryEdgeCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.PowerInventoryEdge) error {
	powerinventoryedgelog.Info("Defaulting for PowerInventoryEdge", "name", obj.GetName())

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-powerinventoryedge,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powerinventoryedges,verbs=create;update,versions=v1alpha1,name=vpowerinventoryedge-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerInventoryEdgeCustomValidator struct is responsible for validating the PowerInventoryEdge resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PowerInventoryEdgeCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type PowerInventoryEdge.
func (v *PowerInventoryEdgeCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.PowerInventoryEdge) (admission.Warnings, error) {
	powerinventoryedgelog.Info("Validation for PowerInventoryEdge upon creation", "name", obj.GetName())

	return nil, validatePowerInventoryEdgeAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type PowerInventoryEdge.
func (v *PowerInventoryEdgeCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.PowerInventoryEdge) (admission.Warnings, error) {
	powerinventoryedgelog.Info("Validation for PowerInventoryEdge upon update", "name", newObj.GetName())

	return nil, validatePowerInventoryEdgeAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type PowerInventoryEdge.
func (v *PowerInventoryEdgeCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.PowerInventoryEdge) (admission.Warnings, error) {
	powerinventoryedgelog.Info("Validation for PowerInventoryEdge upon deletion", "name", obj.GetName())

	return nil, nil
}

func validatePowerInventoryEdgeAdmission(obj *powerv1alpha1.PowerInventoryEdge) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")
	fromPath := specPath.Child("from")
	toPath := specPath.Child("to")

	errs = append(errs, validateInventoryEntityReference(fromPath, obj.Spec.From)...)
	errs = append(errs, validateInventoryEntityReference(toPath, obj.Spec.To)...)
	if obj.Spec.From.Kind == obj.Spec.To.Kind && obj.Spec.From.Name != "" && obj.Spec.From.Name == obj.Spec.To.Name {
		errs = append(errs, field.Invalid(specPath, obj.Spec, "from and to cannot reference the same inventory entity"))
	}

	switch obj.Spec.Relation {
	case powerv1alpha1.PowerInventoryEdgeFeeds:
		errs = append(errs, validateIdentifierText(specPath.Child("input"), obj.Spec.Input, "Feeds edges require a target power input")...)
	case powerv1alpha1.PowerInventoryEdgeCarries:
		if obj.Spec.Input != "" {
			errs = append(errs, field.Forbidden(specPath.Child("input"), "only Feeds edges carry an input qualifier"))
		}
	case "":
		errs = append(errs, field.Required(specPath.Child("relation"), "required as Feeds or Carries"))
	default:
		errs = append(errs, field.NotSupported(specPath.Child("relation"), obj.Spec.Relation, []string{
			string(powerv1alpha1.PowerInventoryEdgeFeeds),
			string(powerv1alpha1.PowerInventoryEdgeCarries),
		}))
	}
	if containsControlCharacter(obj.Spec.Description) {
		errs = append(errs, field.Invalid(specPath.Child("description"), obj.Spec.Description, "must not contain control characters"))
	}
	return newInvalidAdmissionError("PowerInventoryEdge", obj, errs)
}

func validateInventoryEntityReference(path *field.Path, ref powerv1alpha1.PowerInventoryEntityReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Kind == "" {
		errs = append(errs, field.Required(path.Child("kind"), "required as UPSDevice, Node, or PowerInfrastructure"))
	} else if !isSupportedInventoryEntityKind(ref.Kind) {
		errs = append(errs, field.NotSupported(path.Child("kind"), ref.Kind, supportedInventoryEntityKinds()))
	}
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the referenced object or node name"))
	} else {
		errs = append(errs, validateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	return errs
}

func isSupportedInventoryEntityKind(kind powerv1alpha1.PowerInventoryEntityKind) bool {
	for _, supported := range supportedInventoryEntityKinds() {
		if string(kind) == supported {
			return true
		}
	}
	return false
}

func supportedInventoryEntityKinds() []string {
	return []string{
		string(powerv1alpha1.PowerInventoryEntityUPSDevice),
		string(powerv1alpha1.PowerInventoryEntityNode),
		string(powerv1alpha1.PowerInventoryEntityPowerInfrastructure),
	}
}
