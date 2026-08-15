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
	"path"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var nodepoweragentlog = logf.Log.WithName("nodepoweragent-resource")

const defaultNodePowerAgentPriorityClassName = "system-node-critical"

// minimumSignalTTL is derived from the delivery bound rather than chosen beside it (F-70).
//
// A shutdown signal reaches the actuator through a projected Secret, and this project's own
// measurement put that at ~44 seconds; kubelet's sync period and cache TTL push the worst case
// higher. A TTL below that window does not make the system stricter, it makes every correctly
// delivered signal arrive already expired -- rejected as SignalStale on every node at once, at the
// moment the flow needed them. 90s is the measured bound with room for the sync period on top.
const minimumSignalTTL = 90 * time.Second

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
		obj.Spec.Shutdown.ActuatorPolicy = powerv1alpha1.ActuatorPolicySimulate
	}
	if obj.Spec.Shutdown.SignalPath == "" {
		obj.Spec.Shutdown.SignalPath = "/run/power-agent/shutdown.json"
	}
	if obj.Spec.Shutdown.RequireFreshTelemetry == nil {
		obj.Spec.Shutdown.RequireFreshTelemetry = ptrBool(true)
	}
	defaultNodePowerAgentPlacement(obj)
	defaultNodePowerAgentResources(obj)
	defaultPodHardening(&obj.Spec.Hardening)
}

// defaultNodePowerAgentResources fills in conservative requests/limits for whichever resource keys
// the user left unset (F-34). Without this, the tier-0 DaemonSet this whole project depends on
// surviving node pressure runs BestEffort QoS by default -- no OOM-score protection, no scheduler
// capacity reservation -- unless every user remembers to set spec.resources themselves. Per-key, not
// wholesale: a user-supplied request or limit for a given resource name is never overwritten.
func defaultNodePowerAgentResources(obj *powerv1alpha1.NodePowerAgent) {
	defaultResourceRequirements(&obj.Spec.Resources.Upsmon, corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	}, corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("100m"),
		corev1.ResourceMemory: resource.MustParse("64Mi"),
	})
	defaultResourceRequirements(&obj.Spec.Resources.Actuator, corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("5m"),
		corev1.ResourceMemory: resource.MustParse("16Mi"),
	}, corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	})
}

func defaultResourceRequirements(resources *corev1.ResourceRequirements, defaultRequests, defaultLimits corev1.ResourceList) {
	if resources.Requests == nil {
		resources.Requests = corev1.ResourceList{}
	}
	for name, quantity := range defaultRequests {
		if _, set := resources.Requests[name]; !set {
			resources.Requests[name] = quantity
		}
	}
	if resources.Limits == nil {
		resources.Limits = corev1.ResourceList{}
	}
	for name, quantity := range defaultLimits {
		if _, set := resources.Limits[name]; !set {
			resources.Limits[name] = quantity
		}
	}
}

func defaultNodePowerAgentPlacement(obj *powerv1alpha1.NodePowerAgent) {
	if obj.Spec.Placement.PriorityClassName == "" {
		obj.Spec.Placement.PriorityClassName = defaultNodePowerAgentPriorityClassName
	}
	defaultTolerations := []corev1.Toleration{
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
	}
	for _, toleration := range defaultTolerations {
		if !hasToleration(obj.Spec.Placement.Tolerations, toleration) {
			obj.Spec.Placement.Tolerations = append(obj.Spec.Placement.Tolerations, toleration)
		}
	}
}

func hasToleration(tolerations []corev1.Toleration, candidate corev1.Toleration) bool {
	for _, toleration := range tolerations {
		if toleration.Key == candidate.Key &&
			toleration.Operator == candidate.Operator &&
			toleration.Value == candidate.Value &&
			toleration.Effect == candidate.Effect &&
			toleration.TolerationSeconds == candidate.TolerationSeconds {
			return true
		}
	}
	return false
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
	case "", powerv1alpha1.ActuatorPolicyDisabled, powerv1alpha1.ActuatorPolicySimulate:
	case powerv1alpha1.ActuatorPolicyPowerOff:
		if obj.Spec.Mode != powerv1alpha1.NodePowerAgentModeActuate {
			errs = append(errs, field.Invalid(pathField.Child("actuatorPolicy"), shutdown.ActuatorPolicy, "PowerOff requires spec.mode Actuate"))
		}
		if shutdown.ApprovalAnnotation == "" {
			errs = append(errs, field.Required(pathField.Child("approvalAnnotation"), "required for PowerOff actuation"))
		} else if obj.Annotations[shutdown.ApprovalAnnotation] != "true" {
			errs = append(errs, field.Invalid(field.NewPath("metadata").Child("annotations").Key(shutdown.ApprovalAnnotation), obj.Annotations[shutdown.ApprovalAnnotation], "must be set to \"true\" for PowerOff actuation"))
		}
	default:
		errs = append(errs, field.NotSupported(pathField.Child("actuatorPolicy"), shutdown.ActuatorPolicy, []string{
			string(powerv1alpha1.ActuatorPolicyDisabled),
			string(powerv1alpha1.ActuatorPolicySimulate),
			string(powerv1alpha1.ActuatorPolicyPowerOff),
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

	// F-70: the TTL has to cover how long delivery actually takes.
	//
	// The measured projected-Secret delivery on this project's own smoke run was ~44s, and kubelet
	// sync period plus cache TTL push the worst case above that. A TTL under the delivery bound
	// means the actuator rejects operator-issued signals as SignalStale fleet-wide, evidenced only
	// by a container log line -- so it is refused here rather than discovered during an outage.
	if shutdown.SignalTTL != nil && shutdown.SignalTTL.Duration > 0 && shutdown.SignalTTL.Duration < minimumSignalTTL {
		errs = append(errs, field.Invalid(pathField.Child("signalTTL"), shutdown.SignalTTL.Duration.String(),
			fmt.Sprintf("must be at least %s: projected Secret delivery was measured at ~44s and kubelet sync period plus cache TTL push the worst case higher, so a shorter TTL rejects signals that arrived correctly", minimumSignalTTL)))
	}
	errs = append(errs, validateAnnotationKey(pathField.Child("approvalAnnotation"), shutdown.ApprovalAnnotation)...)
	return errs
}
