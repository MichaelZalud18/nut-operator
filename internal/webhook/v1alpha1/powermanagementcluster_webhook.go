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
var powermanagementclusterlog = logf.Log.WithName("powermanagementcluster-resource")

// SetupPowerManagementClusterWebhookWithManager registers the webhook for PowerManagementCluster in the manager.
func SetupPowerManagementClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.PowerManagementCluster{}).
		WithValidator(&PowerManagementClusterCustomValidator{}).
		WithDefaulter(&PowerManagementClusterCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-powermanagementcluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powermanagementclusters,verbs=create;update,versions=v1alpha1,name=mpowermanagementcluster-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerManagementClusterCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind PowerManagementCluster when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type PowerManagementClusterCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind PowerManagementCluster.
func (d *PowerManagementClusterCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.PowerManagementCluster) error {
	powermanagementclusterlog.Info("Defaulting for PowerManagementCluster", "name", obj.GetName())

	defaultPowerManagementCluster(obj)

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-powermanagementcluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=powermanagementclusters,verbs=create;update,versions=v1alpha1,name=vpowermanagementcluster-v1alpha1.kb.io,admissionReviewVersions=v1

// PowerManagementClusterCustomValidator struct is responsible for validating the PowerManagementCluster resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PowerManagementClusterCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type PowerManagementCluster.
func (v *PowerManagementClusterCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.PowerManagementCluster) (admission.Warnings, error) {
	powermanagementclusterlog.Info("Validation for PowerManagementCluster upon creation", "name", obj.GetName())

	return powerManagementClusterAdmissionWarnings(obj), validatePowerManagementClusterAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type PowerManagementCluster.
func (v *PowerManagementClusterCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.PowerManagementCluster) (admission.Warnings, error) {
	powermanagementclusterlog.Info("Validation for PowerManagementCluster upon update", "name", newObj.GetName())

	return powerManagementClusterAdmissionWarnings(newObj), validatePowerManagementClusterAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type PowerManagementCluster.
func (v *PowerManagementClusterCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.PowerManagementCluster) (admission.Warnings, error) {
	powermanagementclusterlog.Info("Validation for PowerManagementCluster upon deletion", "name", obj.GetName())

	return nil, nil
}

func defaultPowerManagementCluster(obj *powerv1alpha1.PowerManagementCluster) {
	if obj.Spec.OperandNamespace == nil {
		obj.Spec.OperandNamespace = &powerv1alpha1.OperandNamespaceSpec{
			Name:   "power-system",
			Create: ptrBool(true),
		}
	} else {
		if obj.Spec.OperandNamespace.Name == "" {
			obj.Spec.OperandNamespace.Name = "power-system"
		}
		if obj.Spec.OperandNamespace.Create == nil {
			obj.Spec.OperandNamespace.Create = ptrBool(true)
		}
	}
	if obj.Spec.Storage.Mode == "" {
		obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageCNPG
	}
	if obj.Spec.Storage.CNPG != nil {
		if obj.Spec.Storage.CNPG.Database == "" {
			obj.Spec.Storage.CNPG.Database = "power"
		}
		if obj.Spec.Storage.CNPG.Schema == "" {
			obj.Spec.Storage.CNPG.Schema = "power"
		}
	}
	if obj.Spec.Storage.ExternalPostgres != nil {
		if obj.Spec.Storage.ExternalPostgres.Schema == "" {
			obj.Spec.Storage.ExternalPostgres.Schema = "power"
		}
		if obj.Spec.Storage.ExternalPostgres.RequireTLS == nil {
			obj.Spec.Storage.ExternalPostgres.RequireTLS = ptrBool(true)
		}
	}
	if obj.Spec.Security.Profile == "" {
		obj.Spec.Security.Profile = powerv1alpha1.PowerSecurityRestricted
	}
	defaultPodHardening(&obj.Spec.Security.DefaultPodHardening)
	if obj.Spec.Security.RequireExplicitActuation == nil {
		obj.Spec.Security.RequireExplicitActuation = ptrBool(true)
	}
	if obj.Spec.Observability.ServiceMonitor == nil {
		obj.Spec.Observability.ServiceMonitor = ptrBool(true)
	}
	if obj.Spec.Observability.KubernetesEvents == nil {
		obj.Spec.Observability.KubernetesEvents = ptrBool(true)
	}
}

func validatePowerManagementClusterAdmission(obj *powerv1alpha1.PowerManagementCluster) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if obj.Spec.OperandNamespace != nil {
		errs = append(errs, validateOptionalNamespace(specPath.Child("operandNamespace").Child("name"), obj.Spec.OperandNamespace.Name)...)
	}
	errs = append(errs, validatePowerStorage(specPath.Child("storage"), obj.Spec.Storage)...)
	errs = append(errs, validatePowerSecurity(specPath.Child("security"), obj.Spec.Security)...)
	errs = append(errs, validatePowerObservability(specPath.Child("observability"), obj.Spec.Observability)...)

	return newInvalidAdmissionError("PowerManagementCluster", obj, errs)
}

func validatePowerStorage(path *field.Path, storage powerv1alpha1.PowerStorageSpec) field.ErrorList {
	var errs field.ErrorList
	switch storage.Mode {
	case "", powerv1alpha1.PowerStorageCNPG:
		if storage.CNPG == nil {
			errs = append(errs, field.Required(path.Child("cnpg"), "CNPG storage mode requires a CloudNativePG cluster reference"))
		} else {
			errs = append(errs, validateNamespacedNameReference(path.Child("cnpg").Child("clusterRef"), storage.CNPG.ClusterRef)...)
			errs = append(errs, validateIdentifierText(path.Child("cnpg").Child("database"), storage.CNPG.Database, "required as the PostgreSQL database name")...)
			errs = append(errs, validateIdentifierText(path.Child("cnpg").Child("schema"), storage.CNPG.Schema, "required as the PostgreSQL schema name")...)
			errs = append(errs, validateOptionalNamespacedNameReference(path.Child("cnpg").Child("appCredentialSecretRef"), storage.CNPG.AppCredentialSecretRef)...)
		}
		if storage.ExternalPostgres != nil {
			errs = append(errs, field.Forbidden(path.Child("externalPostgres"), "cannot be set when storage mode is CNPG"))
		}
	case powerv1alpha1.PowerStorageExternalPostgres:
		if storage.ExternalPostgres == nil {
			errs = append(errs, field.Required(path.Child("externalPostgres"), "ExternalPostgres storage mode requires a DSN Secret reference"))
		} else {
			errs = append(errs, validateSecretKeyReference(path.Child("externalPostgres").Child("dsnSecretKeyRef"), storage.ExternalPostgres.DSNSecretKeyRef)...)
			errs = append(errs, validateIdentifierText(path.Child("externalPostgres").Child("schema"), storage.ExternalPostgres.Schema, "required as the PostgreSQL schema name")...)
		}
		if storage.CNPG != nil {
			errs = append(errs, field.Forbidden(path.Child("cnpg"), "cannot be set when storage mode is ExternalPostgres"))
		}
	case powerv1alpha1.PowerStorageDisabled:
		if storage.CNPG != nil {
			errs = append(errs, field.Forbidden(path.Child("cnpg"), "cannot be set when storage mode is Disabled"))
		}
		if storage.ExternalPostgres != nil {
			errs = append(errs, field.Forbidden(path.Child("externalPostgres"), "cannot be set when storage mode is Disabled"))
		}
	default:
		errs = append(errs, field.NotSupported(path.Child("mode"), storage.Mode, []string{
			string(powerv1alpha1.PowerStorageDisabled),
			string(powerv1alpha1.PowerStorageExternalPostgres),
			string(powerv1alpha1.PowerStorageCNPG),
		}))
	}
	if storage.Retention != nil {
		errs = append(errs, validatePositiveDuration(path.Child("retention").Child("events"), storage.Retention.Events)...)
		errs = append(errs, validatePositiveDuration(path.Child("retention").Child("telemetry"), storage.Retention.Telemetry)...)
	}
	return errs
}

func validatePowerSecurity(path *field.Path, security powerv1alpha1.PowerSecuritySpec) field.ErrorList {
	var errs field.ErrorList
	switch security.Profile {
	case "", powerv1alpha1.PowerSecurityRestricted, powerv1alpha1.PowerSecurityHostActuatorIsolated:
	default:
		errs = append(errs, field.NotSupported(path.Child("profile"), security.Profile, []string{
			string(powerv1alpha1.PowerSecurityRestricted),
			string(powerv1alpha1.PowerSecurityHostActuatorIsolated),
		}))
	}
	errs = append(errs, validatePodHardening(path.Child("defaultPodHardening"), security.DefaultPodHardening)...)
	for i, namespace := range security.AllowedActuatorNamespaces {
		errs = append(errs, validateDNSLabel(path.Child("allowedActuatorNamespaces").Index(i), namespace)...)
	}
	return errs
}

func validatePowerObservability(path *field.Path, observability powerv1alpha1.PowerObservabilitySpec) field.ErrorList {
	return validatePositiveDuration(path.Child("telemetryInterval"), observability.TelemetryInterval)
}

func powerManagementClusterAdmissionWarnings(obj *powerv1alpha1.PowerManagementCluster) admission.Warnings {
	if obj.Spec.Storage.Mode == powerv1alpha1.PowerStorageDisabled {
		return admission.Warnings{"storage mode Disabled is intended only for local development and tests"}
	}
	return nil
}

func defaultPodHardening(hardening *powerv1alpha1.PodHardeningSpec) {
	if hardening.ReadOnlyRootFilesystem == nil {
		hardening.ReadOnlyRootFilesystem = ptrBool(true)
	}
	if hardening.SeccompProfileType == "" {
		hardening.SeccompProfileType = "RuntimeDefault"
	}
	if hardening.RunAsNonRoot == nil {
		hardening.RunAsNonRoot = ptrBool(true)
	}
}
