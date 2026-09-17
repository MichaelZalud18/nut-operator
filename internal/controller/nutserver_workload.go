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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// upsdReadinessProbeScript accepts output from `upsdrvctl status` when at least one field
// equals RESPONSIVE. This flag check does not establish live driver health (NS-1).
//
// `upsc -l` cannot answer it: it lists every name defined in ups.conf
// whether or not the driver ever connected, so a fully-disconnected driver still reports as
// present.
//
// The match is field-exact and deliberately so: NOT_RESPONSIVE contains RESPONSIVE as a
// substring, so a grep for the token would report every dead driver as healthy -- a readiness
// probe that can never fail. Comparing whole awk fields also skips the header row for free,
// since its token is S_RESPONSIVE.
func upsdReadinessProbeScript() string {
	return `upsdrvctl status 2>/dev/null | ` +
		`awk '{for (i = 1; i <= NF; i++) if ($i == "RESPONSIVE") { found = 1; exit } } END { exit !found }'`
}

// upsdResources returns what the upsd container asks for, defaulting it when spec.resources says
// nothing.
//
// Requests equal limits for the same reason they do on the supervisor: the pod is Guaranteed, so it
// sits in the last eviction class during node pressure, when upsd must remain available.
//
// spec.resources is honoured verbatim the moment it declares anything at all. Filling in
// individual missing keys would quietly convert a deliberate requests-only declaration into a
// Guaranteed pod, so a user who states part of the shape owns all of it.
func upsdResources(server *powerv1alpha1.NUTServer) corev1.ResourceRequirements {
	declared := server.Spec.Resources
	if len(declared.Requests) > 0 || len(declared.Limits) > 0 || len(declared.Claims) > 0 {
		return declared
	}
	return defaultUpsdResources()
}

// defaultUpsdResources sizes the protocol server independently of the driver supervisor.
func defaultUpsdResources() corev1.ResourceRequirements {
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
	return corev1.ResourceRequirements{Requests: requests, Limits: limits}
}

// driverSupervisorResources sizes the driver sidecar without spending the user's upsd budget on it.
//
// spec.resources describes upsd. Reusing it here would silently double whatever the user declared
// for the server, which is the kind of surprise that shows up as an unschedulable pod on a full
// node rather than as an error. The supervisor owns the variable-cost process set -- one NUT driver
// worker per configured UPS -- so it gets its own conservative default.
//
// Requests equal limits deliberately. Without them the sidecar would drag a pod whose upsd is
// Guaranteed down to Burstable, changing the eviction ordering of the server on the observability
// path for every agent -- an operand-shape change nobody asked for.
func driverSupervisorResources() corev1.ResourceRequirements {
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("128Mi"),
	}
	return corev1.ResourceRequirements{Requests: requests, Limits: limits}
}

func serviceType(server *powerv1alpha1.NUTServer) corev1.ServiceType {
	if server.Spec.Service.Type != "" {
		return server.Spec.Service.Type
	}
	return corev1.ServiceTypeClusterIP
}

func (r *NUTServerReconciler) ensureNUTServerService(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (*corev1.Service, error) {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		service.Labels = labelsForNUTServer(server)
		service.Annotations = server.Spec.Service.Annotations
		service.Spec.Type = serviceType(server)
		service.Spec.Selector = labelsForNUTServer(server)
		service.Spec.Ports = []corev1.ServicePort{
			{
				Name:     nutServerPortName,
				Protocol: corev1.ProtocolTCP,
				Port:     servicePort(server),
			},
		}
		return controllerutil.SetControllerReference(server, service, r.Scheme)
	})
	return service, err
}

