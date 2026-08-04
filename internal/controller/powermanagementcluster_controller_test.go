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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

type fakeAuditConnector struct {
	store *fakeAuditStore
	err   error
	opens int
}

func (f *fakeAuditConnector) OpenAuditStore(context.Context, *powerv1alpha1.PowerManagementCluster) (audit.Store, error) {
	f.opens++
	if f.err != nil {
		return nil, f.err
	}
	if f.store == nil {
		f.store = &fakeAuditStore{}
	}
	return f.store, nil
}

type fakeAuditStore struct {
	powerEvents              []audit.PowerEvent
	telemetrySnapshots       []audit.TelemetrySnapshot
	capabilityProfileMatches []audit.CapabilityProfileMatch
	capabilityVerifications  []audit.CapabilityProfileVerification
	shutdownFlowCompilations []audit.ShutdownFlowCompilation
	shutdownFlowDecisions    []audit.ShutdownFlowDecision
	shutdownFlowExecutions   []audit.ShutdownFlowExecution
	executionWaves           []audit.ShutdownFlowExecutionWave
	executionGroups          []audit.ShutdownFlowExecutionGroup
	actionAttempts           []audit.ShutdownFlowActionAttempt
	nodeReleases             []audit.NodeReleaseRecord
	nodeSignalHandoffs       []audit.NodeSignalHandoff
	executorResumeStates     []audit.ExecutorResumeState
	retentionRuns            []time.Time
	closeCalls               int
	eventErr                 error
	writeErr                 error
	retentionErr             error
	closeErr                 error
}

func (s *fakeAuditStore) EnsureSchema(context.Context) error {
	return nil
}

func (s *fakeAuditStore) EnforceRetention(_ context.Context, now time.Time) error {
	s.retentionRuns = append(s.retentionRuns, now)
	return s.retentionErr
}

func (s *fakeAuditStore) Close() error {
	s.closeCalls++
	return s.closeErr
}

func (s *fakeAuditStore) recordErr() error {
	return s.writeErr
}

func (s *fakeAuditStore) RecordPowerEvent(_ context.Context, event audit.PowerEvent) error {
	if s.eventErr != nil {
		return s.eventErr
	}
	if err := s.recordErr(); err != nil {
		return err
	}
	s.powerEvents = append(s.powerEvents, event)
	return nil
}

func (s *fakeAuditStore) RecordTelemetrySnapshot(_ context.Context, snapshot audit.TelemetrySnapshot) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.telemetrySnapshots = append(s.telemetrySnapshots, snapshot)
	return nil
}

func (s *fakeAuditStore) RecordCapabilityProfileMatch(_ context.Context, match audit.CapabilityProfileMatch) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.capabilityProfileMatches = append(s.capabilityProfileMatches, match)
	return nil
}

func (s *fakeAuditStore) RecordCapabilityProfileVerification(_ context.Context, verification audit.CapabilityProfileVerification) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.capabilityVerifications = append(s.capabilityVerifications, verification)
	return nil
}

func (s *fakeAuditStore) RecordShutdownFlowCompilation(_ context.Context, compilation audit.ShutdownFlowCompilation) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.shutdownFlowCompilations = append(s.shutdownFlowCompilations, compilation)
	return nil
}

func (s *fakeAuditStore) RecordShutdownFlowDecision(_ context.Context, decision audit.ShutdownFlowDecision) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.shutdownFlowDecisions = append(s.shutdownFlowDecisions, decision)
	return nil
}

func (s *fakeAuditStore) RecordShutdownFlowExecution(_ context.Context, execution audit.ShutdownFlowExecution) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.shutdownFlowExecutions = append(s.shutdownFlowExecutions, execution)
	return nil
}

func (s *fakeAuditStore) RecordShutdownFlowExecutionWave(_ context.Context, wave audit.ShutdownFlowExecutionWave) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.executionWaves = append(s.executionWaves, wave)
	return nil
}

func (s *fakeAuditStore) RecordShutdownFlowExecutionGroup(_ context.Context, group audit.ShutdownFlowExecutionGroup) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.executionGroups = append(s.executionGroups, group)
	return nil
}

func (s *fakeAuditStore) RecordShutdownFlowActionAttempt(_ context.Context, attempt audit.ShutdownFlowActionAttempt) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.actionAttempts = append(s.actionAttempts, attempt)
	return nil
}

func (s *fakeAuditStore) RecordNodeRelease(_ context.Context, release audit.NodeReleaseRecord) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.nodeReleases = append(s.nodeReleases, release)
	return nil
}

