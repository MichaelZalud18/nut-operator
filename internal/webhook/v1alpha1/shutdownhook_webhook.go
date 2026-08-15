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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var shutdownhooklog = logf.Log.WithName("shutdownhook-resource")

// SetupShutdownHookWebhookWithManager registers the webhook for ShutdownHook in the manager.
func SetupShutdownHookWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.ShutdownHook{}).
		WithValidator(&ShutdownHookCustomValidator{}).
		WithDefaulter(&ShutdownHookCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-shutdownhook,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=shutdownhooks,verbs=create;update,versions=v1alpha1,name=mshutdownhook-v1alpha1.kb.io,admissionReviewVersions=v1

// ShutdownHookCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind ShutdownHook when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ShutdownHookCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind ShutdownHook.
func (d *ShutdownHookCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.ShutdownHook) error {
	shutdownhooklog.Info("Defaulting for ShutdownHook", "name", obj.GetName())

	defaultShutdownHook(obj)

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-shutdownhook,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=shutdownhooks,verbs=create;update,versions=v1alpha1,name=vshutdownhook-v1alpha1.kb.io,admissionReviewVersions=v1

// ShutdownHookCustomValidator struct is responsible for validating the ShutdownHook resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ShutdownHookCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ShutdownHook.
func (v *ShutdownHookCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.ShutdownHook) (admission.Warnings, error) {
	shutdownhooklog.Info("Validation for ShutdownHook upon creation", "name", obj.GetName())

	return nil, validateShutdownHookAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ShutdownHook.
func (v *ShutdownHookCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.ShutdownHook) (admission.Warnings, error) {
	shutdownhooklog.Info("Validation for ShutdownHook upon update", "name", newObj.GetName())

	return nil, validateShutdownHookAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ShutdownHook.
func (v *ShutdownHookCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.ShutdownHook) (admission.Warnings, error) {
	shutdownhooklog.Info("Validation for ShutdownHook upon deletion", "name", obj.GetName())

	return nil, nil
}

func defaultShutdownHook(obj *powerv1alpha1.ShutdownHook) {
	if obj.Spec.FailurePolicy == "" {
		obj.Spec.FailurePolicy = powerv1alpha1.ShutdownHookFailureMarkDegraded
	}
	defaultShutdownHookInvocation(&obj.Spec.Invocation)
	if obj.Spec.DryRun != nil {
		defaultShutdownHookInvocation(obj.Spec.DryRun)
	}
}

func defaultShutdownHookInvocation(invocation *powerv1alpha1.ShutdownHookInvocationSpec) {
	if invocation.Transport == "" {
		invocation.Transport = powerv1alpha1.ShutdownHookTransportHTTP
	}
	if invocation.Timeout == nil {
		invocation.Timeout = &metav1.Duration{Duration: 10 * time.Second}
	}
	if invocation.HTTP != nil && invocation.HTTP.Method == "" {
		invocation.HTTP.Method = http.MethodPost
	}
}

func validateShutdownHookAdmission(obj *powerv1alpha1.ShutdownHook) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")
	errs = append(errs, validateShutdownHookInvocation(specPath.Child("invocation"), obj.Spec.Invocation)...)
	if obj.Spec.DryRun != nil {
		errs = append(errs, validateShutdownHookInvocation(specPath.Child("dryRun"), *obj.Spec.DryRun)...)
	}
	switch obj.Spec.FailurePolicy {
	case "", powerv1alpha1.ShutdownHookFailureMarkDegraded:
	default:
		errs = append(errs, field.NotSupported(specPath.Child("failurePolicy"), obj.Spec.FailurePolicy, []string{
			string(powerv1alpha1.ShutdownHookFailureMarkDegraded),
		}))
	}
	return newInvalidAdmissionError("ShutdownHook", obj, errs)
}

func validateShutdownHookInvocation(path *field.Path, invocation powerv1alpha1.ShutdownHookInvocationSpec) field.ErrorList {
	var errs field.ErrorList
	transport := invocation.Transport
	if transport == "" {
		transport = powerv1alpha1.ShutdownHookTransportHTTP
	}
	errs = append(errs, validatePositiveDuration(path.Child("timeout"), invocation.Timeout)...)
	switch transport {
	case powerv1alpha1.ShutdownHookTransportHTTP:
		if invocation.HTTP == nil {
			errs = append(errs, field.Required(path.Child("http"), "HTTP transport requires http"))
		} else {
			errs = append(errs, validateShutdownHookHTTP(path.Child("http"), *invocation.HTTP)...)
		}
		if invocation.KubernetesObject != nil {
			errs = append(errs, field.Forbidden(path.Child("kubernetesObject"), "cannot be set with HTTP transport"))
		}
	case powerv1alpha1.ShutdownHookTransportKubernetesObject:
		if invocation.KubernetesObject == nil {
			errs = append(errs, field.Required(path.Child("kubernetesObject"), "KubernetesObject transport requires kubernetesObject"))
		} else {
			errs = append(errs, validateShutdownHookKubernetesObject(path.Child("kubernetesObject"), *invocation.KubernetesObject)...)
		}
		if invocation.HTTP != nil {
			errs = append(errs, field.Forbidden(path.Child("http"), "cannot be set with KubernetesObject transport"))
		}
	case "":
		errs = append(errs, field.Required(path.Child("transport"), "required as HTTP or KubernetesObject"))
	default:
		errs = append(errs, field.NotSupported(path.Child("transport"), transport, []string{
			string(powerv1alpha1.ShutdownHookTransportHTTP),
			string(powerv1alpha1.ShutdownHookTransportKubernetesObject),
		}))
	}
	return errs
}

func validateShutdownHookHTTP(path *field.Path, spec powerv1alpha1.ShutdownHookHTTPSpec) field.ErrorList {
	var errs field.ErrorList
	parsed, parseErr := url.Parse(spec.URL)
	if spec.URL == "" {
		errs = append(errs, field.Required(path.Child("url"), "required as the hook URL"))
	} else if parseErr != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		errs = append(errs, field.Invalid(path.Child("url"), spec.URL, "must be an absolute http or https URL with a host"))
	} else if parsed.Scheme != "http" && parsed.Scheme != "https" {
		errs = append(errs, field.NotSupported(path.Child("url"), parsed.Scheme, []string{"http", "https"}))
	}
	if spec.Method != "" && spec.Method != http.MethodPost {
		errs = append(errs, field.NotSupported(path.Child("method"), spec.Method, []string{http.MethodPost}))
	}
	if containsControlCharacter(spec.CloudEventType) {
		errs = append(errs, field.Invalid(path.Child("cloudEventType"), spec.CloudEventType, "must not contain control characters"))
	}
	if containsControlCharacter(spec.CloudEventSource) {
		errs = append(errs, field.Invalid(path.Child("cloudEventSource"), spec.CloudEventSource, "must not contain control characters"))
	}
	errs = append(errs, validateShutdownHookHeaders(path, spec.Headers, spec.SecretHeaders)...)
	errs = append(errs, validateStringMap(path.Child("data"), spec.Data)...)
	return errs
}

func validateShutdownHookHeaders(path *field.Path, headers []powerv1alpha1.ShutdownHookHTTPHeader, secretHeaders []powerv1alpha1.ShutdownHookHTTPSecretHeader) field.ErrorList {
	var errs field.ErrorList
	seen := map[string]struct{}{}
	for i, header := range headers {
		headerPath := path.Child("headers").Index(i)
		normalized := strings.ToLower(header.Name)
		errs = append(errs, validateHTTPHeaderName(headerPath.Child("name"), header.Name)...)
		if _, exists := seen[normalized]; exists && normalized != "" {
			errs = append(errs, field.Duplicate(headerPath.Child("name"), header.Name))
		}
		seen[normalized] = struct{}{}
		if isSensitiveHTTPHeader(header.Name) {
			errs = append(errs, field.Forbidden(headerPath.Child("value"), "sensitive headers must use secretHeaders"))
		}
		if containsControlCharacter(header.Value) {
			errs = append(errs, field.Invalid(headerPath.Child("value"), header.Value, "must not contain control characters"))
		}
	}
	for i, header := range secretHeaders {
		headerPath := path.Child("secretHeaders").Index(i)
		normalized := strings.ToLower(header.Name)
		errs = append(errs, validateHTTPHeaderName(headerPath.Child("name"), header.Name)...)
		if _, exists := seen[normalized]; exists && normalized != "" {
			errs = append(errs, field.Duplicate(headerPath.Child("name"), header.Name))
		}
		seen[normalized] = struct{}{}
		errs = append(errs, validateSecretKeyReference(headerPath.Child("valueFrom"), header.ValueFrom)...)
	}
	return errs
}

func validateHTTPHeaderName(path *field.Path, name string) field.ErrorList {
	if name == "" {
		return field.ErrorList{field.Required(path, "required as the HTTP header name")}
	}
	if containsControlCharacter(name) {
		return field.ErrorList{field.Invalid(path, name, "must not contain control characters")}
	}
	if strings.TrimSpace(name) != name {
		return field.ErrorList{field.Invalid(path, name, "must not have leading or trailing whitespace")}
	}
	if !httpgutsValidHeaderFieldName(name) {
		return field.ErrorList{field.Invalid(path, name, "must be a valid HTTP header name")}
	}
	return nil
}

func httpgutsValidHeaderFieldName(name string) bool {
	for _, r := range name {
		if r <= 32 || r >= 127 {
			return false
		}
		if strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return false
		}
	}
	return true
}

func isSensitiveHTTPHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie", "proxy-authorization", "x-api-key":
		return true
	default:
		return false
	}
}

