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
	"fmt"
	"strings"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *NodePowerAgentReconciler) ensureNodePowerAgentServiceAccount(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string) (*corev1.ServiceAccount, error) {
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentServiceAccountName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
		serviceAccount.Labels = labelsForNodePowerAgent(agent)
		serviceAccount.AutomountServiceAccountToken = ptrBool(false)
		return controllerutil.SetControllerReference(agent, serviceAccount, r.Scheme)
	})
	return serviceAccount, err
}

type nodePowerAgentDaemonSetSpec struct {
	ConfigMapName      string
	SecretName         string
	ServiceAccountName string
	ConfigHash         string
	UpsmonImage        string
	UpsmonPullPolicy   corev1.PullPolicy
	ActuatorImage      string
	ActuatorPullPolicy corev1.PullPolicy
	SignalSecretName   string
	SelectedUPSDevices []string
	MountServerCA      bool
	Talos              *nodePowerAgentTalosConfig
}

// ensureNodePowerAgentDaemonSet renders the agent DaemonSet, deferring the write while a flow is live.
//
// heldBy names that flow when one is (F-92). The rendered spec is correct at any moment; the timing
// is what is wrong. maxSurge: 1 with maxUnavailable: 0 means a rollout replaces monitoring pods
// node by node, and doing that while a shutdown flow is releasing nodes churns exactly the workload
// whose absence is the failure it exists to prevent. A config change can wait for the outage to end;
// the outage cannot wait for the config change.
//
// A DaemonSet that does not exist yet is created regardless. A node with no agent at all is worse
// than a node whose agent restarts at an awkward moment, and deferring creation would mean an agent
// added mid-outage never arrives.
func (r *NodePowerAgentReconciler) ensureNodePowerAgentDaemonSet(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace, heldBy string, spec nodePowerAgentDaemonSetSpec) (*appsv1.DaemonSet, error) {
	if heldBy != "" {
		var existing appsv1.DaemonSet
		key := types.NamespacedName{Namespace: namespace, Name: nodePowerAgentDaemonSetName(agent)}
		err := r.Get(ctx, key, &existing)
		if err == nil {
			return &existing, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("get DaemonSet %s/%s while a flow holds rollouts: %w", key.Namespace, key.Name, err)
		}
	}

	podNodeSelector, err := nodePowerAgentPodNodeSelector(agent)
	if err != nil {
		return nil, err
	}
	affinity, err := nodePowerAgentAffinity(agent)
	if err != nil {
		return nil, err
	}

	labels := labelsForNodePowerAgent(agent)
	hostPoweroff := nodePowerAgentRequiresHostPoweroff(agent)
	daemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentDaemonSetName(agent), Namespace: namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, daemonSet, func() error {
		daemonSet.Labels = labels
		daemonSet.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		daemonSet.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
			Type: appsv1.RollingUpdateDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{
				// Surge rather than go unavailable (F-72). maxUnavailable: 1 left a node with no
				// agent for a full pull-and-start window on every rollout, and this is a workload
				// whose absence is the failure it exists to prevent. Nothing blocks two agent pods
				// briefly coexisting: the pod declares no hostPort and no hostNetwork.
				MaxUnavailable: ptrIntOrStringFromInt32(0),
				MaxSurge:       ptrIntOrStringFromInt32(1),
			},
		}
		daemonSet.Spec.Template.Labels = labels
		daemonSet.Spec.Template.Annotations = map[string]string{
			"power.zalud.io/config-hash": spec.ConfigHash,
		}
		daemonSet.Spec.Template.Spec.ServiceAccountName = spec.ServiceAccountName
		daemonSet.Spec.Template.Spec.AutomountServiceAccountToken = ptrBool(false)
		daemonSet.Spec.Template.Spec.HostPID = hostPoweroff
		daemonSet.Spec.Template.Spec.NodeSelector = podNodeSelector
		daemonSet.Spec.Template.Spec.Tolerations = nodePowerAgentTolerations(agent)
		daemonSet.Spec.Template.Spec.Affinity = affinity
		daemonSet.Spec.Template.Spec.PriorityClassName = nodePowerAgentPriorityClassName(agent)
		daemonSet.Spec.Template.Spec.TerminationGracePeriodSeconds = ptrInt64(nodePowerAgentDefaultTerminationGracePeriodSecs)
		daemonSet.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot: ptrBool(true),
			RunAsUser:    ptrInt64(65532),
			RunAsGroup:   ptrInt64(65532),
			FSGroup:      ptrInt64(65532),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}
		daemonSet.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				// One projected volume rather than two subPath mounts (F-69).
				//
				// A subPath mount is resolved once at container start and never receives ConfigMap
				// or Secret updates, which is why the config-hash rolling restart was the only
				// mechanism that could work rather than the one chosen. upsmon reads its config
				// from a compiled-in sysconfdir and has no flag to point it elsewhere -- -c takes a
				// command, not a path -- so the files have to arrive at /etc/nut under their real
				// names. A projected volume is what lets both sources do that through one mount
				// that does propagate updates.
				//
				// Masking /etc/nut costs nothing: the image ships only *.sample files there, and
				// the TLS CApath deliberately lives outside it so this mount does not shadow it.
				Name: "upsmon-etc",
				VolumeSource: corev1.VolumeSource{
					Projected: &corev1.ProjectedVolumeSource{
						DefaultMode: ptrInt32(0440),
						Sources: []corev1.VolumeProjection{
							{
								ConfigMap: &corev1.ConfigMapProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: spec.ConfigMapName},
									Items: []corev1.KeyToPath{
										{Key: nodePowerAgentConfigFile, Path: nodePowerAgentConfigFile},
									},
								},
							},
							{
								Secret: &corev1.SecretProjection{
									LocalObjectReference: corev1.LocalObjectReference{Name: spec.SecretName},
									Items: []corev1.KeyToPath{
										{Key: upsmonConfigFile, Path: upsmonConfigFile},
									},
								},
							},
						},
					},
				},
			},
			{
				Name: "power-agent-run",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(16*1024*1024, resource.BinarySI)},
				},
			},
			{
				// The actuator's own health evidence, mounted into the actuator and nowhere else
				// (F-64). It is deliberately not the power-agent-run emptyDir the signal writer
				// also has open for writing: health evidence a neighbouring container can rewrite
				// is not evidence, and F-57 is already open on that shared boundary.
				Name: "power-agent-actuator-state",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(1024*1024, resource.BinarySI)},
				},
			},
			{
				Name: "power-agent-signals",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  spec.SignalSecretName,
						DefaultMode: ptrInt32(0440),
						// Not optional (F-86). The operator reconciles this Secret before the
						// DaemonSet and it always carries the delivery-channel marker, so a missing
						// one is not a startup race -- it means the only authorized path to a halt
						// does not exist, and a pod that refuses to start says that far louder than
						// one that runs blind against an empty directory.
						Optional: ptrBool(false),
					},
				},
			},
			{
				Name: "upsmon-run",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(16*1024*1024, resource.BinarySI)},
				},
			},
		}
		daemonSet.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:            "upsmon",
				Image:           spec.UpsmonImage,
				ImagePullPolicy: spec.UpsmonPullPolicy,
				Command:         []string{"upsmon"},
				// -F, not -D (F-67). Both foreground upsmon, but -D does it as a side effect of
				// raising the debugging level, so every agent in the fleet ran at debug level
				// permanently to get a behavior -F provides on its own. upsmon has no -FF: unlike
				// upsd it has no PID-file-on-foreground variant to select.
				Args:            []string{"-F"},
				Resources:       agent.Spec.Resources.Upsmon,
				SecurityContext: restrictedContainerSecurityContext(),
				ReadinessProbe:  upsmonReadinessProbe(),
				LivenessProbe:   upsmonLivenessProbe(),
				Env:             nodePowerAgentSignalEnv(agent, spec.ConfigHash, spec.SelectedUPSDevices),
				VolumeMounts: []corev1.VolumeMount{
					// A directory mount, so config updates reach the container (F-69).
					{Name: "upsmon-etc", MountPath: nodePowerAgentConfigDirectory, ReadOnly: true},
					{Name: "upsmon-run", MountPath: "/run"},
					{Name: "power-agent-run", MountPath: "/run/power-agent"},
				},
			},
		}
		if spec.MountServerCA {
			// CERTPATH is an OpenSSL CApath, so it has to be a directory whose entries are
			// named by subject hash. A projected Secret cannot carry those symlinks, so an
			// init container rehashes the bundle into an emptyDir and upsmon reads that.
			daemonSet.Spec.Template.Spec.Volumes = append(
				daemonSet.Spec.Template.Spec.Volumes,
				corev1.Volume{
					Name: "upsmon-server-ca-source",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  spec.SecretName,
							DefaultMode: ptrInt32(0440),
							Items: []corev1.KeyToPath{
								{Key: nodePowerAgentServerCAFile, Path: nodePowerAgentServerCAFile},
							},
						},
					},
				},
				corev1.Volume{
					Name: "upsmon-server-ca",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium:    corev1.StorageMediumMemory,
							SizeLimit: resource.NewQuantity(1024*1024, resource.BinarySI),
						},
					},
				},
			)
			daemonSet.Spec.Template.Spec.InitContainers = append(
				daemonSet.Spec.Template.Spec.InitContainers,
				corev1.Container{
					Name:            "rehash-server-ca",
					Image:           spec.UpsmonImage,
					ImagePullPolicy: spec.UpsmonPullPolicy,
					Command:         []string{"/bin/sh", "-c", nodePowerAgentServerCARehashScript()},
					SecurityContext: restrictedContainerSecurityContext(),
					VolumeMounts: []corev1.VolumeMount{
						{
							// Its own Secret mount: the agent's /etc/nut projection deliberately
							// carries only upsmon.conf, so the CA bundle is not there to read.
							Name:      "upsmon-server-ca-source",
							MountPath: nodePowerAgentServerCASourceDirectory,
							ReadOnly:  true,
						},
						{Name: "upsmon-server-ca", MountPath: nodePowerAgentServerCAPath},
					},
				},
			)
			daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts = append(
				daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts,
				corev1.VolumeMount{
					Name:      "upsmon-server-ca",
					MountPath: nodePowerAgentServerCAPath,
					ReadOnly:  true,
				},
			)
		}
		if spec.Talos.enabled() {
			daemonSet.Spec.Template.Spec.Volumes = append(daemonSet.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "talosconfig",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  spec.Talos.ConfigSecretName,
						DefaultMode: ptrInt32(0440),
						Items: []corev1.KeyToPath{
							{Key: spec.Talos.ConfigSecretKey, Path: "config"},
						},
					},
				},
			})
		}
		if spec.ActuatorImage != "" {
			actuatorVolumeMounts := []corev1.VolumeMount{
				{Name: "power-agent-signals", MountPath: nodePowerAgentProjectedSignalDirectory, ReadOnly: true},
				{Name: "power-agent-actuator-state", MountPath: nodePowerAgentActuatorStateDirectory},
			}
			if spec.Talos.enabled() {
				actuatorVolumeMounts = append(actuatorVolumeMounts, corev1.VolumeMount{
					Name:      "talosconfig",
					MountPath: nodePowerAgentTalosConfigDirectory,
					ReadOnly:  true,
				})
			}
			daemonSet.Spec.Template.Spec.Containers = append(daemonSet.Spec.Template.Spec.Containers, corev1.Container{
				Name:            "actuator",
				Image:           spec.ActuatorImage,
				ImagePullPolicy: spec.ActuatorPullPolicy,
				Resources:       agent.Spec.Resources.Actuator,
				Env:             nodePowerAgentActuatorEnv(agent, spec.Talos),
				SecurityContext: actuatorContainerSecurityContext(hostPoweroff),
				ReadinessProbe:  actuatorReadinessProbe(),
				// power-agent-run is deliberately absent. The actuator no longer reads the local
				// signal, and a volume it does not read is a volume it cannot be tricked through --
				// stronger than mounting the shared tmpfs read-only, which would have left the read
				// path open while closing a write path that was never the threat.
				VolumeMounts: actuatorVolumeMounts,
			})
		}
		return controllerutil.SetControllerReference(agent, daemonSet, r.Scheme)
	})
	return daemonSet, err
}

