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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	storageconfig "github.com/MichaelZalud18/nut-operator/internal/storage"
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

			// F-3: capture before reconciling so the metrics assertions below are delta-based, not
			// absolute -- other specs in this Describe reconcile the same-named resource and share the
			// same global collectors.
			compileAcceptedBefore := testutil.ToFloat64(metrics.ShutdownFlowCompileTotal.WithLabelValues(shutdownFlowTestResourceName, "Accepted"))
			compileDurationSamplesBefore := histogramSampleCount(metrics.ShutdownFlowCompileDurationSeconds.WithLabelValues(shutdownFlowTestResourceName))
			triggerEvaluationsBefore := testutil.ToFloat64(metrics.ShutdownFlowTriggerEvaluationsTotal.WithLabelValues(shutdownFlowTestResourceName, "false"))

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

			By("confirming the reconcile actually recorded metrics, not just compiled against them")
			Expect(testutil.ToFloat64(metrics.ShutdownFlowCompileTotal.WithLabelValues(shutdownFlowTestResourceName, "Accepted"))).To(Equal(compileAcceptedBefore + 1))
			Expect(histogramSampleCount(metrics.ShutdownFlowCompileDurationSeconds.WithLabelValues(shutdownFlowTestResourceName))).To(Equal(compileDurationSamplesBefore + 1))
			Expect(testutil.ToFloat64(metrics.ShutdownFlowTriggerEvaluationsTotal.WithLabelValues(shutdownFlowTestResourceName, "false"))).To(Equal(triggerEvaluationsBefore + 1))
			Expect(testutil.ToFloat64(metrics.ShutdownFlowDegraded.WithLabelValues(shutdownFlowTestResourceName))).To(Equal(float64(0)))
			Expect(resource.Status.CompiledSteps).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves[0].Groups).To(ConsistOf("applications"))
			Expect(resource.Status.CompiledWaves[1].Groups).To(ConsistOf("databases"))
			Expect(resource.Status.PublishedArtifact).NotTo(BeNil())
			Expect(resource.Status.PublishedArtifact.Graph.Vertices).To(HaveLen(2))
			Expect(resource.Status.PublishedArtifact.Graph.Edges).To(HaveLen(1))
			Expect(resource.Status.PublishedArtifact.StartupWaves).To(HaveLen(2))
			Expect(resource.Status.PublishedArtifact.Explanations).NotTo(BeEmpty())
			Expect(resource.Status.PublishedArtifact.Diagrams.Mermaid).To(ContainSubstring("applications -->|Before| databases"))
			Expect(resource.Status.ConfigHash).NotTo(BeEmpty())
			Expect(resource.Status.ResolvedInputHash).NotTo(BeEmpty())
			Expect(resource.Status.TopologyHash).NotTo(BeEmpty())
			Expect(resource.Status.InventoryEntityCount).To(Equal(int32(3)))
			Expect(resource.Status.InventoryEdgeCount).To(Equal(int32(2)))
			Expect(resource.Status.CapabilityMatchCount).To(Equal(int32(1)))
		})

		It("tolerates the object advancing between Get and the status write (F-31)", func() {
			By("Reconciling through a client that simulates a concurrent external write landing between the reconciler's Get and its status write")
			controllerReconciler := &ShutdownFlowReconciler{
				Client: &resourceVersionRaceInjectingClient{Client: k8sClient, key: typeNamespacedName},
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
			Expect(resource.Annotations).To(HaveKeyWithValue("f31-race-test/external-write", "true"))
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
			// An eligible trigger means the flow is mid-outage, so it republishes on the EX-29
			// active cadence rather than waiting for the next watch event.
			Expect(result.RequeueAfter).To(Equal(adaptive.DefaultParameters().ActiveCadence))

			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.TriggerEvaluation).NotTo(BeNil())
			Expect(resource.Status.TriggerEvaluation.Eligible).To(BeTrue())
			Expect(resource.Status.TriggerEvaluation.Reason).To(Equal("TriggerEligible"))
			Expect(resource.Status.TriggerEvaluation.SelectedUPSDevices).To(ConsistOf(shutdownFlowTestUPSName))
			Expect(resource.Status.TriggerEvaluation.PlanConfigHash).To(Equal(resource.Status.ConfigHash))
			// EX-29: the heartbeat is republished every reconcile, so its absence means the
			// operator stopped rather than that nothing happened.
			Expect(resource.Status.LastPublishTime).NotTo(BeNil())
			Expect(resource.Status.LastPublishTime.Time).To(BeTemporally("==", observedAt))
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
			// The hold expires in five minutes, but a matched-and-holding trigger is mid-outage,
			// so the EX-29 heartbeat requeues sooner. The cadence is a ceiling on silence, never a
			// floor on action: it may only bring the next reconcile forward.
			Expect(result.RequeueAfter).To(Equal(adaptive.DefaultParameters().ActiveCadence))
			Expect(result.RequeueAfter).To(BeNumerically("<=", 5*time.Minute))

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
			// The hold is satisfied, so nothing schedules the next reconcile except the
			// heartbeat -- which is the point: an eligible flow must not go quiet.
			Expect(result.RequeueAfter).To(Equal(adaptive.DefaultParameters().ActiveCadence))

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
			Expect(resource.Status.PublishedArtifact).To(BeNil())
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
			publishedArtifact := &powerv1alpha1.PublishedPlannerArtifactStatus{
				Graph: powerv1alpha1.PlannerGraphStatus{
					Vertices: []powerv1alpha1.PlannerGraphVertexStatus{{
						ID:     "applications",
						Kind:   "ShutdownGroup",
						Action: "ScaleWorkload",
					}},
					Edges: []powerv1alpha1.PlannerGraphEdgeStatus{{
						ID:          "applications-before-databases",
						From:        "applications",
						To:          "databases",
						Relation:    "Before",
						Provenance:  "Declared",
						Explanation: "applications runs before databases because applications declares before: databases.",
					}},
				},
				StartupWaves: waves,
				Explanations: []powerv1alpha1.PlannerExplanationStatus{{
					ID:      "plan-compiled",
					Reason:  "PlanCompiled",
					Message: "compiled one test plan",
				}},
				Diagrams: powerv1alpha1.PlannerDiagramExportsStatus{
					Mermaid: "flowchart TD",
				},
			}

			err := reconciler.recordShutdownFlowAudit(
				context.Background(),
				flow,
				accepted("compiled"),
				diagnostics,
				nil,
				bundle,
				waves,
				publishedArtifact,
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
			Expect(compilation.DependencyGraph).To(Equal(publishedArtifact.Graph))
			Expect(compilation.StartupWaves).To(Equal(publishedArtifact.StartupWaves))
			Expect(compilation.Explanations).To(Equal(publishedArtifact.Explanations))
			Expect(compilation.DiagramExports).To(Equal(publishedArtifact.Diagrams))
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

		It("enumerates concrete executor targets from selectors", func() {
			scheme := runtime.NewScheme()
			Expect(powerv1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(appsv1.AddToScheme(scheme)).To(Succeed())
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			reconciler := &ShutdownFlowReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
						Name:   "apps",
						Labels: map[string]string{"power.example.com/shutdown-tier": "application"},
					}},
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
						Name:   "storage",
						Labels: map[string]string{"power.example.com/shutdown-tier": "storage"},
					}},
					&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
						Namespace: "apps",
						Name:      "web",
						Labels:    map[string]string{"app": "web"},
					}},
					&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
						Namespace: "storage",
						Name:      "postgres",
						Labels:    map[string]string{"app": "postgres"},
					}},
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{
						Name:   "node-a",
						Labels: map[string]string{"node-role": "worker"},
					}},
				).Build(),
			}
			flow := &powerv1alpha1.ShutdownFlow{
				ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
				Spec: powerv1alpha1.ShutdownFlowSpec{
					Groups: []powerv1alpha1.ShutdownGroup{
						{
							Name:   "applications",
							Action: powerv1alpha1.ShutdownStepScaleWorkload,
							Target: powerv1alpha1.ShutdownStepTarget{
								NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"power.example.com/shutdown-tier": "application"}},
							},
							Params: map[string]string{"replicas": "0"},
						},
						{
							Name:   "nodes",
							Action: powerv1alpha1.ShutdownStepCordonNodes,
							Target: powerv1alpha1.ShutdownStepTarget{
								NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"node-role": "worker"}},
							},
						},
						{
							Name:   "workflow",
							Action: powerv1alpha1.ShutdownStepRunWorkflow,
							Target: powerv1alpha1.ShutdownStepTarget{
								NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"power.example.com/shutdown-tier": "storage"}},
							},
						},
					},
				},
			}

			groups, err := reconciler.executorGroupsFromFlow(context.Background(), flow)

			Expect(err).NotTo(HaveOccurred())
			Expect(groups).To(HaveLen(3))
			Expect(groups[0].Params).To(HaveKeyWithValue("replicas", "0"))
			Expect(groups[0].SelectedTargets).To(ConsistOf(executorpkg.Target{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"}))
			Expect(groups[1].SelectedTargets).To(ConsistOf(executorpkg.Target{APIVersion: "v1", Kind: "Node", Name: "node-a"}))
			Expect(groups[2].SelectedTargets).To(ConsistOf(executorpkg.Target{APIVersion: "v1", Kind: "Namespace", Name: "storage"}))
		})

		It("maps NodePowerAgent coverage into AgentShutdown releases", func() {
			scheme := runtime.NewScheme()
			Expect(powerv1alpha1.AddToScheme(scheme)).To(Succeed())
			heartbeat := metav1.NewTime(time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC))
			agent := &powerv1alpha1.NodePowerAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-a-agents"},
				Spec: powerv1alpha1.NodePowerAgentSpec{
					NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a"}},
					// This spec covers the coverage-mapping logic, not telemetry-freshness gating (F-33)
					// -- covered separately below -- so disable the requirement rather than adding
					// unrelated NUTServer/UPSDevice fixtures here.
					Shutdown: powerv1alpha1.AgentShutdownSpec{RequireFreshTelemetry: boolPtr(false)},
				},
				Status: powerv1alpha1.NodePowerAgentStatus{
					SelectedNodes: []string{"node-a", "node-b"},
					NodeStatuses: []powerv1alpha1.NodePowerAgentNodeStatus{
						{
							NodeName:          "node-a",
							Ready:             true,
							PodName:           "agent-node-a",
							Reason:            "AgentPodReady",
							Message:           "ready NodePowerAgent pod is observed on this node",
							LastHeartbeatTime: &heartbeat,
						},
						{
							NodeName: "node-b",
							Ready:    false,
							Reason:   "AgentPodMissing",
							Message:  "no NodePowerAgent pod is currently observed on this selected node",
						},
					},
				},
			}
			Expect(corev1.AddToScheme(scheme)).To(Succeed())
			reconciler := &ShutdownFlowReconciler{
				// The clearance check selects pods by spec.nodeName, which the API server
				// resolves natively; the fake client needs the index declared.
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).
					WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
						return []string{object.(*corev1.Pod).Spec.NodeName}
					}).Build(),
			}
			flow := &powerv1alpha1.ShutdownFlow{
				ObjectMeta: metav1.ObjectMeta{Name: "test-flow"},
				Spec: powerv1alpha1.ShutdownFlowSpec{
					Groups: []powerv1alpha1.ShutdownGroup{{
						Name:   "nodes",
						Action: powerv1alpha1.ShutdownStepAgentShutdown,
						Target: powerv1alpha1.ShutdownStepTarget{
							AgentRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a-agents"}},
						},
					}},
				},
			}

			groups, err := reconciler.executorGroupsFromFlow(context.Background(), flow)

			Expect(err).NotTo(HaveOccurred())
			Expect(groups).To(HaveLen(1))
			Expect(groups[0].NodeReleases).To(HaveLen(2))
			releasesByNode := map[string]executorpkg.NodeRelease{}
			for _, release := range groups[0].NodeReleases {
				releasesByNode[release.NodeName] = release
			}
			Expect(releasesByNode["node-a"].NodePowerAgent).To(Equal("rack-a-agents"))
			Expect(releasesByNode["node-a"].SignalPath).To(Equal("/run/power-agent/shutdown.json"))
			Expect(releasesByNode["node-a"].SignalSecretNamespace).To(Equal("power-system"))
			Expect(releasesByNode["node-a"].SignalSecretName).To(Equal("rack-a-agents-node-signals"))
			// EX-9: an empty node clears. The proof is re-derived here rather than trusted
			// from the compiled plan, because placement moves between compile and execution.
			Expect(releasesByNode["node-a"].Cleared).To(BeTrue())
			Expect(releasesByNode["node-a"].SignalSecretKey).To(Equal("node-a.json"))
			Expect(releasesByNode["node-a"].AgentReady).To(BeTrue())
			Expect(releasesByNode["node-a"].ReadinessReason).To(Equal("AgentPodReady"))
			Expect(releasesByNode["node-a"].ReadinessMessage).To(Equal("ready NodePowerAgent pod is observed on this node"))
			Expect(releasesByNode["node-a"].PodName).To(Equal("agent-node-a"))
			Expect(releasesByNode["node-a"].LastHeartbeatTime).NotTo(BeNil())
			Expect(*releasesByNode["node-a"].LastHeartbeatTime).To(BeTemporally("==", heartbeat.Time))
			Expect(releasesByNode["node-a"].TelemetryFresh).To(BeTrue())
			Expect(releasesByNode["node-b"]).To(Equal(executorpkg.NodeRelease{
				NodeName:              "node-b",
				NodePowerAgent:        "rack-a-agents",
				SignalPath:            "/run/power-agent/shutdown.json",
				SignalSecretNamespace: "power-system",
				SignalSecretName:      "rack-a-agents-node-signals",
				SignalSecretKey:       "node-b.json",
				AgentReady:            false,
				ReadinessReason:       "AgentPodMissing",
				ReadinessMessage:      "no NodePowerAgent pod is currently observed on this selected node",
				TelemetryFresh:        true,
				// An empty node clears. Readiness and clearance are independent gates: this
				// node has no agent pod but also nothing left to lose.
				Cleared: true,
			}))
		})

		It("gates AgentShutdown releases on requireFreshTelemetry (F-33)", func() {
			scheme := runtime.NewScheme()
			Expect(powerv1alpha1.AddToScheme(scheme)).To(Succeed())
			freshDevice := &powerv1alpha1.UPSDevice{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-a-ups"},
				Status:     powerv1alpha1.UPSDeviceStatus{Phase: powerv1alpha1.UPSDevicePhaseOnline},
			}
			staleDevice := &powerv1alpha1.UPSDevice{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-b-ups"},
				Status:     powerv1alpha1.UPSDeviceStatus{Phase: powerv1alpha1.UPSDevicePhaseStale},
			}
			freshServer := &powerv1alpha1.NUTServer{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-a"},
				Status:     powerv1alpha1.NUTServerStatus{SelectedDevices: []string{"rack-a-ups"}},
			}
			staleServer := &powerv1alpha1.NUTServer{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-b"},
				Status:     powerv1alpha1.NUTServerStatus{SelectedDevices: []string{"rack-b-ups"}},
			}
			unknownServer := &powerv1alpha1.NUTServer{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-c"},
			}
			freshAgent := &powerv1alpha1.NodePowerAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-a-agents"},
				Spec: powerv1alpha1.NodePowerAgentSpec{
					NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a"}},
				},
				Status: powerv1alpha1.NodePowerAgentStatus{
					SelectedNodes: []string{"node-a"},
					NodeStatuses: []powerv1alpha1.NodePowerAgentNodeStatus{
						{NodeName: "node-a", Ready: true, Reason: "AgentPodReady"},
					},
				},
			}
			staleAgent := &powerv1alpha1.NodePowerAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-b-agents"},
				Spec: powerv1alpha1.NodePowerAgentSpec{
					NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-b"}},
				},
				Status: powerv1alpha1.NodePowerAgentStatus{
					SelectedNodes: []string{"node-b"},
					NodeStatuses: []powerv1alpha1.NodePowerAgentNodeStatus{
						{NodeName: "node-b", Ready: true, Reason: "AgentPodReady"},
					},
				},
			}
			unknownAgent := &powerv1alpha1.NodePowerAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-c-agents"},
				Spec: powerv1alpha1.NodePowerAgentSpec{
					NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-c"}},
				},
				Status: powerv1alpha1.NodePowerAgentStatus{
					SelectedNodes: []string{"node-c"},
					NodeStatuses: []powerv1alpha1.NodePowerAgentNodeStatus{
						{NodeName: "node-c", Ready: true, Reason: "AgentPodReady"},
					},
				},
			}
			optedOutAgent := &powerv1alpha1.NodePowerAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-b-agents-opted-out"},
				Spec: powerv1alpha1.NodePowerAgentSpec{
					NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-b"}},
					Shutdown:      powerv1alpha1.AgentShutdownSpec{RequireFreshTelemetry: boolPtr(false)},
				},
				Status: powerv1alpha1.NodePowerAgentStatus{
					SelectedNodes: []string{"node-d"},
					NodeStatuses: []powerv1alpha1.NodePowerAgentNodeStatus{
						{NodeName: "node-d", Ready: true, Reason: "AgentPodReady"},
					},
				},
			}
			reconciler := &ShutdownFlowReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
					freshDevice, staleDevice, freshServer, staleServer, unknownServer,
					freshAgent, staleAgent, unknownAgent, optedOutAgent,
				).Build(),
			}

			fresh, freshReason, err := reconciler.nodePowerAgentTelemetryFreshness(context.Background(), freshAgent)
			Expect(err).NotTo(HaveOccurred())
			Expect(fresh).To(BeTrue())
			Expect(freshReason).To(BeEmpty())

			stale, staleReason, err := reconciler.nodePowerAgentTelemetryFreshness(context.Background(), staleAgent)
			Expect(err).NotTo(HaveOccurred())
			Expect(stale).To(BeFalse())
			Expect(staleReason).To(Equal("AgentTelemetryStale"))

			unknown, unknownReason, err := reconciler.nodePowerAgentTelemetryFreshness(context.Background(), unknownAgent)
			Expect(err).NotTo(HaveOccurred())
			Expect(unknown).To(BeFalse())
			Expect(unknownReason).To(Equal("AgentTelemetryUnknown"))

			optedOut, optedOutReason, err := reconciler.nodePowerAgentTelemetryFreshness(context.Background(), optedOutAgent)
			Expect(err).NotTo(HaveOccurred())
			Expect(optedOut).To(BeTrue())
			Expect(optedOutReason).To(BeEmpty())
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
				nil,
				resolver.StructuralBundle{},
				nil,
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

		It("continues shutdown execution through the audit spool when primary audit writes fail", func() {
			observedAt := time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
			spoolDir := GinkgoT().TempDir()

			cluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestPowerClusterName},
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageExternalPostgres,
						ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
							DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
								Namespace: "power-system",
								Name:      "power-postgres",
								Key:       "dsn",
							},
						},
						AuditSpool: powerv1alpha1.AuditSpoolSpec{
							Enabled: true,
							Path:    spoolDir,
						},
					},
				},
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

			store := &fakeAuditStore{writeErr: errors.New("postgres write failed")}
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
			Expect(store.shutdownFlowExecutions).To(BeEmpty())
			Expect(store.actionAttempts).To(BeEmpty())
			Expect(store.closeCalls).To(Equal(1))

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.LastExecution).NotTo(BeNil())
			Expect(resource.Status.LastExecution.Phase).To(Equal(powerv1alpha1.ShutdownExecutionPhaseCompleted))
			Expect(resource.Status.LastExecution.Message).To(ContainSubstring("audit records were spooled"))
			degraded := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("AuditSpoolFallback"))
			executionReady := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionExecutionReady)
			Expect(executionReady).NotTo(BeNil())
			Expect(executionReady.Status).To(Equal(metav1.ConditionTrue))
			Expect(executionReady.Reason).To(Equal("AuditSpoolFallback"))

			content, err := os.ReadFile(filepath.Join(spoolDir, "audit-spool.jsonl"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(ContainSubstring(`"kind":"shutdownflow_execution"`))
			Expect(string(content)).To(ContainSubstring(`"kind":"shutdownflow_action_attempt"`))
			Expect(string(content)).To(ContainSubstring(`"ExecutionID":"`))
			Expect(strings.Count(string(content), "\n")).To(BeNumerically(">=", 1))
		})

		It("orders node work before the poweroff that releases the same node", func() {
			By("giving the cluster a real node and an agent that covers it")
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   shutdownFlowTestNodeName,
				Labels: map[string]string{"power.zalud.io/rack": "a"},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
			})

			agent := &powerv1alpha1.NodePowerAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test-resolver-agent"},
				Spec: powerv1alpha1.NodePowerAgentSpec{
					NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a"}},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, agent))).To(Succeed())
			})
			agent.Status.SelectedNodes = []string{shutdownFlowTestNodeName}
			Expect(k8sClient.Status().Update(ctx, agent)).To(Succeed())

			By("declaring a drain and a poweroff with no ordering between them")
			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Groups = []powerv1alpha1.ShutdownGroup{
				{
					Name:   "drain-rack-a",
					Action: powerv1alpha1.ShutdownStepDrainNodes,
					Target: powerv1alpha1.ShutdownStepTarget{
						NodeSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"power.zalud.io/rack": "a"},
						},
					},
				},
				{
					Name:   "poweroff-rack-a",
					Action: powerv1alpha1.ShutdownStepAgentShutdown,
					Target: powerv1alpha1.ShutdownStepTarget{
						AgentRefs: []powerv1alpha1.ObjectNameReference{{Name: agent.Name}},
					},
				},
			}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			controllerReconciler := &ShutdownFlowReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			accepted := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(accepted).NotTo(BeNil())
			Expect(accepted.Status).To(Equal(metav1.ConditionTrue))

			// The ordering is derived, not declared: nothing in the spec above says
			// the drain comes first. Only the node they share can establish that.
			Expect(resource.Status.CompiledWaves).To(HaveLen(2))
			Expect(resource.Status.CompiledWaves[0].Groups).To(Equal([]string{"drain-rack-a"}))
			Expect(resource.Status.CompiledWaves[1].Groups).To(Equal([]string{"poweroff-rack-a"}))

			Expect(resource.Status.PublishedArtifact).NotTo(BeNil())
			var clearance *powerv1alpha1.PlannerGraphEdgeStatus
			for i := range resource.Status.PublishedArtifact.Graph.Edges {
				if resource.Status.PublishedArtifact.Graph.Edges[i].Relation == "NodeClearance" {
					clearance = &resource.Status.PublishedArtifact.Graph.Edges[i]
				}
			}
			Expect(clearance).NotTo(BeNil())
			Expect(clearance.From).To(Equal("drain-rack-a"))
			Expect(clearance.To).To(Equal("poweroff-rack-a"))
			Expect(clearance.Provenance).To(Equal("Derived"))
			Expect(clearance.Explanation).To(ContainSubstring(shutdownFlowTestNodeName))
		})

		It("rejects the flow when a node is not reachable from any UPS", func() {
			By("adding a node no feeds path reaches")
			orphan := &powerv1alpha1.PowerInventoryNode{
				ObjectMeta: metav1.ObjectMeta{Name: "test-resolver-orphan-node"},
				Spec:       powerv1alpha1.PowerInventoryNodeSpec{NodeName: "orphan-node"},
			}
			Expect(k8sClient.Create(ctx, orphan)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, orphan))).To(Succeed())
			})

			controllerReconciler := &ShutdownFlowReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// The orphan rule is enforced in the compiler; this proves it actually
			// blocks acceptance through the real reconcile path rather than only in
			// the compiler's own unit tests.
			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			accepted := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(accepted).NotTo(BeNil())
			Expect(accepted.Status).To(Equal(metav1.ConditionFalse))
			Expect(accepted.Reason).To(Equal("PowerPlanningOrphan"))
			Expect(accepted.Message).To(ContainSubstring("orphan-node"))
			Expect(resource.Status.Phase).To(Equal(powerv1alpha1.ShutdownFlowPhaseError))
			Expect(resource.Status.CompiledWaves).To(BeNil())
			Expect(resource.Status.ConfigHash).To(BeEmpty())
		})

		It("rejects the flow when the same topology edge is declared twice", func() {
			By("declaring a second edge between the same endpoints")
			duplicate := &powerv1alpha1.PowerInventoryEdge{
				ObjectMeta: metav1.ObjectMeta{Name: "test-resolver-duplicate-edge"},
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
			}
			Expect(k8sClient.Create(ctx, duplicate)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, duplicate))).To(Succeed())
			})

			controllerReconciler := &ShutdownFlowReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			accepted := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(accepted).NotTo(BeNil())
			Expect(accepted.Status).To(Equal(metav1.ConditionFalse))
			Expect(accepted.Reason).To(Equal("DuplicateEdge"))
			Expect(resource.Status.Phase).To(Equal(powerv1alpha1.ShutdownFlowPhaseError))
		})

		It("surfaces planner findings on the flow instead of discarding them", func() {
			By("declaring a node that powers off before the workload running on it")
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   shutdownFlowTestNodeName,
				Labels: map[string]string{"power.zalud.io/rack": "a"},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
			})

			inventoryNode := &powerv1alpha1.PowerInventoryNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestNodeInventoryName}, inventoryNode)).To(Succeed())
			nodeTier := int32(4)
			inventoryNode.Spec.Roles.ShutdownTier = &nodeTier
			Expect(k8sClient.Update(ctx, inventoryNode)).To(Succeed())

			workloadTier := int32(2)
			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Groups = []powerv1alpha1.ShutdownGroup{{
				Name:         "late-workload",
				Action:       powerv1alpha1.ShutdownStepScaleWorkload,
				ShutdownTier: &workloadTier,
				Target: powerv1alpha1.ShutdownStepTarget{
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"power.zalud.io/rack": "a"},
					},
				},
			}}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			store := &fakeAuditStore{}
			controllerReconciler := &ShutdownFlowReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				StorageConnector: &fakeAuditConnector{store: store},
			}
			createSpoolingPowerCluster(ctx, GinkgoT().TempDir(), nil)
			attachShutdownFlowToPowerCluster(ctx, typeNamespacedName)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("reporting the inversion on the flow rather than only in the planner")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			degraded := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("ShutdownTierInversion"))
			Expect(degraded.Message).To(ContainSubstring(shutdownFlowTestNodeName))

			By("recording it durably, attributed to the planner")
			Expect(store.shutdownFlowCompilations).NotTo(BeEmpty())
			var plannerRecord *audit.DiagnosticRecord
			for i := range store.shutdownFlowCompilations[0].Diagnostics {
				record := &store.shutdownFlowCompilations[0].Diagnostics[i]
				if record.Reason == "ShutdownTierInversion" {
					plannerRecord = record
				}
			}
			Expect(plannerRecord).NotTo(BeNil())
			Expect(plannerRecord.Source).To(Equal("Planner"))
			Expect(plannerRecord.Severity).To(Equal("Warning"))

			By("withholding the node from power-off rather than only reporting it (OD-18)")
			Expect(resource.Status.BlockedNodeReleases).To(HaveLen(1))
			blocked := resource.Status.BlockedNodeReleases[0]
			Expect(blocked.NodeName).To(Equal(shutdownFlowTestNodeName))
			Expect(blocked.Reason).To(Equal("ShutdownTierInversion"))
			Expect(blocked.NodeTier).To(Equal(int32(4)))
			Expect(blocked.Groups).To(ConsistOf("late-workload"))
		})

		It("powers the node off on schedule when the group accepts the inversion", func() {
			By("declaring the same inversion with tierInversionPolicy Allow")
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   shutdownFlowTestNodeName,
				Labels: map[string]string{"power.zalud.io/rack": "a"},
			}}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, node))).To(Succeed())
			})

			inventoryNode := &powerv1alpha1.PowerInventoryNode{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestNodeInventoryName}, inventoryNode)).To(Succeed())
			nodeTier := int32(4)
			inventoryNode.Spec.Roles.ShutdownTier = &nodeTier
			Expect(k8sClient.Update(ctx, inventoryNode)).To(Succeed())

			workloadTier := int32(2)
			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Groups = []powerv1alpha1.ShutdownGroup{{
				Name:                "late-workload",
				Action:              powerv1alpha1.ShutdownStepScaleWorkload,
				ShutdownTier:        &workloadTier,
				TierInversionPolicy: powerv1alpha1.ShutdownTierInversionAllow,
				Target: powerv1alpha1.ShutdownStepTarget{
					NodeSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"power.zalud.io/rack": "a"},
					},
				},
			}}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			controllerReconciler := &ShutdownFlowReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				StorageConnector: &fakeAuditConnector{store: &fakeAuditStore{}},
			}
			createSpoolingPowerCluster(ctx, GinkgoT().TempDir(), nil)
			attachShutdownFlowToPowerCluster(ctx, typeNamespacedName)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("blocking nothing while still reporting the accepted risk")
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.BlockedNodeReleases).To(BeEmpty())
			degraded := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Reason).To(Equal("ShutdownTierInversionAllowed"))
		})

		It("returns spooled records to PostgreSQL on the first reconcile that can write again", func() {
			observedAt := time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
			spoolDir := GinkgoT().TempDir()
			journalPath := filepath.Join(spoolDir, "audit-spool.jsonl")

			createSpoolingPowerCluster(ctx, spoolDir, nil)
			attachShutdownFlowToPowerCluster(ctx, typeNamespacedName)
			putUPSDeviceOnBattery(ctx, observedAt)

			By("spooling execution evidence while PostgreSQL refuses writes")
			failing := &fakeAuditStore{writeErr: errors.New("postgres write failed")}
			_, err := newSpoolingShutdownFlowReconciler(failing, observedAt).
				Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(failing.shutdownFlowExecutions).To(BeEmpty())
			Expect(journalPath).To(BeAnExistingFile())

			By("draining the journal once a healthy store is in hand")
			replayedBefore := testutil.ToFloat64(metrics.AuditSpoolReplayRecordsTotal.WithLabelValues("replayed"))
			healthy := &fakeAuditStore{}
			_, err = newSpoolingShutdownFlowReconciler(healthy, observedAt.Add(time.Minute)).
				Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// The evidence PostgreSQL rejected the first time is now in PostgreSQL,
			// and the journal is gone because the drain was clean.
			Expect(healthy.shutdownFlowExecutions).NotTo(BeEmpty())
			Expect(healthy.actionAttempts).NotTo(BeEmpty())
			Expect(journalPath).NotTo(BeAnExistingFile())
			Expect(testutil.ToFloat64(metrics.AuditSpoolReplayRecordsTotal.WithLabelValues("replayed"))).
				To(BeNumerically(">", replayedBefore))

			resource := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			degraded := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Reason).NotTo(Equal("AuditSpoolFallback"))
		})

		It("reports lost records rather than growing past the spool cap", func() {
			observedAt := time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
			spoolDir := GinkgoT().TempDir()
			journalPath := filepath.Join(spoolDir, "audit-spool.jsonl")

			// A journal left at its cap by an earlier outage. The record kind is one
			// no build recognizes, so the drain leaves it in place instead of
			// clearing the very condition under test.
			capBytes := storageconfig.MinimumAuditSpoolMaxBytes
			filler := fmt.Sprintf(`{"kind":"unrecognized","key":"filler","payload":{"pad":%q}}`+"\n",
				strings.Repeat("x", int(capBytes)))
			Expect(os.WriteFile(journalPath, []byte(filler), 0o600)).To(Succeed())

			createSpoolingPowerCluster(ctx, spoolDir, resource.NewQuantity(capBytes, resource.BinarySI))
			attachShutdownFlowToPowerCluster(ctx, typeNamespacedName)
			putUPSDeviceOnBattery(ctx, observedAt)

			droppedBefore := testutil.ToFloat64(metrics.AuditSpoolRecordsTotal.WithLabelValues("dropped"))
			store := &fakeAuditStore{writeErr: errors.New("postgres write failed")}
			_, err := newSpoolingShutdownFlowReconciler(store, observedAt).
				Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			info, err := os.Stat(journalPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Size()).To(BeNumerically("<=", capBytes+int64(len(filler))))
			Expect(testutil.ToFloat64(metrics.AuditSpoolRecordsTotal.WithLabelValues("dropped"))).
				To(BeNumerically(">", droppedBefore))

			// Lost evidence gets its own reason: a delayed audit trail and a
			// permanently incomplete one are different operator problems.
			flow := &powerv1alpha1.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, flow)).To(Succeed())
			degraded := meta.FindStatusCondition(flow.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("AuditSpoolFull"))
			Expect(degraded.Message).To(ContainSubstring("dropped"))
		})

	})
})

