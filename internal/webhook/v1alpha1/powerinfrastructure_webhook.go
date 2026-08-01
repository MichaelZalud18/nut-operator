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
var powerinfrastructurelog = logf.Log.WithName("powerinfrastructure-resource")

// SetupPowerInfrastructureWebhookWithManager registers the webhook for PowerInfrastructure in the manager.
func SetupPowerInfrastructureWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.PowerInfrastructure{}).
		WithValidator(&PowerInfrastructureCustomValidator{}).
		WithDefaulter(&PowerInfrastructureCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-powerinfrastructure,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powerinfrastructures,verbs=create;update,versions=v1alpha1,name=mpowerinfrastructure-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerInfrastructureCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind PowerInfrastructure when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PowerInfrastructureCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind PowerInfrastructure.
func (d *PowerInfrastructureCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.PowerInfrastructure) error {
	powerinfrastructurelog.Info("Defaulting for PowerInfrastructure", "name", obj.GetName())

	if obj.Spec.Class == "" {
		obj.Spec.Class = powerv1alpha1.PowerInfrastructureClassOther
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-powerinfrastructure,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powerinfrastructures,verbs=create;update,versions=v1alpha1,name=vpowerinfrastructure-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerInfrastructureCustomValidator struct is responsible for validating the PowerInfrastructure resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PowerInfrastructureCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type PowerInfrastructure.
func (v *PowerInfrastructureCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.PowerInfrastructure) (admission.Warnings, error) {
	powerinfrastructurelog.Info("Validation for PowerInfrastructure upon creation", "name", obj.GetName())

	return nil, validatePowerInfrastructureAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type PowerInfrastructure.
func (v *PowerInfrastructureCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.PowerInfrastructure) (admission.Warnings, error) {
	powerinfrastructurelog.Info("Validation for PowerInfrastructure upon update", "name", newObj.GetName())

	return nil, validatePowerInfrastructureAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type PowerInfrastructure.
func (v *PowerInfrastructureCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.PowerInfrastructure) (admission.Warnings, error) {
	powerinfrastructurelog.Info("Validation for PowerInfrastructure upon deletion", "name", obj.GetName())

	return nil, nil
}

func validatePowerInfrastructureAdmission(obj *powerv1alpha1.PowerInfrastructure) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")
	if obj.Spec.Class != "" && !isSupportedPowerInfrastructureClass(obj.Spec.Class) {
		errs = append(errs, field.NotSupported(specPath.Child("class"), obj.Spec.Class, supportedPowerInfrastructureClasses()))
	}
	if containsControlCharacter(obj.Spec.DisplayName) {
		errs = append(errs, field.Invalid(specPath.Child("displayName"), obj.Spec.DisplayName, "must not contain control characters"))
	}
	if containsControlCharacter(obj.Spec.Description) {
		errs = append(errs, field.Invalid(specPath.Child("description"), obj.Spec.Description, "must not contain control characters"))
	}
	return newInvalidAdmissionError("PowerInfrastructure", obj, errs)
}

func isSupportedPowerInfrastructureClass(class powerv1alpha1.PowerInfrastructureClass) bool {
	for _, supported := range supportedPowerInfrastructureClasses() {
		if string(class) == supported {
			return true
		}
	}
	return false
}

func supportedPowerInfrastructureClasses() []string {
	return []string{
		string(powerv1alpha1.PowerInfrastructureClassPDU),
		string(powerv1alpha1.PowerInfrastructureClassSwitch),
		string(powerv1alpha1.PowerInfrastructureClassRouter),
		string(powerv1alpha1.PowerInfrastructureClassTransferSwitch),
		string(powerv1alpha1.PowerInfrastructureClassPowerPanel),
		string(powerv1alpha1.PowerInfrastructureClassNetworkDevice),
		string(powerv1alpha1.PowerInfrastructureClassOther),
	}
}
