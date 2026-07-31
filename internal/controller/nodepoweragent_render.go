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
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

const (
	nodePowerAgentConfigFile = "nut.conf"
	upsmonConfigFile         = "upsmon.conf"
)

type renderedNodePowerAgent struct {
	Namespace              string
	SelectedNodes          []string
	DesiredNumberScheduled int32
	NumberReady            int32
	ConfigHash             string
	ManagedResources       []powerv1alpha1.ManagedResourceStatus
}

type agentMonitorTarget struct {
	UPSName   string
	ServerDNS string
	Port      int32
	Username  string
	Password  string
}

func (r *NodePowerAgentReconciler) reconcileNodePowerAgentOperands(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (renderedNodePowerAgent, error) {
	cluster, err := r.getManagementCluster(ctx, agent)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	namespace := nodePowerAgentNamespace(agent, cluster)
	if err := r.ensureOperandNamespace(ctx, namespace); err != nil {
		return renderedNodePowerAgent{}, err
	}

	mode := nodePowerAgentMode(agent)
	policy := nodePowerAgentActuatorPolicy(agent)
	if mode == powerv1alpha1.NodePowerAgentModeActuate && policy == powerv1alpha1.ActuatorPolicySystemdPoweroff {
		return renderedNodePowerAgent{}, fmt.Errorf("SystemdPoweroff actuator rendering is not implemented; only network monitoring, dry-run, and stub actuator modes are currently rendered")
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

	configData := renderNodePowerAgentConfig()
	secretData, err := renderNodePowerAgentSecret(agent, targets)
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
	networkPolicy, err := r.ensureNodePowerAgentNetworkPolicy(ctx, agent, namespace, egressRules)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}
	daemonSet, err := r.ensureNodePowerAgentDaemonSet(ctx, agent, namespace, nodePowerAgentDaemonSetSpec{
		ConfigMapName:      configMap.Name,
		SecretName:         secret.Name,
		ServiceAccountName: serviceAccount.Name,
		ConfigHash:         configHash,
		UpsmonImage:        upsmonImage,
		UpsmonPullPolicy:   upsmonPullPolicy,
		ActuatorImage:      actuatorImage,
		ActuatorPullPolicy: actuatorPullPolicy,
	})
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	selectedNodes, err := r.selectedNodeNames(ctx, agent)
	if err != nil {
		return renderedNodePowerAgent{}, err
	}

	return renderedNodePowerAgent{
		Namespace:              namespace,
		SelectedNodes:          selectedNodes,
		DesiredNumberScheduled: daemonSet.Status.DesiredNumberScheduled,
		NumberReady:            daemonSet.Status.NumberReady,
		ConfigHash:             configHash,
		ManagedResources: []powerv1alpha1.ManagedResourceStatus{
			{APIVersion: "v1", Kind: "Namespace", Name: namespace},
			{APIVersion: "v1", Kind: "ServiceAccount", Namespace: namespace, Name: serviceAccount.Name},
			{APIVersion: "v1", Kind: "ConfigMap", Namespace: namespace, Name: configMap.Name, Hash: hashStringMap(configData)},
			{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: secret.Name, Hash: hashByteMap(secretData)},
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

func (r *NodePowerAgentReconciler) getNUTServerManagementCluster(ctx context.Context, server *powerv1alpha1.NUTServer) (*powerv1alpha1.PowerManagementCluster, error) {
	if server.Spec.ManagementClusterRef == nil || server.Spec.ManagementClusterRef.Name == "" {
		return nil, nil
	}

	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, types.NamespacedName{Name: server.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
		return nil, fmt.Errorf("get PowerManagementCluster %q for NUTServer %q: %w", server.Spec.ManagementClusterRef.Name, server.Name, err)
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
	return powerv1alpha1.ActuatorPolicyStub
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

func renderImageReference(image powerv1alpha1.ImageReference, label string) (string, corev1.PullPolicy, error) {
	if image.Repository == "" {
		return "", "", fmt.Errorf("%s rendering requires an image repository", label)
	}

	ref := image.Repository
	if image.Tag != "" {
		ref += ":" + image.Tag
	}
	if image.Digest != "" {
		ref += "@" + image.Digest
	}

	pullPolicy := image.PullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullIfNotPresent
	}
	return ref, pullPolicy, nil
}

func (r *NodePowerAgentReconciler) resolveAgentMonitorTargets(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, agentNamespace string) ([]agentMonitorTarget, []networkingv1.NetworkPolicyEgressRule, error) {
	targets := make([]agentMonitorTarget, 0)
	egressRules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(agent.Spec.NUTServerRefs)+1)

	for _, ref := range agent.Spec.NUTServerRefs {
		var server powerv1alpha1.NUTServer
		if err := r.Get(ctx, types.NamespacedName{Name: ref.Name}, &server); err != nil {
			return nil, nil, fmt.Errorf("get NUTServer %q: %w", ref.Name, err)
		}
		serverCluster, err := r.getNUTServerManagementCluster(ctx, &server)
		if err != nil {
			return nil, nil, err
		}
		serverNamespace := nutServerNamespace(&server, serverCluster)
		if serverNamespace != agentNamespace {
			return nil, nil, fmt.Errorf("NodePowerAgent %q must share operand namespace %q with NUTServer %q until cross-namespace credential projection is implemented", agent.Name, serverNamespace, server.Name)
		}

		password, err := r.monitorPassword(ctx, &server, serverNamespace)
		if err != nil {
			return nil, nil, err
		}
		devices, err := selectUPSDevices(ctx, r.Client, &server)
		if err != nil {
			return nil, nil, err
		}
		if len(devices) == 0 {
			return nil, nil, fmt.Errorf("NUTServer %q selected no UPSDevice resources", server.Name)
		}

		serverDNS := nutServerDNSName(&server, serverNamespace)
		for _, device := range devices {
			targets = append(targets, agentMonitorTarget{
				UPSName:   nutDeviceName(device),
				ServerDNS: serverDNS,
				Port:      servicePort(&server),
				Username:  "monitor",
				Password:  password,
			})
		}

		egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{
				{
					PodSelector: &metav1.LabelSelector{MatchLabels: labelsForNUTServer(&server)},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: ptrProtocol(corev1.ProtocolTCP),
					Port:     ptrIntOrStringFromInt32(servicePort(&server)),
				},
			},
		})
	}

	egressRules = append(egressRules, dnsEgressRule())
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].ServerDNS != targets[j].ServerDNS {
			return targets[i].ServerDNS < targets[j].ServerDNS
		}
		return targets[i].UPSName < targets[j].UPSName
	})
	return targets, egressRules, nil
}

