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

var _ = Describe("PowerInfrastructure Webhook", func() {
	var (
		obj       *powerv1alpha1.PowerInfrastructure
		oldObj    *powerv1alpha1.PowerInfrastructure
		validator PowerInfrastructureCustomValidator
		defaulter PowerInfrastructureCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.PowerInfrastructure{}
		oldObj = &powerv1alpha1.PowerInfrastructure{}
		validator = PowerInfrastructureCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = PowerInfrastructureCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating PowerInfrastructure under Defaulting Webhook", func() {
		It("Should default missing class to Other", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Class).To(Equal(powerv1alpha1.PowerInfrastructureClassOther))
		})
	})

	Context("When creating or updating PowerInfrastructure under Validating Webhook", func() {
		It("Should admit a supported infrastructure class", func() {
			obj.Spec.Class = powerv1alpha1.PowerInfrastructureClassSwitch

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject unsupported infrastructure classes", func() {
			obj.Spec.Class = powerv1alpha1.PowerInfrastructureClass("Generator")

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.class"))
		})

		It("Should reject control characters in review text", func() {
			obj.Spec.Class = powerv1alpha1.PowerInfrastructureClassOther
			obj.Spec.DisplayName = "bad\nname"

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.displayName"))
		})
	})

})
