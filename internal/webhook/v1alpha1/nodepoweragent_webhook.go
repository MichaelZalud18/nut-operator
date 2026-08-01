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
	"path"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var nodepoweragentlog = logf.Log.WithName("nodepoweragent-resource")

// SetupNodePowerAgentWebhookWithManager registers the webhook for NodePowerAgent in the manager.
func SetupNodePowerAgentWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &powerv1alpha1.NodePowerAgent{}).
		WithValidator(&NodePowerAgentCustomValidator{}).
		WithDefaulter(&NodePowerAgentCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-power-zalud-io-v1alpha1-nodepoweragent,mutating=true,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=nodepoweragents,verbs=create;update,versions=v1alpha1,name=mnodepoweragent-v1alpha1.kb.io,admissionReviewVersions=v1

// NodePowerAgentCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind NodePowerAgent when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type NodePowerAgentCustomDefaulter struct {
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind NodePowerAgent.
func (d *NodePowerAgentCustomDefaulter) Default(_ context.Context, obj *powerv1alpha1.NodePowerAgent) error {
	nodepoweragentlog.Info("Defaulting for NodePowerAgent", "name", obj.GetName())

	defaultNodePowerAgent(obj)

	return nil
}

// +kubebuilder:webhook:path=/validate-power-zalud-io-v1alpha1-nodepoweragent,mutating=false,failurePolicy=fail,sideEffects=None,groups=power.zalud.io,resources=nodepoweragents,verbs=create;update,versions=v1alpha1,name=vnodepoweragent-v1alpha1.kb.io,admissionReviewVersions=v1

// NodePowerAgentCustomValidator struct is responsible for validating the NodePowerAgent resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NodePowerAgentCustomValidator struct {
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type NodePowerAgent.
func (v *NodePowerAgentCustomValidator) ValidateCreate(_ context.Context, obj *powerv1alpha1.NodePowerAgent) (admission.Warnings, error) {
	nodepoweragentlog.Info("Validation for NodePowerAgent upon creation", "name", obj.GetName())

	return nil, validateNodePowerAgentAdmission(obj)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type NodePowerAgent.
func (v *NodePowerAgentCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *powerv1alpha1.NodePowerAgent) (admission.Warnings, error) {
	nodepoweragentlog.Info("Validation for NodePowerAgent upon update", "name", newObj.GetName())

	return nil, validateNodePowerAgentAdmission(newObj)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type NodePowerAgent.
func (v *NodePowerAgentCustomValidator) ValidateDelete(_ context.Context, obj *powerv1alpha1.NodePowerAgent) (admission.Warnings, error) {
	nodepoweragentlog.Info("Validation for NodePowerAgent upon deletion", "name", obj.GetName())

	return nil, nil
}

func defaultNodePowerAgent(obj *powerv1alpha1.NodePowerAgent) {
	if obj.Spec.Mode == "" {
		obj.Spec.Mode = powerv1alpha1.NodePowerAgentModeDryRun
	}
	if obj.Spec.Shutdown.ActuatorPolicy == "" {
		obj.Spec.Shutdown.ActuatorPolicy = powerv1alpha1.ActuatorPolicyStub
	}
	if obj.Spec.Shutdown.SignalPath == "" {
		obj.Spec.Shutdown.SignalPath = "/run/power-agent/shutdown.json"
	}
	if obj.Spec.Shutdown.RequireFreshTelemetry == nil {
		obj.Spec.Shutdown.RequireFreshTelemetry = ptrBool(true)
	}
	defaultPodHardening(&obj.Spec.Hardening)
}

func validateNodePowerAgentAdmission(obj *powerv1alpha1.NodePowerAgent) error {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	errs = append(errs, validateOptionalObjectNameReference(specPath.Child("managementClusterRef"), obj.Spec.ManagementClusterRef)...)
	errs = append(errs, validateOptionalNamespace(specPath.Child("namespace"), obj.Spec.Namespace)...)
	if len(obj.Spec.NUTServerRefs) == 0 {
		errs = append(errs, field.Required(specPath.Child("nutServerRefs"), "requires at least one NUTServer reference"))
	}
	for i, ref := range obj.Spec.NUTServerRefs {
		errs = append(errs, validateObjectNameReference(specPath.Child("nutServerRefs").Index(i), ref)...)
	}
	errs = append(errs, validateOptionalObjectNameReference(specPath.Child("shutdownFlowRef"), obj.Spec.ShutdownFlowRef)...)
	errs = append(errs, validateNodePowerAgentMode(specPath.Child("mode"), obj.Spec.Mode)...)
	errs = append(errs, validateUpsmonConfig(specPath.Child("upsmon"), obj.Spec.Upsmon)...)
	errs = append(errs, validateAgentShutdown(specPath.Child("shutdown"), obj)...)
	errs = append(errs, validatePodHardening(specPath.Child("hardening"), obj.Spec.Hardening)...)

	return newInvalidAdmissionError("NodePowerAgent", obj, errs)
}

func validateNodePowerAgentMode(path *field.Path, mode powerv1alpha1.NodePowerAgentMode) field.ErrorList {
	switch mode {
	case "", powerv1alpha1.NodePowerAgentModeMonitorOnly, powerv1alpha1.NodePowerAgentModeDryRun, powerv1alpha1.NodePowerAgentModeActuate:
		return nil
	default:
		return field.ErrorList{field.NotSupported(path, mode, []string{
			string(powerv1alpha1.NodePowerAgentModeMonitorOnly),
			string(powerv1alpha1.NodePowerAgentModeDryRun),
			string(powerv1alpha1.NodePowerAgentModeActuate),
		})}
	}
}

func validateUpsmonConfig(path *field.Path, config powerv1alpha1.UpsmonConfigSpec) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, validatePositiveDuration(path.Child("pollFrequency"), config.PollFrequency)...)
	errs = append(errs, validatePositiveDuration(path.Child("alertPollFrequency"), config.AlertPollFrequency)...)
	errs = append(errs, validatePositiveDuration(path.Child("deadTime"), config.DeadTime)...)
	errs = append(errs, validatePositiveDuration(path.Child("hostSync"), config.HostSync)...)
	errs = append(errs, validatePositiveDuration(path.Child("finalDelay"), config.FinalDelay)...)
	return errs
}

func validateAgentShutdown(pathField *field.Path, obj *powerv1alpha1.NodePowerAgent) field.ErrorList {
	var errs field.ErrorList
	shutdown := obj.Spec.Shutdown
	switch shutdown.ActuatorPolicy {
	case "", powerv1alpha1.ActuatorPolicyDisabled, powerv1alpha1.ActuatorPolicyStub:
	case powerv1alpha1.ActuatorPolicySystemdPoweroff:
		if obj.Spec.Mode != powerv1alpha1.NodePowerAgentModeActuate {
			errs = append(errs, field.Invalid(pathField.Child("actuatorPolicy"), shutdown.ActuatorPolicy, "SystemdPoweroff requires spec.mode Actuate"))
		}
		if shutdown.ApprovalAnnotation == "" {
			errs = append(errs, field.Required(pathField.Child("approvalAnnotation"), "required for SystemdPoweroff actuation"))
		} else if obj.Annotations[shutdown.ApprovalAnnotation] != "true" {
			errs = append(errs, field.Invalid(field.NewPath("metadata").Child("annotations").Key(shutdown.ApprovalAnnotation), obj.Annotations[shutdown.ApprovalAnnotation], "must be set to \"true\" for SystemdPoweroff actuation"))
		}
	default:
		errs = append(errs, field.NotSupported(pathField.Child("actuatorPolicy"), shutdown.ActuatorPolicy, []string{
			string(powerv1alpha1.ActuatorPolicyDisabled),
			string(powerv1alpha1.ActuatorPolicyStub),
			string(powerv1alpha1.ActuatorPolicySystemdPoweroff),
		}))
	}
	if shutdown.SignalPath != "" {
		if containsControlCharacter(shutdown.SignalPath) {
			errs = append(errs, field.Invalid(pathField.Child("signalPath"), shutdown.SignalPath, "must not contain control characters"))
		}
		if !path.IsAbs(shutdown.SignalPath) {
			errs = append(errs, field.Invalid(pathField.Child("signalPath"), shutdown.SignalPath, "must be an absolute in-pod path"))
		}
	}
	errs = append(errs, validatePositiveDuration(pathField.Child("signalTTL"), shutdown.SignalTTL)...)
	errs = append(errs, validateAnnotationKey(pathField.Child("approvalAnnotation"), shutdown.ApprovalAnnotation)...)
	return errs
}