func (r *NodePowerAgentReconciler) monitorPassword(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (string, error) {
	secretRef := powerv1alpha1.NamespacedNameReference{Namespace: namespace, Name: operatorManagedUsersSecretName(server)}
	if server.Spec.Auth.Mode == powerv1alpha1.NUTAuthExistingSecret && server.Spec.Auth.ExistingSecretRef != nil {
		secretRef = *server.Spec.Auth.ExistingSecretRef
	}
	if secretRef.Namespace != namespace {
		return "", fmt.Errorf("NUTServer %q monitor Secret must be in operand namespace %q", server.Name, namespace)
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: secretRef.Namespace, Name: secretRef.Name}, &secret); err != nil {
		return "", fmt.Errorf("get monitor Secret %s/%s for NUTServer %q: %w", secretRef.Namespace, secretRef.Name, server.Name, err)
	}
	password := string(secret.Data["monitor-password"])
	if password == "" {
		return "", fmt.Errorf("monitor Secret %s/%s for NUTServer %q requires data[monitor-password]", secretRef.Namespace, secretRef.Name, server.Name)
	}
	return password, nil
}

func nutServerDNSName(server *powerv1alpha1.NUTServer, namespace string) string {
	for _, endpoint := range server.Status.ServiceEndpoints {
		if endpoint.DNSName != "" {
			return endpoint.DNSName
		}
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local", server.Name, namespace)
}

func renderNodePowerAgentConfig() map[string]string {
	return map[string]string{
		nodePowerAgentConfigFile: "MODE=netclient\n",
	}
}

func renderNodePowerAgentSecret(agent *powerv1alpha1.NodePowerAgent, targets []agentMonitorTarget) (map[string][]byte, error) {
	var out strings.Builder
	out.WriteString("MINSUPPLIES 1\n")
	out.WriteString("SHUTDOWNCMD \"/bin/true\"\n")
	fmt.Fprintf(&out, "POLLFREQ %d\n", durationSeconds(agent.Spec.Upsmon.PollFrequency, 15))
	fmt.Fprintf(&out, "POLLFREQALERT %d\n", durationSeconds(agent.Spec.Upsmon.AlertPollFrequency, 5))
	fmt.Fprintf(&out, "HOSTSYNC %d\n", durationSeconds(agent.Spec.Upsmon.HostSync, 15))
	fmt.Fprintf(&out, "DEADTIME %d\n", durationSeconds(agent.Spec.Upsmon.DeadTime, 45))
	fmt.Fprintf(&out, "POWERDOWNFLAG %s\n", shellQuotedNUTValue(powerdownFlagPath(agent)))
	fmt.Fprintf(&out, "FINALDELAY %d\n", durationSeconds(agent.Spec.Upsmon.FinalDelay, 10))

	for _, target := range targets {
		serverAddress := target.ServerDNS
		if target.Port != 3493 {
			serverAddress = fmt.Sprintf("%s:%d", target.ServerDNS, target.Port)
		}
		system := fmt.Sprintf("%s@%s", target.UPSName, serverAddress)
		if err := validateNUTConfigToken(target.UPSName); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor target name %q: %w", target.UPSName, err)
		}
		if err := validateNUTConfigValue(serverAddress); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor server %q: %w", serverAddress, err)
		}
		if err := validateNUTConfigValue(target.Username); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor username: %w", err)
		}
		if err := validateNUTConfigValue(target.Password); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor password: %w", err)
		}
		fmt.Fprintf(&out, "MONITOR %s 1 %s %s secondary\n", system, target.Username, target.Password)
	}

	return map[string][]byte{
		upsmonConfigFile: []byte(out.String()),
	}, nil
}

