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
	"fmt"
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
	if obj.Spec.ShutdownTiers.LabelKey == "" {
		obj.Spec.ShutdownTiers.LabelKey = powerv1alpha1.DefaultShutdownTierLabelKey
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
	if obj.Spec.Hooks.DefaultTimeout == nil {
		obj.Spec.Hooks.DefaultTimeout = &metav1.Duration{Duration: 10 * time.Second}
	}
}

func validatePowerManagementClusterAdmission(obj *powerv1alpha1.PowerManagementCluster) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if obj.Spec.OperandNamespace != nil {
		errs = append(errs, validateOptionalNamespace(specPath.Child("operandNamespace").Child("name"), obj.Spec.OperandNamespace.Name)...)
	}
	errs = append(errs, validatePowerStorage(specPath.Child("storage"), obj.Spec.Storage)...)
	errs = append(errs, validatePowerShutdownTiers(specPath.Child("shutdownTiers"), obj.Spec.ShutdownTiers)...)
	errs = append(errs, validatePowerSecurity(specPath.Child("security"), obj.Spec.Security)...)
	errs = append(errs, validatePowerHookPolicy(specPath.Child("hooks"), obj.Spec.Hooks)...)
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
	errs = append(errs, validateAuditSpool(path.Child("auditSpool"), storage.AuditSpool, storage.Mode)...)
	return errs
}

func validateAuditSpool(path *field.Path, spool powerv1alpha1.AuditSpoolSpec, mode powerv1alpha1.PowerStorageMode) field.ErrorList {
	if !spool.Enabled {
		return nil
	}
	if mode == powerv1alpha1.PowerStorageDisabled {
		return field.ErrorList{field.Invalid(path.Child("enabled"), spool.Enabled, "requires CNPG or ExternalPostgres storage")}
	}
	return validateAbsoluteFilePath(path.Child("path"), spool.Path, "required when audit spool is enabled")
}

func validatePowerShutdownTiers(path *field.Path, policy powerv1alpha1.PowerShutdownTierPolicySpec) field.ErrorList {
	var errs field.ErrorList
	labelKey := policy.LabelKey
	if labelKey == "" {
		labelKey = powerv1alpha1.DefaultShutdownTierLabelKey
	}
	errs = append(errs, validateAnnotationKey(path.Child("labelKey"), labelKey)...)
	if policy.DefaultTier != nil && *policy.DefaultTier < 2 {
		errs = append(errs, field.Invalid(path.Child("defaultTier"), *policy.DefaultTier, "must be 2 or greater; tiers 0 and 1 are reserved"))
	}

	seenTiers := map[int32]struct{}{}
	for i, tier := range policy.Tiers {
		tierPath := path.Child("tiers").Index(i)
		if tier.Tier < 0 {
			errs = append(errs, field.Invalid(tierPath.Child("tier"), tier.Tier, "must be zero or greater"))
		}
		if _, exists := seenTiers[tier.Tier]; exists {
			errs = append(errs, field.Duplicate(tierPath.Child("tier"), tier.Tier))
		}
		seenTiers[tier.Tier] = struct{}{}
		if containsControlCharacter(tier.Name) {
			errs = append(errs, field.Invalid(tierPath.Child("name"), tier.Name, "must not contain control characters"))
		}
		if containsControlCharacter(tier.Description) {
			errs = append(errs, field.Invalid(tierPath.Child("description"), tier.Description, "must not contain control characters"))
		}
	}

	seenRules := map[string]struct{}{}
	for i, rule := range policy.SelectorRules {
		rulePath := path.Child("selectorRules").Index(i)
		if rule.Name == "" {
			errs = append(errs, field.Required(rulePath.Child("name"), "required as the selector rule name"))
		} else if containsControlCharacter(rule.Name) {
			errs = append(errs, field.Invalid(rulePath.Child("name"), rule.Name, "must not contain control characters"))
		}
		if _, exists := seenRules[rule.Name]; rule.Name != "" && exists {
			errs = append(errs, field.Duplicate(rulePath.Child("name"), rule.Name))
		}
		seenRules[rule.Name] = struct{}{}
		switch rule.Subject {
		case "", powerv1alpha1.PowerShutdownTierSubjectAny, powerv1alpha1.PowerShutdownTierSubjectNamespace, powerv1alpha1.PowerShutdownTierSubjectWorkload, powerv1alpha1.PowerShutdownTierSubjectNode:
		default:
			errs = append(errs, field.NotSupported(rulePath.Child("subject"), rule.Subject, []string{
				string(powerv1alpha1.PowerShutdownTierSubjectAny),
				string(powerv1alpha1.PowerShutdownTierSubjectNamespace),
				string(powerv1alpha1.PowerShutdownTierSubjectWorkload),
				string(powerv1alpha1.PowerShutdownTierSubjectNode),
			}))
		}
		if rule.Tier < 0 {
			errs = append(errs, field.Invalid(rulePath.Child("tier"), rule.Tier, "must be zero or greater"))
		}
		if rule.Subject == powerv1alpha1.PowerShutdownTierSubjectNode && rule.Tier == 0 {
			errs = append(errs, field.Invalid(rulePath.Child("tier"), rule.Tier, "node selector rules cannot assign workload-only tier 0"))
		}
		if _, err := metav1.LabelSelectorAsSelector(&rule.Selector); err != nil {
			errs = append(errs, field.Invalid(rulePath.Child("selector"), rule.Selector, err.Error()))
		}
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

func validatePowerHookPolicy(path *field.Path, hooks powerv1alpha1.PowerHookPolicySpec) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, validatePositiveDuration(path.Child("defaultTimeout"), hooks.DefaultTimeout)...)
	seen := map[string]struct{}{}
	for i, endpoint := range hooks.AllowedEndpoints {
		endpointPath := path.Child("allowedEndpoints").Index(i)
		switch endpoint.Scheme {
		case "http", "https":
		case "":
			errs = append(errs, field.Required(endpointPath.Child("scheme"), "required as http or https"))
		default:
			errs = append(errs, field.NotSupported(endpointPath.Child("scheme"), endpoint.Scheme, []string{"http", "https"}))
		}
		errs = append(errs, validateIdentifierText(endpointPath.Child("host"), endpoint.Host, "required as the allowed endpoint host")...)
		if endpoint.Port != nil && (*endpoint.Port < 1 || *endpoint.Port > 65535) {
			errs = append(errs, field.Invalid(endpointPath.Child("port"), *endpoint.Port, "must be between 1 and 65535"))
		}
		if endpoint.PathPrefix != "" {
			if !strings.HasPrefix(endpoint.PathPrefix, "/") {
				errs = append(errs, field.Invalid(endpointPath.Child("pathPrefix"), endpoint.PathPrefix, "must start with /"))
			}
			if containsControlCharacter(endpoint.PathPrefix) {
				errs = append(errs, field.Invalid(endpointPath.Child("pathPrefix"), endpoint.PathPrefix, "must not contain control characters"))
			}
		}
		key := fmt.Sprintf("%s://%s:%d%s", endpoint.Scheme, endpoint.Host, optionalPort(endpoint.Port), endpoint.PathPrefix)
		if _, exists := seen[key]; exists {
			errs = append(errs, field.Duplicate(endpointPath, key))
		}
		seen[key] = struct{}{}
	}
	return errs
}

func optionalPort(port *int32) int32 {
	if port == nil {
		return 0
	}
	return *port
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