func validateShutdownHookKubernetesObject(path *field.Path, spec powerv1alpha1.ShutdownHookKubernetesObjectSpec) field.ErrorList {
	var errs field.ErrorList
	if len(spec.Object.Raw) == 0 {
		return field.ErrorList{field.Required(path.Child("object"), "required as the Kubernetes object body")}
	}
	var body map[string]any
	if err := json.Unmarshal(spec.Object.Raw, &body); err != nil {
		return field.ErrorList{field.Invalid(path.Child("object"), "<raw>", fmt.Sprintf("must be a JSON object: %v", err))}
	}
	apiVersion, _ := body["apiVersion"].(string)
	kind, _ := body["kind"].(string)
	errs = append(errs, validateIdentifierText(path.Child("object").Child("apiVersion"), apiVersion, "apiVersion is required")...)
	errs = append(errs, validateIdentifierText(path.Child("object").Child("kind"), kind, "kind is required")...)
	metadata, _ := body["metadata"].(map[string]any)
	if metadata == nil {
		errs = append(errs, field.Required(path.Child("object").Child("metadata"), "metadata is required"))
		return errs
	}
	name, _ := metadata["name"].(string)
	generateName, _ := metadata["generateName"].(string)
	if name == "" && generateName == "" {
		errs = append(errs, field.Required(path.Child("object").Child("metadata").Child("name"), "metadata.name or metadata.generateName is required"))
	}
	if name != "" {
		errs = append(errs, validateDNSSubdomain(path.Child("object").Child("metadata").Child("name"), name)...)
	}
	if generateName != "" && containsControlCharacter(generateName) {
		errs = append(errs, field.Invalid(path.Child("object").Child("metadata").Child("generateName"), generateName, "must not contain control characters"))
	}
	return errs
}
