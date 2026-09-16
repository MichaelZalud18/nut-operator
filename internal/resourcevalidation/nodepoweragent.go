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
	"fmt"
	"net"
	"path"
	"time"

	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/util/validation/field"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// minimumSignalTTL is derived from the delivery bound rather than chosen beside it (F-70).
//
// A shutdown signal reaches the actuator through a projected Secret, and this project's own
// measurement put that at ~44 seconds; kubelet's sync period and cache TTL push the worst case
// higher. A TTL below that window does not make the system stricter, it makes every correctly
// delivered signal arrive already expired -- rejected as SignalStale on every node at once, at the
// moment the flow needed them. 90s is the measured bound with room for the sync period on top.
const MinimumSignalTTL = 90 * time.Second

func ValidateNodePowerAgentFields(obj *powerv1alpha1.NodePowerAgent) field.ErrorList {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	errs = append(errs, ValidateOptionalObjectNameReference(specPath.Child("managementClusterRef"), obj.Spec.ManagementClusterRef)...)
	errs = append(errs, ValidateOptionalNamespace(specPath.Child("namespace"), obj.Spec.Namespace)...)
	if len(obj.Spec.NUTServerRefs) == 0 {
		errs = append(errs, field.Required(specPath.Child("nutServerRefs"), "requires at least one NUTServer reference").WithOrigin("NUTServerRefsRequired"))
	}
	for i, ref := range obj.Spec.NUTServerRefs {
		errs = append(errs, ValidateObjectNameReference(specPath.Child("nutServerRefs").Index(i), ref)...)
	}
	errs = append(errs, ValidateOptionalObjectNameReference(specPath.Child("shutdownFlowRef"), obj.Spec.ShutdownFlowRef)...)
	errs = append(errs, ValidateNodePowerAgentMode(specPath.Child("mode"), obj.Spec.Mode)...)
	errs = append(errs, ValidateUpsmonConfig(specPath.Child("upsmon"), obj.Spec.Upsmon)...)
	errs = append(errs, ValidateAgentShutdown(specPath.Child("shutdown"), obj)...)
	errs = append(errs, ValidatePodHardening(specPath.Child("hardening"), obj.Spec.Hardening)...)

	for _, image := range []struct {
		name string
		ref  powerv1alpha1.ImageReference
	}{{"upsmon", obj.Spec.Images.Upsmon}, {"actuator", obj.Spec.Images.Actuator}} {
		if image.ref.PullPolicy == corev1.PullAlways {
			errs = append(errs, field.Invalid(specPath.Child("images", image.name, "pullPolicy"), image.ref.PullPolicy,
				"cannot be Always: the agent must be able to start while its registry is unavailable").WithOrigin("AgentImagePullPolicyAlways"))
		}
	}
	return errs
}

func ValidateNodePowerAgentMode(path *field.Path, mode powerv1alpha1.NodePowerAgentMode) field.ErrorList {
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

func ValidateUpsmonConfig(path *field.Path, config powerv1alpha1.UpsmonConfigSpec) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, ValidatePositiveDuration(path.Child("pollFrequency"), config.PollFrequency)...)
	errs = append(errs, ValidatePositiveDuration(path.Child("alertPollFrequency"), config.AlertPollFrequency)...)
	errs = append(errs, ValidatePositiveDuration(path.Child("deadTime"), config.DeadTime)...)
	errs = append(errs, ValidatePositiveDuration(path.Child("hostSync"), config.HostSync)...)
	errs = append(errs, ValidatePositiveDuration(path.Child("finalDelay"), config.FinalDelay)...)
	return errs
}