// upsmonLivenessProbe restarts upsmon if the process itself has died, and only that (F-35). It is
// deliberately NOT tied to NUT server reachability -- that's upsmonReadinessProbe's job -- so a upsmon
// that's alive but can't currently reach its configured UPS server stays up and NotReady rather than
// getting restarted, which would just churn the same failure. This is the read-only monitoring
// container, not the actuator: unlike the actuator (see actuatorReadinessProbe's caller in
// ensureNodePowerAgentDaemonSet, which deliberately carries no LivenessProbe at all), a restart here
// mid-event has no risk of re-triggering or losing actuation signal state.
func upsmonLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"pgrep", "-x", "upsmon"},
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       30,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}
}

// upsmonReadinessProbe checks that every UPS this agent is configured to monitor is actually being
// served to it (F-65).
//
// Two things were wrong with the previous script, and the first is the serious one. It piped the
// `MONITOR` targets into a `while` loop, so with zero targets the loop body never ran and the
// pipeline exited 0 -- a rendering bug that dropped every monitor line presented as a healthy agent.
// The rewrite counts the targets first and fails on none, and runs the loop in the current shell so
// `set -e` applies to it.
//
// Second, it ran `upsc -l "$server"`, an anonymous LIST UPS against the host half of the target.
// That proves a NUT server is listening and nothing else: it does not check that the UPS this agent
// monitors is among the ones served, which is the actual precondition for upsmon working. Querying
// the full `<ups>@<server>` target does.
//
// The credentials and TLS posture are covered by the third step rather than by upsc, which cannot
// reach them: upsc does not read upsmon.conf, so it exercises neither. That was the gap `F-40` fell
// into, where upsmon failed with an SSL error against a server upsc reached fine. COMMOK/COMMBAD
// come from upsmon's own session, so `power-notify-writer --check` answers the question upsc cannot
// -- it fails when upsmon's last word on a UPS was that it lost contact (`F-68` supplies the
// events). Silence passes: an agent that has never dispatched one has nothing to report.
func upsmonReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{
					"sh",
					"-ec",
					`test -r /etc/nut/upsmon.conf
targets=$(awk '/^MONITOR[[:space:]]/ {print $2}' /etc/nut/upsmon.conf)
test -n "$targets"
for target in $targets; do
  case "$target" in *@*) ;; *) exit 1 ;; esac
  upsc "$target" >/dev/null
done
power-notify-writer --check`,
				},
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       30,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}
}