// createSpoolingPowerCluster creates a ready ExternalPostgres cluster with the
// audit spool enabled at spoolDir.
func createSpoolingPowerCluster(ctx context.Context, spoolDir string, maxSize *resource.Quantity) {
	GinkgoHelper()
	cluster := &powerv1alpha1.PowerManagementCluster{
		ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestPowerClusterName},
		Spec: powerv1alpha1.PowerManagementClusterSpec{
			Storage: powerv1alpha1.PowerStorageSpec{
				Mode: powerv1alpha1.PowerStorageExternalPostgres,
				ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
					DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
						Namespace: "power-system",
						Name:      "power-postgres",
						Key:       "dsn",
					},
				},
				AuditSpool: powerv1alpha1.AuditSpoolSpec{
					Enabled: true,
					Path:    spoolDir,
					MaxSize: maxSize,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
	cluster.Status.Storage = powerv1alpha1.StorageStatus{
		Mode:  powerv1alpha1.PowerStorageExternalPostgres,
		Ready: true,
	}
	Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())
}

func attachShutdownFlowToPowerCluster(ctx context.Context, name types.NamespacedName) {
	GinkgoHelper()
	flow := &powerv1alpha1.ShutdownFlow{}
	Expect(k8sClient.Get(ctx, name, flow)).To(Succeed())
	flow.Spec.ManagementClusterRef = &powerv1alpha1.ObjectNameReference{Name: shutdownFlowTestPowerClusterName}
	Expect(k8sClient.Update(ctx, flow)).To(Succeed())
}

