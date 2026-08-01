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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

const (
	shutdownFlowTestResourceName         = "test-shutdownflow"
	shutdownFlowTestUPSName              = "test-resolver-ups"
	shutdownFlowTestSwitchName           = "test-resolver-switch"
	shutdownFlowTestNodeInventoryName    = "test-resolver-node-inventory"
	shutdownFlowTestNodeName             = "test-resolver-node"
	shutdownFlowTestFeedsEdgeName        = "test-resolver-ups-feeds-node"
	shutdownFlowTestCarriesEdgeName      = "test-resolver-switch-carries-node"
	shutdownFlowTestDriverProfileName    = "test-resolver-snmp-profile"
	shutdownFlowTestUniversalProfileName = "test-resolver-universal-profile"
	shutdownFlowTestInvalidEdgeName      = "test-resolver-invalid-edge"
)

var _ = Describe("ShutdownFlow Controller", func() {
	Context("When reconciling a resource", func() {
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: shutdownFlowTestResourceName}
		appPhase := int32(10)
		dbPhase := int32(20)

		BeforeEach(func() {
			cleanupShutdownFlowResolverFixture(ctx)
			Expect(createShutdownFlowResolverFixture(ctx)).To(Succeed())

			By("creating the custom resource for the Kind ShutdownFlow")
			resource := &powerv1alpha1.ShutdownFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name: shutdownFlowTestResourceName,
				},
				Spec: powerv1alpha1.ShutdownFlowSpec{
					Mode: powerv1alpha1.ShutdownFlowModeDryRun,
					Triggers: []powerv1alpha1.ShutdownTrigger{
						{
							Type: powerv1alpha1.ShutdownTriggerOnBattery,
							PowerDomains: []string{
								"rack-a",
							},
						},
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
		})

		AfterEach(func() {
			cleanupShutdownFlowResolverFixture(ctx)
		})

		It("should successfully reconcile the resource against declarative inventory", func() {
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
			degraded := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionFalse))
			Expect(resource.Status.CompiledSteps).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves[0].Groups).To(ConsistOf("applications"))
			Expect(resource.Status.CompiledWaves[1].Groups).To(ConsistOf("databases"))
			Expect(resource.Status.ConfigHash).NotTo(BeEmpty())
			Expect(resource.Status.ResolvedInputHash).NotTo(BeEmpty())
			Expect(resource.Status.TopologyHash).NotTo(BeEmpty())
			Expect(resource.Status.InventoryEntityCount).To(Equal(int32(3)))
			Expect(resource.Status.InventoryEdgeCount).To(Equal(int32(2)))
			Expect(resource.Status.CapabilityMatchCount).To(Equal(int32(1)))
		})

		It("should change plan identity when capability profiles change", func() {
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
			firstConfigHash := resource.Status.ConfigHash
			firstResolvedInputHash := resource.Status.ResolvedInputHash

			profile := &powerv1alpha1.UPSCapabilityProfile{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestDriverProfileName}, profile)).To(Succeed())
			profile.Spec.Version = "1.0.1"
			Expect(k8sClient.Update(ctx, profile)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.ResolvedInputHash).NotTo(Equal(firstResolvedInputHash))
			Expect(resource.Status.ConfigHash).NotTo(Equal(firstConfigHash))
		})

		It("should block compiled status when declarative inventory is rejected", func() {
			badEdge := &powerv1alpha1.PowerInventoryEdge{
				ObjectMeta: metav1.ObjectMeta{
					Name: shutdownFlowTestInvalidEdgeName,
				},
				Spec: powerv1alpha1.PowerInventoryEdgeSpec{
					From: powerv1alpha1.PowerInventoryEntityReference{
						Kind: powerv1alpha1.PowerInventoryEntityUPSDevice,
						Name: shutdownFlowTestUPSName,
					},
					To: powerv1alpha1.PowerInventoryEntityReference{
						Kind: powerv1alpha1.PowerInventoryEntityNode,
						Name: "missing-node",
					},
					Relation: powerv1alpha1.PowerInventoryEdgeFeeds,
					Input:    "psu-a",
				},
			}
			Expect(k8sClient.Create(ctx, badEdge)).To(Succeed())

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
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal("UnknownEdgeEndpoint"))
			Expect(resource.Status.Phase).To(Equal(powerv1alpha1.ShutdownFlowPhaseError))
			Expect(resource.Status.CompiledSteps).To(BeEmpty())
			Expect(resource.Status.ConfigHash).To(BeEmpty())
			Expect(resource.Status.ResolvedInputHash).To(BeEmpty())
			Expect(resource.Status.TopologyHash).To(BeEmpty())
		})

	})
})

