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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("ShutdownFlow Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		shutdownflow := &powerv1alpha1.ShutdownFlow{}
		appPhase := int32(10)
		dbPhase := int32(20)

		BeforeEach(func() {
			By("creating the custom resource for the Kind ShutdownFlow")
			err := k8sClient.Get(ctx, typeNamespacedName, shutdownflow)
			if err != nil && errors.IsNotFound(err) {
				resource := &powerv1alpha1.ShutdownFlow{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: powerv1alpha1.ShutdownFlowSpec{
						Mode: powerv1alpha1.ShutdownFlowModeDryRun,
						Triggers: []powerv1alpha1.ShutdownTrigger{
							{Type: powerv1alpha1.ShutdownTriggerOnBattery},
						},
						Groups: []powerv1alpha1.ShutdownGroup{
							{
								Name:   "applications",
								Action: powerv1alpha1.ShutdownStepScaleWorkload,
								Before: []string{"databases"},
								Phase:  &appPhase,
							},
							{
								Name:   "databases",
								Action: powerv1alpha1.ShutdownStepScaleWorkload,
								Phase:  &dbPhase,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &powerv1alpha1.ShutdownFlow{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance ShutdownFlow")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &ShutdownFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			condition := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.CompiledSteps).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves[0].Groups).To(ConsistOf("applications"))
			Expect(resource.Status.CompiledWaves[1].Groups).To(ConsistOf("databases"))
			Expect(resource.Status.ConfigHash).NotTo(BeEmpty())
		})
	})
})
