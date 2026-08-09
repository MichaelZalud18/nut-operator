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
	"path"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var upscapabilityprofilelog = logf.Log.WithName("upscapabilityprofile-resource")

// SetupUPSCapabilityProfileWebhookWithManager registers the webhook for UPSCapabilityProfile in the manager.
func SetupUPSCapabilityProfileWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.UPSCapabilityProfile{}).
		WithValidator(&UPSCapabilityProfileCustomValidator{}).
		WithDefaulter(&UPSCapabilityProfileCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-upscapabilityprofile,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=upscapabilityprofiles,verbs=create;update,versions=v1alpha1,name=mupscapabilityprofile-v1alpha1.kb.io,admissionReviewVersions=v1

// UPSCapabilityProfileCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind UPSCapabilityProfile when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type UPSCapabilityProfileCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind UPSCapabilityProfile.
func (d *UPSCapabilityProfileCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.UPSCapabilityProfile) error {
	upscapabilityprofilelog.Info("Defaulting for UPSCapabilityProfile", "name", obj.GetName())

	if obj.Spec.Selector.Universal == nil {
		obj.Spec.Selector.Universal = ptrBool(false)
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-upscapabilityprofile,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=upscapabilityprofiles,verbs=create;update,versions=v1alpha1,name=vupscapabilityprofile-v1alpha1.kb.io,admissionReviewVersions=v1

// UPSCapabilityProfileCustomValidator struct is responsible for validating the UPSCapabilityProfile resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type UPSCapabilityProfileCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type UPSCapabilityProfile.
func (v *UPSCapabilityProfileCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.UPSCapabilityProfile) (admission.Warnings, error) {
	upscapabilityprofilelog.Info("Validation for UPSCapabilityProfile upon creation", "name", obj.GetName())

	return nil, validateUPSCapabilityProfileAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type UPSCapabilityProfile.
func (v *UPSCapabilityProfileCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.UPSCapabilityProfile) (admission.Warnings, error) {
	upscapabilityprofilelog.Info("Validation for UPSCapabilityProfile upon update", "name", newObj.GetName())

	return nil, validateUPSCapabilityProfileAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type UPSCapabilityProfile.
func (v *UPSCapabilityProfileCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.UPSCapabilityProfile) (admission.Warnings, error) {
	upscapabilityprofilelog.Info("Validation for UPSCapabilityProfile upon deletion", "name", obj.GetName())

	return nil, nil
}

func validateUPSCapabilityProfileAdmission(obj *powerv1alpha1.UPSCapabilityProfile) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")
	selectorPath := specPath.Child("selector")

	if obj.Spec.Version == "" {
		errs = append(errs, field.Required(specPath.Child("version"), "required for deterministic profile precedence"))
	} else if !semanticVersionPattern.MatchString(obj.Spec.Version) {
		errs = append(errs, field.Invalid(specPath.Child("version"), obj.Spec.Version, "must be semantic version x.y.z, optionally prefixed with v and with prerelease/build metadata"))
	}

	selector := obj.Spec.Selector
	universal := boolValue(selector.Universal)
	selectorCount := 0
	for _, value := range []string{selector.Model, selector.ModelGlob, selector.DriverFamily} {
		if value != "" {
			selectorCount++
		}
	}
	if universal {
		selectorCount++
	}
	switch {
	case selectorCount == 0:
		errs = append(errs, field.Required(selectorPath, "requires exactly one of model, modelGlob, driverFamily, or universal"))
	case selectorCount > 1:
		errs = append(errs, field.Invalid(selectorPath, selector, "requires exactly one match tier; firmware may only narrow an exact model selector"))
	}
	if selector.Firmware != "" && selector.Model == "" {
		errs = append(errs, field.Required(selectorPath.Child("model"), "required when spec.selector.firmware is set"))
	}
	if selector.ModelGlob != "" {
		if _, err := path.Match(selector.ModelGlob, "probe"); err != nil {
			errs = append(errs, field.Invalid(selectorPath.Child("modelGlob"), selector.ModelGlob, "must be a valid path.Match glob"))
		}
	}
	errs = append(errs, validateStringSet(specPath.Child("telemetry").Child("variables"), obj.Spec.Telemetry.Variables)...)
	errs = append(errs, validateStringSet(specPath.Child("actuation").Child("behaviors"), obj.Spec.Actuation.Behaviors)...)
	errs = append(errs, validateCapabilityQuirks(specPath.Child("quirks"), obj.Spec.Quirks)...)

	return newInvalidAdmissionError("UPSCapabilityProfile", obj, errs)
}

// validateCapabilityQuirks rejects quirk declarations the matcher cannot evaluate.
//
// Firmware scope is only worth having if a malformed scope is caught at admission
// rather than discovered as a quirk that silently never applies (F-26).
func validateCapabilityQuirks(quirksPath *field.Path, quirks []powerv1alpha1.CapabilityQuirk) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]struct{}{}
	for i, quirk := range quirks {
		quirkPath := quirksPath.Index(i)
		if quirk.Name == "" {
			errs = append(errs, field.Required(quirkPath.Child("name"), "quirk name is required"))
			continue
		}
		if _, duplicate := seen[quirk.Name]; duplicate {
			errs = append(errs, field.Duplicate(quirkPath.Child("name"), quirk.Name))
		}
		seen[quirk.Name] = struct{}{}
		if quirk.Firmware == nil {
			continue
		}
		for j, pattern := range quirk.Firmware.Matches {
			if pattern == "" {
				errs = append(errs, field.Required(quirkPath.Child("firmware", "matches").Index(j), "firmware glob cannot be empty"))
				continue
			}
			if _, err := path.Match(pattern, "probe"); err != nil {
				errs = append(errs, field.Invalid(quirkPath.Child("firmware", "matches").Index(j), pattern,
					"must be a valid path.Match glob"))
			}
		}
	}
	return errs
}

func validateStringSet(path *field.Path, values []string) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]struct{}{}
	for i, value := range values {
		itemPath := path.Index(i)
		if value == "" {
			errs = append(errs, field.Required(itemPath, "must not be empty"))
			continue
		}
		if containsControlCharacter(value) {
			errs = append(errs, field.Invalid(itemPath, value, "must not contain control characters"))
		}
		if _, exists := seen[value]; exists {
			errs = append(errs, field.Duplicate(itemPath, value))
		}
		seen[value] = struct{}{}
	}
	return errs
}
