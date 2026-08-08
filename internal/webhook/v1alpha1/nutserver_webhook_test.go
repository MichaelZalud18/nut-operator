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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("NUTServer Webhook", func() {
	var (
		obj       *powerv1alpha1.NUTServer
		oldObj    *powerv1alpha1.NUTServer
		validator NUTServerCustomValidator
		defaulter NUTServerCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.NUTServer{}
		oldObj = &powerv1alpha1.NUTServer{}
		validator = NUTServerCustomValidator{}
		defaulter = NUTServerCustomDefaulter{}
	})

	Context("When creating NUTServer under Defaulting Webhook", func() {
		It("Should default to a secure cluster-local NUT server", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.Replicas).NotTo(BeNil())
			Expect(*obj.Spec.Replicas).To(Equal(int32(1)))
			Expect(obj.Spec.Service.Type).To(Equal(corev1.ServiceTypeClusterIP))
			Expect(obj.Spec.Service.Port).NotTo(BeNil())
			Expect(*obj.Spec.Service.Port).To(Equal(int32(3493)))
			Expect(obj.Spec.Auth.Mode).To(Equal(powerv1alpha1.NUTAuthOperatorManaged))
			Expect(*obj.Spec.Auth.PerNodeUsers).To(BeTrue())
			Expect(obj.Spec.TLS.Mode).To(Equal(powerv1alpha1.NUTTLSRequired))
			Expect(*obj.Spec.TLS.VerifyClientCertificates).To(BeTrue())
			Expect(obj.Spec.Config.ListenAddress).To(Equal("0.0.0.0"))
			Expect(obj.Spec.Placement.PriorityClassName).To(Equal("system-cluster-critical"))
		})

		It("Should not override an explicitly set priority class", func() {
			obj.Spec.Placement.PriorityClassName = "custom-priority"

			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.Placement.PriorityClassName).To(Equal("custom-priority"))
		})
	})

	Context("When creating or updating NUTServer under Validating Webhook", func() {
		It("Should admit a mTLS-enabled server with a selector", func() {
			obj.Spec = secureNUTServerSpec()

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject missing device selection", func() {
			obj.Spec = secureNUTServerSpec()
			obj.Spec.DeviceSelector = nil

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.deviceRefs"))
		})

		It("Should reject existing-secret auth without a Secret reference", func() {
			obj.Spec = secureNUTServerSpec()
			obj.Spec.Auth.Mode = powerv1alpha1.NUTAuthExistingSecret

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.auth.existingSecretRef"))
		})

		It("Should reject required TLS without a client CA when client verification is enabled", func() {
			obj.Spec = secureNUTServerSpec()
			obj.Spec.TLS.ClientCARef = nil
			obj.Spec.TLS.VerifyClientCertificates = ptrBool(true)

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.tls.clientCARef"))
		})

		// Unset means off, matching the CRD default. The operator does not issue client
		// certificates to upsmon, so a defaulted-on CERTREQUEST would lock out every agent.
		It("Should accept required TLS without a client CA when client verification is unset", func() {
			obj.Spec = secureNUTServerSpec()
			obj.Spec.TLS.ClientCARef = nil
			obj.Spec.TLS.VerifyClientCertificates = nil

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject a server CA reference when TLS is disabled", func() {
			obj.Spec = secureNUTServerSpec()
			obj.Spec.TLS = powerv1alpha1.NUTTLSSpec{
				Mode: powerv1alpha1.NUTTLSDisabled,
				ServerCARef: &powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "rack-a-nut-server-ca",
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.tls.serverCARef"))
		})

		It("Should reject a replica count other than 1", func() {
			obj.Spec = secureNUTServerSpec()
			replicas := int32(2)
			obj.Spec.Replicas = &replicas

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.replicas"))
		})

		It("Should admit an explicit replica count of 1", func() {
			obj.Spec = secureNUTServerSpec()
			replicas := int32(1)
			obj.Spec.Replicas = &replicas

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject a reserved namespace as the operand namespace", func() {
			obj.Spec = secureNUTServerSpec()
			obj.Spec.Namespace = "kube-system"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.namespace"))
			Expect(err.Error()).To(ContainSubstring("reserved"))
		})
	})
})

func secureNUTServerSpec() powerv1alpha1.NUTServerSpec {
	return powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"power.zalud.io/domain": "rack-a"},
		},
		Auth: powerv1alpha1.NUTAuthSpec{
			Mode:         powerv1alpha1.NUTAuthOperatorManaged,
			PerNodeUsers: ptrBool(true),
		},
		TLS: powerv1alpha1.NUTTLSSpec{
			Mode: powerv1alpha1.NUTTLSRequired,
			ServerCertificateRef: &powerv1alpha1.NamespacedNameReference{
				Namespace: "power-system",
				Name:      "rack-a-nut-server-tls",
			},
			ServerCARef: &powerv1alpha1.NamespacedNameReference{
				Namespace: "power-system",
				Name:      "rack-a-nut-server-ca",
			},
		},
	}
}
