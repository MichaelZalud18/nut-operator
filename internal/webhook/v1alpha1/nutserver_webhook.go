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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var nutserverlog = logf.Log.WithName("nutserver-resource")

// SetupNUTServerWebhookWithManager registers the webhook for NUTServer in the manager.
func SetupNUTServerWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.NUTServer{}).
		WithValidator(&NUTServerCustomValidator{}).
		WithDefaulter(&NUTServerCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-nutserver,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=nutservers,verbs=create;update,versions=v1alpha1,name=mnutserver-v1alpha1.kb.io,admissionReviewVersions=v1

// NUTServerCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind NUTServer when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type NUTServerCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind NUTServer.
func (d *NUTServerCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.NUTServer) error {
	nutserverlog.Info("Defaulting for NUTServer", "name", obj.GetName())

	defaultNUTServer(obj)

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-nutserver,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=nutservers,verbs=create;update,versions=v1alpha1,name=vnutserver-v1alpha1.kb.io,admissionReviewVersions=v1

// NUTServerCustomValidator struct is responsible for validating the NUTServer resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NUTServerCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type NUTServer.
func (v *NUTServerCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.NUTServer) (admission.Warnings, error) {
	nutserverlog.Info("Validation for NUTServer upon creation", "name", obj.GetName())

	return nil, validateNUTServerAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type NUTServer.
func (v *NUTServerCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.NUTServer) (admission.Warnings, error) {
	nutserverlog.Info("Validation for NUTServer upon update", "name", newObj.GetName())

	return nil, validateNUTServerAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type NUTServer.
func (v *NUTServerCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.NUTServer) (admission.Warnings, error) {
	nutserverlog.Info("Validation for NUTServer upon deletion", "name", obj.GetName())

	return nil, nil
}

func defaultNUTServer(obj *powerv1alpha1.NUTServer) {
	if obj.Spec.Replicas == nil {
		obj.Spec.Replicas = ptrInt32(1)
	}
	if obj.Spec.Service.Type == "" {
		obj.Spec.Service.Type = corev1.ServiceTypeClusterIP
	}
	if obj.Spec.Service.Port == nil {
		obj.Spec.Service.Port = ptrInt32(3493)
	}
	if obj.Spec.Auth.Mode == "" {
		obj.Spec.Auth.Mode = powerv1alpha1.NUTAuthOperatorManaged
	}
	if obj.Spec.Auth.PerNodeUsers == nil {
		obj.Spec.Auth.PerNodeUsers = ptrBool(true)
	}
	if obj.Spec.TLS.Mode == "" {
		obj.Spec.TLS.Mode = powerv1alpha1.NUTTLSRequired
	}
	if obj.Spec.TLS.VerifyClientCertificates == nil {
		obj.Spec.TLS.VerifyClientCertificates = ptrBool(true)
	}
	if obj.Spec.Config.ListenAddress == "" {
		obj.Spec.Config.ListenAddress = "0.0.0.0"
	}
}

func validateNUTServerAdmission(obj *powerv1alpha1.NUTServer) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	errs = append(errs, validateOptionalObjectNameReference(specPath.Child("managementClusterRef"), obj.Spec.ManagementClusterRef)...)
	errs = append(errs, validateOptionalNamespace(specPath.Child("namespace"), obj.Spec.Namespace)...)
	if obj.Spec.Replicas != nil && (*obj.Spec.Replicas < 1 || *obj.Spec.Replicas > 5) {
		errs = append(errs, field.Invalid(specPath.Child("replicas"), *obj.Spec.Replicas, "must be between 1 and 5"))
	}
	if len(obj.Spec.DeviceRefs) == 0 && obj.Spec.DeviceSelector == nil {
		errs = append(errs, field.Required(specPath.Child("deviceRefs"), "spec.deviceRefs or spec.deviceSelector is required"))
	}
	for i, ref := range obj.Spec.DeviceRefs {
		errs = append(errs, validateObjectNameReference(specPath.Child("deviceRefs").Index(i), ref)...)
	}
	errs = append(errs, validateNUTService(specPath.Child("service"), obj.Spec.Service)...)
	errs = append(errs, validateNUTAuth(specPath.Child("auth"), obj.Spec.Auth)...)
	errs = append(errs, validateNUTTLS(specPath.Child("tls"), obj.Spec.TLS)...)
	errs = append(errs, validateNUTServerConfig(specPath.Child("config"), obj.Spec.Config)...)

	return newInvalidAdmissionError("NUTServer", obj, errs)
}

func validateNUTService(path *field.Path, service powerv1alpha1.NUTServiceSpec) field.ErrorList {
	var errs field.ErrorList
	switch service.Type {
	case "", corev1.ServiceTypeClusterIP, corev1.ServiceTypeNodePort, corev1.ServiceTypeLoadBalancer:
	default:
		errs = append(errs, field.NotSupported(path.Child("type"), service.Type, []string{
			string(corev1.ServiceTypeClusterIP),
			string(corev1.ServiceTypeNodePort),
			string(corev1.ServiceTypeLoadBalancer),
		}))
	}
	if service.Port != nil && (*service.Port < 1 || *service.Port > 65535) {
		errs = append(errs, field.Invalid(path.Child("port"), *service.Port, "must be between 1 and 65535"))
	}
	for key, value := range service.Annotations {
		errs = append(errs, validateAnnotationKey(path.Child("annotations").Key(key), key)...)
		if containsControlCharacter(value) {
			errs = append(errs, field.Invalid(path.Child("annotations").Key(key), value, "must not contain control characters"))
		}
	}
	return errs
}

func validateNUTAuth(path *field.Path, auth powerv1alpha1.NUTAuthSpec) field.ErrorList {
	var errs field.ErrorList
	switch auth.Mode {
	case "", powerv1alpha1.NUTAuthOperatorManaged:
		if auth.ExistingSecretRef != nil {
			errs = append(errs, field.Forbidden(path.Child("existingSecretRef"), "cannot be set when auth mode is OperatorManaged"))
		}
	case powerv1alpha1.NUTAuthExistingSecret:
		if auth.ExistingSecretRef == nil {
			errs = append(errs, field.Required(path.Child("existingSecretRef"), "required when auth mode is ExistingSecret"))
		} else {
			errs = append(errs, validateNamespacedNameReference(path.Child("existingSecretRef"), *auth.ExistingSecretRef)...)
		}
	default:
		errs = append(errs, field.NotSupported(path.Child("mode"), auth.Mode, []string{
			string(powerv1alpha1.NUTAuthOperatorManaged),
			string(powerv1alpha1.NUTAuthExistingSecret),
		}))
	}
	return errs
}

func validateNUTTLS(path *field.Path, tls powerv1alpha1.NUTTLSSpec) field.ErrorList {
	var errs field.ErrorList
	switch tls.Mode {
	case "", powerv1alpha1.NUTTLSRequired:
		if tls.ServerCertificateRef == nil {
			errs = append(errs, field.Required(path.Child("serverCertificateRef"), "required when TLS mode is Required"))
		} else {
			errs = append(errs, validateNamespacedNameReference(path.Child("serverCertificateRef"), *tls.ServerCertificateRef)...)
		}
	case powerv1alpha1.NUTTLSOpportunistic:
		errs = append(errs, validateOptionalNamespacedNameReference(path.Child("serverCertificateRef"), tls.ServerCertificateRef)...)
	case powerv1alpha1.NUTTLSDisabled:
		if tls.ServerCertificateRef != nil {
			errs = append(errs, field.Forbidden(path.Child("serverCertificateRef"), "cannot be set when TLS mode is Disabled"))
		}
		if tls.ClientCARef != nil {
			errs = append(errs, field.Forbidden(path.Child("clientCARef"), "cannot be set when TLS mode is Disabled"))
		}
	default:
		errs = append(errs, field.NotSupported(path.Child("mode"), tls.Mode, []string{
			string(powerv1alpha1.NUTTLSDisabled),
			string(powerv1alpha1.NUTTLSOpportunistic),
			string(powerv1alpha1.NUTTLSRequired),
		}))
	}
	if tls.Mode != powerv1alpha1.NUTTLSDisabled && verifyClientCertificatesEnabled(tls) {
		if tls.ClientCARef == nil {
			errs = append(errs, field.Required(path.Child("clientCARef"), "required when verifyClientCertificates is true"))
		} else {
			errs = append(errs, validateNamespacedNameReference(path.Child("clientCARef"), *tls.ClientCARef)...)
		}
	}
	return errs
}

func verifyClientCertificatesEnabled(tls powerv1alpha1.NUTTLSSpec) bool {
	return tls.VerifyClientCertificates == nil || *tls.VerifyClientCertificates
}

func validateNUTServerConfig(path *field.Path, config powerv1alpha1.NUTServerConfigSpec) field.ErrorList {
	var errs field.ErrorList
	if containsControlCharacter(config.ListenAddress) {
		errs = append(errs, field.Invalid(path.Child("listenAddress"), config.ListenAddress, "must not contain control characters"))
	}
	errs = append(errs, validatePositiveDuration(path.Child("maxStartDelay"), config.MaxStartDelay)...)
	errs = append(errs, validatePositiveDuration(path.Child("pollInterval"), config.PollInterval)...)
	return errs
}
