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

package controller

import (
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func validateNodePowerAgentRenderSafety(agent *powerv1alpha1.NodePowerAgent) error {
	policy := nodePowerAgentActuatorPolicy(agent)
	if !nodePowerAgentActuatorPolicyRequiresApproval(policy) {
		return nil
	}
	if nodePowerAgentMode(agent) != powerv1alpha1.NodePowerAgentModeActuate {
		return fmt.Errorf("%s actuator rendering requires spec.mode Actuate", policy)
	}
	approvalAnnotation := agent.Spec.Shutdown.ApprovalAnnotation
	if approvalAnnotation == "" {
		return fmt.Errorf("%s actuator rendering requires spec.shutdown.approvalAnnotation", policy)
	}
	if agent.Annotations[approvalAnnotation] != "true" {
		return fmt.Errorf("%s actuator rendering requires approval annotation %q=true", policy, approvalAnnotation)
	}
	return nil
}

func nodePowerAgentActuatorPolicyRequiresApproval(policy powerv1alpha1.ActuatorPolicy) bool {
	switch policy {
	case powerv1alpha1.ActuatorPolicyPowerOff, powerv1alpha1.ActuatorPolicyTalosShutdown:
		return true
	default:
		return false
	}
}

func nodePowerAgentRequiresHostPoweroff(agent *powerv1alpha1.NodePowerAgent) bool {
	return nodePowerAgentMode(agent) == powerv1alpha1.NodePowerAgentModeActuate &&
		nodePowerAgentActuatorPolicy(agent) == powerv1alpha1.ActuatorPolicyPowerOff
}

func nodePowerAgentUsesTalosAPI(agent *powerv1alpha1.NodePowerAgent) bool {
	return nodePowerAgentActuatorPolicy(agent) == powerv1alpha1.ActuatorPolicyTalosShutdown
}

// nodePowerAgentPodSecurityConflict reports whether the operand namespace's enforced Pod Security
// level will reject the actuating agent pod, and names the exception needed.
//
// This reads the namespace and reports. It deliberately does not write the labels. Relaxing a
// namespace from `baseline` to `privileged` is a decision about how much the cluster is willing to
// trust one workload, and an operator that quietly widens it on the user's behalf has taken that
// decision away from the person accountable for it -- while making a CR field the thing that edits
// a security boundary.
//
// Two things put the actuating pod outside `baseline`, both measured on kind against a labelled
// namespace rather than read off the standard:
//
//	host namespaces          hostPID=true
//	non-default capabilities container "actuator" must not include "SYS_BOOT"
//
// The stub and dry-run default has neither, and is admitted by `restricted` -- the strictest level
// there is. That asymmetry is worth keeping: the exception is scoped to the configuration that
// actually halts machines, not to the operator as a whole.
//
// Only the `enforce` label can reject a pod. `warn` and `audit` produce messages and admit it, so a
// namespace carrying only those is not reported here.
func nodePowerAgentPodSecurityConflict(namespace *corev1.Namespace) string {
	if namespace == nil {
		return ""
	}
	level := namespace.Labels["pod-security.kubernetes.io/enforce"]
	if level != "baseline" && level != "restricted" {
		return ""
	}
	return fmt.Sprintf(
		"namespace %q enforces pod-security.kubernetes.io/enforce=%s, which rejects the actuating agent pod "+
			"on hostPID=true (host namespaces) and CAP_SYS_BOOT (non-default capabilities); "+
			"actuation needs that namespace exempted or labelled pod-security.kubernetes.io/enforce=privileged. "+
			"The Simulate and DryRun default needs neither and is admitted under restricted",
		namespace.Name, level,
	)
}

func actuatorContainerSecurityContext(hostPoweroff bool) *corev1.SecurityContext {
	if !hostPoweroff {
		return restrictedContainerSecurityContext()
	}
	// Keep the pod's RuntimeDefault seccomp profile. The kind probe of reboot(2) with an invalid
	// argument distinguishes the capability gate from a seccomp denial:
	//
	//   RuntimeDefault + CAP_SYS_BOOT  -> EINVAL, the kernel's reboot handler ran and rejected the
	//                                    argument, so the capability check passed
	//   RuntimeDefault, no capability  -> EPERM, refused on the capability
	//
	// The two errnos are what separate the explanations. Seccomp denials surface as EPERM before the
	// handler runs; EINVAL can only come from the handler itself. CAP_SYS_BOOT permits the call
	// while RuntimeDefault retains the other syscall filters on the container that can halt the host.
	//
	// hostPID still places this pod outside Pod Security "baseline" on its own.
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		ReadOnlyRootFilesystem:   ptrBool(true),
		Capabilities: &corev1.Capabilities{
			Add:  []corev1.Capability{"SYS_BOOT"},
			Drop: []corev1.Capability{"ALL"},
		},
	}
}