func createShutdownFlowResolverFixture(ctx context.Context) error {
	port := int32(161)
	objects := []client.Object{
		&powerv1alpha1.UPSDevice{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestUPSName},
			Spec: powerv1alpha1.UPSDeviceSpec{
				Driver: "snmp-ups",
				Endpoint: &powerv1alpha1.UPSEndpointSpec{
					Host: "ups.example.net",
					Port: &port,
				},
				PowerDomains: []string{"rack-a"},
			},
		},
		&powerv1alpha1.PowerInfrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestSwitchName},
			Spec: powerv1alpha1.PowerInfrastructureSpec{
				Class: powerv1alpha1.PowerInfrastructureClassSwitch,
			},
		},
		&powerv1alpha1.PowerInventoryNode{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestNodeInventoryName},
			Spec: powerv1alpha1.PowerInventoryNodeSpec{
				NodeName: shutdownFlowTestNodeName,
			},
		},
		&powerv1alpha1.PowerInventoryEdge{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestFeedsEdgeName},
			Spec: powerv1alpha1.PowerInventoryEdgeSpec{
				From: powerv1alpha1.PowerInventoryEntityReference{
					Kind: powerv1alpha1.PowerInventoryEntityUPSDevice,
					Name: shutdownFlowTestUPSName,
				},
				To: powerv1alpha1.PowerInventoryEntityReference{
					Kind: powerv1alpha1.PowerInventoryEntityNode,
					Name: shutdownFlowTestNodeName,
				},
				Relation: powerv1alpha1.PowerInventoryEdgeFeeds,
				Input:    "psu-a",
			},
		},
		&powerv1alpha1.PowerInventoryEdge{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestCarriesEdgeName},
			Spec: powerv1alpha1.PowerInventoryEdgeSpec{
				From: powerv1alpha1.PowerInventoryEntityReference{
					Kind: powerv1alpha1.PowerInventoryEntityPowerInfrastructure,
					Name: shutdownFlowTestSwitchName,
				},
				To: powerv1alpha1.PowerInventoryEntityReference{
					Kind: powerv1alpha1.PowerInventoryEntityNode,
					Name: shutdownFlowTestNodeName,
				},
				Relation: powerv1alpha1.PowerInventoryEdgeCarries,
			},
		},
		&powerv1alpha1.UPSCapabilityProfile{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestDriverProfileName},
			Spec: powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "1.0.0",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					DriverFamily: "snmp-ups",
				},
				Telemetry: powerv1alpha1.UPSCapabilityTelemetrySpec{
					Variables: []string{"battery.runtime", "ups.status"},
				},
			},
		},
		&powerv1alpha1.UPSCapabilityProfile{
			ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestUniversalProfileName},
			Spec: powerv1alpha1.UPSCapabilityProfileSpec{
				Version: "1.0.0",
				Selector: powerv1alpha1.UPSCapabilityProfileSelector{
					Universal: boolPtr(true),
				},
				Telemetry: powerv1alpha1.UPSCapabilityTelemetrySpec{
					Variables: []string{"ups.status"},
				},
			},
		},
	}
	for _, obj := range objects {
		if err := k8sClient.Create(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

func cleanupShutdownFlowResolverFixture(ctx context.Context) {
	objects := []client.Object{
		&powerv1alpha1.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestResourceName}},
		&powerv1alpha1.PowerInventoryEdge{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestInvalidEdgeName}},
		&powerv1alpha1.PowerInventoryEdge{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestFeedsEdgeName}},
		&powerv1alpha1.PowerInventoryEdge{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestCarriesEdgeName}},
		&powerv1alpha1.UPSCapabilityProfile{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestDriverProfileName}},
		&powerv1alpha1.UPSCapabilityProfile{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestUniversalProfileName}},
		&powerv1alpha1.PowerInventoryNode{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestNodeInventoryName}},
		&powerv1alpha1.PowerInfrastructure{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestSwitchName}},
		&powerv1alpha1.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestUPSName}},
	}
	for _, obj := range objects {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
	}
}

func boolPtr(value bool) *bool {
	return &value
}
