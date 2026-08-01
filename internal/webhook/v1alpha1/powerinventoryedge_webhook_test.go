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

var _ = Describe("PowerInventoryEdge Webhook", func() {
	var (
		obj       *powerv1alpha1.PowerInventoryEdge
		oldObj    *powerv1alpha1.PowerInventoryEdge
		validator PowerInventoryEdgeCustomValidator
		defaulter PowerInventoryEdgeCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.PowerInventoryEdge{}
		oldObj = &powerv1alpha1.PowerInventoryEdge{}
		validator = PowerInventoryEdgeCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = PowerInventoryEdgeCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating PowerInventoryEdge under Defaulting Webhook", func() {
		It("Should not rewrite authored topology", func() {
			obj.Spec = feedEdgeSpec()

			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Input).To(Equal("psu-a"))
		})
	})

	Context("When creating or updating PowerInventoryEdge under Validating Webhook", func() {
		It("Should admit a feeds edge with an input qualifier", func() {
			obj.Spec = feedEdgeSpec()

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should admit a carries edge without an input qualifier", func() {
			obj.Spec = powerv1alpha1.PowerInventoryEdgeSpec{
				From: powerv1alpha1.PowerInventoryEntityReference{
					Kind: powerv1alpha1.PowerInventoryEntityPowerInfrastructure,
					Name: "rack-a-switch",
				},
				To: powerv1alpha1.PowerInventoryEntityReference{
					Kind: powerv1alpha1.PowerInventoryEntityNode,
					Name: "worker-a",
				},
				Relation: powerv1alpha1.PowerInventoryEdgeCarries,
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject feeds edges without input qualifiers", func() {
			obj.Spec = feedEdgeSpec()
			obj.Spec.Input = ""

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.input"))
		})

		It("Should reject input qualifiers on carries edges", func() {
			obj.Spec = feedEdgeSpec()
			obj.Spec.Relation = powerv1alpha1.PowerInventoryEdgeCarries

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.input"))
		})

		It("Should reject self edges", func() {
			obj.Spec = feedEdgeSpec()
			obj.Spec.To = obj.Spec.From

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("same inventory entity"))
		})

		It("Should reject unsupported references and relations", func() {
			obj.Spec = feedEdgeSpec()
			obj.Spec.From.Kind = powerv1alpha1.PowerInventoryEntityKind("Rack")
			obj.Spec.Relation = powerv1alpha1.PowerInventoryEdgeRelation("DependsOn")

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.from.kind"))
			Expect(err.Error()).To(ContainSubstring("spec.relation"))
		})
	})

})

func feedEdgeSpec() powerv1alpha1.PowerInventoryEdgeSpec {
	return powerv1alpha1.PowerInventoryEdgeSpec{
		From: powerv1alpha1.PowerInventoryEntityReference{
			Kind: powerv1alpha1.PowerInventoryEntityUPSDevice,
			Name: "rack-a-ups",
		},
		To: powerv1alpha1.PowerInventoryEntityReference{
			Kind: powerv1alpha1.PowerInventoryEntityNode,
			Name: "worker-a",
		},
		Relation: powerv1alpha1.PowerInventoryEdgeFeeds,
		Input:    "psu-a",
	}
}
