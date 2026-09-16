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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/nodeselector"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	shutdownflowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
)

func ValidateShutdownFlowFields(obj *powerv1alpha1.ShutdownFlow) ([]string, field.ErrorList) {
	var errs field.ErrorList
	var warnings []string
	specPath := field.NewPath("spec")

	errs = append(errs, ValidateOptionalObjectNameReference(specPath.Child("managementClusterRef"), obj.Spec.ManagementClusterRef)...)
	errs = append(errs, ValidateShutdownFlowMode(specPath.Child("mode"), obj.Spec.Mode)...)
	errs = append(errs, ValidateShutdownTriggers(specPath.Child("triggers"), obj.Spec.Triggers)...)
	errs = append(errs, ValidateShutdownGroups(specPath.Child("groups"), obj.Spec.Groups)...)
	errs = append(errs, ValidateShutdownSteps(specPath.Child("steps"), obj.Spec.Steps)...)
	errs = append(errs, ValidateShutdownHookPolicy(specPath, obj)...)
	errs = append(errs, ValidateShutdownConcurrencyPolicy(specPath.Child("concurrencyPolicy"), obj.Spec.ConcurrencyPolicy)...)
	errs = append(errs, ValidateShutdownTierOverrunPolicy(specPath.Child("tierOverrunPolicy"), obj.Spec.TierOverrunPolicy)...)
	errs = append(errs, ValidateAbortPolicy(specPath.Child("abortPolicy"), obj.Spec.AbortPolicy)...)
	errs = append(errs, ValidateFlowSafety(specPath.Child("safety"), obj)...)

	inputs, err := shutdownflowadapter.PlannerInputs(obj)
	if err != nil {
		errs = append(errs, field.InternalError(specPath, err))
		return warnings, errs
	}
	plan, diagnostics, err := planner.Compile(inputs, planner.TelemetryInputs{})
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == planner.DiagnosticWarning {
			warnings = append(warnings, diagnostic.Message)
		}
		if diagnostic.Severity == planner.DiagnosticError {
			errs = append(errs, field.Invalid(specPath, obj.Spec, diagnostic.Message).WithOrigin(diagnostic.Reason))
		}
	}
	if err != nil && !errors.Is(err, planner.ErrRejected) {
		errs = append(errs, field.InternalError(specPath, err))
	}
	if obj.Spec.Safety.MaxEstimatedDuration != nil && err == nil && plan.EstimatedDuration.Duration > obj.Spec.Safety.MaxEstimatedDuration.Duration {
		errs = append(errs, field.Invalid(specPath.Child("safety").Child("maxEstimatedDuration"), obj.Spec.Safety.MaxEstimatedDuration.Duration.String(), "compiled plan estimated duration exceeds this budget"))
	}

	return warnings, errs
}

func ValidateShutdownFlowMode(path *field.Path, mode powerv1alpha1.ShutdownFlowMode) field.ErrorList {
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
func ValidateTriggerFallbackType(path *field.Path, trigger powerv1alpha1.ShutdownTrigger) field.ErrorList {
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

func ValidateShutdownTriggers(path *field.Path, triggers []powerv1alpha1.ShutdownTrigger) field.ErrorList {
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
			errs = append(errs, field.NotSupported(triggerPath.Child("type"), trigger.Type, SupportedShutdownTriggerTypes()))
		}
		if trigger.RuntimeBelowSeconds != nil && *trigger.RuntimeBelowSeconds < 0 {
			errs = append(errs, field.Invalid(triggerPath.Child("runtimeBelowSeconds"), *trigger.RuntimeBelowSeconds, "must be greater than or equal to zero"))
		}
		if trigger.ChargeBelowPercent != nil && (*trigger.ChargeBelowPercent < 0 || *trigger.ChargeBelowPercent > 100) {
			errs = append(errs, field.Invalid(triggerPath.Child("chargeBelowPercent"), *trigger.ChargeBelowPercent, "must be between 0 and 100"))
		}
		errs = append(errs, ValidateTriggerFallbackType(triggerPath.Child("fallbackType"), trigger)...)
		errs = append(errs, ValidatePositiveDuration(triggerPath.Child("for"), trigger.For)...)
		for j, ref := range trigger.UPSDeviceRefs {
			errs = append(errs, ValidateObjectNameReference(triggerPath.Child("upsDeviceRefs").Index(j), ref)...)
		}
		for j, domain := range trigger.PowerDomains {
			errs = append(errs, ValidateIdentifierText(triggerPath.Child("powerDomains").Index(j), domain, "power domain must not be empty")...)
		}
	}
	return errs
}

