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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	shutdownflowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
)

// nolint:unused
// log is for logging in this package.
var shutdownflowlog = logf.Log.WithName("shutdownflow-resource")

// SetupShutdownFlowWebhookWithManager registers the webhook for ShutdownFlow in the manager.
func SetupShutdownFlowWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.ShutdownFlow{}).
		WithValidator(&ShutdownFlowCustomValidator{}).
		WithDefaulter(&ShutdownFlowCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-shutdownflow,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=shutdownflows,verbs=create;update,versions=v1alpha1,name=mshutdownflow-v1alpha1.kb.io,admissionReviewVersions=v1

// ShutdownFlowCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind ShutdownFlow when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type ShutdownFlowCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind ShutdownFlow.
func (d *ShutdownFlowCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.ShutdownFlow) error {
	shutdownflowlog.Info("Defaulting for ShutdownFlow", "name", obj.GetName())

	defaultShutdownFlow(obj)

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-shutdownflow,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=shutdownflows,verbs=create;update,versions=v1alpha1,name=vshutdownflow-v1alpha1.kb.io,admissionReviewVersions=v1

// ShutdownFlowCustomValidator struct is responsible for validating the ShutdownFlow resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type ShutdownFlowCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ShutdownFlow.
func (v *ShutdownFlowCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.ShutdownFlow) (admission.Warnings, error) {
	shutdownflowlog.Info("Validation for ShutdownFlow upon creation", "name", obj.GetName())

	return validateShutdownFlowAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ShutdownFlow.
func (v *ShutdownFlowCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.ShutdownFlow) (admission.Warnings, error) {
	shutdownflowlog.Info("Validation for ShutdownFlow upon update", "name", newObj.GetName())

	return validateShutdownFlowAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ShutdownFlow.
func (v *ShutdownFlowCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.ShutdownFlow) (admission.Warnings, error) {
	shutdownflowlog.Info("Validation for ShutdownFlow upon deletion", "name", obj.GetName())

	return nil, nil
}

func defaultShutdownFlow(obj *powerv1alpha1.ShutdownFlow) {
	if obj.Spec.Mode == "" {
		obj.Spec.Mode = powerv1alpha1.ShutdownFlowModeDryRun
	}
	if obj.Spec.ConcurrencyPolicy == "" {
		obj.Spec.ConcurrencyPolicy = "Forbid"
	}
	if obj.Spec.AbortPolicy.Behavior == "" {
		obj.Spec.AbortPolicy.Behavior = powerv1alpha1.AbortBehaviorHaltAndSurface
	}
	if obj.Spec.AbortPolicy.Notify == nil {
		obj.Spec.AbortPolicy.Notify = ptrBool(true)
	}
	if obj.Spec.Safety.RequireManualApproval == nil {
		obj.Spec.Safety.RequireManualApproval = ptrBool(true)
	}
	for i := range obj.Spec.Steps {
		if obj.Spec.Steps[i].ContinueOnError == nil {
			obj.Spec.Steps[i].ContinueOnError = ptrBool(false)
		}
	}
}

func validateShutdownFlowAdmission(obj *powerv1alpha1.ShutdownFlow) (admission.Warnings, error) {
	var errs field.ErrorList
	var warnings admission.Warnings
	specPath := field.NewPath("spec")

	errs = append(errs, validateOptionalObjectNameReference(specPath.Child("managementClusterRef"), obj.Spec.ManagementClusterRef)...)
	errs = append(errs, validateShutdownFlowMode(specPath.Child("mode"), obj.Spec.Mode)...)
	errs = append(errs, validateShutdownTriggers(specPath.Child("triggers"), obj.Spec.Triggers)...)
	errs = append(errs, validateShutdownGroups(specPath.Child("groups"), obj.Spec.Groups)...)
	errs = append(errs, validateShutdownSteps(specPath.Child("steps"), obj.Spec.Steps)...)
	errs = append(errs, validateShutdownConcurrencyPolicy(specPath.Child("concurrencyPolicy"), obj.Spec.ConcurrencyPolicy)...)
	errs = append(errs, validateAbortPolicy(specPath.Child("abortPolicy"), obj.Spec.AbortPolicy)...)
	errs = append(errs, validateFlowSafety(specPath.Child("safety"), obj)...)

	plan, diagnostics, err := planner.Compile(shutdownflowadapter.PlannerInputs(obj), planner.TelemetryInputs{})
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == planner.DiagnosticWarning {
			warnings = append(warnings, diagnostic.Message)
		}
		if diagnostic.Severity == planner.DiagnosticError {
			errs = append(errs, field.Invalid(specPath, obj.Spec, diagnostic.Message))
		}
	}
	if err != nil && !errors.Is(err, planner.ErrRejected) {
		errs = append(errs, field.InternalError(specPath, err))
	}
	if obj.Spec.Safety.MaxEstimatedDuration != nil && err == nil && plan.EstimatedDuration.Duration > obj.Spec.Safety.MaxEstimatedDuration.Duration {
		errs = append(errs, field.Invalid(specPath.Child("safety").Child("maxEstimatedDuration"), obj.Spec.Safety.MaxEstimatedDuration.Duration.String(), "compiled plan estimated duration exceeds this budget"))
	}

	return warnings, newInvalidAdmissionError("ShutdownFlow", obj, errs)
}

func validateShutdownFlowMode(path *field.Path, mode powerv1alpha1.ShutdownFlowMode) field.ErrorList {
	switch mode {
	case "", powerv1alpha1.ShutdownFlowModeDryRun, powerv1alpha1.ShutdownFlowModeEnforce:
		return nil
	default:
		return field.ErrorList{field.NotSupported(path, mode, []string{
			string(powerv1alpha1.ShutdownFlowModeDryRun),
			string(powerv1alpha1.ShutdownFlowModeEnforce),
		})}
	}
}

// validateTriggerFallbackType rejects a declared OD-9 fallback that is not a
// strictly coarser class for the trigger it is attached to.
//
// Caught here rather than only at compile time because the mistake is a static
// property of the spec: RuntimeBelow can only ever fall back to LowBattery, and
// no cluster state changes that. Admission also happens to be the only layer that
// sees it before it is stored -- a rejected compile leaves the flow in the
// cluster looking configured.
func validateTriggerFallbackType(path *field.Path, trigger powerv1alpha1.ShutdownTrigger) field.ErrorList {
	if trigger.FallbackType == nil {
		return nil
	}
	declared := *trigger.FallbackType
	if declared == "" {
		return field.ErrorList{field.Required(path, "must name a trigger type when set")}
	}
	if declared == trigger.Type {
		return field.ErrorList{field.Invalid(path, declared,
			"must differ from the trigger's own type; a fallback to the same class covers nothing")}
	}

	coarser, ok := capability.CoarserTriggerForFallback(capability.TriggerType(trigger.Type))
	if !ok {
		return field.ErrorList{field.Invalid(path, declared, fmt.Sprintf(
			"%q already needs only the telemetry every device reports, so it has no coarser fallback",
			trigger.Type))}
	}
	if declared != powerv1alpha1.ShutdownTriggerType(coarser) {
		return field.ErrorList{field.NotSupported(path, declared,
			[]string{string(coarser)})}
	}
	return nil
}

func validateShutdownTriggers(path *field.Path, triggers []powerv1alpha1.ShutdownTrigger) field.ErrorList {
	var errs field.ErrorList
	if len(triggers) == 0 {
		errs = append(errs, field.Required(path, "requires at least one trigger"))
	}
	for i, trigger := range triggers {
		triggerPath := path.Index(i)
		switch trigger.Type {
		case powerv1alpha1.ShutdownTriggerOnBattery, powerv1alpha1.ShutdownTriggerLowBattery, powerv1alpha1.ShutdownTriggerTelemetryStale:
		case powerv1alpha1.ShutdownTriggerRuntimeBelow:
			if trigger.RuntimeBelowSeconds == nil {
				errs = append(errs, field.Required(triggerPath.Child("runtimeBelowSeconds"), "required for RuntimeBelow triggers"))
			}
		case powerv1alpha1.ShutdownTriggerChargeBelow:
			if trigger.ChargeBelowPercent == nil {
				errs = append(errs, field.Required(triggerPath.Child("chargeBelowPercent"), "required for ChargeBelow triggers"))
			}
		case "":
			errs = append(errs, field.Required(triggerPath.Child("type"), "required as a supported trigger type"))
		default:
			errs = append(errs, field.NotSupported(triggerPath.Child("type"), trigger.Type, supportedShutdownTriggerTypes()))
		}
		if trigger.RuntimeBelowSeconds != nil && *trigger.RuntimeBelowSeconds < 0 {
			errs = append(errs, field.Invalid(triggerPath.Child("runtimeBelowSeconds"), *trigger.RuntimeBelowSeconds, "must be greater than or equal to zero"))
		}
		if trigger.ChargeBelowPercent != nil && (*trigger.ChargeBelowPercent < 0 || *trigger.ChargeBelowPercent > 100) {
			errs = append(errs, field.Invalid(triggerPath.Child("chargeBelowPercent"), *trigger.ChargeBelowPercent, "must be between 0 and 100"))
		}
		errs = append(errs, validateTriggerFallbackType(triggerPath.Child("fallbackType"), trigger)...)
		errs = append(errs, validatePositiveDuration(triggerPath.Child("for"), trigger.For)...)
		for j, ref := range trigger.UPSDeviceRefs {
			errs = append(errs, validateObjectNameReference(triggerPath.Child("upsDeviceRefs").Index(j), ref)...)
		}
		for j, domain := range trigger.PowerDomains {
			errs = append(errs, validateIdentifierText(triggerPath.Child("powerDomains").Index(j), domain, "power domain must not be empty")...)
		}
	}
	return errs
}

func validateShutdownGroups(path *field.Path, groups []powerv1alpha1.ShutdownGroup) field.ErrorList {
	var errs field.ErrorList
	for i, group := range groups {
		groupPath := path.Index(i)
		errs = append(errs, validateIdentifierText(groupPath.Child("name"), group.Name, "group name must not be empty")...)
		errs = append(errs, validateShutdownStepType(groupPath.Child("action"), group.Action)...)
		errs = append(errs, validateShutdownTarget(groupPath.Child("target"), group.Target)...)
		errs = append(errs, validatePositiveDuration(groupPath.Child("timeout"), group.Timeout)...)
		for j, dependency := range group.Requires {
			errs = append(errs, validateIdentifierText(groupPath.Child("requires").Index(j), dependency, "dependency name must not be empty")...)
		}
		for j, dependency := range group.Before {
			errs = append(errs, validateIdentifierText(groupPath.Child("before").Index(j), dependency, "dependency name must not be empty")...)
		}
		for j, dependency := range group.After {
			errs = append(errs, validateIdentifierText(groupPath.Child("after").Index(j), dependency, "dependency name must not be empty")...)
		}
		errs = append(errs, validateStringMap(groupPath.Child("params"), group.Params)...)
	}
	return errs
}

func validateShutdownSteps(path *field.Path, steps []powerv1alpha1.ShutdownStep) field.ErrorList {
	var errs field.ErrorList
	for i, step := range steps {
		stepPath := path.Index(i)
		errs = append(errs, validateIdentifierText(stepPath.Child("id"), step.ID, "step id must not be empty")...)
		errs = append(errs, validateShutdownStepType(stepPath.Child("type"), step.Type)...)
		errs = append(errs, validateShutdownTarget(stepPath.Child("target"), step.Target)...)
		errs = append(errs, validatePositiveDuration(stepPath.Child("duration"), step.Duration)...)
		errs = append(errs, validatePositiveDuration(stepPath.Child("timeout"), step.Timeout)...)
		errs = append(errs, validateStringMap(stepPath.Child("params"), step.Params)...)
	}
	return errs
}

func validateShutdownTarget(path *field.Path, target powerv1alpha1.ShutdownStepTarget) field.ErrorList {
	var errs field.ErrorList
	for i, namespace := range target.Namespaces {
		errs = append(errs, validateDNSLabel(path.Child("namespaces").Index(i), namespace)...)
	}
	for i, ref := range target.WorkloadRefs {
		refPath := path.Child("workloadRefs").Index(i)
		errs = append(errs, validateIdentifierText(refPath.Child("apiVersion"), ref.APIVersion, "apiVersion is required")...)
		errs = append(errs, validateIdentifierText(refPath.Child("kind"), ref.Kind, "kind is required")...)
		errs = append(errs, validateDNSLabel(refPath.Child("namespace"), ref.Namespace)...)
		if ref.Name == "" {
			errs = append(errs, field.Required(refPath.Child("name"), "workload name is required"))
		} else {
			errs = append(errs, validateDNSSubdomain(refPath.Child("name"), ref.Name)...)
		}
	}
	for i, ref := range target.AgentRefs {
		errs = append(errs, validateObjectNameReference(path.Child("agentRefs").Index(i), ref)...)
	}
	return errs
}

func validateShutdownStepType(path *field.Path, stepType powerv1alpha1.ShutdownStepType) field.ErrorList {
	switch stepType {
	case powerv1alpha1.ShutdownStepNotify,
		powerv1alpha1.ShutdownStepWait,
		powerv1alpha1.ShutdownStepCordonNodes,
		powerv1alpha1.ShutdownStepDrainNodes,
		powerv1alpha1.ShutdownStepScaleWorkload,
		powerv1alpha1.ShutdownStepRunWorkflow,
		powerv1alpha1.ShutdownStepAgentShutdown:
		return nil
	case "":
		return field.ErrorList{field.Required(path, "required as a supported shutdown action")}
	default:
		return field.ErrorList{field.NotSupported(path, stepType, supportedShutdownStepTypes())}
	}
}

func validateShutdownConcurrencyPolicy(path *field.Path, policy string) field.ErrorList {
	switch policy {
	case "", "Forbid", "Replace":
		return nil
	default:
		return field.ErrorList{field.NotSupported(path, policy, []string{"Forbid", "Replace"})}
	}
}

func validateAbortPolicy(path *field.Path, policy powerv1alpha1.AbortPolicySpec) field.ErrorList {
	var errs field.ErrorList
	switch policy.Behavior {
	case "", powerv1alpha1.AbortBehaviorHaltAndSurface, powerv1alpha1.AbortBehaviorContinueSafeSteps:
	default:
		errs = append(errs, field.NotSupported(path.Child("behavior"), policy.Behavior, []string{
			string(powerv1alpha1.AbortBehaviorHaltAndSurface),
			string(powerv1alpha1.AbortBehaviorContinueSafeSteps),
		}))
	}
	return errs
}

func validateFlowSafety(path *field.Path, obj *powerv1alpha1.ShutdownFlow) field.ErrorList {
	var errs field.ErrorList
	safety := obj.Spec.Safety
	errs = append(errs, validateAnnotationKey(path.Child("approvalAnnotation"), safety.ApprovalAnnotation)...)
	errs = append(errs, validatePositiveDuration(path.Child("maxEstimatedDuration"), safety.MaxEstimatedDuration)...)
	if obj.Spec.Mode == powerv1alpha1.ShutdownFlowModeEnforce {
		if safety.RequireManualApproval != nil && !*safety.RequireManualApproval {
			errs = append(errs, field.Invalid(path.Child("requireManualApproval"), *safety.RequireManualApproval, "Enforce mode requires manual approval"))
		}
		if safety.ApprovalAnnotation == "" {
			errs = append(errs, field.Required(path.Child("approvalAnnotation"), "required when mode is Enforce"))
		} else if obj.Annotations[safety.ApprovalAnnotation] != "true" {
			errs = append(errs, field.Invalid(field.NewPath("metadata").Child("annotations").Key(safety.ApprovalAnnotation), obj.Annotations[safety.ApprovalAnnotation], "must be set to \"true\" when mode is Enforce"))
		}
	}
	return errs
}

func validateStringMap(path *field.Path, values map[string]string) field.ErrorList {
	var errs field.ErrorList
	for key, value := range values {
		if key == "" {
			errs = append(errs, field.Required(path.Key(key), "map keys must not be empty"))
		} else if containsControlCharacter(key) {
			errs = append(errs, field.Invalid(path.Key(key), key, "map keys must not contain control characters"))
		}
		if containsControlCharacter(value) {
			errs = append(errs, field.Invalid(path.Key(key), value, "map values must not contain control characters"))
		}
	}
	return errs
}

func supportedShutdownTriggerTypes() []string {
	return []string{
		string(powerv1alpha1.ShutdownTriggerOnBattery),
		string(powerv1alpha1.ShutdownTriggerLowBattery),
		string(powerv1alpha1.ShutdownTriggerRuntimeBelow),
		string(powerv1alpha1.ShutdownTriggerChargeBelow),
		string(powerv1alpha1.ShutdownTriggerTelemetryStale),
	}
}

func supportedShutdownStepTypes() []string {
	return []string{
		string(powerv1alpha1.ShutdownStepNotify),
		string(powerv1alpha1.ShutdownStepWait),
		string(powerv1alpha1.ShutdownStepCordonNodes),
		string(powerv1alpha1.ShutdownStepDrainNodes),
		string(powerv1alpha1.ShutdownStepScaleWorkload),
		string(powerv1alpha1.ShutdownStepRunWorkflow),
		string(powerv1alpha1.ShutdownStepAgentShutdown),
	}
}
