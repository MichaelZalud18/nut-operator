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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/resourcevalidation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
	return resourcevalidation.ValidateDNSSubdomain(path, value)
}

func validateDNSLabel(path *field.Path, value string) field.ErrorList {
	return resourcevalidation.ValidateDNSLabel(path, value)
}

func validateOptionalNamespace(path *field.Path, value string) field.ErrorList {
	return resourcevalidation.ValidateOptionalNamespace(path, value)
}

func containsControlCharacter(value string) bool {
	return resourcevalidation.ContainsControlCharacter(value)
}

func validateIdentifierText(path *field.Path, value, purpose string) field.ErrorList {
	return resourcevalidation.ValidateIdentifierText(path, value, purpose)
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
	return resourcevalidation.ValidateObjectNameReference(path, ref)
}

func validateOptionalObjectNameReference(path *field.Path, ref *powerv1alpha1.ObjectNameReference) field.ErrorList {
	return resourcevalidation.ValidateOptionalObjectNameReference(path, ref)
}

func validateNamespacedNameReference(path *field.Path, ref powerv1alpha1.NamespacedNameReference) field.ErrorList {
	return resourcevalidation.ValidateNamespacedNameReference(path, ref)
}

func validateOptionalNamespacedNameReference(path *field.Path, ref *powerv1alpha1.NamespacedNameReference) field.ErrorList {
	return resourcevalidation.ValidateOptionalNamespacedNameReference(path, ref)
}

func validateSecretKeyReference(path *field.Path, ref powerv1alpha1.SecretKeyReference) field.ErrorList {
	return resourcevalidation.ValidateSecretKeyReference(path, ref)
}

func validateAnnotationKey(path *field.Path, value string) field.ErrorList {
	return resourcevalidation.ValidateAnnotationKey(path, value)
}

func validatePositiveDuration(path *field.Path, duration *metav1.Duration) field.ErrorList {
	return resourcevalidation.ValidatePositiveDuration(path, duration)
}

func validatePodHardening(path *field.Path, hardening powerv1alpha1.PodHardeningSpec) field.ErrorList {
	return resourcevalidation.ValidatePodHardening(path, hardening)
}