func durationSeconds(duration *metav1.Duration, fallback int64) int64 {
	if duration == nil {
		return fallback
	}
	seconds := int64(duration.Duration.Round(time.Second) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func powerdownFlagPath(agent *powerv1alpha1.NodePowerAgent) string {
	signalPath := nodePowerAgentSignalPath(agent)
	if strings.HasSuffix(signalPath, ".json") {
		return strings.TrimSuffix(signalPath, ".json") + ".powerdown"
	}
	return signalPath + ".powerdown"
}

func nodePowerAgentSignalPath(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.Shutdown.SignalPath != "" {
		return agent.Spec.Shutdown.SignalPath
	}
	return "/run/power-agent/shutdown.json"
}

func shellQuotedNUTValue(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func (r *NodePowerAgentReconciler) ensureOperandNamespace(ctx context.Context, name string) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, namespace, func() error {
		if namespace.Labels == nil {
			namespace.Labels = map[string]string{}
		}
		namespace.Labels["app.kubernetes.io/managed-by"] = "nut-operator"
		namespace.Labels["power.zalud.io/operand-namespace"] = "true"
		return nil
	})
	return err
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentConfigMap(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, data map[string]string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentConfigMapName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labelsForNodePowerAgent(agent)
		cm.Data = data
		return controllerutil.SetControllerReference(agent, cm, r.Scheme)
	})
	return cm, err
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentSecret(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, data map[string][]byte) (*corev1.Secret, error) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentSecretName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNodePowerAgent(agent)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return controllerutil.SetControllerReference(agent, secret, r.Scheme)
	})
	return secret, err
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentServiceAccount(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string) (*corev1.ServiceAccount, error) {
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentServiceAccountName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, serviceAccount, func() error {
		serviceAccount.Labels = labelsForNodePowerAgent(agent)
		serviceAccount.AutomountServiceAccountToken = ptrBool(false)
		return controllerutil.SetControllerReference(agent, serviceAccount, r.Scheme)
	})
	return serviceAccount, err
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentNetworkPolicy(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, egressRules []networkingv1.NetworkPolicyEgressRule) (*networkingv1.NetworkPolicy, error) {
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentNetworkPolicyName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		podLabels := labelsForNodePowerAgent(agent)
		policy.Labels = podLabels
		policy.Spec.PodSelector = metav1.LabelSelector{MatchLabels: podLabels}
		policy.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
		policy.Spec.Egress = egressRules
		return controllerutil.SetControllerReference(agent, policy, r.Scheme)
	})
	return policy, err
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
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentDaemonSet(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, spec nodePowerAgentDaemonSetSpec) (*appsv1.DaemonSet, error) {
	podNodeSelector, err := nodePowerAgentPodNodeSelector(agent)
	if err != nil {
		return nil, err
	}
	affinity, err := nodePowerAgentAffinity(agent)
	if err != nil {
		return nil, err
	}

	labels := labelsForNodePowerAgent(agent)
	daemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentDaemonSetName(agent), Namespace: namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, daemonSet, func() error {
		daemonSet.Labels = labels
		daemonSet.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		daemonSet.Spec.Template.Labels = labels
		daemonSet.Spec.Template.Annotations = map[string]string{
			"power.zalud.io/config-hash": spec.ConfigHash,
		}
		daemonSet.Spec.Template.Spec.ServiceAccountName = spec.ServiceAccountName
		daemonSet.Spec.Template.Spec.AutomountServiceAccountToken = ptrBool(false)
		daemonSet.Spec.Template.Spec.NodeSelector = podNodeSelector
		daemonSet.Spec.Template.Spec.Tolerations = agent.Spec.Placement.Tolerations
		daemonSet.Spec.Template.Spec.Affinity = affinity
		daemonSet.Spec.Template.Spec.PriorityClassName = agent.Spec.Placement.PriorityClassName
		daemonSet.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsNonRoot: ptrBool(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		}
		daemonSet.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				Name: "nut-client-config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.ConfigMapName},
					},
				},
			},
			{
				Name: "upsmon-config",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: spec.SecretName,
					},
				},
			},
			{
				Name: "power-agent-run",
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
				Args:            []string{"-D"},
				Resources:       agent.Spec.Resources.Upsmon,
				SecurityContext: restrictedContainerSecurityContext(),
				VolumeMounts: []corev1.VolumeMount{
					{Name: "nut-client-config", MountPath: "/etc/nut/nut.conf", SubPath: nodePowerAgentConfigFile, ReadOnly: true},
					{Name: "upsmon-config", MountPath: "/etc/nut/upsmon.conf", SubPath: upsmonConfigFile, ReadOnly: true},
					{Name: "power-agent-run", MountPath: "/run/power-agent"},
				},
			},
		}
		if spec.ActuatorImage != "" {
			daemonSet.Spec.Template.Spec.Containers = append(daemonSet.Spec.Template.Spec.Containers, corev1.Container{
				Name:            "actuator",
				Image:           spec.ActuatorImage,
				ImagePullPolicy: spec.ActuatorPullPolicy,
				Resources:       agent.Spec.Resources.Actuator,
				Env: []corev1.EnvVar{
					{Name: "POWER_AGENT_MODE", Value: string(nodePowerAgentMode(agent))},
					{Name: "POWER_ACTUATOR_POLICY", Value: string(nodePowerAgentActuatorPolicy(agent))},
					{Name: "POWER_SIGNAL_PATH", Value: nodePowerAgentSignalPath(agent)},
					{Name: "POWER_SIGNAL_TTL", Value: durationString(agent.Spec.Shutdown.SignalTTL, "2m")},
				},
				SecurityContext: restrictedContainerSecurityContext(),
				VolumeMounts: []corev1.VolumeMount{
					{Name: "power-agent-run", MountPath: "/run/power-agent"},
				},
			})
		}
		return controllerutil.SetControllerReference(agent, daemonSet, r.Scheme)
	})
	return daemonSet, err
}

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		ReadOnlyRootFilesystem:   ptrBool(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func durationString(duration *metav1.Duration, fallback string) string {
	if duration == nil {
		return fallback
	}
	return duration.Duration.String()
}

func nodePowerAgentPodNodeSelector(agent *powerv1alpha1.NodePowerAgent) (map[string]string, error) {
	selector := make(map[string]string, len(agent.Spec.Placement.NodeSelector))
	for key, value := range agent.Spec.Placement.NodeSelector {
		selector[key] = value
	}
	if agent.Spec.NodeSelector == nil {
		return selector, nil
	}
	for key, value := range agent.Spec.NodeSelector.MatchLabels {
		if existing, found := selector[key]; found && existing != value {
			return nil, fmt.Errorf("node selector label %q has conflicting values %q and %q", key, existing, value)
		}
		selector[key] = value
	}
	return selector, nil
}

func nodePowerAgentAffinity(agent *powerv1alpha1.NodePowerAgent) (*corev1.Affinity, error) {
	var affinity *corev1.Affinity
	if agent.Spec.Placement.Affinity != nil {
		affinity = agent.Spec.Placement.Affinity.DeepCopy()
	}
	if agent.Spec.NodeSelector == nil || len(agent.Spec.NodeSelector.MatchExpressions) == 0 {
		return affinity, nil
	}
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		return nil, fmt.Errorf("nodeSelector.matchExpressions cannot be combined with placement.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution")
	}
	if affinity == nil {
		affinity = &corev1.Affinity{}
	}
	if affinity.NodeAffinity == nil {
		affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchExpressions: nodeSelectorRequirements(agent.Spec.NodeSelector.MatchExpressions),
			},
		},
	}
	return affinity, nil
}

