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

var _ = Describe("UPSDevice Webhook", func() {
	var (
		obj       *powerv1alpha1.UPSDevice
		oldObj    *powerv1alpha1.UPSDevice
		validator UPSDeviceCustomValidator
		defaulter UPSDeviceCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.UPSDevice{}
		oldObj = &powerv1alpha1.UPSDevice{}
		validator = UPSDeviceCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = UPSDeviceCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	AfterEach(func() {
	})

	Context("When creating UPSDevice under Defaulting Webhook", func() {
		It("Should default upstream NUT relay fields", func() {
			obj.Spec.UpstreamNUT = &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-tower.example.net",
				UPSName: "ups",
			}

			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.UpstreamNUT.Port).NotTo(BeNil())
			Expect(*obj.Spec.UpstreamNUT.Port).To(Equal(int32(3493)))
			Expect(obj.Spec.UpstreamNUT.StrictStart).NotTo(BeNil())
			Expect(*obj.Spec.UpstreamNUT.StrictStart).To(BeTrue())
			Expect(obj.Spec.UpstreamNUT.Auth.Mode).To(Equal(powerv1alpha1.UPSUpstreamNUTAuthNone))
		})
	})

	Context("When creating or updating UPSDevice under Validating Webhook", func() {
		It("Should admit a direct network UPS driver", func() {
			obj.Spec.Driver = "snmp-ups"
			obj.Spec.Endpoint = &powerv1alpha1.UPSEndpointSpec{Host: "ups-rack-a.example.net"}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject a local USB UPS driver", func() {
			obj.Spec.Driver = "usbhid-ups"
			obj.Spec.Endpoint = &powerv1alpha1.UPSEndpointSpec{Host: "ups-rack-a.example.net"}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.driver"))
			// F-103: the rejection used to hand field.NotSupported the unsupported list, so it
			// named the rejected driver as a supported value. The message must state the scope
			// boundary and must not offer the driver back to the user.
			Expect(err.Error()).To(ContainSubstring("USB and serial attachment is out of scope"))
			Expect(err.Error()).NotTo(ContainSubstring("supported values"))
		})

		It("Should admit an upstream NUT relay device", func() {
			obj.Spec.UpstreamNUT = &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-2u.example.net",
				UPSName: "ups",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject upstream NUT with direct endpoint fields", func() {
			obj.Spec.Driver = "snmp-ups"
			obj.Spec.Endpoint = &powerv1alpha1.UPSEndpointSpec{Host: "ups-rack-a.example.net"}
			obj.Spec.UpstreamNUT = &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-2u.example.net",
				UPSName: "ups",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.driver"))
			Expect(err.Error()).To(ContainSubstring("spec.endpoint"))
		})

		It("Should reject reserved upstream NUT driver options", func() {
			obj.Spec.UpstreamNUT = &powerv1alpha1.UPSUpstreamNUTSpec{
				Host:    "ups-2u.example.net",
				UPSName: "ups",
			}
			obj.Spec.DriverOptions = map[string]string{
				"authconf": "default",
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.driverOptions[authconf]"))
		})

		// F-85: the reserved-key check lived only on the upstreamNUT path, so this shape was
		// admitted and then rendered a second `driver =` line that ups.conf resolves in favor of
		// the unvetted one -- an allowlist rejection turned into a runtime failure.
		It("Should reject a driver override in driverOptions on a non-upstream device", func() {
			obj.Spec.Driver = "snmp-ups"
			obj.Spec.Endpoint = &powerv1alpha1.UPSEndpointSpec{Host: "ups-rack-a.example.net"}
			obj.Spec.DriverOptions = map[string]string{
				"driver": "usbhid-ups",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.driverOptions[driver]"))
		})
	})

})