func (r *NUTServerReconciler) ensureNUTServerDeployment(ctx context.Context, server *powerv1alpha1.NUTServer, namespace, image, configName, driverConfigSecretName string, secretRef powerv1alpha1.NamespacedNameReference, configHash string, devices []powerv1alpha1.UPSDevice) (*appsv1.Deployment, error) {
	upstreamAuthProjections, err := upstreamNUTAuthProjections(devices, namespace)
	if err != nil {
		return nil, err
	}
	replicas := int32(1)
	if server.Spec.Replicas != nil {
		replicas = *server.Spec.Replicas
	}
	pullPolicy := server.Spec.Image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}
	labels := labelsForNUTServer(server)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deploymentName(server), Namespace: namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Labels = labels
		deployment.Spec.Replicas = &replicas
		// Recreate: upsd is a singleton with long-lived client TCP sessions and NUT's own
		// login accounting. RollingUpdate would briefly run two instances and split that
		// accounting, violating the single-replica constraint. A short outage
		// window on upgrade is the accepted trade-off.
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType,
		}
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Template.Labels = labels
		deployment.Spec.Template.Annotations = map[string]string{
			"power.zalud.io/config-hash": configHash,
		}
		deployment.Spec.Template.Spec.NodeSelector = server.Spec.Placement.NodeSelector
		deployment.Spec.Template.Spec.Tolerations = server.Spec.Placement.Tolerations
		deployment.Spec.Template.Spec.Affinity = server.Spec.Placement.Affinity
		deployment.Spec.Template.Spec.PriorityClassName = server.Spec.Placement.PriorityClassName
		deployment.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot: ptrBool(true),
			RunAsUser:    ptrInt64(65532),
			RunAsGroup:   ptrInt64(65532),
			FSGroup:      ptrInt64(65532),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}
		// The supervisor signals upsd with `upsd -c reload`, and signalling across the
		// container boundary needs a shared PID namespace. Without it upsd is PID 1 in its own
		// container, so the PID file reads "1" and upsd refuses it -- "Ignoring invalid pid number
		// 1". The reload is not merely blocked but blocked in a way that resembles success, since
		// the command still exits 0.
		//
		// The isolation cost is small here in a way it would not be elsewhere: both containers run
		// the same image as the same non-root UID and are peers. This is not the node agent's
		// split, which has a trust boundary between a container that parses network
		// responses and one holding CAP_SYS_BOOT.
		//
		// It also makes the pause container PID 1 to reap orphaned drivers.
		deployment.Spec.Template.Spec.ShareProcessNamespace = ptrBool(true)
		deployment.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				Name: "nut-config",
				VolumeSource: corev1.VolumeSource{
					Projected: &corev1.ProjectedVolumeSource{
						DefaultMode: ptrInt32(0440),
						Sources: []corev1.VolumeProjection{
							{
								ConfigMap: &corev1.ConfigMapProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: configName},
								},
							},
							{
								Secret: &corev1.SecretProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: secretRef.Name},
									Items: []corev1.KeyToPath{
										{Key: "upsd.users", Path: "upsd.users"},
									},
								},
							},
							{
								Secret: &corev1.SecretProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: driverConfigSecretName},
									Items: []corev1.KeyToPath{
										{Key: "ups.conf", Path: "ups.conf"},
									},
								},
							},
						},
					},
				},
			},
			{
				Name: "nut-run",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(16*1024*1024, resource.BinarySI)},
				},
			},
		}
		deployment.Spec.Template.Spec.Volumes[0].Projected.Sources = append(
			deployment.Spec.Template.Spec.Volumes[0].Projected.Sources,
			upstreamAuthProjections...,
		)
		deployment.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:            nutServerUpsdContainerName,
				Image:           image,
				ImagePullPolicy: pullPolicy,
				Ports: []corev1.ContainerPort{
					{
						Name:          nutServerPortName,
						ContainerPort: servicePort(server),
						Protocol:      corev1.ProtocolTCP,
					},
				},
				Resources: upsdResources(server),
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptrBool(false),
					ReadOnlyRootFilesystem:   ptrBool(true),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "nut-config", MountPath: "/etc/nut", ReadOnly: true},
					{Name: "nut-run", MountPath: "/run/nut"},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						Exec: &corev1.ExecAction{
							Command: []string{"sh", "-c", upsdReadinessProbeScript()},
						},
					},
					InitialDelaySeconds: upsdReadinessInitialDelaySeconds,
					PeriodSeconds:       upsdReadinessPeriodSeconds,
					TimeoutSeconds:      upsdReadinessTimeoutSeconds,
					FailureThreshold:    upsdReadinessFailureThreshold,
				},
			},
			{
				// The driver supervisor runs the same image and reaches the drivers the same way
				// upsd does: through /run/nut, which holds sockets, state, and PID files, and
				// /etc/nut, which is where upsdrvctl reads the device list from. It needs no
				// privilege upsd does not already have, and it carries no probes -- a restart of
				// driver supervision must never take the server down with it, which is the whole
				// reason it is a separate container.
				Name:            driverSupervisorContainerName,
				Image:           image,
				ImagePullPolicy: pullPolicy,
				Command:         []string{"/usr/local/bin/nut-driver-supervisor"},
				Resources:       driverSupervisorResources(),
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptrBool(false),
					ReadOnlyRootFilesystem:   ptrBool(true),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "nut-config", MountPath: "/etc/nut", ReadOnly: true},
					{Name: "nut-run", MountPath: "/run/nut"},
				},
			},
		}
		applyNUTServerTLSOperand(deployment, server, image, pullPolicy)
		return controllerutil.SetControllerReference(server, deployment, r.Scheme)
	})
	return deployment, err
}

// ensureNUTServerPodDisruptionBudget renders a PDB with minAvailable 1. Paired with the
// single-replica constraint, this blocks voluntary eviction of the sole upsd pod entirely, which is the
// desired behavior: upsd is on the observability path for every NodePowerAgent, and draining it
// mid-event puts every agent into DEADTIME simultaneously.
func (r *NUTServerReconciler) ensureNUTServerPodDisruptionBudget(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (*policyv1.PodDisruptionBudget, error) {
	minAvailable := intstr.FromInt32(1)
	labels := labelsForNUTServer(server)
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: podDisruptionBudgetName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pdb, func() error {
		pdb.Labels = labels
		pdb.Spec.MinAvailable = &minAvailable
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		return controllerutil.SetControllerReference(server, pdb, r.Scheme)
	})
	return pdb, err
}

// serviceClusterIP returns the Service's allocated cluster IP, if it has a usable one.
//
// "None" is what a headless Service reports, and it is not an address. Returning it would render a
// MONITOR line pointing at a literal "None" that fails every connection with no obvious cause.
func serviceClusterIP(service *corev1.Service) string {
	if service == nil || service.Spec.ClusterIP == corev1.ClusterIPNone {
		return ""
	}
	return service.Spec.ClusterIP
}
