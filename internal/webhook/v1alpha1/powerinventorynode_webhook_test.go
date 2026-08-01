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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("PowerInventoryNode Webhook", func() {
	var (
		obj       *powerv1alpha1.PowerInventoryNode
		oldObj    *powerv1alpha1.PowerInventoryNode
		validator PowerInventoryNodeCustomValidator
		defaulter PowerInventoryNodeCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.PowerInventoryNode{}
		oldObj = &powerv1alpha1.PowerInventoryNode{}
		validator = PowerInventoryNodeCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = PowerInventoryNodeCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating PowerInventoryNode under Defaulting Webhook", func() {
		It("Should default boolean policy fields to false", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.PowerPlanningExempt).NotTo(BeNil())
			Expect(*obj.Spec.PowerPlanningExempt).To(BeFalse())
			Expect(obj.Spec.CommunicationPathExempt).NotTo(BeNil())
			Expect(*obj.Spec.CommunicationPathExempt).To(BeFalse())
			Expect(obj.Spec.Roles.ControlPlane).NotTo(BeNil())
			Expect(*obj.Spec.Roles.ControlPlane).To(BeFalse())
			Expect(obj.Spec.Roles.ControlPlaneQuorumMember).NotTo(BeNil())
			Expect(*obj.Spec.Roles.ControlPlaneQuorumMember).To(BeFalse())
		})
	})

	Context("When creating or updating PowerInventoryNode under Validating Webhook", func() {
		It("Should admit a Kubernetes node name", func() {
			obj.Spec.NodeName = "worker-a.example.net"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject missing nodeName", func() {
			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.nodeName"))
		})

		It("Should reject invalid Kubernetes node names", func() {
			obj.Spec.NodeName = "Worker_A"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.nodeName"))
		})

		It("Should reject control characters in role names", func() {
			obj.Spec.NodeName = "worker-a"
			obj.Spec.Roles.LastDitchRole = "storage\nlate"

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.roles.lastDitchRole"))
		})
	})

})
