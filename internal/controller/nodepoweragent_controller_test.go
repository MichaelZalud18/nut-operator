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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

var _ = Describe("NodePowerAgent Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
			deviceName   = "rack-a-ups"
			serverName   = "rack-a"
			namespace    = "test-agent-power-system"
			nodeName     = "test-agent-node"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: resourceName,
		}
		nodepoweragent := &powerv1alpha1.NodePowerAgent{}

		BeforeEach(func() {
			By("creating a selected Kubernetes Node")
			node := &corev1.Node{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName,
						Labels: map[string]string{
							"power.zalud.io/test-agent-node": "true",
						},
					},
				})).To(Succeed())
			}

			By("creating the operand namespace")
			ns := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)
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
						NodeSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"power.zalud.io/test-agent-node": "true",
							},
						},
						Mode: powerv1alpha1.NodePowerAgentModeDryRun,
						Images: powerv1alpha1.NodePowerAgentImages{
							Upsmon: powerv1alpha1.ImageReference{
								Repository: "ghcr.io/michaelzalud18/upsmon-agent",
								Tag:        "main",
							},
							Actuator: powerv1alpha1.ImageReference{
								Repository: "ghcr.io/michaelzalud18/node-actuator",
								Tag:        "main",
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

				By("Reconciling to process the finalizer")
				controllerReconciler := &NodePowerAgentReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
				_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

			for _, key := range []types.NamespacedName{
				{Namespace: namespace, Name: "test-resource-node-power-agent"},
				{Namespace: namespace, Name: "test-resource-node-agent-config"},
				{Namespace: namespace, Name: "test-resource-upsmon-config"},
				{Namespace: namespace, Name: "test-resource-node-signals"},
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

			pod := &corev1.Pod{}
			err = k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent-ready"}, pod)
			if err == nil {
				Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
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

			// Namespace deletion is intentionally not attempted here: envtest runs no
			// namespace-lifecycle controller, so a deleted Namespace never clears its
			// finalizer and stays Terminating for the life of the test binary. The next
			// spec's BeforeEach would then hit "namespace is being terminated" trying to
			// reuse this same namespace name. Leaving the namespace Active and reused across
			// specs is correct for this test environment; envtest itself is torn down after
			// the suite completes.

			node := &corev1.Node{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			if err == nil {
				Expect(k8sClient.Delete(ctx, node)).To(Succeed())
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
			Expect(controllerutil.ContainsFinalizer(resource, nodePowerAgentFinalizer)).To(BeTrue())
			condition := meta.FindStatusCondition(resource.Status.Conditions, powerv1alpha1.ConditionAccepted)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(resource.Status.SelectedNodes).To(ConsistOf(nodeName))
			Expect(resource.Status.UnavailableNodeCount).To(Equal(int32(1)))
			Expect(resource.Status.NodeStatuses).To(HaveLen(1))
			Expect(resource.Status.NodeStatuses[0].Reason).To(Equal("AgentPodMissing"))
			Expect(resource.Status.ConfigHash).NotTo(BeEmpty())
			Expect(resource.Status.ManagedResources).NotTo(BeEmpty())

			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-agent-config"}, configMap)).To(Succeed())
			Expect(configMap.Data["nut.conf"]).To(Equal("MODE=netclient\n"))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-upsmon-config"}, secret)).To(Succeed())
			Expect(string(secret.Data["upsmon.conf"])).To(ContainSubstring("MONITOR rack-a-ups@rack-a.test-agent-power-system.svc.cluster.local 1 monitor test-monitor-password secondary"))
			Expect(string(secret.Data["upsmon.conf"])).To(ContainSubstring("SHUTDOWNCMD \"/usr/local/bin/power-signal-writer\""))

			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, serviceAccount)).To(Succeed())
			Expect(*serviceAccount.AutomountServiceAccountToken).To(BeFalse())

			signalSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-signals"}, signalSecret)).To(Succeed())
			Expect(signalSecret.Type).To(Equal(corev1.SecretTypeOpaque))
			// F-86: the Secret is never empty, so the projected directory always has a file in it.
			// An empty Secret mounts exactly like a Secret that does not exist, which is what made a
			// dead delivery channel indistinguishable from a quiet one.
			Expect(signalSecret.Data).To(HaveKey(nodeagent.DeliveryChannelMarker))

			networkPolicy := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, networkPolicy)).To(Succeed())
			Expect(networkPolicy.Spec.PolicyTypes).To(ContainElement(networkingv1.PolicyTypeEgress))
			Expect(networkPolicy.Spec.Egress).NotTo(BeEmpty())

			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.AutomountServiceAccountToken).NotTo(BeNil())
			Expect(*daemonSet.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())
			Expect(daemonSet.Spec.UpdateStrategy.Type).To(Equal(appsv1.RollingUpdateDaemonSetStrategyType))
			Expect(daemonSet.Spec.UpdateStrategy.RollingUpdate).NotTo(BeNil())
			// F-72: surge rather than go unavailable. maxUnavailable: 1 left a node with no agent
			// for a full pull-and-start window on every rollout, on the workload whose absence is
			// the failure it exists to prevent.
			Expect(daemonSet.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable.IntVal).To(Equal(int32(0)))
			Expect(daemonSet.Spec.UpdateStrategy.RollingUpdate.MaxSurge).NotTo(BeNil())
			Expect(daemonSet.Spec.UpdateStrategy.RollingUpdate.MaxSurge.IntVal).To(Equal(int32(1)))
			Expect(daemonSet.Spec.Template.Spec.HostPID).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.PriorityClassName).To(Equal("system-node-critical"))
			Expect(*daemonSet.Spec.Template.Spec.TerminationGracePeriodSeconds).To(Equal(int64(60)))
			Expect(daemonSet.Spec.Template.Spec.Tolerations).To(ContainElement(corev1.Toleration{
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}))
			Expect(daemonSet.Spec.Template.Spec.Tolerations).To(ContainElement(corev1.Toleration{
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoExecute,
			}))
			Expect(daemonSet.Spec.Template.Spec.Containers).To(HaveLen(2))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Name).To(Equal("upsmon"))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/michaelzalud18/upsmon-agent:main"))
			Expect(*daemonSet.Spec.Template.Spec.Containers[0].SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].ReadinessProbe).NotTo(BeNil())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].LivenessProbe).NotTo(BeNil())
			Expect(daemonSet.Spec.Template.Spec.Containers[0].LivenessProbe.Exec.Command).To(Equal([]string{"pgrep", "-x", "upsmon"}))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{Name: "POWER_SIGNAL_PATH", Value: "/run/power-agent/shutdown.json"}))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{Name: "POWER_SHUTDOWN_FLOW", Value: "upsmon-local"}))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{Name: "POWER_SELECTED_UPS_DEVICES", Value: "rack-a-ups"}))
			Expect(daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name:      "upsmon-run",
				MountPath: "/run",
			}))
			Expect(daemonSet.Spec.Template.Spec.Volumes).To(ContainElement(WithTransform(func(volume corev1.Volume) string {
				return volume.Name
			}, Equal("upsmon-run"))))
			Expect(daemonSet.Spec.Template.Spec.Containers[1].Name).To(Equal("actuator"))
			Expect(daemonSet.Spec.Template.Spec.Containers[1].Image).To(Equal("ghcr.io/michaelzalud18/node-actuator:main"))
			Expect(*daemonSet.Spec.Template.Spec.Containers[1].SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.Containers[1].SecurityContext.Capabilities.Add).To(BeEmpty())
			Expect(daemonSet.Spec.Template.Spec.Containers[1].ReadinessProbe).NotTo(BeNil())
			Expect(daemonSet.Spec.Template.Spec.Containers[1].LivenessProbe).To(BeNil(), "actuator must never carry a liveness probe: a restart loop during a live shutdown could re-trigger or lose signal state")
			Expect(daemonSet.Spec.Template.Spec.Containers[1].Env).To(ContainElement(corev1.EnvVar{
				Name: "POWER_NODE_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "spec.nodeName"},
				},
			}))
			// F-57/OD-37: the projected Secret is the only path with authority. The local tmpfs
			// path the upsmon container writes through SHUTDOWNCMD used to lead this list.
			Expect(daemonSet.Spec.Template.Spec.Containers[1].Env).To(ContainElement(corev1.EnvVar{
				Name:  "POWER_SIGNAL_PATHS",
				Value: "/var/lib/power-agent/signals/$(POWER_NODE_NAME).json",
			}))
			for _, variable := range daemonSet.Spec.Template.Spec.Containers[1].Env {
				if variable.Name == "POWER_SIGNAL_PATH" || variable.Name == "POWER_SIGNAL_PATHS" {
					Expect(variable.Value).NotTo(ContainSubstring("/run/power-agent"),
						"the actuator must not watch the tmpfs the network-facing container can write (F-57)")
				}
			}
			// Not mounted read-only -- not mounted. The actuator reads no local signal, and the
			// halt came from reading rather than writing, so removing the mount is what closes it.
			for _, mount := range daemonSet.Spec.Template.Spec.Containers[1].VolumeMounts {
				Expect(mount.Name).NotTo(Equal("power-agent-run"),
					"the actuator must not mount the shared signal tmpfs at all (F-57)")
			}
			// F-36: the poweroff mechanism is fixed to the reboot(2) syscall and is not configurable.
			// No env var should be able to redirect it to an arbitrary host command.
			for _, variable := range daemonSet.Spec.Template.Spec.Containers[1].Env {
				Expect(variable.Name).NotTo(HavePrefix("POWER_POWEROFF_"))
			}
			Expect(daemonSet.Spec.Template.Spec.Containers[1].VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name:      "power-agent-signals",
				MountPath: "/var/lib/power-agent/signals",
				ReadOnly:  true,
			}))
			// F-55: this agent declares no spec.shutdownFlowRef, so the actuator must be told to
			// accept any flow rather than handed "upsmon-local" -- the name the upsmon container
			// stamps for the locked-down local path, which no executor ever sends. Rendering it
			// here would reject every operator-issued release.
			Expect(daemonSet.Spec.Template.Spec.Containers[1].Env).To(ContainElement(corev1.EnvVar{
				Name:  "POWER_SHUTDOWN_FLOW",
				Value: "",
			}))
			// F-86: kubelet substitutes an empty directory for an absent optional Secret, so the
			// agent came up healthy against a channel that could never deliver.
			for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
				if volume.Name == "power-agent-signals" {
					Expect(volume.Secret).NotTo(BeNil())
					Expect(*volume.Secret.Optional).To(BeFalse(),
						"the only authorized delivery channel must not be optional (F-86)")
				}
			}

			By("publishing ready pod coverage for the selected node")
			heartbeat := metav1.NewTime(time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC))
			readyPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      "test-resource-node-power-agent-ready",
					Labels:    labelsForNodePowerAgent(resource),
				},
				Spec: corev1.PodSpec{
					NodeName: nodeName,
					Containers: []corev1.Container{{
						Name:  "upsmon",
						Image: "ghcr.io/michaelzalud18/upsmon-agent:main",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, readyPod)).To(Succeed())
			readyPod.Status.Phase = corev1.PodRunning
			readyPod.Status.Conditions = []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: heartbeat,
			}}
			Expect(k8sClient.Status().Update(ctx, readyPod)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(resource.Status.ReadyNodeCount).To(Equal(int32(1)))
			Expect(resource.Status.UnavailableNodeCount).To(Equal(int32(0)))
			Expect(resource.Status.NodeStatuses).To(HaveLen(1))
			Expect(resource.Status.NodeStatuses[0].NodeName).To(Equal(nodeName))
			Expect(resource.Status.NodeStatuses[0].Ready).To(BeTrue())
			Expect(resource.Status.NodeStatuses[0].Reason).To(Equal("AgentPodReady"))
			Expect(resource.Status.NodeStatuses[0].PodName).To(Equal("test-resource-node-power-agent-ready"))
			Expect(resource.Status.NodeStatuses[0].LastHeartbeatTime.Time).To(BeTemporally("==", heartbeat.Time))
		})

		It("converges from a partial, stale operand state and stays stable on a repeat reconcile (F-7)", func() {
			By("seeding a stale operand ConfigMap as if an earlier reconcile crashed midway through rendering")
			Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "test-resource-node-agent-config", Namespace: namespace},
				Data:       map[string]string{"nut.conf": "stale-content-from-a-previous-spec\n"},
			})).To(Succeed())

			controllerReconciler := &NodePowerAgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("reconciling once: should converge to the full desired state despite the stale, partial seed")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			configMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-agent-config"}, configMap)).To(Succeed())
			Expect(configMap.Data["nut.conf"]).To(Equal("MODE=netclient\n"))
			Expect(configMap.OwnerReferences).NotTo(BeEmpty())

			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)).To(Succeed())
			Expect(ns.Labels).To(HaveKeyWithValue("power.zalud.io/operand-namespace", "true"))

			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, daemonSet)).To(Succeed())
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-upsmon-config"}, secret)).To(Succeed())
			serviceAccount := &corev1.ServiceAccount{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, serviceAccount)).To(Succeed())
			networkPolicy := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, networkPolicy)).To(Succeed())

			daemonSetResourceVersion := daemonSet.ResourceVersion
			configMapResourceVersion := configMap.ResourceVersion
			namespaceResourceVersion := ns.ResourceVersion

			By("reconciling again with nothing changed: should be a stable no-op, not a spurious rewrite")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, daemonSet)).To(Succeed())
			Expect(daemonSet.ResourceVersion).To(Equal(daemonSetResourceVersion))
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-agent-config"}, configMap)).To(Succeed())
			Expect(configMap.ResourceVersion).To(Equal(configMapResourceVersion))
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: namespace}, ns)).To(Succeed())
			Expect(ns.ResourceVersion).To(Equal(namespaceResourceVersion))
		})

		It("should finalize and actually delete on deletion", func() {
			controllerReconciler := &NodePowerAgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			By("Reconciling once to add the finalizer")
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			resource := &powerv1alpha1.NodePowerAgent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(controllerutil.ContainsFinalizer(resource, nodePowerAgentFinalizer)).To(BeTrue())

			By("Deleting the resource")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Reconciling again to process the finalizer")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, &powerv1alpha1.NodePowerAgent{})).To(HaveOccurred())
		})

		It("renders approved host poweroff with the narrow actuator privilege profile", func() {
			resource := &powerv1alpha1.NodePowerAgent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Annotations = map[string]string{"power.zalud.io/approved-for-actuation": "true"}
			resource.Spec.Mode = powerv1alpha1.NodePowerAgentModeActuate
			resource.Spec.Shutdown.ActuatorPolicy = powerv1alpha1.ActuatorPolicySystemdPoweroff
			resource.Spec.Shutdown.ApprovalAnnotation = "power.zalud.io/approved-for-actuation"
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			controllerReconciler := &NodePowerAgentReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			daemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, daemonSet)).To(Succeed())
			Expect(daemonSet.Spec.Template.Spec.HostPID).To(BeTrue())
			Expect(daemonSet.Spec.Template.Spec.AutomountServiceAccountToken).NotTo(BeNil())
			Expect(*daemonSet.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())
			Expect(daemonSet.Spec.Template.Spec.Containers).To(HaveLen(2))
			actuator := daemonSet.Spec.Template.Spec.Containers[1]
			Expect(actuator.Name).To(Equal("actuator"))
			Expect(actuator.SecurityContext).NotTo(BeNil())
			Expect(*actuator.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
			Expect(*actuator.SecurityContext.ReadOnlyRootFilesystem).To(BeTrue())
			Expect(actuator.SecurityContext.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
			Expect(actuator.SecurityContext.Capabilities.Add).To(ConsistOf(corev1.Capability("SYS_BOOT")))
			// F-62: no seccomp override, so the pod's RuntimeDefault applies. Measured on kind with
			// the capability actually held: RuntimeDefault + CAP_SYS_BOOT reaches the kernel's
			// reboot handler (EINVAL on the argument), while the same profile without the
			// capability is refused with EPERM. The capability is the gate; Unconfined was removing
			// every other syscall filter from the one container that can halt a host, and buying
			// nothing for it.
			Expect(actuator.SecurityContext.SeccompProfile).To(BeNil())
			Expect(actuator.Env).To(ContainElement(corev1.EnvVar{Name: "POWER_AGENT_MODE", Value: "Actuate"}))
			Expect(actuator.Env).To(ContainElement(corev1.EnvVar{Name: "POWER_ACTUATOR_POLICY", Value: "SystemdPoweroff"}))
			for _, variable := range actuator.Env {
				Expect(variable.Name).NotTo(HavePrefix("POWER_POWEROFF_"))
			}
		})

		It("reconciles via a real running manager and reacts to a labeled Pod through the dedicated watch (F-32)", func() {
			By("starting a real manager with SetupWithManager, exactly as cmd/main.go does")
			mgr, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme:                 k8sClient.Scheme(),
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect((&NodePowerAgentReconciler{
				Client: mgr.GetClient(),
				Scheme: mgr.GetScheme(),
			}).SetupWithManager(mgr)).To(Succeed())

			mgrCtx, mgrCancel := context.WithCancel(ctx)
			defer mgrCancel()
			go func() {
				defer GinkgoRecover()
				Expect(mgr.Start(mgrCtx)).To(Succeed())
			}()
			Expect(mgr.GetCache().WaitForCacheSync(mgrCtx)).To(BeTrue())

			By("waiting for the manager's own initial reconcile to render the DaemonSet")
			daemonSet := &appsv1.DaemonSet{}
			Eventually(func() error {
				return k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: "test-resource-node-power-agent"}, daemonSet)
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			By("creating a ready Pod carrying the NodePowerAgent label and letting the dedicated watch pick it up")
			resource := &powerv1alpha1.NodePowerAgent{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			watchedPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      "test-resource-node-power-agent-watched",
					Labels:    labelsForNodePowerAgent(resource),
				},
				Spec: corev1.PodSpec{
					NodeName: nodeName,
					Containers: []corev1.Container{{
						Name:  "upsmon",
						Image: "ghcr.io/michaelzalud18/upsmon-agent:main",
					}},
				},
			}
			Expect(k8sClient.Create(ctx, watchedPod)).To(Succeed())
			watchedPod.Status.Phase = corev1.PodRunning
			watchedPod.Status.Conditions = []corev1.PodCondition{{
				Type:               corev1.PodReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now()),
			}}
			Expect(k8sClient.Status().Update(ctx, watchedPod)).To(Succeed())

			By("asserting the manager reconciled off the Pod watch alone -- no manual Reconcile call here")
			Eventually(func() int32 {
				current := &powerv1alpha1.NodePowerAgent{}
				if err := k8sClient.Get(ctx, typeNamespacedName, current); err != nil {
					return -1
				}
				return current.Status.ReadyNodeCount
			}, 10*time.Second, 100*time.Millisecond).Should(Equal(int32(1)))

			mgrCancel()
			Expect(k8sClient.Delete(ctx, watchedPod)).To(Succeed())
		})
	})
})