func ValidateShutdownGroups(path *field.Path, groups []powerv1alpha1.ShutdownGroup) field.ErrorList {
	var errs field.ErrorList
	for i, group := range groups {
		groupPath := path.Index(i)
		errs = append(errs, ValidateIdentifierText(groupPath.Child("name"), group.Name, "group name must not be empty")...)
		errs = append(errs, ValidateShutdownStepType(groupPath.Child("action"), group.Action)...)
		errs = append(errs, ValidateShutdownTarget(groupPath.Child("target"), group.Target)...)
		errs = append(errs, ValidateRunHookReference(groupPath.Child("hookRef"), group.Action, group.HookRef)...)
		errs = append(errs, ValidatePositiveDuration(groupPath.Child("timeout"), group.Timeout)...)
		for j, dependency := range group.Requires {
			errs = append(errs, ValidateIdentifierText(groupPath.Child("requires").Index(j), dependency, "dependency name must not be empty")...)
		}
		for j, dependency := range group.Before {
			errs = append(errs, ValidateIdentifierText(groupPath.Child("before").Index(j), dependency, "dependency name must not be empty")...)
		}
		for j, dependency := range group.After {
			errs = append(errs, ValidateIdentifierText(groupPath.Child("after").Index(j), dependency, "dependency name must not be empty")...)
		}
		errs = append(errs, ValidateStringMap(groupPath.Child("params"), group.Params)...)
		errs = append(errs, ValidateRemovedWorkflowParams(groupPath.Child("params"), group.Action, group.Params)...)
	}
	return errs
}

func ValidateShutdownSteps(path *field.Path, steps []powerv1alpha1.ShutdownStep) field.ErrorList {
	var errs field.ErrorList
	for i, step := range steps {
		stepPath := path.Index(i)
		errs = append(errs, ValidateIdentifierText(stepPath.Child("id"), step.ID, "step id must not be empty")...)
		errs = append(errs, ValidateShutdownStepType(stepPath.Child("type"), step.Type)...)
		errs = append(errs, ValidateShutdownTarget(stepPath.Child("target"), step.Target)...)
		errs = append(errs, ValidateRunHookReference(stepPath.Child("hookRef"), step.Type, step.HookRef)...)
		errs = append(errs, ValidatePositiveDuration(stepPath.Child("duration"), step.Duration)...)
		errs = append(errs, ValidatePositiveDuration(stepPath.Child("timeout"), step.Timeout)...)
		errs = append(errs, ValidateStringMap(stepPath.Child("params"), step.Params)...)
		errs = append(errs, ValidateRemovedWorkflowParams(stepPath.Child("params"), step.Type, step.Params)...)
	}
	return errs
}

func ValidateShutdownHookPolicy(path *field.Path, obj *powerv1alpha1.ShutdownFlow) field.ErrorList {
	if !ShutdownFlowUsesRunHook(obj) {
		return nil
	}
	if obj.Spec.ManagementClusterRef == nil || obj.Spec.ManagementClusterRef.Name == "" {
		return field.ErrorList{field.Required(path.Child("managementClusterRef"), "RunHook requires a PowerManagementCluster for outbound endpoint policy")}
	}
	return nil
}

func ShutdownFlowUsesRunHook(obj *powerv1alpha1.ShutdownFlow) bool {
	for _, group := range obj.Spec.Groups {
		if group.Action == powerv1alpha1.ShutdownStepRunHook {
			return true
		}
	}
	for _, step := range obj.Spec.Steps {
		if step.Type == powerv1alpha1.ShutdownStepRunHook {
			return true
		}
	}
	return false
}

