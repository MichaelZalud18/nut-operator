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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("PowerManagementCluster Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		powermanagementcluster := &powerv1alpha1.PowerManagementCluster{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind PowerManagementCluster")
			err := k8sClient.Get(ctx, typeNamespacedName, powermanagementcluster)
			if err != nil && errors.IsNotFound(err) {
				resource := &powerv1alpha1.PowerManagementCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: powerv1alpha1.PowerManagementClusterSpec{
						OperandNamespace: &powerv1alpha1.OperandNamespaceSpec{
							Name: "power-system",
						},
						Storage: powerv1alpha1.PowerStorageSpec{
							Mode: powerv1alpha1.PowerStorageDisabled,
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &powerv1alpha1.PowerManagementCluster{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance PowerManagementCluster")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &PowerManagementClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.PowerManagementCluster{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			condition := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.Storage.Ready).To(BeTrue())
		})
	})

	Context("When evaluating CNPG storage", func() {
		It("reports a healthy referenced CNPG cluster as ready", func() {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(cnpgClusterGVK, &unstructured.Unstructured{})
			cnpgCluster := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "postgresql.cnpg.io/v1",
				"kind":       "Cluster",
				"metadata": map[string]any{
					"namespace": "power-data",
					"name":      "cluster-power-audit",
				},
				"spec": map[string]any{
					"instances": int64(3),
				},
				"status": map[string]any{
					"phase":          "Cluster in healthy state",
					"readyInstances": int64(3),
				},
			}}
			reconciler := &PowerManagementClusterReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(cnpgCluster).Build(),
				Scheme: scheme,
			}
			cluster := &powerv1alpha1.PowerManagementCluster{
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageCNPG,
						CNPG: &powerv1alpha1.CNPGStorageSpec{
							ClusterRef: powerv1alpha1.NamespacedNameReference{
								Namespace: "power-data",
								Name:      "cluster-power-audit",
							},
						},
					},
				},
			}

			status, ready, reason, message := reconciler.evaluateStorage(context.Background(), cluster, accepted("contract accepted"))

			Expect(ready).To(BeTrue())
			Expect(reason).To(Equal("CNPGClusterReady"))
			Expect(status.Ready).To(BeTrue())
			Expect(status.Mode).To(Equal(powerv1alpha1.PowerStorageCNPG))
			Expect(message).To(ContainSubstring("3/3 ready instances"))
		})

		It("reports a missing referenced CNPG cluster as not ready", func() {
			scheme := runtime.NewScheme()
			scheme.AddKnownTypeWithName(cnpgClusterGVK, &unstructured.Unstructured{})
			reconciler := &PowerManagementClusterReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				Scheme: scheme,
			}
			cluster := &powerv1alpha1.PowerManagementCluster{
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageCNPG,
						CNPG: &powerv1alpha1.CNPGStorageSpec{
							ClusterRef: powerv1alpha1.NamespacedNameReference{
								Namespace: "power-data",
								Name:      "cluster-missing",
							},
						},
					},
				},
			}

			status, ready, reason, message := reconciler.evaluateStorage(context.Background(), cluster, accepted("contract accepted"))

			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("CNPGClusterNotFound"))
			Expect(status.Ready).To(BeFalse())
			Expect(message).To(ContainSubstring("was not found"))
		})

		It("maps CNPG cluster events to referencing PowerManagementClusters", func() {
			scheme := runtime.NewScheme()
			Expect(powerv1alpha1.AddToScheme(scheme)).To(Succeed())
			scheme.AddKnownTypeWithName(cnpgClusterGVK, &unstructured.Unstructured{})
			referencingCluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "power-alpha",
					Name:      "alpha-cnpg",
				},
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageCNPG,
						CNPG: &powerv1alpha1.CNPGStorageSpec{
							ClusterRef: powerv1alpha1.NamespacedNameReference{
								Namespace: "power-alpha",
								Name:      "cluster-nut-operator-alpha",
							},
						},
					},
				},
			}
			ignoredCluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "power-alpha",
					Name:      "other-cnpg",
				},
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageCNPG,
						CNPG: &powerv1alpha1.CNPGStorageSpec{
							ClusterRef: powerv1alpha1.NamespacedNameReference{
								Namespace: "power-alpha",
								Name:      "cluster-other",
							},
						},
					},
				},
			}
			reconciler := &PowerManagementClusterReconciler{
				Client: fake.NewClientBuilder().
					WithScheme(scheme).
					WithIndex(&powerv1alpha1.PowerManagementCluster{}, powerManagementClusterCNPGRefField, indexPowerManagementClusterByCNPGCluster).
					WithObjects(referencingCluster, ignoredCluster).
					Build(),
				Scheme: scheme,
			}
			cnpgCluster := &unstructured.Unstructured{}
			cnpgCluster.SetGroupVersionKind(cnpgClusterGVK)
			cnpgCluster.SetNamespace("power-alpha")
			cnpgCluster.SetName("cluster-nut-operator-alpha")

			requests := reconciler.mapCNPGClusterToPowerManagementClusters(context.Background(), cnpgCluster)

			Expect(requests).To(HaveLen(1))
			Expect(requests[0].NamespacedName).To(Equal(types.NamespacedName{Namespace: "power-alpha", Name: "alpha-cnpg"}))
		})
	})
})
