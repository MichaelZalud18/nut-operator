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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/polling"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

var _ = Describe("UPSDevice Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		upsdevice := &powerv1alpha1.UPSDevice{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind UPSDevice")
			err := k8sClient.Get(ctx, typeNamespacedName, upsdevice)
			if err != nil && errors.IsNotFound(err) {
				resource := &powerv1alpha1.UPSDevice{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: powerv1alpha1.UPSDeviceSpec{
						Driver: "dummy-ups",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &powerv1alpha1.UPSDevice{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance UPSDevice")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &UPSDeviceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.UPSDevice{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			condition := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.NUTName).To(Equal(resourceName))
		})

		It("should update telemetry status from a ready selected NUTServer", func() {
			deviceName := "telemetry-ups"
			serverName := "telemetry-server"
			namespace := "telemetry-power-system"
			pollInterval := metav1.Duration{Duration: time.Minute}
			alertPollInterval := metav1.Duration{Duration: 3 * time.Second}
			observedAt := time.Date(2026, 8, 2, 19, 0, 0, 0, time.UTC)

			device := &powerv1alpha1.UPSDevice{
				ObjectMeta: metav1.ObjectMeta{Name: deviceName},
				Spec: powerv1alpha1.UPSDeviceSpec{
					Driver: "dummy-ups",
					Telemetry: powerv1alpha1.UPSTelemetrySpec{
						PollInterval:      &pollInterval,
						AlertPollInterval: &alertPollInterval,
					},
				},
			}
			Expect(k8sClient.Create(ctx, device)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.UPSDevice{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, resource)
				if err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})

			server := &powerv1alpha1.NUTServer{
				ObjectMeta: metav1.ObjectMeta{Name: serverName},
				Spec: powerv1alpha1.NUTServerSpec{
					Namespace: namespace,
					DeviceRefs: []powerv1alpha1.ObjectNameReference{
						{Name: deviceName},
					},
					Image: powerv1alpha1.ImageReference{
						Repository: "ghcr.io/michaelzalud18/nut-server",
						Tag:        "main",
					},
					TLS: powerv1alpha1.NUTTLSSpec{
						Mode: powerv1alpha1.NUTTLSDisabled,
					},
				},
			}
			Expect(k8sClient.Create(ctx, server)).To(Succeed())
			DeferCleanup(func() {
				resource := &powerv1alpha1.NUTServer{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, resource)
				if err == nil {
					Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
				}
			})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, server)).To(Succeed())
			server.Status = powerv1alpha1.NUTServerStatus{
				ObservedGeneration: server.Generation,
				Phase:              powerv1alpha1.NUTServerPhaseReady,
				SelectedDevices:    []string{deviceName},
				ReadyReplicas:      1,
				ServiceEndpoints: []powerv1alpha1.ServiceEndpointStatus{
					{
						Name:      serverName,
						Namespace: namespace,
						DNSName:   serverName + "." + namespace + ".svc.cluster.local",
						Port:      3493,
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:               powerv1alpha1.ConditionReady,
						Status:             metav1.ConditionTrue,
						ObservedGeneration: server.Generation,
						LastTransitionTime: metav1.NewTime(observedAt),
						Reason:             "Ready",
						Message:            "ready for telemetry test",
					},
				},
			}
			Expect(k8sClient.Status().Update(ctx, server)).To(Succeed())

			fakePoller := &fakeTelemetryPoller{
				result: polling.Result{
					Snapshot: telemetry.Normalize(telemetry.Sample{
						UPSDevice:  deviceName,
						NUTServer:  serverName,
						NUTName:    deviceName,
						ObservedAt: observedAt,
						Variables: map[string]string{
							"ups.status":      "OB LB",
							"battery.charge":  "18.5",
							"battery.runtime": "90",
							"ups.load":        "44",
						},
					}),
				},
			}
			controllerReconciler := &UPSDeviceReconciler{
				Client:          k8sClient,
				Scheme:          k8sClient.Scheme(),
				TelemetryPoller: fakePoller,
			}

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: deviceName},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(3 * time.Second))

			resource := &powerv1alpha1.UPSDevice{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, resource)).To(Succeed())
			Expect(fakePoller.calls).To(Equal(1))
			Expect(fakePoller.target.Host).To(Equal(serverName + "." + namespace + ".svc.cluster.local"))
			Expect(fakePoller.target.Port).To(Equal(3493))
			Expect(fakePoller.target.NUTName).To(Equal(deviceName))

			Expect(resource.Status.Phase).To(Equal(powerv1alpha1.UPSDevicePhaseLowBattery))
			Expect(resource.Status.ServerRefs).To(Equal([]string{serverName}))
			Expect(resource.Status.LastStatus).To(Equal("OB LB"))
			Expect(resource.Status.LastPollTime).NotTo(BeNil())
			Expect(resource.Status.LastPollTime.Time.Equal(observedAt)).To(BeTrue())
			Expect(resource.Status.BatteryChargePercent).NotTo(BeNil())
			Expect(*resource.Status.BatteryChargePercent).To(Equal(int32(19)))
			Expect(resource.Status.RuntimeSeconds).NotTo(BeNil())
			Expect(*resource.Status.RuntimeSeconds).To(Equal(int64(90)))
			Expect(resource.Status.LoadPercent).NotTo(BeNil())
			Expect(*resource.Status.LoadPercent).To(Equal(int32(44)))

			ready := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionReady)
			Expect(ready).NotTo(BeNil())
			Expect(ready.Status).To(Equal(metav1.ConditionTrue))
			degraded := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionDegraded)
			Expect(degraded).NotTo(BeNil())
			Expect(degraded.Status).To(Equal(metav1.ConditionTrue))
			Expect(degraded.Reason).To(Equal("UPSLowBattery"))
		})
	})
})

type fakeTelemetryPoller struct {
	target polling.Target
	result polling.Result
	err    error
	calls  int
}

func (p *fakeTelemetryPoller) Poll(_ context.Context, target polling.Target) (polling.Result, error) {
	p.calls++
	p.target = target
	if p.err != nil {
		return polling.Result{}, p.err
	}
	if p.result.Target.UPSDevice == "" {
		p.result.Target = target
	}
	return p.result, nil
}
