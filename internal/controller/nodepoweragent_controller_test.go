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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("NodePowerAgent Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
			deviceName   = "rack-a-ups"
			serverName   = "rack-a"
			namespace    = "test-agent-power-system"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		nodepoweragent := &powerv1alpha1.NodePowerAgent{}

		BeforeEach(func() {
			By("creating the operand namespace")
			ns := &corev1.Namespace{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: namespace},
				})).To(Succeed())
			}

			By("creating the referenced UPSDevice")
			device := &powerv1alpha1.UPSDevice{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, device)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &powerv1alpha1.UPSDevice{
					ObjectMeta: metav1.ObjectMeta{Name: deviceName},
					Spec: powerv1alpha1.UPSDeviceSpec{
						Driver: "dummy-ups",
						DriverOptions: map[string]string{
							"port": "dummy.dev",
						},
					},
				})).To(Succeed())
			}

			By("creating the referenced NUTServer")
			server := &powerv1alpha1.NUTServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, server)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &powerv1alpha1.NUTServer{
					ObjectMeta: metav1.ObjectMeta{Name: serverName},
					Spec: powerv1alpha1.NUTServerSpec{
						Namespace: namespace,
						DeviceRefs: []powerv1alpha1.ObjectNameReference{
							{Name: deviceName},
						},
						Auth: powerv1alpha1.NUTAuthSpec{
							Mode: powerv1alpha1.NUTAuthExistingSecret,
							ExistingSecretRef: &powerv1alpha1.NamespacedNameReference{
								Namespace: namespace,
								Name:      "rack-a-nut-users",
							},
						},
					},
				})).To(Succeed())
			}

			By("creating the NUT monitor credential Secret")
			monitorSecret := &corev1.Secret{}
			err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "rack-a-nut-users"}, monitorSecret)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      "rack-a-nut-users",
					},
					Type: corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"monitor-password": []byte("test-monitor-password"),
					},
				})).To(Succeed())
			}

			By("creating the custom resource for the Kind NodePowerAgent")
			err = k8sClient.Get(ctx, typeNamespacedName, nodepoweragent)
			if err != nil && errors.IsNotFound(err) {
				resource := &powerv1alpha1.NodePowerAgent{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: powerv1alpha1.NodePowerAgentSpec{
						Namespace: namespace,
						NUTServerRefs: []powerv1alpha1.ObjectNameReference{
							{Name: serverName},
						},
						Mode: powerv1alpha1.NodePowerAgentModeDryRun,
						Images: powerv1alpha1.NodePowerAgentImages{
							Upsmon: powerv1alpha1.ImageReference{
								Repository: "registry.example.com/power/upsmon-agent",
								Tag:        "2.8.5",
							},
							Actuator: powerv1alpha1.ImageReference{
								Repository: "registry.example.com/power/node-actuator",
								Tag:        "v0.1.0",
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &powerv1alpha1.NodePowerAgent{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance NodePowerAgent")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}

			for _, key := range []types.NamespacedName{
				{Namespace: namespace, Name: "test-resource-node-power-agent"},
				{Namespace: namespace, Name: "test-resource-node-agent-config"},
				{Namespace: namespace, Name: "test-resource-upsmon-config"},
				{Namespace: namespace, Name: "rack-a-nut-users"},
			} {
				ds := &appsv1.DaemonSet{}
				if err := k8sClient.Get(ctx, key, ds); err == nil {
					Expect(k8sClient.Delete(ctx, ds)).To(Succeed())
					continue
				}
				cm := &corev1.ConfigMap{}
				if err := k8sClient.Get(ctx, key, cm); err == nil {
					Expect(k8sClient.Delete(ctx, cm)).To(Succeed())
					continue
				}
				secret := &corev1.Secret{}
				if err := k8sClient.Get(ctx, key, secret); err == nil {
					Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
				}
			}

			serviceAccount := &corev1.ServiceAccount{}
			err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, serviceAccount)
			if err == nil {
				Expect(k8sClient.Delete(ctx, serviceAccount)).To(Succeed())
			}

			networkPolicy := &networkingv1.NetworkPolicy{}
			err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, networkPolicy)
			if err == nil {
				Expect(k8sClient.Delete(ctx, networkPolicy)).To(Succeed())
			}

			server := &powerv1alpha1.NUTServer{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: serverName}, server)
			if err == nil {
				Expect(k8sClient.Delete(ctx, server)).To(Succeed())
			}

			device := &powerv1alpha1.UPSDevice{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, device)
			if err == nil {
				Expect(k8sClient.Delete(ctx, device)).To(Succeed())
			}

			ns := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)
			if err == nil {
				Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			}
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NodePowerAgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.NodePowerAgent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			condition := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.ConfigHash).NotTo(BeEmpty())
			Expect(resource.Status.ManagedResources).NotTo(BeEmpty())

			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-agent-config"}, configMap)).To(Succeed())
			Expect(configMap.Data["nut.conf"]).To(Equal("MODE=netclient\n"))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-upsmon-config"}, secret)).To(Succeed())
			Expect(string(secret.Data["upsmon.conf"])).To(ContainSubstring("MONITOR rack-a-ups@rack-a.test-agent-power-system.svc.cluster.local 1 monitor test-monitor-password secondary"))

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, serviceAccount)).To(Succeed())
			Expect(*serviceAccount.AutomountServiceAccountToken).To(BeFalse())

			networkPolicy := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, networkPolicy)).To(Succeed())
			Expect(networkPolicy.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeEgress))
			Expect(networkPolicy.Spec.Egress).NotTo(BeEmpty())

			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.AutomountServiceAccountToken).NotTo(BeNil())
			Expect(*daemonSet.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.Containers).To(HaveLen(2))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Name).To(Equal("upsmon"))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.example.com/power/upsmon-agent:2.8.5"))
			Expect(*daemonSet.Spec.Template.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.Containers[1].Name).To(Equal("actuator"))
			Expect(*daemonSet.Spec.Template.Spec.Containers[1].SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
		})
	})
})
