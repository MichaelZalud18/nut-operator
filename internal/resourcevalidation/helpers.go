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

package resourcevalidation

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apivalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func ValidateDNSSubdomain(path *field.Path, value string) field.ErrorList {
	var errs field.ErrorList
	for _, msg := range apivalidation.IsDNS1123Subdomain(value) {
		errs = append(errs, field.Invalid(path, value, msg))
	}
	return errs
}

func ValidateDNSLabel(path *field.Path, value string) field.ErrorList {
	var errs field.ErrorList
	for _, msg := range apivalidation.IsDNS1123Label(value) {
		errs = append(errs, field.Invalid(path, value, msg))
	}
	return errs
}

// reservedOperandNamespaces are namespaces this operator must never be pointed at as an operand
// namespace. The `namespaces` RBAC verb (create/update/patch) has no way to scope itself to specific
// namespace names -- Kubernetes RBAC only supports resourceNames on verbs that act on an object that
// already exists, not create -- so this is the only place that blast radius can actually be narrowed:
// reject the request before the controller ever touches one of these (F-4).
var reservedOperandNamespaces = map[string]bool{
	"default":         true,
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

// IsReservedOperandNamespace identifies namespaces operands must never manage.
func IsReservedOperandNamespace(name string) bool {
	return reservedOperandNamespaces[name]
}

func ValidateOptionalNamespace(path *field.Path, value string) field.ErrorList {
	if value == "" {
		return nil
	}
	if errs := ValidateDNSLabel(path, value); len(errs) > 0 {
		return errs
	}
	if IsReservedOperandNamespace(value) {
		return field.ErrorList{field.Invalid(path, value, "must not be a reserved Kubernetes system namespace")}
	}
	return nil
}

func ContainsControlCharacter(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	})
}

func ValidateIdentifierText(path *field.Path, value, purpose string) field.ErrorList {
	if strings.TrimSpace(value) == "" {
		return field.ErrorList{field.Required(path, purpose)}
	}
	if ContainsControlCharacter(value) {
		return field.ErrorList{field.Invalid(path, value, "must not contain control characters")}
	}
	return nil
}

func ValidateObjectNameReference(path *field.Path, ref powerv1alpha1.ObjectNameReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the referenced object name"))
	} else {
		errs = append(errs, ValidateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	return errs
}

func ValidateOptionalObjectNameReference(path *field.Path, ref *powerv1alpha1.ObjectNameReference) field.ErrorList {
	if ref == nil {
		return nil
	}
	return ValidateObjectNameReference(path, *ref)
}

func ValidateNamespacedNameReference(path *field.Path, ref powerv1alpha1.NamespacedNameReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Namespace == "" {
		errs = append(errs, field.Required(path.Child("namespace"), "required as the referenced object namespace"))
	} else {
		errs = append(errs, ValidateDNSLabel(path.Child("namespace"), ref.Namespace)...)
	}
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the referenced object name"))
	} else {
		errs = append(errs, ValidateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	return errs
}

func ValidateOptionalNamespacedNameReference(path *field.Path, ref *powerv1alpha1.NamespacedNameReference) field.ErrorList {
	if ref == nil {
		return nil
	}
	return ValidateNamespacedNameReference(path, *ref)
}

func ValidateSecretKeyReference(path *field.Path, ref powerv1alpha1.SecretKeyReference) field.ErrorList {
	var errs field.ErrorList
	if ref.Namespace == "" {
		errs = append(errs, field.Required(path.Child("namespace"), "required as the Secret namespace"))
	} else {
		errs = append(errs, ValidateDNSLabel(path.Child("namespace"), ref.Namespace)...)
	}
	if ref.Name == "" {
		errs = append(errs, field.Required(path.Child("name"), "required as the Secret name"))
	} else {
		errs = append(errs, ValidateDNSSubdomain(path.Child("name"), ref.Name)...)
	}
	if ref.Key == "" {
		errs = append(errs, field.Required(path.Child("key"), "required as the Secret data key"))
	} else if ContainsControlCharacter(ref.Key) {
		errs = append(errs, field.Invalid(path.Child("key"), ref.Key, "must not contain control characters"))
	}
	return errs
}

func ValidateAnnotationKey(path *field.Path, value string) field.ErrorList {
	if value == "" {
		return nil
	}
	var errs field.ErrorList
	for _, msg := range apivalidation.IsQualifiedName(value) {
		errs = append(errs, field.Invalid(path, value, msg))
	}
	return errs
}

func ValidatePositiveDuration(path *field.Path, duration *metav1.Duration) field.ErrorList {
	if duration == nil {
		return nil
	}
	if duration.Duration <= 0 {
		return field.ErrorList{field.Invalid(path, duration.Duration.String(), "must be greater than zero")}
	}
	return nil
}

func ValidatePodHardening(path *field.Path, hardening powerv1alpha1.PodHardeningSpec) field.ErrorList {
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
