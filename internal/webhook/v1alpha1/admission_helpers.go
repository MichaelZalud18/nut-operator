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

func boolValue(value *bool) bool {
	return value != nil && *value
}