// actuatorReadinessProbe asks whether the watch loop is running (F-64).
//
// It used to exec `--version`, which prints a string and returns before any configuration is
// parsed. That passed on a process parked forever in block(), on one whose signal directory had
// never appeared, and on one that had stopped looping entirely -- the three things readiness is for.
// `F-46` retired the same instruction on the server image: a check that cannot fail is worse than no
// check, because it reads as coverage.
//
// `--ready` reads the state file the loop writes after each pass and fails on a missing, stale, or
// unparseable record, or on a signal directory the loop could not see. Under a Disabled policy it
// passes without consulting it, because a process whose configured job is to block has no loop to
// report on and the exec itself proves the container is alive.
func actuatorReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/node-actuator", "--ready"},
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       30,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}
}

func nodePowerAgentActuatorEnv(agent *powerv1alpha1.NodePowerAgent, talos *nodePowerAgentTalosConfig) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "POWER_AGENT_MODE", Value: string(nodePowerAgentMode(agent))},
		{Name: "POWER_ACTUATOR_POLICY", Value: string(nodePowerAgentActuatorPolicy(agent))},
		{
			Name: "POWER_NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "spec.nodeName"},
			},
		},
		// The projected Secret, and nothing else (F-57, OD-37). The local tmpfs path the upsmon
		// container writes through SHUTDOWNCMD used to lead this list, which made the
		// network-facing container able to halt the host by writing one file. It is gone from both
		// variables rather than reordered: the operator path is the only path with authority, so a
		// second entry here is not a fallback, it is the bypass.
		{Name: "POWER_SIGNAL_PATHS", Value: nodePowerAgentProjectedSignalPath},
		{Name: "POWER_SIGNAL_TTL", Value: durationString(agent.Spec.Shutdown.SignalTTL, "2m")},
		{Name: "POWER_ACTUATOR_STATE_PATH", Value: nodePowerAgentActuatorStatePath},
		// Only when the agent declares a flow (F-55). nodePowerAgentShutdownFlowName falls back to
		// "upsmon-local" for the upsmon container, which is a name for the locked-down local path
		// rather than a flow anything issues signals under -- rendering it here would make the
		// actuator compare against a value the executor can never send and reject every release.
		{Name: "POWER_SHUTDOWN_FLOW", Value: nodePowerAgentDeclaredShutdownFlow(agent)},
	}
	if !talos.enabled() {
		return env
	}

	nodeFieldPath := "status.hostIP"
	if talos.NodeAddressSource == powerv1alpha1.TalosNodeAddressSourceNodeName {
		nodeFieldPath = "spec.nodeName"
	}
	return append(env,
		corev1.EnvVar{Name: "POWER_TALOS_CONFIG", Value: nodePowerAgentTalosConfigPath},
		corev1.EnvVar{Name: "POWER_TALOS_ENDPOINTS", Value: strings.Join(talos.Endpoints, ",")},
		corev1.EnvVar{
			Name: "POWER_TALOS_NODE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: nodeFieldPath},
			},
		},
		corev1.EnvVar{Name: "POWER_TALOS_SHUTDOWN_TIMEOUT", Value: talos.ShutdownTimeout},
	)
}

func nodePowerAgentSignalEnv(agent *powerv1alpha1.NodePowerAgent, configHash string, selectedUPSDevices []string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "POWER_AGENT_MODE", Value: string(nodePowerAgentMode(agent))},
		{
			Name: "POWER_NODE_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "spec.nodeName"},
			},
		},
		{Name: "POWER_SIGNAL_PATH", Value: nodePowerAgentSignalPath(agent)},
		{Name: "POWER_SIGNAL_TTL", Value: durationString(agent.Spec.Shutdown.SignalTTL, "2m")},
		{Name: "POWER_SIGNAL_HOLD_AFTER_WRITE", Value: "true"},
		{Name: "POWER_AGENT_CONFIG_HASH", Value: configHash},
		{Name: "POWER_SELECTED_UPS_DEVICES", Value: strings.Join(selectedUPSDevices, ",")},
		{Name: "POWER_SHUTDOWN_FLOW", Value: nodePowerAgentShutdownFlowName(agent)},
		{Name: "POWER_SIGNAL_REASON", Value: nodePowerAgentSignalReason},
		{Name: "POWER_NOTIFY_STATE_PATH", Value: nodePowerAgentNotifyStatePath},
	}
}