func putUPSDeviceOnBattery(ctx context.Context, observedAt time.Time) {
	GinkgoHelper()
	pollTime := metav1.NewTime(observedAt.Add(-10 * time.Second))
	device := &powerv1alpha1.UPSDevice{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Name: shutdownFlowTestUPSName}, device)).To(Succeed())
	device.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery
	device.Status.LastPollTime = &pollTime
	Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())
}

func newSpoolingShutdownFlowReconciler(store *fakeAuditStore, now time.Time) *ShutdownFlowReconciler {
	return &ShutdownFlowReconciler{
		Client:           k8sClient,
		Scheme:           k8sClient.Scheme(),
		StorageConnector: &fakeAuditConnector{store: store},
		Clock:            func() time.Time { return now },
	}
}

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

// histogramSampleCount reads the current observation count directly off a histogram's Write() output,
// rather than via testutil.CollectAndCount (which counts distinct label-combination series, not
// observations). Other specs in this Describe reconcile the same-named ShutdownFlow resource and share
// the same global collectors, so an order-independent, observation-level delta is what actually proves
// a new Observe() happened on this reconcile.
func histogramSampleCount(observer prometheus.Observer) uint64 {
	histogram, ok := observer.(prometheus.Histogram)
	Expect(ok).To(BeTrue(), "observer does not implement prometheus.Histogram")
	var metric dto.Metric
	Expect(histogram.Write(&metric)).To(Succeed())
	return metric.GetHistogram().GetSampleCount()
}