func ValidateRunHookReference(path *field.Path, action powerv1alpha1.ShutdownStepType, ref *powerv1alpha1.NamespacedNameReference) field.ErrorList {
	if action != powerv1alpha1.ShutdownStepRunHook {
		if ref != nil {
			return field.ErrorList{field.Forbidden(path, "hookRef is only valid with action RunHook")}
		}
		return nil
	}
	if ref == nil {
		return field.ErrorList{field.Required(path, "RunHook requires a ShutdownHook reference")}
	}
	return ValidateNamespacedNameReference(path, *ref)
}

func ValidateRemovedWorkflowParams(path *field.Path, action powerv1alpha1.ShutdownStepType, params map[string]string) field.ErrorList {
	var errs field.ErrorList
	for _, key := range slices.Sorted(maps.Keys(params)) {
		if strings.HasPrefix(key, "workflow.") {
			errs = append(errs, field.Forbidden(path.Key(key),
				fmt.Sprintf("RunWorkflow was removed; action %s must use RunHook with hookRef and a ShutdownHook resource", action)))
		}
	}
	return errs
}

func ValidateShutdownTarget(path *field.Path, target powerv1alpha1.ShutdownStepTarget) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, ValidateNodeSelectorRequirements(path.Child("nodeSelectorRequirements"), target.NodeSelectorRequirements)...)
	for i, namespace := range target.Namespaces {
		errs = append(errs, ValidateDNSLabel(path.Child("namespaces").Index(i), namespace)...)
	}
	for i, ref := range target.WorkloadRefs {
		refPath := path.Child("workloadRefs").Index(i)
		errs = append(errs, ValidateIdentifierText(refPath.Child("apiVersion"), ref.APIVersion, "apiVersion is required")...)
		errs = append(errs, ValidateIdentifierText(refPath.Child("kind"), ref.Kind, "kind is required")...)
		errs = append(errs, ValidateDNSLabel(refPath.Child("namespace"), ref.Namespace)...)
		if ref.Name == "" {
			errs = append(errs, field.Required(refPath.Child("name"), "workload name is required"))
		} else {
			errs = append(errs, ValidateDNSSubdomain(refPath.Child("name"), ref.Name)...)
		}
	}
	for i, ref := range target.AgentRefs {
		errs = append(errs, ValidateObjectNameReference(path.Child("agentRefs").Index(i), ref)...)
	}
	return errs
}

func ValidateNodeSelectorRequirements(path *field.Path, requirements []corev1.NodeSelectorRequirement) field.ErrorList {
	var errs field.ErrorList
	for i, requirement := range requirements {
		reqPath := path.Index(i)
		if _, err := nodeselector.Requirement(requirement); err != nil {
			errs = append(errs, field.Invalid(reqPath, requirement, err.Error()))
		}
	}
	return errs
}

func ValidateShutdownStepType(path *field.Path, stepType powerv1alpha1.ShutdownStepType) field.ErrorList {
	switch stepType {
	case powerv1alpha1.ShutdownStepNotify,
		powerv1alpha1.ShutdownStepWait,
		powerv1alpha1.ShutdownStepCordonNodes,
		powerv1alpha1.ShutdownStepDrainNodes,
		powerv1alpha1.ShutdownStepScaleWorkload,
		powerv1alpha1.ShutdownStepRunHook,
		powerv1alpha1.ShutdownStepAgentShutdown:
		return nil
	case "":
		return field.ErrorList{field.Required(path, "required as a supported shutdown action")}
	default:
		return field.ErrorList{field.NotSupported(path, stepType, SupportedShutdownStepTypes())}
	}
}

func ValidateShutdownConcurrencyPolicy(path *field.Path, policy string) field.ErrorList {
	switch policy {
	case "", "Forbid", "Replace":
		return nil
	default:
		return field.ErrorList{field.NotSupported(path, policy, []string{"Forbid", "Replace"})}
	}
}

