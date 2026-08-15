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
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("ShutdownHook Webhook", func() {
	var (
		obj       *powerv1alpha1.ShutdownHook
		oldObj    *powerv1alpha1.ShutdownHook
		validator ShutdownHookCustomValidator
		defaulter ShutdownHookCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.ShutdownHook{}
		oldObj = &powerv1alpha1.ShutdownHook{}
		validator = ShutdownHookCustomValidator{}
		defaulter = ShutdownHookCustomDefaulter{}
	})

	Context("When creating ShutdownHook under Defaulting Webhook", func() {
		It("Should default to HTTP POST with a bounded advisory failure policy", func() {
			obj.Spec.Invocation.HTTP = &powerv1alpha1.ShutdownHookHTTPSpec{URL: "https://hooks.example.test/power"}

			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.Invocation.Transport).To(Equal(powerv1alpha1.ShutdownHookTransportHTTP))
			Expect(obj.Spec.Invocation.HTTP.Method).To(Equal("POST"))
			Expect(obj.Spec.Invocation.Timeout.Duration).To(Equal(10 * time.Second))
			Expect(obj.Spec.FailurePolicy).To(Equal(powerv1alpha1.ShutdownHookFailureMarkDegraded))
		})
	})

	Context("When creating or updating ShutdownHook under Validating Webhook", func() {
		It("Should admit an HTTP hook with Secret-backed Authorization", func() {
			obj.ObjectMeta = metav1.ObjectMeta{Name: "flush", Namespace: "storage"}
			obj.Spec = powerv1alpha1.ShutdownHookSpec{
				Invocation: powerv1alpha1.ShutdownHookInvocationSpec{
					Transport: powerv1alpha1.ShutdownHookTransportHTTP,
					Timeout:   &metav1.Duration{Duration: 5 * time.Second},
					HTTP: &powerv1alpha1.ShutdownHookHTTPSpec{
						URL: "https://hooks.example.test/power/flush",
						SecretHeaders: []powerv1alpha1.ShutdownHookHTTPSecretHeader{{
							Name: "Authorization",
							ValueFrom: powerv1alpha1.SecretKeyReference{
								Namespace: "storage",
								Name:      "hook-auth",
								Key:       "authorization",
							},
						}},
					},
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)

			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject inline sensitive headers", func() {
			obj.ObjectMeta = metav1.ObjectMeta{Name: "flush", Namespace: "storage"}
			obj.Spec.Invocation = powerv1alpha1.ShutdownHookInvocationSpec{
				Transport: powerv1alpha1.ShutdownHookTransportHTTP,
				HTTP: &powerv1alpha1.ShutdownHookHTTPSpec{
					URL:     "https://hooks.example.test/power/flush",
					Headers: []powerv1alpha1.ShutdownHookHTTPHeader{{Name: "Authorization", Value: "Bearer nope"}},
				},
			}

			_, err := validator.ValidateCreate(ctx, obj)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("secretHeaders"))
		})

		It("Should require the active transport body", func() {
			obj.ObjectMeta = metav1.ObjectMeta{Name: "flush", Namespace: "storage"}
			obj.Spec.Invocation = powerv1alpha1.ShutdownHookInvocationSpec{
				Transport: powerv1alpha1.ShutdownHookTransportKubernetesObject,
				HTTP:      &powerv1alpha1.ShutdownHookHTTPSpec{URL: "https://hooks.example.test/power/flush"},
			}

			_, err := validator.ValidateCreate(ctx, obj)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("kubernetesObject"))
		})

		It("Should admit a raw Kubernetes object hook", func() {
			obj.ObjectMeta = metav1.ObjectMeta{Name: "flush", Namespace: "storage"}
			raw, err := json.Marshal(map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"namespace": "storage",
					"name":      "flush-request",
				},
			})
			Expect(err).NotTo(HaveOccurred())
			obj.Spec.Invocation = powerv1alpha1.ShutdownHookInvocationSpec{
				Transport: powerv1alpha1.ShutdownHookTransportKubernetesObject,
				KubernetesObject: &powerv1alpha1.ShutdownHookKubernetesObjectSpec{
					Object: runtime.RawExtension{Raw: raw},
				},
			}

			_, err = validator.ValidateCreate(ctx, obj)

			Expect(err).NotTo(HaveOccurred())
		})

		It("Should validate updates the same way as creates", func() {
			obj.ObjectMeta = metav1.ObjectMeta{Name: "flush", Namespace: "storage"}
			obj.Spec.Invocation = powerv1alpha1.ShutdownHookInvocationSpec{
				Transport: powerv1alpha1.ShutdownHookTransportHTTP,
				HTTP:      &powerv1alpha1.ShutdownHookHTTPSpec{URL: "ftp://hooks.example.test/power"},
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ftp"))
		})
	})
})
