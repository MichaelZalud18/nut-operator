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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/resourcevalidation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
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
	if obj.Spec.TierOverrunPolicy == "" {
		obj.Spec.TierOverrunPolicy = powerv1alpha1.ShutdownTierOverrunWait
	}
	if obj.Spec.AbortPolicy.Behavior == "" {
		obj.Spec.AbortPolicy.Behavior = powerv1alpha1.AbortBehaviorHaltAndSurface
	}
	if obj.Spec.AbortPolicy.Notify == nil {
		obj.Spec.AbortPolicy.Notify = ptrBool(false)
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
	warnings, errs := validateShutdownFlowFields(obj)
	return warnings, newInvalidAdmissionError("ShutdownFlow", obj, errs)
}

func validateShutdownFlowFields(obj *powerv1alpha1.ShutdownFlow) ([]string, field.ErrorList) {
	return resourcevalidation.ValidateShutdownFlowFields(obj)
}

func validateShutdownHookPolicy(path *field.Path, obj *powerv1alpha1.ShutdownFlow) field.ErrorList {
	return resourcevalidation.ValidateShutdownHookPolicy(path, obj)
}

func validateRunHookReference(path *field.Path, action powerv1alpha1.ShutdownStepType, ref *powerv1alpha1.NamespacedNameReference) field.ErrorList {
	return resourcevalidation.ValidateRunHookReference(path, action, ref)
}

func validateRemovedWorkflowParams(path *field.Path, action powerv1alpha1.ShutdownStepType, params map[string]string) field.ErrorList {
	return resourcevalidation.ValidateRemovedWorkflowParams(path, action, params)
}

func validateStringMap(path *field.Path, values map[string]string) field.ErrorList {
	return resourcevalidation.ValidateStringMap(path, values)
}
