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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

const (
	shutdownFlowTestResourceName         = "test-shutdownflow"
	shutdownFlowTestUPSName              = "test-resolver-ups"
	shutdownFlowTestSwitchName           = "test-resolver-switch"
	shutdownFlowTestPowerClusterName     = "test-resolver-power-cluster"
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

		It("evaluates trigger eligibility from UPSDevice telemetry status", func() {
			observedAt := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
			pollTime := metav1.NewTime(observedAt.Add(-10 * time.Second))
			device := &powerv1alpha1.UPSDevice{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestUPSName}, device)).To(Succeed())
			device.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery
			device.Status.LastPollTime = &pollTime
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

			controllerReconciler := &ShutdownFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock: func() time.Time {
					return observedAt
				},
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.TriggerEvaluation).NotTo(BeNil())
			Expect(resource.Status.TriggerEvaluation.Eligible).To(BeTrue())
			Expect(resource.Status.TriggerEvaluation.Reason).To(Equal("TriggerEligible"))
			Expect(resource.Status.TriggerEvaluation.SelectedUPSDevices).To(ConsistOf(shutdownFlowTestUPSName))
			Expect(resource.Status.TriggerEvaluation.PlanConfigHash).To(Equal(resource.Status.ConfigHash))
			triggerEligible := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionTriggerEligible)
			Expect(triggerEligible).NotTo(BeNil())
			Expect(triggerEligible.Status).To(Equal(metav1.ConditionTrue))
		})

		It("persists trigger hold state and requeues until the hold duration elapses", func() {
			startedAt := time.Date(2026, 8, 2, 10, 30, 0, 0, time.UTC)
			holdDuration := metav1.Duration{Duration: 5 * time.Minute}
			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Triggers[0].For = &holdDuration
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			pollTime := metav1.NewTime(startedAt.Add(-10 * time.Second))
			device := &powerv1alpha1.UPSDevice{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestUPSName}, device)).To(Succeed())
			device.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery
			device.Status.LastPollTime = &pollTime
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

			controllerReconciler := &ShutdownFlowReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Clock: func() time.Time {
					return startedAt
				},
			}
			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.TriggerEvaluation).NotTo(BeNil())
			Expect(resource.Status.TriggerEvaluation.Eligible).To(BeFalse())
			Expect(resource.Status.TriggerEvaluation.Reason).To(Equal("TriggerHoldPending"))
			Expect(resource.Status.TriggerHoldStates).To(HaveLen(1))
			Expect(resource.Status.TriggerHoldStates[0].StartedAt.Time).To(BeTemporally("==", startedAt))

			controllerReconciler.Clock = func() time.Time {
				return startedAt.Add(6 * time.Minute)
			}
			result, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.TriggerEvaluation.Eligible).To(BeTrue())
			Expect(resource.Status.TriggerEvaluation.Decisions[0].HoldStartedAt.Time).To(BeTemporally("==", startedAt))
		})

		It("records dry-run execution once per active trigger episode", func() {
			observedAt := time.Date(2026, 8, 2, 11, 0, 0, 0, time.UTC)
			cluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestPowerClusterName},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			cluster.Status.Storage = powerv1alpha1.StorageStatus{
				Mode:  powerv1alpha1.PowerStorageExternalPostgres,
				Ready: true,
			}
			Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())

			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.ManagementClusterRef = &powerv1alpha1.ObjectNameReference{Name: shutdownFlowTestPowerClusterName}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			pollTime := metav1.NewTime(observedAt.Add(-10 * time.Second))
			device := &powerv1alpha1.UPSDevice{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestUPSName}, device)).To(Succeed())
			device.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery
			device.Status.LastPollTime = &pollTime
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

			store := &fakeAuditStore{}
			controllerReconciler := &ShutdownFlowReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				StorageConnector: &fakeAuditConnector{store: store},
				Clock: func() time.Time {
					return observedAt
				},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(store.shutdownFlowExecutions).To(HaveLen(2))
			Expect(store.executionWaves).To(HaveLen(4))
			Expect(store.executionGroups).To(HaveLen(2))
			Expect(store.actionAttempts).To(HaveLen(2))

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.LastExecution).NotTo(BeNil())
			Expect(resource.Status.LastExecution.Phase).To(Equal(powerv1alpha1.ShutdownExecutionPhaseCompleted))
			Expect(resource.Status.LastExecution.TriggerActive).To(BeTrue())
			firstKey := resource.Status.LastExecution.DeduplicationKey
			Expect(firstKey).NotTo(BeEmpty())
			executionReady := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionExecutionReady)
			Expect(executionReady).NotTo(BeNil())
			Expect(executionReady.Status).To(Equal(metav1.ConditionTrue))

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(store.shutdownFlowExecutions).To(HaveLen(2))
			Expect(store.actionAttempts).To(HaveLen(2))
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.Phase).To(Equal(powerv1alpha1.ShutdownFlowPhaseCompleted))
			Expect(resource.Status.LastExecution.Reason).To(Equal("AlreadyExecuted"))

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestUPSName}, device)).To(Succeed())
			device.Status.Phase = powerv1alpha1.UPSDevicePhaseOnline
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.LastExecution.TriggerActive).To(BeFalse())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestUPSName}, device)).To(Succeed())
			device.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery
			Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(store.shutdownFlowExecutions).To(HaveLen(4))
			Expect(store.actionAttempts).To(HaveLen(4))
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.LastExecution.DeduplicationKey).To(Equal(firstKey))
			Expect(resource.Status.LastExecution.TriggerActive).To(BeTrue())
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

		It("records compilation audit through the referenced management cluster", func() {
			scheme := runtime.NewScheme()
			Expect(powerv1alpha1.AddToScheme(scheme)).To(Succeed())
			store := &fakeAuditStore{}
			fixed := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
			cluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test-power"},
				Status: powerv1alpha1.PowerManagementClusterStatus{
					Storage: powerv1alpha1.StorageStatus{
						Mode:  powerv1alpha1.PowerStorageExternalPostgres,
						Ready: true,
					},
				},
			}
			reconciler := &ShutdownFlowReconciler{
				Client:           fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(cluster).WithObjects(cluster).Build(),
				StorageConnector: &fakeAuditConnector{store: store},
				Clock: func() time.Time {
					return fixed
				},
			}
			flow := &powerv1alpha1.ShutdownFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-flow",
					Generation: 4,
				},
				Spec: powerv1alpha1.ShutdownFlowSpec{
					ManagementClusterRef: &powerv1alpha1.ObjectNameReference{Name: "test-power"},
					Groups: []powerv1alpha1.ShutdownGroup{{
						Name:   "applications",
						Action: powerv1alpha1.ShutdownStepScaleWorkload,
					}},
				},
				Status: powerv1alpha1.ShutdownFlowStatus{
					CompiledWaves: []powerv1alpha1.CompiledShutdownWave{{
						Index:  0,
						Groups: []string{"applications"},
					}},
				},
			}
			diagnostics := []resolver.Diagnostic{{
				Severity: resolver.DiagnosticWarning,
				Source:   resolver.DiagnosticSourceInventory,
				Reason:   "CommunicationPathUnmodeled",
				Subject:  "node/test-node",
				Message:  "communication path is not fully modeled",
			}}
			waves := []powerv1alpha1.CompiledShutdownWave{{
				Index:  0,
				Groups: []string{"applications"},
			}}
			triggerObservedAt := metav1.NewTime(fixed)
			triggerEvaluation := &powerv1alpha1.ShutdownTriggerEvaluationStatus{
				ObservedAt:          &triggerObservedAt,
				Mode:                powerv1alpha1.ShutdownFlowModeDryRun,
				Eligible:            true,
				Reason:              "TriggerEligible",
				MatchedTriggerCount: 1,
				SelectedUPSDevices:  []string{"ups-a"},
				PlanConfigHash:      "plan-hash-a",
				Decisions: []powerv1alpha1.ShutdownTriggerDecisionStatus{{
					TriggerID:          "trigger-000-onbattery",
					Type:               powerv1alpha1.ShutdownTriggerOnBattery,
					Matched:            true,
					Eligible:           true,
					Reason:             "TriggerEligible",
					SelectedUPSDevices: []string{"ups-a"},
				}},
			}
			bundle := resolver.StructuralBundle{
				Hash: "input-hash-a",
				CapabilityMatches: []capability.MatchResult{{
					DeviceID:       "ups-a",
					ProfileID:      "profile-a",
					ProfileVersion: "1.0.0",
					ProfileSource:  capability.ProfileSourceBundled,
					Tier:           capability.MatchTierDriverFamily,
				}},
			}

			err := reconciler.recordShutdownFlowAudit(
				context.Background(),
				flow,
				accepted("compiled"),
				diagnostics,
				bundle,
				waves,
				"plan-hash-a",
				triggerEvaluation,
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(store.shutdownFlowCompilations).To(HaveLen(1))
			compilation := store.shutdownFlowCompilations[0]
			Expect(compilation.ShutdownFlow).To(Equal("test-flow"))
			Expect(compilation.ResourceGeneration).To(Equal(int64(4)))
			Expect(compilation.ConfigHash).To(Equal("plan-hash-a"))
			Expect(compilation.InputHash).To(Equal("input-hash-a"))
			Expect(compilation.ObservedAt).To(Equal(fixed))
			Expect(compilation.CompiledWaves).To(Equal(waves))
			Expect(compilation.Diagnostics).To(HaveLen(1))
			Expect(compilation.Diagnostics[0].Source).To(Equal(resolver.DiagnosticSourceInventory))
			Expect(store.capabilityProfileMatches).To(HaveLen(1))
			Expect(store.capabilityProfileMatches[0].UPSDevice).To(Equal("ups-a"))
			Expect(store.capabilityProfileMatches[0].ProfileID).To(Equal("profile-a"))
			Expect(store.capabilityProfileMatches[0].ProfileSource).To(Equal(string(capability.ProfileSourceBundled)))
			Expect(store.shutdownFlowDecisions).To(HaveLen(1))
			decision := store.shutdownFlowDecisions[0]
			Expect(decision.ShutdownFlow).To(Equal("test-flow"))
			Expect(decision.TriggerType).To(Equal(string(powerv1alpha1.ShutdownTriggerOnBattery)))
			Expect(decision.Mode).To(Equal(string(powerv1alpha1.ShutdownFlowModeDryRun)))
			Expect(decision.Approved).To(BeTrue())
			Expect(decision.Decision).To(Equal("Eligible"))
			Expect(decision.Reason).To(Equal("TriggerEligible"))
			Expect(decision.SelectedUPSDevices).To(ConsistOf("ups-a"))
			Expect(decision.PlanConfigHash).To(Equal("plan-hash-a"))
			Expect(store.shutdownFlowExecutions).To(HaveLen(2))
			Expect(store.shutdownFlowExecutions[0].Phase).To(Equal("Running"))
			Expect(store.shutdownFlowExecutions[1].Phase).To(Equal("Completed"))
			Expect(store.executionWaves).To(HaveLen(2))
			Expect(store.executionGroups).To(HaveLen(1))
			Expect(store.actionAttempts).To(HaveLen(1))
			Expect(store.actionAttempts[0].Outcome).To(Equal("Simulated"))
			Expect(store.executorResumeStates).NotTo(BeEmpty())
			Expect(flow.Status.LastExecution).NotTo(BeNil())
			Expect(flow.Status.LastExecution.Phase).To(Equal(powerv1alpha1.ShutdownExecutionPhaseCompleted))
			Expect(flow.Status.LastExecution.TriggerActive).To(BeTrue())
			Expect(store.closeCalls).To(Equal(1))
		})

		It("records rejected compilation audit without requiring a plan hash", func() {
			scheme := runtime.NewScheme()
			Expect(powerv1alpha1.AddToScheme(scheme)).To(Succeed())
			store := &fakeAuditStore{}
			fixed := time.Date(2026, 8, 2, 9, 30, 0, 0, time.UTC)
			cluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{Name: "test-power"},
				Status: powerv1alpha1.PowerManagementClusterStatus{
					Storage: powerv1alpha1.StorageStatus{
						Mode:  powerv1alpha1.PowerStorageExternalPostgres,
						Ready: true,
					},
				},
			}
			reconciler := &ShutdownFlowReconciler{
				Client:           fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build(),
				StorageConnector: &fakeAuditConnector{store: store},
				Clock: func() time.Time {
					return fixed
				},
			}
			flow := &powerv1alpha1.ShutdownFlow{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-flow",
					Generation: 5,
				},
				Spec: powerv1alpha1.ShutdownFlowSpec{
					ManagementClusterRef: &powerv1alpha1.ObjectNameReference{Name: "test-power"},
				},
			}

			err := reconciler.recordShutdownFlowAudit(
				context.Background(),
				flow,
				rejected("PlannerRejected", "cycle detected"),
				nil,
				resolver.StructuralBundle{},
				nil,
				"",
				nil,
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(store.shutdownFlowCompilations).To(HaveLen(1))
			compilation := store.shutdownFlowCompilations[0]
			Expect(compilation.ShutdownFlow).To(Equal("test-flow"))
			Expect(compilation.ResourceGeneration).To(Equal(int64(5)))
			Expect(compilation.Accepted).To(BeFalse())
			Expect(compilation.ConfigHash).To(BeEmpty())
			Expect(compilation.InputHash).To(BeEmpty())
			Expect(compilation.ObservedAt).To(Equal(fixed))
			Expect(compilation.CompiledWaves).To(BeNil())
			Expect(compilation.Diagnostics).To(HaveLen(1))
			Expect(compilation.Diagnostics[0].Severity).To(Equal(resolver.DiagnosticError))
			Expect(compilation.Diagnostics[0].Reason).To(Equal("PlannerRejected"))
			Expect(store.capabilityProfileMatches).To(BeEmpty())
			Expect(store.shutdownFlowDecisions).To(BeEmpty())
			Expect(store.closeCalls).To(Equal(1))
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
		&powerv1alpha1.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestPowerClusterName}},
		&powerv1alpha1.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestUPSName}},
	}
	for _, obj := range objects {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
	}
}

func boolPtr(value bool) *bool {
	return &value
}
