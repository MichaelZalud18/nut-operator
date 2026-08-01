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

var _ = Describe("UPSCapabilityProfile Webhook", func() {
	var (
		obj       *powerv1alpha1.UPSCapabilityProfile
		oldObj    *powerv1alpha1.UPSCapabilityProfile
		validator UPSCapabilityProfileCustomValidator
		defaulter UPSCapabilityProfileCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.UPSCapabilityProfile{}
		oldObj = &powerv1alpha1.UPSCapabilityProfile{}
		validator = UPSCapabilityProfileCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = UPSCapabilityProfileCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	Context("When creating UPSCapabilityProfile under Defaulting Webhook", func() {
		It("Should default universal to false", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Selector.Universal).NotTo(BeNil())
			Expect(*obj.Spec.Selector.Universal).To(BeFalse())
		})
	})

	Context("When creating or updating UPSCapabilityProfile under Validating Webhook", func() {
		It("Should admit a product model glob profile", func() {
			obj.Spec = powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "0.1.0",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					ModelGlob: "TOWER_*VA_*V",
				},
				Telemetry: powerv1alpha1.UPSCapabilityTelemetrySpec{
					Variables: []string{"ups.status", "battery.runtime"},
				},
				Quirks: []string{"built-in-nut-server"},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should admit a firmware-narrowed exact model profile", func() {
			obj.Spec = powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "v1.2.3-firmware.4",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					Model:    "TOWER_1000VA_120V",
					Firmware: "1.5.0",
				},
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject a missing selector", func() {
			obj.Spec.Version = "0.1.0"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.selector"))
		})

		It("Should reject multiple match tiers", func() {
			obj.Spec = powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "0.1.0",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					Model:        "TOWER_1000VA_120V",
					DriverFamily: "dummy-ups",
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exactly one match tier"))
		})

		It("Should reject invalid model globs", func() {
			obj.Spec = powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "0.1.0",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					ModelGlob: "TOWER_[",
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.selector.modelGlob"))
		})

		It("Should reject duplicate telemetry variables", func() {
			obj.Spec = powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "0.1.0",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					DriverFamily: "snmp-ups",
				},
				Telemetry: powerv1alpha1.UPSCapabilityTelemetrySpec{
					Variables: []string{"ups.status", "ups.status"},
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.telemetry.variables[1]"))
		})

		It("Should reject non-semantic versions", func() {
			obj.Spec = powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "latest",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					DriverFamily: "snmp-ups",
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.version"))
		})
	})

})
