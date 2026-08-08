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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/polling"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

var _ = Describe("UPSCapabilityProbe Controller", func() {
	Context("When probing a UPS with no packaged profile", func() {
		ctx := context.Background()

		const (
			probeName  = "test-probe"
			deviceName = "test-probe-ups"
			serverName = "test-probe-server"
			namespace  = "test-probe-operands"
		)
		observedAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

		createProbeFixture := func(variables map[string]string) *fakeTelemetryPoller {
			device := &powerv1alpha1.UPSDevice{
				ObjectMeta: metav1.ObjectMeta{Name: deviceName},
				Spec:       powerv1alpha1.UPSDeviceSpec{Driver: "dummy-ups"},
			}
			Expect(k8sClient.Create(ctx, device)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.UPSDevice{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})

			server := &powerv1alpha1.NUTServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName},
				Spec: powerv1alpha1.NUTServerSpec{
					Namespace:  namespace,
					DeviceRefs: []powerv1alpha1.ObjectNameReference{{Name: deviceName}},
					Image: powerv1alpha1.ImageReference{
						Repository: "ghcr.io/michaelzalud18/nut-server",
						Tag:        "main",
					},
					TLS: powerv1alpha1.NUTTLSSpec{Mode: powerv1alpha1.NUTTLSDisabled},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.NUTServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, server)).To(Succeed())
			server.Status = powerv1alpha1.NUTServerStatus{
				ObservedGeneration: server.Generation,
				Phase:              powerv1alpha1.NUTServerPhaseReady,
				SelectedDevices:    []string{deviceName},
				ReadyReplicas:      1,
				ServiceEndpoints: []powerv1alpha1.ServiceEndpointStatus{{
					Name:      serverName,
					Namespace: namespace,
					DNSName:   serverName + "." + namespace + ".svc.cluster.local",
					Port:      3493,
				}},
				Conditions: []metav1.Condition{{
					Type:               powerv1alpha1.ConditionReady,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: server.Generation,
					LastTransitionTime: metav1.NewTime(observedAt),
					Reason:             "Ready",
					Message:            "ready for probe test",
				}},
			}
			Expect(k8sClient.Status().Update(ctx, server)).To(Succeed())

			return &fakeTelemetryPoller{
				result: polling.Result{
					Snapshot: telemetry.Normalize(telemetry.Sample{
						UPSDevice:  deviceName,
						NUTServer:  serverName,
						NUTName:    deviceName,
						ObservedAt: observedAt,
						Variables:  variables,
					}),
				},
			}
		}

		createProbeFor := func(device string) {
			probe := &powerv1alpha1.UPSCapabilityProbe{
				ObjectMeta: metav1.ObjectMeta{Name: probeName},
				Spec: powerv1alpha1.UPSCapabilityProbeSpec{
					DeviceRef: powerv1alpha1.UPSCapabilityProbeDeviceReference{Name: device},
				},
			}
			Expect(k8sClient.Create(ctx, probe)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.UPSCapabilityProbe{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})
		}

		It("drafts an applyable profile from what the device actually reports", func() {
			poller := createProbeFixture(map[string]string{
				"ups.mfr":         "Vendor",
				"ups.model":       "SUPER 1500",
				"ups.firmware":    "2.1",
				"ups.status":      "OL",
				"battery.charge":  "97",
				"battery.runtime": "3600",
				"battery.low":     "20",
			})
			createProbeFor(deviceName)

			reconciler := &UPSCapabilityProbeReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				TelemetryPoller: poller,
				Clock:           func() time.Time { return observedAt },
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: probeName},
			})
			Expect(err).NotTo(HaveOccurred())

			probe := &powerv1alpha1.UPSCapabilityProbe{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, probe)).To(Succeed())

			Expect(probe.Status.Phase).To(Equal(powerv1alpha1.UPSCapabilityProbePhaseProbed))
			Expect(probe.Status.Identity.Model).To(Equal("SUPER 1500"))
			Expect(probe.Status.Identity.Manufacturer).To(Equal("Vendor"))
			Expect(probe.Status.Identity.Firmware).To(Equal("2.1"))
			Expect(probe.Status.ReportedVariables).To(ContainElement("battery.runtime"))
			Expect(probe.Status.ProbedAt).NotTo(BeNil())

			By("suggesting the alias the non-standard name implies")
			Expect(probe.Status.SuggestedAliases).To(HaveLen(1))
			Expect(probe.Status.SuggestedAliases).To(HaveKeyWithValue("battery.low", "battery.charge.low"))

			By("recording that the device is currently unidentified, which is why the probe exists")
			Expect(probe.Status.CurrentMatch.Unidentified).To(BeTrue())

			By("drafting a profile that carries variable names but no readings")
			Expect(probe.Status.DraftProfile).To(ContainSubstring("kind: UPSCapabilityProfile"))
			Expect(probe.Status.DraftProfile).To(ContainSubstring("model: SUPER 1500"))
			Expect(probe.Status.DraftProfile).To(ContainSubstring("behaviors: []"))
			Expect(probe.Status.DraftProfile).NotTo(ContainSubstring("3600"))

			By("producing a report that can be pasted straight into an issue")
			Expect(probe.Status.IssueReport).To(ContainSubstring("### Proposed profile"))
			Expect(probe.Status.IssueReport).To(ContainSubstring("- [ ]"))

			ready := meta.FindStatusCondition(probe.Status.Conditions, powerv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		})

		It("reports a probe failure instead of erroring when the device cannot be read", func() {
			poller := createProbeFixture(map[string]string{"ups.status": "OL"})
			poller.err = context.DeadlineExceeded
			createProbeFor(deviceName)

			reconciler := &UPSCapabilityProbeReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				TelemetryPoller: poller,
				Clock:           func() time.Time { return observedAt },
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: probeName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(probeRetryInterval))

			probe := &powerv1alpha1.UPSCapabilityProbe{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, probe)).To(Succeed())
			Expect(probe.Status.Phase).To(Equal(powerv1alpha1.UPSCapabilityProbePhaseFailed))
			Expect(probe.Status.DraftProfile).To(BeEmpty())

			ready := meta.FindStatusCondition(probe.Status.Conditions, powerv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionFalse))
			Expect(ready.Reason).To(Equal("ProbeReadFailed"))
		})

		It("fails cleanly when the referenced UPSDevice does not exist", func() {
			createProbeFor("no-such-device")

			reconciler := &UPSCapabilityProbeReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: probeName},
			})
			Expect(err).NotTo(HaveOccurred())

			stored := &powerv1alpha1.UPSCapabilityProbe{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, stored)).To(Succeed())
			Expect(stored.Status.Phase).To(Equal(powerv1alpha1.UPSCapabilityProbePhaseFailed))

			ready := meta.FindStatusCondition(stored.Status.Conditions, powerv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Reason).To(Equal("UPSDeviceNotFound"))
			Expect(ready.Message).To(ContainSubstring("no-such-device"))
		})
	})

	// OD-15 closed the design question -- probe history lives in PostgreSQL, not CR status -- but
	// until now the capability_profile_verifications table existed ahead of its writer: the schema,
	// the SQL insert, the spool path, and the replay decoder were all present and nothing ever
	// called them. These specs exist to prove a real reconcile actually produces a row.
	Context("When recording probe history to the audit store", func() {
		ctx := context.Background()

		const (
			probeName   = "history-probe"
			deviceName  = "history-probe-ups"
			serverName  = "history-probe-server"
			clusterName = "history-probe-cluster"
			profileName = "history-probe-profile"
			namespace   = "history-probe-operands"
		)
		observedAt := time.Date(2026, 8, 8, 9, 30, 0, 0, time.UTC)

		// createHistoryFixture wires a device whose NUTServer belongs to a storage-ready
		// PowerManagementCluster, which is what gives the probe somewhere to write.
		createHistoryFixture := func(profileVariables []string, reported map[string]string) *fakeTelemetryPoller {
			cluster := &powerv1alpha1.PowerManagementCluster{
				ObjectMeta: metav1.ObjectMeta{Name: clusterName},
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
					},
				},
			}
			Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
			cluster.Status.Storage = powerv1alpha1.StorageStatus{
				Mode:  powerv1alpha1.PowerStorageExternalPostgres,
				Ready: true,
			}
			Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.PowerManagementCluster{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: clusterName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})

			if len(profileVariables) > 0 {
				profile := &powerv1alpha1.UPSCapabilityProfile{
					ObjectMeta: metav1.ObjectMeta{Name: profileName},
					Spec: powerv1alpha1.UPSCapabilityProfileSpec{
						Version: "1.0.0",
						Selector: powerv1alpha1.UPSCapabilityProfileSelector{
							Model: "HISTORY 900",
						},
						Telemetry: powerv1alpha1.UPSCapabilityTelemetrySpec{
							Variables: profileVariables,
						},
					},
				}
				Expect(k8sClient.Create(ctx, profile)).To(Succeed())
				DeferCleanup(func() {
					resource := &powerv1alpha1.UPSCapabilityProfile{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: profileName}, resource); err == nil {
						Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
					}
				})
			}

			device := &powerv1alpha1.UPSDevice{
				ObjectMeta: metav1.ObjectMeta{Name: deviceName},
				Spec: powerv1alpha1.UPSDeviceSpec{
					Driver:   "dummy-ups",
					Identity: powerv1alpha1.UPSDeviceIdentitySpec{Model: "HISTORY 900"},
				},
			}
			Expect(k8sClient.Create(ctx, device)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.UPSDevice{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})

			server := &powerv1alpha1.NUTServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName},
				Spec: powerv1alpha1.NUTServerSpec{
					Namespace:            namespace,
					ManagementClusterRef: &powerv1alpha1.ObjectNameReference{Name: clusterName},
					DeviceRefs:           []powerv1alpha1.ObjectNameReference{{Name: deviceName}},
					Image: powerv1alpha1.ImageReference{
						Repository: "ghcr.io/michaelzalud18/nut-server",
						Tag:        "main",
					},
					TLS: powerv1alpha1.NUTTLSSpec{Mode: powerv1alpha1.NUTTLSDisabled},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.NUTServer{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, server)).To(Succeed())
			server.Status = powerv1alpha1.NUTServerStatus{
				ObservedGeneration: server.Generation,
				Phase:              powerv1alpha1.NUTServerPhaseReady,
				SelectedDevices:    []string{deviceName},
				ReadyReplicas:      1,
				ServiceEndpoints: []powerv1alpha1.ServiceEndpointStatus{{
					Name:      serverName,
					Namespace: namespace,
					DNSName:   serverName + "." + namespace + ".svc.cluster.local",
					Port:      3493,
				}},
				Conditions: []metav1.Condition{{
					Type:               powerv1alpha1.ConditionReady,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: server.Generation,
					LastTransitionTime: metav1.NewTime(observedAt),
					Reason:             "Ready",
					Message:            "ready for probe history test",
				}},
			}
			Expect(k8sClient.Status().Update(ctx, server)).To(Succeed())

			probe := &powerv1alpha1.UPSCapabilityProbe{
				ObjectMeta: metav1.ObjectMeta{Name: probeName},
				Spec: powerv1alpha1.UPSCapabilityProbeSpec{
					DeviceRef: powerv1alpha1.UPSCapabilityProbeDeviceReference{Name: deviceName},
				},
			}
			Expect(k8sClient.Create(ctx, probe)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.UPSCapabilityProbe{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, resource); err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})

			return &fakeTelemetryPoller{
				result: polling.Result{
					Target: polling.Target{UPSDevice: deviceName, NUTServer: serverName, NUTName: deviceName},
					Snapshot: telemetry.Normalize(telemetry.Sample{
						UPSDevice:  deviceName,
						NUTServer:  serverName,
						NUTName:    deviceName,
						ObservedAt: observedAt,
						Variables:  reported,
					}),
				},
			}
		}

		reconcileProbe := func(poller *fakeTelemetryPoller, connector *fakeAuditConnector) {
			GinkgoHelper()
			reconciler := &UPSCapabilityProbeReconciler{
				Client:           k8sClient,
				Scheme:           k8sClient.Scheme(),
				TelemetryPoller:  poller,
				Clock:            func() time.Time { return observedAt },
				StorageConnector: connector,
			}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: probeName},
			})
			Expect(err).NotTo(HaveOccurred())
		}

		It("writes one verification row tying the read to the matched profile version", func() {
			poller := createHistoryFixture(
				[]string{"battery.charge", "ups.status"},
				map[string]string{
					"ups.mfr":        "Vendor",
					"ups.model":      "HISTORY 900",
					"ups.firmware":   "4.2",
					"ups.status":     "OL",
					"battery.charge": "88",
				},
			)
			connector := &fakeAuditConnector{}

			reconcileProbe(poller, connector)

			Expect(connector.store).NotTo(BeNil())
			Expect(connector.store.capabilityVerifications).To(HaveLen(1))
			record := connector.store.capabilityVerifications[0]

			Expect(record.UPSDevice).To(Equal(deviceName))
			Expect(record.ProfileID).To(Equal(profileName))
			Expect(record.ProfileVersion).To(Equal("1.0.0"))
			Expect(record.Verified).To(BeTrue())
			Expect(record.DriftDetected).To(BeFalse())
			Expect(record.ObservedAt).To(Equal(observedAt))

			By("carrying the firmware the claim was verified against, which is the point of OD-15")
			Expect(record.Firmware).To(Equal("4.2"))
			Expect(record.Model).To(Equal("HISTORY 900"))
			Expect(record.NUTServer).To(Equal(serverName))

			By("carrying the evidence, not just the verdict")
			Expect(record.ExpectedVariables).To(ConsistOf("battery.charge", "ups.status"))
			Expect(record.ProbeVariables).To(HaveKeyWithValue("battery.charge", "88"))
			Expect(record.MissingVariables).To(BeEmpty())

			By("closing the store it opened")
			Expect(connector.store.closeCalls).To(Equal(1))
		})

		It("records drift and surfaces it on the probe when declared telemetry is absent", func() {
			poller := createHistoryFixture(
				[]string{"battery.charge", "battery.runtime"},
				map[string]string{
					"ups.mfr":        "Vendor",
					"ups.model":      "HISTORY 900",
					"ups.firmware":   "5.0",
					"ups.status":     "OL",
					"battery.charge": "88",
				},
			)
			connector := &fakeAuditConnector{}

			reconcileProbe(poller, connector)

			Expect(connector.store.capabilityVerifications).To(HaveLen(1))
			record := connector.store.capabilityVerifications[0]
			Expect(record.DriftDetected).To(BeTrue())
			Expect(record.Verified).To(BeFalse())
			Expect(record.MissingVariables).To(ConsistOf("battery.runtime"))
			Expect(record.Firmware).To(Equal("5.0"))

			By("telling the user without making them query PostgreSQL")
			stored := &powerv1alpha1.UPSCapabilityProbe{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, stored)).To(Succeed())
			degraded := meta.FindStatusCondition(stored.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("CapabilityProfileDrift"))
			Expect(degraded.Message).To(ContainSubstring("battery.runtime"))
		})

		// A probe is advisory work that never runs on the failure path, so losing the database
		// must not cost the operator a drafted profile -- but it must not pass silently either.
		It("still drafts a profile when the audit store is unreachable, and says so", func() {
			poller := createHistoryFixture(
				[]string{"battery.charge"},
				map[string]string{
					"ups.model":      "HISTORY 900",
					"ups.status":     "OL",
					"battery.charge": "88",
				},
			)
			connector := &fakeAuditConnector{err: errors.New("postgres is down")}

			reconcileProbe(poller, connector)

			stored := &powerv1alpha1.UPSCapabilityProbe{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, stored)).To(Succeed())

			Expect(stored.Status.Phase).To(Equal(powerv1alpha1.UPSCapabilityProbePhaseProbed))
			Expect(stored.Status.DraftProfile).To(ContainSubstring("kind: UPSCapabilityProfile"))

			degraded := meta.FindStatusCondition(stored.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("VerificationNotRecorded"))
			Expect(degraded.Message).To(ContainSubstring("postgres is down"))
		})

		It("writes nothing when the cluster has storage disabled", func() {
			poller := createHistoryFixture(
				[]string{"battery.charge"},
				map[string]string{"ups.model": "HISTORY 900", "battery.charge": "88"},
			)
			cluster := &powerv1alpha1.PowerManagementCluster{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterName}, cluster)).To(Succeed())
			cluster.Status.Storage = powerv1alpha1.StorageStatus{Mode: powerv1alpha1.PowerStorageDisabled}
			Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())
			connector := &fakeAuditConnector{}

			reconcileProbe(poller, connector)

			Expect(connector.opens).To(BeZero())
			stored := &powerv1alpha1.UPSCapabilityProbe{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: probeName}, stored)).To(Succeed())
			Expect(stored.Status.Phase).To(Equal(powerv1alpha1.UPSCapabilityProbePhaseProbed))
		})
	})
})
