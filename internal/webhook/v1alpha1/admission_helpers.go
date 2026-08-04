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
	"path/filepath"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apivalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var semanticVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func newInvalidAdmissionError(kind string, obj metav1.Object, errs field.ErrorList) error {
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(
		powerv1alpha1.GroupVersion.WithKind(kind).GroupKind(),
		obj.GetName(),
		errs,
	)
}

func validateDNSSubdomain(path *field.Path, value string) field.ErrorList {
	var errs field.ErrorList
	for _, msg := range apivalidation.IsDNS1123Subdomain(value) {
		errs = append(errs, field.Invalid(path, value, msg))
	}
	return errs
}

func validateDNSLabel(path *field.Path, value string) field.ErrorList {
	var errs field.ErrorList
	for _, msg := range apivalidation.IsDNS1123Label(value) {
		errs = append(errs, field.Invalid(path, value, msg))
	}
	return errs
}

func validateOptionalNamespace(path *field.Path, value string) field.ErrorList {
	if value == "" {
		return nil
	}
	return validateDNSLabel(path, value)
}

func containsControlCharacter(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	})
}

func validateIdentifierText(path *field.Path, value, purpose string) field.ErrorList {
	if strings.TrimSpace(value) == "" {
		return field.ErrorList{field.Required(path, purpose)}
	}
	if containsControlCharacter(value) {
		return field.ErrorList{field.Invalid(path, value, "must not contain control characters")}
	}
	return nil
}

func validateAbsoluteFilePath(path *field.Path, value, purpose string) field.ErrorList {
	if strings.TrimSpace(value) == "" {
		return field.ErrorList{field.Required(path, purpose)}
	}
	if containsControlCharacter(value) {
		return field.ErrorList{field.Invalid(path, value, "must not contain control characters")}
	}
	if !filepath.IsAbs(value) {
		return field.ErrorList{field.Invalid(path, value, "must be absolute")}
	}
	return nil
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func validateObjectNameReference(path *field.Path, ref powerv1alpha1.ObjectNameReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the referenced object name"))
	} else {
		errs = append(errs, validateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	return errs
}

func validateOptionalObjectNameReference(path *field.Path, ref *powerv1alpha1.ObjectNameReference) field.ErrorList {
	if ref == nil {
		return nil
	}
	return validateObjectNameReference(path, *ref)
}

func validateNamespacedNameReference(path *field.Path, ref powerv1alpha1.NamespacedNameReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Namespace == "" {
		errs = append(errs, field.Required(path.Child("namespace"), "required as the referenced object namespace"))
	} else {
		errs = append(errs, validateDNSLabel(path.Child("namespace"), ref.Namespace)...)
	}
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the referenced object name"))
	} else {
		errs = append(errs, validateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	return errs
}

func validateOptionalNamespacedNameReference(path *field.Path, ref *powerv1alpha1.NamespacedNameReference) field.ErrorList {
	if ref == nil {
		return nil
	}
	return validateNamespacedNameReference(path, *ref)
}

func validateSecretKeyReference(path *field.Path, ref powerv1alpha1.SecretKeyReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Namespace == "" {
		errs = append(errs, field.Required(path.Child("namespace"), "required as the Secret namespace"))
	} else {
		errs = append(errs, validateDNSLabel(path.Child("namespace"), ref.Namespace)...)
	}
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the Secret name"))
	} else {
		errs = append(errs, validateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	if ref.Key == "" {
		errs = append(errs, field.Required(path.Child("key"), "required as the Secret data key"))
	} else if containsControlCharacter(ref.Key) {
		errs = append(errs, field.Invalid(path.Child("key"), ref.Key, "must not contain control characters"))
	}
	return errs
}

func validateAnnotationKey(path *field.Path, value string) field.ErrorList {
	if value == "" {
		return nil
	}
	var errs field.ErrorList
	for _, msg := range apivalidation.IsQualifiedName(value) {
		errs = append(errs, field.Invalid(path, value, msg))
	}
	return errs
}

func validatePositiveDuration(path *field.Path, duration *metav1.Duration) field.ErrorList {
	if duration == nil {
		return nil
	}
	if duration.Duration <= 0 {
		return field.ErrorList{field.Invalid(path, duration.Duration.String(), "must be greater than zero")}
	}
	return nil
}

func validatePodHardening(path *field.Path, hardening powerv1alpha1.PodHardeningSpec) field.ErrorList {
	if hardening.SeccompProfileType == "" {
		return nil
	}
	switch hardening.SeccompProfileType {
	case "RuntimeDefault", "Localhost", "Unconfined":
		return nil
	default:
		return field.ErrorList{field.NotSupported(path.Child("seccompProfileType"), hardening.SeccompProfileType, []string{
			"RuntimeDefault",
			"Localhost",
			"Unconfined",
		})}
	}
}
