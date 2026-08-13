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
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("NUTServer Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
			deviceName   = "rack-a-ups"
			namespace    = "test-power-system"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		nutserver := &powerv1alpha1.NUTServer{}

		BeforeEach(func() {
			By("creating the referenced UPSDevice")
			device := &powerv1alpha1.UPSDevice{}
			deviceKey := types.NamespacedName{Name: deviceName}
			err := k8sClient.Get(ctx, deviceKey, device)
			if err != nil && errors.IsNotFound(err) {
				resource := &powerv1alpha1.UPSDevice{
					ObjectMeta: metav1.ObjectMeta{
						Name: deviceName,
					},
					Spec: powerv1alpha1.UPSDeviceSpec{
						DisplayName: "Rack A Dummy UPS",
						Driver:      "dummy-ups",
						DriverOptions: map[string]string{
							"desc": "rack-a-ups",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}

			By("creating the custom resource for the Kind NUTServer")
			err = k8sClient.Get(ctx, typeNamespacedName, nutserver)
			if err != nil && errors.IsNotFound(err) {
				replicas := int32(1)
				resource := &powerv1alpha1.NUTServer{
					ObjectMeta: metav1.ObjectMeta{
						Name: resourceName,
					},
					Spec: powerv1alpha1.NUTServerSpec{
						Namespace: namespace,
						DeviceRefs: []powerv1alpha1.ObjectNameReference{
							{Name: deviceName},
						},
						Replicas: &replicas,
						Image: powerv1alpha1.ImageReference{
							Repository: "ghcr.io/michaelzalud18/nut-server",
							Tag:        "main",
						},
						TLS: powerv1alpha1.NUTTLSSpec{
							Mode: powerv1alpha1.NUTTLSDisabled,
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &powerv1alpha1.NUTServer{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance NUTServer")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

				By("Reconciling to process the finalizer")
				controllerReconciler := &NUTServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
				_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

			device := &powerv1alpha1.UPSDevice{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: deviceName}, device)
			if err == nil {
				By("Cleanup the referenced UPSDevice")
				Expect(k8sClient.Delete(ctx, device)).To(Succeed())
			}

			ns := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)
			if err == nil {
				By("Cleanup the rendered operand namespace")
				Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			}
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &NUTServerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.NUTServer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(resource, nutServerFinalizer)).To(BeTrue())
			condition := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.SelectedDevices).To(ContainElement(deviceName))
			Expect(resource.Status.ConfigHash).NotTo(BeEmpty())
			Expect(resource.Status.ManagedResources).NotTo(BeEmpty())
			Expect(resource.Status.ServiceEndpoints).To(HaveLen(1))
			Expect(resource.Status.ServiceEndpoints[0].DNSName).To(Equal("test-resource.test-power-system.svc.cluster.local"))

			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-config"}, configMap)).To(Succeed())
			Expect(configMap.Data["upsd.conf"]).To(Equal("LISTEN 0.0.0.0 3493\nALLOW_NO_DEVICE true\n"))
			Expect(configMap.Data).NotTo(HaveKey("ups.conf"))
			Expect(configMap.Data["rack-a-ups.dev"]).To(ContainSubstring("ups.status: OL"))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-users"}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("upsd.users"))
			Expect(secret.Data).To(HaveKey("monitor-password"))

			driverConfigSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-driver-config"}, driverConfigSecret)).To(Succeed())
			Expect(string(driverConfigSecret.Data["ups.conf"])).To(ContainSubstring("[rack-a-ups]"))
			Expect(string(driverConfigSecret.Data["ups.conf"])).To(ContainSubstring("port = rack-a-ups.dev"))

			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: resourceName}, service)).To(Succeed())
			Expect(service.Spec.Ports[0].Port).To(Equal(int32(3493)))

			networkPolicy := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-server"}, networkPolicy)).To(Succeed())
			Expect(networkPolicy.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeIngress))
			Expect(networkPolicy.Spec.Ingress).To(HaveLen(1))
			Expect(networkPolicy.Spec.Ingress[0].From).To(HaveLen(2))
			Expect(networkPolicy.Spec.Ingress[0].From[0].PodSelector).To(Equal(&metav1.LabelSelector{}))
			// Second peer: the operator's own manager pod lives outside this operand namespace, so a
			// same-namespace-only rule would silently block its own telemetry polling under any
			// NetworkPolicy-enforcing CNI (verified against a real kind cluster, not assumed).
			Expect(networkPolicy.Spec.Ingress[0].From[1].NamespaceSelector).To(Equal(&metav1.LabelSelector{}))
			Expect(networkPolicy.Spec.Ingress[0].From[1].PodSelector).To(Equal(&metav1.LabelSelector{
				MatchLabels: map[string]string{
					"control-plane":          "controller-manager",
					"app.kubernetes.io/name": "nut-operator",
				},
			}))

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-server"}, deployment)).To(Succeed())
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/michaelzalud18/nut-server:main"))
			Expect(*deployment.Spec.Replicas).To(Equal(int32(1)))
			Expect(deployment.Spec.Strategy.Type).To(Equal(appsv1.RecreateDeploymentStrategyType))
			Expect(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe).NotTo(BeNil())
			Expect(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Exec).NotTo(BeNil())
			Expect(deployment.Spec.Template.Spec.Containers[0].ReadinessProbe.Exec.Command).To(Equal(
				[]string{"sh", "-c", upsdReadinessProbeScript()},
			))
			Expect(deployment.Spec.Template.Spec.Volumes).To(ContainElement(
				WithTransform(func(volume corev1.Volume) string { return volume.Name }, Equal("nut-config")),
			))
			configVolume := deployment.Spec.Template.Spec.Volumes[0]
			Expect(configVolume.Name).To(Equal("nut-config"))
			Expect(configVolume.Projected).NotTo(BeNil())
			Expect(configVolume.Projected.Sources).To(HaveLen(3))
			Expect(configVolume.Projected.Sources[0].ConfigMap.Name).To(Equal("test-resource-nut-config"))
			Expect(configVolume.Projected.Sources[1].Secret.Name).To(Equal("test-resource-nut-users"))
			Expect(configVolume.Projected.Sources[1].Secret.Items).To(ContainElement(corev1.KeyToPath{Key: "upsd.users", Path: "upsd.users"}))
			Expect(configVolume.Projected.Sources[2].Secret.Name).To(Equal("test-resource-nut-driver-config"))
			Expect(configVolume.Projected.Sources[2].Secret.Items).To(ContainElement(corev1.KeyToPath{Key: "ups.conf", Path: "ups.conf"}))
			Expect(deployment.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name:      "nut-config",
				MountPath: "/etc/nut",
				ReadOnly:  true,
			}))

			// F-49: the drivers get a supervisor. upsdrvctl start runs once at startup, so a
			// driver that dies later leaves upsd alive, the container is never restarted, and the
			// pod sits out of the Service endpoints indefinitely with every agent in DEADTIME.
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2))
			watchdog := deployment.Spec.Template.Spec.Containers[1]
			Expect(watchdog.Name).To(Equal(driverWatchdogContainerName))
			Expect(watchdog.Image).To(Equal(deployment.Spec.Template.Spec.Containers[0].Image))
			Expect(watchdog.Command).To(Equal([]string{"sh", "-c", driverWatchdogScript()}))

			// It reaches the drivers exactly as upsd does -- the sockets and PID files in
			// /run/nut, and the device list in /etc/nut -- and needs no privilege upsd does not
			// already hold.
			Expect(watchdog.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: "nut-config", MountPath: "/etc/nut", ReadOnly: true,
			}))
			Expect(watchdog.VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name: "nut-run", MountPath: "/run/nut",
			}))
			Expect(*watchdog.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(*watchdog.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
			Expect(watchdog.SecurityContext.Capabilities.Drop).To(Equal([]corev1.Capability{"ALL"}))

			// No probes on the supervisor, deliberately. A readiness probe here would gate the
			// pod's endpoint membership on the watchdog rather than on the server, and a liveness
			// probe would let a supervisor restart take the server down with it. Being a separate
			// container is the point.
			Expect(watchdog.ReadinessProbe).To(BeNil())
			Expect(watchdog.LivenessProbe).To(BeNil())
			Expect(watchdog.StartupProbe).To(BeNil())

			// F-48: without a shared PID namespace upsd is PID 1 in its own container, so its PID
			// file reads "1" and upsd refuses to signal it -- "Ignoring invalid pid number 1" --
			// while `upsd -c reload` still exits 0. The reload would fail in a way that resembles
			// success, so this flag is load-bearing rather than incidental.
			Expect(deployment.Spec.Template.Spec.ShareProcessNamespace).NotTo(BeNil())
			Expect(*deployment.Spec.Template.Spec.ShareProcessNamespace).To(BeTrue())

			pdb := &policyv1.PodDisruptionBudget{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-server"}, pdb)).To(Succeed())
			Expect(pdb.Spec.MinAvailable).NotTo(BeNil())
			Expect(pdb.Spec.MinAvailable.IntValue()).To(Equal(1))
			Expect(pdb.Spec.Selector.MatchLabels).To(Equal(deployment.Spec.Selector.MatchLabels))
		})

		It("converges from a partial, stale operand state and stays stable on a repeat reconcile (F-7)", func() {
			// A dedicated namespace, not the shared `namespace` const: envtest runs no namespace
			// controller, so a Delete() in another spec's AfterEach never actually finishes (the
			// namespace sits in Terminating forever), and a blind Create against that same name here
			// would race it. A per-test namespace name sidesteps that entirely.
			const f7Namespace = "test-power-system-f7-idempotency"

			By("pointing the resource at a dedicated namespace and seeding a partial, stale operand state")
			resource := &powerv1alpha1.NUTServer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Namespace = f7Namespace
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: f7Namespace},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "test-resource-nut-config", Namespace: f7Namespace},
				Data:       map[string]string{"upsd.conf": "stale-content-from-a-previous-spec\n"},
			})).To(Succeed())

			controllerReconciler := &NUTServerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("reconciling once: should converge to the full desired state despite the stale, partial seed")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: f7Namespace, Name: "test-resource-nut-config"}, configMap)).To(Succeed())
			Expect(configMap.Data["upsd.conf"]).To(Equal("LISTEN 0.0.0.0 3493\nALLOW_NO_DEVICE true\n"))
			Expect(configMap.OwnerReferences).NotTo(BeEmpty())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: f7Namespace}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue("power.zalud.io/operand-namespace", "true"))

			deployment := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: f7Namespace, Name: "test-resource-nut-server"}, deployment)).To(Succeed())
			service := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: f7Namespace, Name: resourceName}, service)).To(Succeed())
			pdb := &policyv1.PodDisruptionBudget{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: f7Namespace, Name: "test-resource-nut-server"}, pdb)).To(Succeed())

			deploymentResourceVersion := deployment.ResourceVersion
			configMapResourceVersion := configMap.ResourceVersion
			namespaceResourceVersion := ns.ResourceVersion

			By("reconciling again with nothing changed: should be a stable no-op, not a spurious rewrite")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: f7Namespace, Name: "test-resource-nut-server"}, deployment)).To(Succeed())
			Expect(deployment.ResourceVersion).To(Equal(deploymentResourceVersion))
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: f7Namespace, Name: "test-resource-nut-config"}, configMap)).To(Succeed())
			Expect(configMap.ResourceVersion).To(Equal(configMapResourceVersion))
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: f7Namespace}, ns)).To(Succeed())
			Expect(ns.ResourceVersion).To(Equal(namespaceResourceVersion))

			By("cleaning up the dedicated namespace and its contents")
			Expect(k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f7Namespace}})).To(Succeed())
		})

		// F-48: adding a device must not replace the pod. The old pod-template annotation digested
		// everything rendered, so onboarding one UPS dropped every *other* device's upsmon sessions
		// and NUT's login accounting -- the damage F-15 and F-16 exist to prevent.
		//
		// Asserted end to end rather than against the hash function, because the claim is about
		// which rendered inputs reach the annotation, and a unit test over the same expression the
		// render path uses would agree with itself no matter what it covered.
		It("should not roll the Deployment when only the device set changes", func() {
			controllerReconciler := &NUTServerReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			deployment := &appsv1.Deployment{}
			deploymentKey := types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-server"}
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			annotationBefore := deployment.Spec.Template.Annotations["power.zalud.io/config-hash"]
			Expect(annotationBefore).NotTo(BeEmpty())

			driverConfig := &corev1.Secret{}
			driverConfigKey := types.NamespacedName{Namespace: namespace, Name: "test-resource-nut-driver-config"}
			Expect(k8sClient.Get(ctx, driverConfigKey, driverConfig)).To(Succeed())
			upsConfBefore := string(driverConfig.Data["ups.conf"])

			By("onboarding a second UPSDevice")
			second := &powerv1alpha1.UPSDevice{
				ObjectMeta: metav1.ObjectMeta{Name: "rack-b-ups"},
				Spec: powerv1alpha1.UPSDeviceSpec{
					DisplayName:   "Rack B Dummy UPS",
					Driver:        "dummy-ups",
					DriverOptions: map[string]string{"desc": "rack-b-ups"},
				},
			}
			Expect(k8sClient.Create(ctx, second)).To(Succeed())
			defer func() {
				Expect(k8sClient.Delete(ctx, second)).To(Succeed())
			}()

			server := &powerv1alpha1.NUTServer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, server)).To(Succeed())
			server.Spec.DeviceRefs = append(server.Spec.DeviceRefs, powerv1alpha1.ObjectNameReference{Name: "rack-b-ups"})
			Expect(k8sClient.Update(ctx, server)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("confirming the device actually reached ups.conf")
			Expect(k8sClient.Get(ctx, driverConfigKey, driverConfig)).To(Succeed())
			Expect(string(driverConfig.Data["ups.conf"])).NotTo(Equal(upsConfBefore))
			Expect(string(driverConfig.Data["ups.conf"])).To(ContainSubstring("[rack-b-ups]"))

			By("confirming the pod template was left alone")
			Expect(k8sClient.Get(ctx, deploymentKey, deployment)).To(Succeed())
			Expect(deployment.Spec.Template.Annotations["power.zalud.io/config-hash"]).To(Equal(annotationBefore))
		})

		It("should finalize and actually delete on deletion", func() {
			controllerReconciler := &NUTServerReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling once to add the finalizer")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.NUTServer{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(resource, nutServerFinalizer)).To(BeTrue())

			By("Deleting the resource")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Reconciling again to process the finalizer")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, &powerv1alpha1.NUTServer{})).To(HaveOccurred())
		})
	})
})