func ValidateShutdownTierOverrunPolicy(path *field.Path, policy powerv1alpha1.ShutdownTierOverrunPolicy) field.ErrorList {
	switch policy {
	case "", powerv1alpha1.ShutdownTierOverrunWait, powerv1alpha1.ShutdownTierOverrunOverlap, powerv1alpha1.ShutdownTierOverrunPreempt:
		return nil
	default:
		return field.ErrorList{field.NotSupported(path, policy, []string{
			string(powerv1alpha1.ShutdownTierOverrunWait),
			string(powerv1alpha1.ShutdownTierOverrunOverlap),
			string(powerv1alpha1.ShutdownTierOverrunPreempt),
		})}
	}
}

func ValidateAbortPolicy(path *field.Path, policy powerv1alpha1.AbortPolicySpec) field.ErrorList {
	var errs field.ErrorList
	switch policy.Behavior {
	case "", powerv1alpha1.AbortBehaviorHaltAndSurface:
	default:
		errs = append(errs, field.NotSupported(path.Child("behavior"), policy.Behavior, []string{
			string(powerv1alpha1.AbortBehaviorHaltAndSurface),
		}))
	}
	return errs
}

func ValidateFlowSafety(path *field.Path, obj *powerv1alpha1.ShutdownFlow) field.ErrorList {
	var errs field.ErrorList
	safety := obj.Spec.Safety
	errs = append(errs, ValidateAnnotationKey(path.Child("approvalAnnotation"), safety.ApprovalAnnotation)...)
	errs = append(errs, ValidatePositiveDuration(path.Child("maxEstimatedDuration"), safety.MaxEstimatedDuration)...)
	if obj.Spec.Mode == powerv1alpha1.ShutdownFlowModeEnforce {
		if safety.RequireManualApproval != nil && !*safety.RequireManualApproval {
			errs = append(errs, field.Invalid(path.Child("requireManualApproval"), *safety.RequireManualApproval, "Enforce mode requires manual approval"))
		}
		if safety.ApprovalAnnotation == "" {
			errs = append(errs, field.Required(path.Child("approvalAnnotation"), "required when mode is Enforce").WithOrigin("ApprovalAnnotationRequired"))
		} else if obj.Annotations[safety.ApprovalAnnotation] != "true" {
			errs = append(errs, field.Invalid(field.NewPath("metadata").Child("annotations").Key(safety.ApprovalAnnotation), obj.Annotations[safety.ApprovalAnnotation], "must be set to \"true\" when mode is Enforce").WithOrigin("FlowNotApproved"))
		}
	}
	return errs
}

func ValidateStringMap(path *field.Path, values map[string]string) field.ErrorList {
	var errs field.ErrorList
	for _, key := range slices.Sorted(maps.Keys(values)) {
		value := values[key]
		if key == "" {
			errs = append(errs, field.Required(path.Key(key), "map keys must not be empty"))
		} else if ContainsControlCharacter(key) {
			errs = append(errs, field.Invalid(path.Key(key), key, "map keys must not contain control characters"))
		}
		if ContainsControlCharacter(value) {
			errs = append(errs, field.Invalid(path.Key(key), value, "map values must not contain control characters"))
		}
	}
	return errs
}

func SupportedShutdownTriggerTypes() []string {
	return []string{
		string(powerv1alpha1.ShutdownTriggerOnBattery),
		string(powerv1alpha1.ShutdownTriggerLowBattery),
		string(powerv1alpha1.ShutdownTriggerRuntimeBelow),
		string(powerv1alpha1.ShutdownTriggerChargeBelow),
		string(powerv1alpha1.ShutdownTriggerTelemetryStale),
	}
}

func SupportedShutdownStepTypes() []string {
	return []string{
		string(powerv1alpha1.ShutdownStepNotify),
		string(powerv1alpha1.ShutdownStepWait),
		string(powerv1alpha1.ShutdownStepCordonNodes),
		string(powerv1alpha1.ShutdownStepDrainNodes),
		string(powerv1alpha1.ShutdownStepScaleWorkload),
		string(powerv1alpha1.ShutdownStepRunHook),
		string(powerv1alpha1.ShutdownStepAgentShutdown),
	}
}
