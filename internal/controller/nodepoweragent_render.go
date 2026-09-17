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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	nodePowerAgentConfigFile               = "nut.conf"
	nodePowerAgentProjectedSignalDirectory = "/var/lib/power-agent/signals"
	nodePowerAgentProjectedSignalPath      = nodePowerAgentProjectedSignalDirectory + "/$(POWER_NODE_NAME).json"
	nodePowerAgentSignalReason             = "upsmon-fsd"
	// A directory mount rather than subPath, so config updates reach the container.
	nodePowerAgentConfigDirectory = "/etc/nut"
	// nodePowerAgentNotifyWriterPath receives upsmon's NOTIFYCMD dispatches.
	nodePowerAgentNotifyWriterPath = "/usr/local/bin/power-notify-writer"
	// nodePowerAgentNotifyStatePath is where those dispatches land for something else to read.
	nodePowerAgentNotifyStatePath = "/run/power-agent/notify.json"
	// nodePowerAgentActuatorStateDirectory is the actuator-only volume holding the record of its
	// watch loop, which is what makes readiness able to fail.
	nodePowerAgentActuatorStateDirectory            = "/run/actuator"
	nodePowerAgentActuatorStatePath                 = nodePowerAgentActuatorStateDirectory + "/state.json"
	nodePowerAgentSignalWriterPath                  = "/usr/local/bin/power-signal-writer"
	nodePowerAgentDefaultPriorityClassName          = "system-node-critical"
	nodePowerAgentDefaultTerminationGracePeriodSecs = 60
	upsmonConfigFile                                = "upsmon.conf"
	nodePowerAgentTalosAPIPort                      = 50000
	nodePowerAgentTalosConfigDirectory              = "/var/run/secrets/talos.dev"
	nodePowerAgentTalosConfigPath                   = nodePowerAgentTalosConfigDirectory + "/config"

	// nodePowerAgentServerCAFile is the key in the agent's rendered Secret holding the
	// concatenated CA bundle for every NUTServer this agent monitors.
	nodePowerAgentServerCAFile = "server-ca.pem"
	// nodePowerAgentServerCASourcePath is where that bundle is mounted read-only.
	nodePowerAgentServerCASourceDirectory = "/etc/nut-tls-source"
	nodePowerAgentServerCASourcePath      = nodePowerAgentServerCASourceDirectory + "/" + nodePowerAgentServerCAFile
	// nodePowerAgentServerCAPath is what upsmon's CERTPATH points at, and it must be a
	// directory rather than the bundle above.
	//
	// upsmon.conf(5) says CERTPATH accepts "a single PEM file with multiple CA
	// certificates", but the client passes it to SSL_CTX_load_verify_locations as the
	// CApath argument and never as CAfile (clients/upsclient.c). OpenSSL treats CApath as
	// a directory of hash-named certificates, so a file there loads without error and then
	// fails every verification -- CERTVERIFY plus FORCESSL turns that into a connection
	// that cannot be established at all.
	nodePowerAgentServerCAPath = "/var/lib/nut-tls/server-ca.d"
)

type renderedNodePowerAgent struct {
	Namespace              string
	SelectedNodes          []string
	DesiredNumberScheduled int32
	NumberReady            int32
	ReadyNodeCount         int32
	UnavailableNodeCount   int32
	NodeStatuses           []powerv1alpha1.NodePowerAgentNodeStatus
	ConfigHash             string
	ManagedResources       []powerv1alpha1.ManagedResourceStatus
	// TLSDowngradeReason is set when the rendered upsmon.conf is less strict than the monitored
	// NUTServers asked for. It is reported rather than treated as a render failure: refusing to
	// render would leave the node with no UPS monitoring at all, which is strictly worse than
	// monitoring over a weaker channel, but it must not pass silently either.
	TLSDowngradeReason string
	// PodSecurityConflict is set when the operand namespace enforces a Pod Security level that
	// will reject the actuating agent pod.
	PodSecurityConflict string
	// UncoveredNodes names inventory nodes this agent's selector does not match.
	UncoveredNodes []string
	// DaemonSetWriteHeldBy names the live ShutdownFlow that deferred this pass's DaemonSet spec
	// write. Empty when nothing is holding it.
	DaemonSetWriteHeldBy string
}