// resourceVersionRaceInjectingClient reproduces the F-31 race under test: it lets the reconciler
// read an object via Get as normal, then -- once, out of band, on a *separate* fetch that never
// touches the obj the reconciler holds -- advances that same object's resourceVersion on the API
// server before the reconciler reaches its status write. This is exactly the failure mode recorded
// in docs/tasks.md (10h production log, 744 "the object has been modified" conflicts on
// ShutdownFlow): a reconcile's cache-backed read lands slightly behind a concurrent write, and the
// reconcile's own later status write then races that stale read. Status().Update() carries the
// stale resourceVersion and is rejected with a 409 Conflict; Status().Patch(client.MergeFrom(...))
// has no resourceVersion precondition and applies cleanly regardless.
type resourceVersionRaceInjectingClient struct {
	client.Client
	key      types.NamespacedName
	injected bool
}

func (c *resourceVersionRaceInjectingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if c.injected || key != c.key {
		return nil
	}
	if _, ok := obj.(*powerv1alpha1.ShutdownFlow); !ok {
		return nil
	}
	c.injected = true

	var live powerv1alpha1.ShutdownFlow
	if err := c.Client.Get(ctx, key, &live); err != nil {
		return err
	}
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations["f31-race-test/external-write"] = "true"
	return c.Update(ctx, &live)
}