func (s *fakeAuditStore) RecordNodeSignalHandoff(_ context.Context, handoff audit.NodeSignalHandoff) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.nodeSignalHandoffs = append(s.nodeSignalHandoffs, handoff)
	return nil
}

func (s *fakeAuditStore) UpsertExecutorResumeState(_ context.Context, state audit.ExecutorResumeState) error {
	if err := s.recordErr(); err != nil {
		return err
	}
	s.executorResumeStates = append(s.executorResumeStates, state)
	return nil
}

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
			if err != nil && apierrors.IsNotFound(err) {
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
			auditConnector := &fakeAuditConnector{}
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
				Client:           fake.NewClientBuilder().WithScheme(scheme).WithObjects(cnpgCluster).Build(),
				Scheme:           scheme,
				StorageConnector: auditConnector,
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
			Expect(reason).To(Equal("AuditStoreReady"))
			Expect(status.Ready).To(BeTrue())
			Expect(status.Mode).To(Equal(powerv1alpha1.PowerStorageCNPG))
			Expect(message).To(ContainSubstring("audit schema migration applied"))
			Expect(auditConnector.opens).To(Equal(1))
			Expect(auditConnector.store.retentionRuns).To(HaveLen(1))
			Expect(auditConnector.store.closeCalls).To(Equal(1))
		})

		It("reports audit store failures as not ready", func() {
			reconciler := &PowerManagementClusterReconciler{
				StorageConnector: &fakeAuditConnector{err: errors.New("database unavailable")},
			}
			cluster := &powerv1alpha1.PowerManagementCluster{
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageExternalPostgres,
						ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
							DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
								Namespace: "power-data",
								Name:      "power-postgres",
								Key:       "uri",
							},
						},
					},
				},
			}

			status, ready, reason, message := reconciler.evaluateStorage(context.Background(), cluster, accepted("contract accepted"))

			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("AuditStoreNotReady"))
			Expect(status.Ready).To(BeFalse())
			Expect(message).To(ContainSubstring("audit store is not ready"))
		})

		It("reports retention failures as audit store readiness failures", func() {
			store := &fakeAuditStore{retentionErr: errors.New("retention failed")}
			reconciler := &PowerManagementClusterReconciler{
				StorageConnector: &fakeAuditConnector{store: store},
				Clock: func() time.Time {
					return time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
				},
			}
			cluster := &powerv1alpha1.PowerManagementCluster{
				Spec: powerv1alpha1.PowerManagementClusterSpec{
					Storage: powerv1alpha1.PowerStorageSpec{
						Mode: powerv1alpha1.PowerStorageExternalPostgres,
						ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
							DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
								Namespace: "power-data",
								Name:      "power-postgres",
								Key:       "uri",
							},
						},
					},
				},
			}

			status, ready, reason, message := reconciler.evaluateStorage(context.Background(), cluster, accepted("contract accepted"))

			Expect(ready).To(BeFalse())
			Expect(reason).To(Equal("AuditStoreNotReady"))
			Expect(status.Ready).To(BeFalse())
			Expect(message).To(ContainSubstring("retention failed"))
			Expect(store.retentionRuns).To(HaveLen(1))
			Expect(store.closeCalls).To(Equal(1))
		})

		It("records a PowerManagementCluster audit event when storage is ready", func() {
			store := &fakeAuditStore{}
			fixed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
			reconciler := &PowerManagementClusterReconciler{
				StorageConnector: &fakeAuditConnector{store: store},
				Clock: func() time.Time {
					return fixed
				},
			}
			cluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "alpha-power",
					Generation: 7,
				},
			}
			storageStatus := powerv1alpha1.StorageStatus{
				Mode:    powerv1alpha1.PowerStorageExternalPostgres,
				Ready:   true,
				Message: "ExternalPostgres storage is ready",
			}

			err := reconciler.recordPowerManagementClusterAuditEvent(
				context.Background(),
				cluster,
				accepted("contract accepted"),
				storageStatus,
				"AuditStoreReady",
				"ExternalPostgres storage is ready",
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(store.powerEvents).To(HaveLen(1))
			Expect(store.powerEvents[0].EventType).To(Equal("PowerManagementClusterReconciled"))
			Expect(store.powerEvents[0].ObservedAt).To(Equal(fixed))
			Expect(*store.powerEvents[0].ResourceGeneration).To(Equal(int64(7)))
			Expect(store.closeCalls).To(Equal(1))
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