func (r *NodePowerAgentReconciler) reconcileNodePowerAgentOperands(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (renderedNodePowerAgent, error) {
	cluster, err := r.getManagementCluster(ctx, agent)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	namespace := nodePowerAgentNamespace(agent, cluster)
	operandNamespace, err := ensureOperandNamespace(ctx, r.Client, namespace, operandNamespaceCreateAllowed(cluster))
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	if err := validateNodePowerAgentRenderSafety(agent); err != nil {
		return renderedNodePowerAgent{}, err
	}

	// Only the actuating shape can be rejected on admission, so only it is checked.
	podSecurityConflict := ""
	if nodePowerAgentRequiresHostPoweroff(agent) {
		podSecurityConflict = nodePowerAgentPodSecurityConflict(operandNamespace)
	}

	upsmonImage, upsmonPullPolicy, err := nodePowerAgentUpsmonImage(agent, cluster)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	actuatorImage, actuatorPullPolicy, err := nodePowerAgentActuatorImage(agent, cluster)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	targets, egressRules, err := r.resolveAgentMonitorTargets(ctx, agent, namespace)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	talosConfig, talosEgressRules, err := r.resolveNodePowerAgentTalosConfig(ctx, agent, namespace)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	egressRules = append(egressRules, talosEgressRules...)

	tlsPosture := deriveAgentTLSPosture(targets)
	configData := renderNodePowerAgentConfig()
	secretData, err := renderNodePowerAgentSecret(agent, targets, tlsPosture)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	configHash := hashStringMap(configData) + "-" + hashByteMap(secretData)

	configMap, err := r.ensureNodePowerAgentConfigMap(ctx, agent, namespace, configData)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	secret, err := r.ensureNodePowerAgentSecret(ctx, agent, namespace, secretData)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	serviceAccount, err := r.ensureNodePowerAgentServiceAccount(ctx, agent, namespace)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	signalSecret, err := r.ensureNodePowerAgentSignalSecret(ctx, agent, namespace)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	networkPolicy, err := r.ensureNodePowerAgentNetworkPolicy(ctx, agent, namespace, egressRules)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	heldBy, err := r.shutdownFlowHoldingRollouts(ctx)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	daemonSet, err := r.ensureNodePowerAgentDaemonSet(ctx, agent, namespace, heldBy, nodePowerAgentDaemonSetSpec{
		ConfigMapName:      configMap.Name,
		SecretName:         secret.Name,
		ServiceAccountName: serviceAccount.Name,
		ConfigHash:         configHash,
		UpsmonImage:        upsmonImage,
		UpsmonPullPolicy:   upsmonPullPolicy,
		ActuatorImage:      actuatorImage,
		ActuatorPullPolicy: actuatorPullPolicy,
		SignalSecretName:   signalSecret.Name,
		SelectedUPSDevices: nodePowerAgentSelectedUPSDevices(targets),
		MountServerCA:      len(tlsPosture.CABundle) > 0,
		Talos:              talosConfig,
	})
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	selectedNodes, err := r.selectedNodeNames(ctx, agent)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	nodeStatuses, readyNodeCount, unavailableNodeCount, err := r.nodePowerAgentNodeStatuses(ctx, agent, namespace, selectedNodes)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	uncoveredNodes, err := r.uncoveredInventoryNodes(ctx, selectedNodes)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	return renderedNodePowerAgent{
		Namespace:              namespace,
		SelectedNodes:          selectedNodes,
		DesiredNumberScheduled: daemonSet.Status.DesiredNumberScheduled,
		NumberReady:            daemonSet.Status.NumberReady,
		ReadyNodeCount:         readyNodeCount,
		UnavailableNodeCount:   unavailableNodeCount,
		NodeStatuses:           nodeStatuses,
		ConfigHash:             configHash,
		TLSDowngradeReason:     tlsPosture.DowngradeReason,
		PodSecurityConflict:    podSecurityConflict,
		UncoveredNodes:         uncoveredNodes,
		DaemonSetWriteHeldBy:   heldBy,
		ManagedResources: []powerv1alpha1.ManagedResourceStatus{
			{APIVersion: "v1", Kind: "Namespace", Name: namespace},
			{APIVersion: "v1", Kind: "ServiceAccount", Namespace: namespace, Name: serviceAccount.Name},
			{APIVersion: "v1", Kind: "ConfigMap", Namespace: namespace, Name: configMap.Name, Hash: hashStringMap(configData)},
			// Publishing a hash of Secret contents in status is safe here and deliberate: it is
			// a one-way SHA-256 over a 32-byte random value from randomPassword(), serving the standard
			// config-hash-triggers-rollout pattern. Recovering the password from it is infeasible. The
			// NUTServer side reaches the opposite conclusion for its own credential Secret, where the
			// content is user-supplied rather than randomly generated.
			{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: secret.Name, Hash: hashByteMap(secretData)},
			{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: signalSecret.Name},
			{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Namespace: namespace, Name: networkPolicy.Name},
			{APIVersion: "apps/v1", Kind: "DaemonSet", Namespace: namespace, Name: daemonSet.Name},
		},
	}, nil
}

func (r *NodePowerAgentReconciler) getManagementCluster(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (*powerv1alpha1.PowerManagementCluster, error) {
	if agent.Spec.ManagementClusterRef == nil || agent.Spec.ManagementClusterRef.Name == "" {
		return nil, nil
	}

	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, types.NamespacedName{Name: agent.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
		return nil, fmt.Errorf("get PowerManagementCluster %q: %w", agent.Spec.ManagementClusterRef.Name, err)
	}
	return &cluster, nil
}

func nodePowerAgentNamespace(agent *powerv1alpha1.NodePowerAgent, cluster *powerv1alpha1.PowerManagementCluster) string {
	if agent.Spec.Namespace != "" {
		return agent.Spec.Namespace
	}
	if cluster != nil && cluster.Spec.OperandNamespace != nil && cluster.Spec.OperandNamespace.Name != "" {
		return cluster.Spec.OperandNamespace.Name
	}
	return defaultOperandNamespace
}

func nodePowerAgentMode(agent *powerv1alpha1.NodePowerAgent) powerv1alpha1.NodePowerAgentMode {
	if agent.Spec.Mode != "" {
		return agent.Spec.Mode
	}
	return powerv1alpha1.NodePowerAgentModeDryRun
}

func nodePowerAgentActuatorPolicy(agent *powerv1alpha1.NodePowerAgent) powerv1alpha1.ActuatorPolicy {
	if agent.Spec.Shutdown.ActuatorPolicy != "" {
		return agent.Spec.Shutdown.ActuatorPolicy
	}
	return powerv1alpha1.ActuatorPolicySimulate
}

func nodePowerAgentUpsmonImage(agent *powerv1alpha1.NodePowerAgent, cluster *powerv1alpha1.PowerManagementCluster) (string, corev1.PullPolicy, error) {
	image := agent.Spec.Images.Upsmon
	if image.Repository == "" && cluster != nil {
		image = cluster.Spec.Images.UpsmonAgent
	}
	return renderImageReference(image, "NodePowerAgent upsmon")
}

func nodePowerAgentActuatorImage(agent *powerv1alpha1.NodePowerAgent, cluster *powerv1alpha1.PowerManagementCluster) (string, corev1.PullPolicy, error) {
	if nodePowerAgentMode(agent) == powerv1alpha1.NodePowerAgentModeMonitorOnly ||
		nodePowerAgentActuatorPolicy(agent) == powerv1alpha1.ActuatorPolicyDisabled {
		return "", "", nil
	}

	image := agent.Spec.Images.Actuator
	if image.Repository == "" && cluster != nil {
		image = cluster.Spec.Images.Actuator
	}
	return renderImageReference(image, "NodePowerAgent actuator")
}

func labelsForNodePowerAgent(agent *powerv1alpha1.NodePowerAgent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "nut-operator",
		"app.kubernetes.io/component":  "node-power-agent",
		"app.kubernetes.io/managed-by": "nut-operator",
		nodePowerAgentPodLabelKey:      agent.Name,
	}
}

func nodePowerAgentDaemonSetName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}

func nodePowerAgentConfigMapName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-agent-config"
}

func nodePowerAgentSecretName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-upsmon-config"
}

func nodePowerAgentSignalSecretName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-signals"
}

func nodePowerAgentServiceAccountName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}

func nodePowerAgentNetworkPolicyName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}
