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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("NodePowerAgent Webhook", func() {
	var (
		obj       *powerv1alpha1.NodePowerAgent
		oldObj    *powerv1alpha1.NodePowerAgent
		validator NodePowerAgentCustomValidator
		defaulter NodePowerAgentCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.NodePowerAgent{}
		oldObj = &powerv1alpha1.NodePowerAgent{}
		validator = NodePowerAgentCustomValidator{}
		defaulter = NodePowerAgentCustomDefaulter{}
	})

	Context("When creating NodePowerAgent under Defaulting Webhook", func() {
		It("Should default to dry-run stub behavior", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.Mode).To(Equal(powerv1alpha1.NodePowerAgentModeDryRun))
			Expect(obj.Spec.Shutdown.ActuatorPolicy).To(Equal(powerv1alpha1.ActuatorPolicyStub))
			Expect(obj.Spec.Shutdown.SignalPath).To(Equal("/run/power-agent/shutdown.json"))
			Expect(obj.Spec.Shutdown.RequireFreshTelemetry).NotTo(BeNil())
			Expect(*obj.Spec.Shutdown.RequireFreshTelemetry).To(BeTrue())
			Expect(obj.Spec.Placement.PriorityClassName).To(Equal("system-node-critical"))
			Expect(obj.Spec.Placement.Tolerations).To(ContainElement(corev1.Toleration{
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}))
			Expect(obj.Spec.Placement.Tolerations).To(ContainElement(corev1.Toleration{
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			}))
			Expect(obj.Spec.Hardening.SeccompProfileType).To(Equal("RuntimeDefault"))
			Expect(obj.Spec.Resources.Upsmon.Requests).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("10m")))
			Expect(obj.Spec.Resources.Upsmon.Requests).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("32Mi")))
			Expect(obj.Spec.Resources.Upsmon.Limits).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("100m")))
			Expect(obj.Spec.Resources.Upsmon.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("64Mi")))
			Expect(obj.Spec.Resources.Actuator.Requests).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("5m")))
			Expect(obj.Spec.Resources.Actuator.Requests).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("16Mi")))
			Expect(obj.Spec.Resources.Actuator.Limits).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("50m")))
			Expect(obj.Spec.Resources.Actuator.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("32Mi")))
		})

		It("Should default only the resource keys the user did not already set", func() {
			obj.Spec.Resources.Upsmon = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
			}

			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.Resources.Upsmon.Requests).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("250m")))
			Expect(obj.Spec.Resources.Upsmon.Requests).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("32Mi")))
			Expect(obj.Spec.Resources.Upsmon.Limits).To(HaveKeyWithValue(corev1.ResourceCPU, resource.MustParse("100m")))
			Expect(obj.Spec.Resources.Upsmon.Limits).To(HaveKeyWithValue(corev1.ResourceMemory, resource.MustParse("64Mi")))
		})
	})

	Context("When creating or updating NodePowerAgent under Validating Webhook", func() {
		It("Should admit a dry-run agent with a NUT server reference", func() {
			obj.Spec = validNodePowerAgentSpec()

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject missing NUT server references", func() {
			obj.Spec = validNodePowerAgentSpec()
			obj.Spec.NUTServerRefs = nil

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.nutServerRefs"))
		})

		It("Should reject real host poweroff outside Actuate mode", func() {
			obj.Spec = validNodePowerAgentSpec()
			obj.Spec.Shutdown.ActuatorPolicy = powerv1alpha1.ActuatorPolicySystemdPoweroff
			obj.Spec.Shutdown.ApprovalAnnotation = "power.zalud.io/approved-for-actuation"

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("SystemdPoweroff requires spec.mode Actuate"))
		})

		It("Should reject real host poweroff without approval annotation value", func() {
			obj.Spec = validNodePowerAgentSpec()
			obj.Spec.Mode = powerv1alpha1.NodePowerAgentModeActuate
			obj.Spec.Shutdown.ActuatorPolicy = powerv1alpha1.ActuatorPolicySystemdPoweroff
			obj.Spec.Shutdown.ApprovalAnnotation = "power.zalud.io/approved-for-actuation"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("metadata.annotations[power.zalud.io/approved-for-actuation]"))
		})

		It("Should admit real host poweroff only when explicitly approved", func() {
			obj.Annotations = map[string]string{"power.zalud.io/approved-for-actuation": "true"}
			obj.Spec = validNodePowerAgentSpec()
			obj.Spec.Mode = powerv1alpha1.NodePowerAgentModeActuate
			obj.Spec.Shutdown.ActuatorPolicy = powerv1alpha1.ActuatorPolicySystemdPoweroff
			obj.Spec.Shutdown.ApprovalAnnotation = "power.zalud.io/approved-for-actuation"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject a reserved namespace as the operand namespace", func() {
			obj.Spec = validNodePowerAgentSpec()
			obj.Spec.Namespace = "kube-system"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.namespace"))
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})
	})
})

func validNodePowerAgentSpec() powerv1alpha1.NodePowerAgentSpec {
	return powerv1alpha1.NodePowerAgentSpec{
		NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a"}},
		Mode:          powerv1alpha1.NodePowerAgentModeDryRun,
		Shutdown: powerv1alpha1.AgentShutdownSpec{
			ActuatorPolicy:        powerv1alpha1.ActuatorPolicyStub,
			SignalPath:            "/run/power-agent/shutdown.json",
			RequireFreshTelemetry: ptrBool(true),
		},
	}
}