func nodeSelectorRequirements(expressions []metav1.LabelSelectorRequirement) []corev1.NodeSelectorRequirement {
	requirements := make([]corev1.NodeSelectorRequirement, 0, len(expressions))
	for _, expression := range expressions {
		requirements = append(requirements, corev1.NodeSelectorRequirement{
			Key:      expression.Key,
			Operator: corev1.NodeSelectorOperator(expression.Operator),
			Values:   expression.Values,
		})
	}
	return requirements
}

func (r *NodePowerAgentReconciler) selectedNodeNames(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) ([]string, error) {
	selector := labels.Everything()
	if agent.Spec.NodeSelector != nil {
		parsed, err := metav1.LabelSelectorAsSelector(agent.Spec.NodeSelector)
		if err != nil {
			return nil, fmt.Errorf("parse nodeSelector: %w", err)
		}
		selector = parsed
	}

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("list selected nodes: %w", err)
	}
	names := make([]string, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		names = append(names, node.Name)
	}
	sort.Strings(names)
	return names, nil
}

func dnsEgressRule() networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: ptrProtocol(corev1.ProtocolUDP),
				Port:     ptrIntOrStringFromInt32(53),
			},
			{
				Protocol: ptrProtocol(corev1.ProtocolTCP),
				Port:     ptrIntOrStringFromInt32(53),
			},
		},
	}
}

func labelsForNodePowerAgent(agent *powerv1alpha1.NodePowerAgent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":        "nut-operator",
		"app.kubernetes.io/component":   "node-power-agent",
		"app.kubernetes.io/managed-by":  "nut-operator",
		"power.zalud.io/nodepoweragent": agent.Name,
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

func nodePowerAgentServiceAccountName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}

func nodePowerAgentNetworkPolicyName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}