func ValidateAgentShutdown(pathField *field.Path, obj *powerv1alpha1.NodePowerAgent) field.ErrorList {
	var errs field.ErrorList
	shutdown := obj.Spec.Shutdown
	switch shutdown.ActuatorPolicy {
	case "", powerv1alpha1.ActuatorPolicyDisabled, powerv1alpha1.ActuatorPolicySimulate:
	case powerv1alpha1.ActuatorPolicyPowerOff, powerv1alpha1.ActuatorPolicyTalosShutdown:
		policy := string(shutdown.ActuatorPolicy)
		if obj.Spec.Mode != powerv1alpha1.NodePowerAgentModeActuate {
			errs = append(errs, field.Invalid(pathField.Child("actuatorPolicy"), shutdown.ActuatorPolicy, policy+" requires spec.mode Actuate").WithOrigin("ActuationModeRequired"))
		}
		if shutdown.ApprovalAnnotation == "" {
			errs = append(errs, field.Required(pathField.Child("approvalAnnotation"), "required for "+policy+" actuation").WithOrigin("ApprovalAnnotationRequired"))
		} else if obj.Annotations[shutdown.ApprovalAnnotation] != "true" {
			errs = append(errs, field.Invalid(field.NewPath("metadata").Child("annotations").Key(shutdown.ApprovalAnnotation), obj.Annotations[shutdown.ApprovalAnnotation], "must be set to \"true\" for "+policy+" actuation").WithOrigin("ActuationNotApproved"))
		}
		if shutdown.ActuatorPolicy == powerv1alpha1.ActuatorPolicyTalosShutdown {
			errs = append(errs, ValidateTalosShutdown(pathField.Child("talos"), shutdown.Talos)...)
		}
	default:
		errs = append(errs, field.NotSupported(pathField.Child("actuatorPolicy"), shutdown.ActuatorPolicy, []string{
			string(powerv1alpha1.ActuatorPolicyDisabled),
			string(powerv1alpha1.ActuatorPolicySimulate),
			string(powerv1alpha1.ActuatorPolicyPowerOff),
			string(powerv1alpha1.ActuatorPolicyTalosShutdown),
		}))
	}
	if shutdown.SignalPath != "" {
		if ContainsControlCharacter(shutdown.SignalPath) {
			errs = append(errs, field.Invalid(pathField.Child("signalPath"), shutdown.SignalPath, "must not contain control characters"))
		}
		if !path.IsAbs(shutdown.SignalPath) {
			errs = append(errs, field.Invalid(pathField.Child("signalPath"), shutdown.SignalPath, "must be an absolute in-pod path"))
		}
	}
	errs = append(errs, ValidatePositiveDuration(pathField.Child("signalTTL"), shutdown.SignalTTL)...)

	if shutdown.SignalTTL != nil && shutdown.SignalTTL.Duration > 0 && shutdown.SignalTTL.Duration < MinimumSignalTTL {
		errs = append(errs, field.Invalid(pathField.Child("signalTTL"), shutdown.SignalTTL.Duration.String(),
			fmt.Sprintf("must be at least %s: projected Secret delivery was measured at ~44s and kubelet sync period plus cache TTL push the worst case higher, so a shorter TTL rejects signals that arrived correctly", MinimumSignalTTL)))
	}
	errs = append(errs, ValidateAnnotationKey(pathField.Child("approvalAnnotation"), shutdown.ApprovalAnnotation)...)
	return errs
}

func ValidateTalosShutdown(pathField *field.Path, talos *powerv1alpha1.TalosShutdownSpec) field.ErrorList {
	if talos == nil {
		return field.ErrorList{field.Required(pathField, "required when actuatorPolicy is TalosShutdown").WithOrigin("TalosShutdownConfigRequired")}
	}
	var errs field.ErrorList
	for _, err := range ValidateSecretKeyReference(pathField.Child("talosConfigSecretKeyRef"), talos.TalosConfigSecretKeyRef) {
		errs = append(errs, err.WithOrigin("TalosConfigSecretRequired"))
	}
	if len(talos.Endpoints) == 0 {
		errs = append(errs, field.Required(pathField.Child("endpoints"), "requires at least one Talos API endpoint IP").WithOrigin("TalosEndpointsRequired"))
	}
	for i, endpoint := range talos.Endpoints {
		endpointPath := pathField.Child("endpoints").Index(i)
		if endpoint == "" {
			errs = append(errs, field.Required(endpointPath, "requires a Talos API endpoint IP").WithOrigin("TalosEndpointInvalid"))
			continue
		}
		if ContainsControlCharacter(endpoint) {
			errs = append(errs, field.Invalid(endpointPath, endpoint, "must not contain control characters").WithOrigin("TalosEndpointInvalid"))
			continue
		}
		if net.ParseIP(endpoint) == nil {
			errs = append(errs, field.Invalid(endpointPath, endpoint, "must be an IP literal so the generated NetworkPolicy can allow only that Talos API endpoint").WithOrigin("TalosEndpointInvalid"))
		}
	}
	switch talos.NodeAddressSource {
	case "", powerv1alpha1.TalosNodeAddressSourceHostIP, powerv1alpha1.TalosNodeAddressSourceNodeName:
	default:
		errs = append(errs, field.NotSupported(pathField.Child("nodeAddressSource"), talos.NodeAddressSource, []string{
			string(powerv1alpha1.TalosNodeAddressSourceHostIP),
			string(powerv1alpha1.TalosNodeAddressSourceNodeName),
		}).WithOrigin("TalosNodeAddressSourceInvalid"))
	}
	for _, err := range ValidatePositiveDuration(pathField.Child("shutdownTimeout"), talos.ShutdownTimeout) {
		errs = append(errs, err.WithOrigin("TalosShutdownTimeoutInvalid"))
	}
	return errs
}
