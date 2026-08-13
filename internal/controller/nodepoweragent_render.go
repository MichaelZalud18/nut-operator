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
	nodePowerAgentConfigFile                        = "nut.conf"
	nodePowerAgentProjectedSignalDirectory          = "/var/lib/power-agent/signals"
	nodePowerAgentProjectedSignalPath               = nodePowerAgentProjectedSignalDirectory + "/$(POWER_NODE_NAME).json"
	nodePowerAgentSignalReason                      = "upsmon-fsd"
	nodePowerAgentSignalWriterPath                  = "/usr/local/bin/power-signal-writer"
	nodePowerAgentDefaultPriorityClassName          = "system-node-critical"
	nodePowerAgentDefaultTerminationGracePeriodSecs = 60
	upsmonConfigFile                                = "upsmon.conf"

	// nodePowerAgentServerCAFile is the key in the agent's rendered Secret holding the
	// concatenated CA bundle for every NUTServer this agent monitors.
	nodePowerAgentServerCAFile = "server-ca.pem"
	// nodePowerAgentServerCASourcePath is where that bundle is mounted read-only.
	nodePowerAgentServerCASourcePath = "/etc/nut/" + nodePowerAgentServerCAFile
	// nodePowerAgentServerCAPath is what upsmon's CERTPATH points at, and it must be a
	// directory rather than the bundle above.
	//
	// upsmon.conf(5) says CERTPATH accepts "a single PEM file with multiple CA
	// certificates", but the client passes it to SSL_CTX_load_verify_locations as the
	// CApath argument and never as CAfile (clients/upsclient.c). OpenSSL treats CApath as
	// a directory of hash-named certificates, so a file there loads without error and then
	// fails every verification -- CERTVERIFY plus FORCESSL turns that into a connection
	// that cannot be established at all (F-40).
	nodePowerAgentServerCAPath = "/etc/nut/tls/server-ca.d"
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
}

type agentMonitorTarget struct {
	UPSName   string
	ServerDNS string
	Port      int32
	Username  string
	Password  string
	TLSMode   powerv1alpha1.NUTTLSMode
	ServerCA  []byte
}

// agentTLSPosture is the upsmon-side TLS configuration derived from the NUTServers this agent
// monitors. CERTVERIFY and FORCESSL are process-global in upsmon (per-host overrides need
// CERTHOST, NUT 2.8.5+), so an agent monitoring servers with different spec.tls.mode values has
// to settle on the weakest common posture or it would break the laxer servers outright.
type agentTLSPosture struct {
	CABundle   []byte
	ForceSSL   bool
	CertVerify bool
	// DowngradeReason is empty when the rendered posture is as strict as the monitored servers
	// asked for. When set it names why upsmon was configured more loosely than spec.tls.mode
	// implies, so the caller can surface it instead of silently under-securing the link.
	DowngradeReason string
}

func (p agentTLSPosture) enabled() bool {
	return p.ForceSSL || p.CertVerify || len(p.CABundle) > 0
}

// deriveAgentTLSPosture folds the monitored servers' TLS modes into one upsmon configuration.
func deriveAgentTLSPosture(targets []agentMonitorTarget) agentTLSPosture {
	var posture agentTLSPosture
	if len(targets) == 0 {
		return posture
	}

	seen := map[string]struct{}{}
	anyRequired := false
	allRequired := true
	anyTLS := false
	for _, target := range targets {
		switch target.TLSMode {
		case powerv1alpha1.NUTTLSRequired:
			anyRequired = true
			anyTLS = true
		case powerv1alpha1.NUTTLSOpportunistic:
			allRequired = false
			anyTLS = true
		default:
			allRequired = false
		}
		if len(target.ServerCA) == 0 {
			continue
		}
		if _, duplicate := seen[string(target.ServerCA)]; duplicate {
			continue
		}
		seen[string(target.ServerCA)] = struct{}{}
		posture.CABundle = append(posture.CABundle, target.ServerCA...)
		if len(target.ServerCA) > 0 && target.ServerCA[len(target.ServerCA)-1] != '\n' {
			posture.CABundle = append(posture.CABundle, '\n')
		}
	}

	if !anyTLS {
		return agentTLSPosture{}
	}
	switch {
	case anyRequired && !allRequired:
		posture.DowngradeReason = "one or more monitored NUTServers do not set spec.tls.mode Required, " +
			"and upsmon applies FORCESSL to every MONITOR line"
	case allRequired && len(posture.CABundle) == 0:
		posture.ForceSSL = true
		posture.DowngradeReason = "no monitored NUTServer sets spec.tls.serverCARef, so upsmon encrypts " +
			"but cannot authenticate the server certificate"
	case allRequired:
		posture.ForceSSL = true
		posture.CertVerify = true
	}
	return posture
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

	if err := validateNodePowerAgentRenderSafety(agent); err != nil {
		return renderedNodePowerAgent{}, err
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
	daemonSet, err := r.ensureNodePowerAgentDaemonSet(ctx, agent, namespace, nodePowerAgentDaemonSetSpec{
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
		ManagedResources: []powerv1alpha1.ManagedResourceStatus{
			{APIVersion: "v1", Kind: "Namespace", Name: namespace},
			{APIVersion: "v1", Kind: "ServiceAccount", Namespace: namespace, Name: serviceAccount.Name},
			{APIVersion: "v1", Kind: "ConfigMap", Namespace: namespace, Name: configMap.Name, Hash: hashStringMap(configData)},
			// Publishing a hash of Secret contents in status is safe here and deliberate (F-24): it is
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

func validateNodePowerAgentRenderSafety(agent *powerv1alpha1.NodePowerAgent) error {
	if nodePowerAgentActuatorPolicy(agent) != powerv1alpha1.ActuatorPolicySystemdPoweroff {
		return nil
	}
	if nodePowerAgentMode(agent) != powerv1alpha1.NodePowerAgentModeActuate {
		return fmt.Errorf("SystemdPoweroff actuator rendering requires spec.mode Actuate")
	}
	approvalAnnotation := agent.Spec.Shutdown.ApprovalAnnotation
	if approvalAnnotation == "" {
		return fmt.Errorf("SystemdPoweroff actuator rendering requires spec.shutdown.approvalAnnotation")
	}
	if agent.Annotations[approvalAnnotation] != "true" {
		return fmt.Errorf("SystemdPoweroff actuator rendering requires approval annotation %q=true", approvalAnnotation)
	}
	return nil
}

func nodePowerAgentRequiresHostPoweroff(agent *powerv1alpha1.NodePowerAgent) bool {
	return nodePowerAgentMode(agent) == powerv1alpha1.NodePowerAgentModeActuate &&
		nodePowerAgentActuatorPolicy(agent) == powerv1alpha1.ActuatorPolicySystemdPoweroff
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

		serverCA, err := r.serverCABundle(ctx, &server, serverNamespace)
		if err != nil {
			return nil, nil, err
		}

		serverDNS := nutServerDNSName(&server, serverNamespace)
		for _, device := range devices {
			targets = append(targets, agentMonitorTarget{
				UPSName:   nutDeviceName(device),
				ServerDNS: serverDNS,
				Port:      servicePort(&server),
				Username:  "monitor",
				Password:  password,
				TLSMode:   monitoredServerTLSMode(&server),
				ServerCA:  serverCA,
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

// monitoredServerTLSMode reports the TLS posture upsd will actually present. A mode of Required
// with no certificate to serve is a server that cannot terminate TLS, so it is reported as
// Disabled rather than taken at its word — otherwise the agent would render FORCESSL against a
// plaintext server and refuse to monitor it at all.
func monitoredServerTLSMode(server *powerv1alpha1.NUTServer) powerv1alpha1.NUTTLSMode {
	if !nutServerTLSEnabled(server) {
		return powerv1alpha1.NUTTLSDisabled
	}
	return server.Spec.TLS.Mode
}

// serverCABundle loads the CA that upsmon should trust for this server's certificate.
func (r *NodePowerAgentReconciler) serverCABundle(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) ([]byte, error) {
	ref := server.Spec.TLS.ServerCARef
	if ref == nil || !nutServerTLSEnabled(server) {
		return nil, nil
	}
	if ref.Namespace != "" && ref.Namespace != namespace {
		return nil, fmt.Errorf("NUTServer %q spec.tls.serverCARef must name a Secret in operand namespace %q, got %q", server.Name, namespace, ref.Namespace)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return nil, fmt.Errorf("get Secret %s/%s for NUTServer %q spec.tls.serverCARef: %w", namespace, ref.Name, server.Name, err)
	}
	bundle := secret.Data[nutServerCABundleKey]
	if len(bundle) == 0 {
		return nil, fmt.Errorf("secret %s/%s for NUTServer %q spec.tls.serverCARef requires a non-empty data[%q]", namespace, ref.Name, server.Name, nutServerCABundleKey)
	}
	return bundle, nil
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

func renderNodePowerAgentSecret(agent *powerv1alpha1.NodePowerAgent, targets []agentMonitorTarget, posture agentTLSPosture) (map[string][]byte, error) {
	var out strings.Builder
	out.WriteString("MINSUPPLIES 1\n")
	fmt.Fprintf(&out, "SHUTDOWNCMD %s\n", shellQuotedNUTValue(nodePowerAgentSignalWriterPath))
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

	// The TLS block is emitted only when at least one monitored server actually offers TLS. NUT
	// compiles CERTPATH/CERTVERIFY/FORCESSL in conditionally (WITH_SSL), so writing them
	// unconditionally would hand a parse error to any upsmon image built without SSL support —
	// including every deployment that legitimately runs spec.tls.mode Disabled.
	if posture.enabled() {
		if len(posture.CABundle) > 0 {
			fmt.Fprintf(&out, "CERTPATH %s\n", nodePowerAgentServerCAPath)
		}
		fmt.Fprintf(&out, "CERTVERIFY %s\n", nutBoolean(posture.CertVerify))
		fmt.Fprintf(&out, "FORCESSL %s\n", nutBoolean(posture.ForceSSL))
	}

	data := map[string][]byte{
		upsmonConfigFile: []byte(out.String()),
	}
	if len(posture.CABundle) > 0 {
		data[nodePowerAgentServerCAFile] = posture.CABundle
	}
	return data, nil
}

func nutBoolean(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func nodePowerAgentSelectedUPSDevices(targets []agentMonitorTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.UPSName != "" {
			names = append(names, target.UPSName)
		}
	}
	sort.Strings(names)
	deduped := names[:0]
	var previous string
	for _, name := range names {
		if name == previous {
			continue
		}
		deduped = append(deduped, name)
		previous = name
	}
	return deduped
}

func durationSeconds(duration *metav1.Duration, fallback int64) int64 {
	if duration == nil {
		return fallback
	}
	seconds := int64(duration.Round(time.Second) / time.Second)
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
	if err := rejectReservedOperandNamespace(name); err != nil {
		return err
	}
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

func (r *NodePowerAgentReconciler) ensureNodePowerAgentSignalSecret(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string) (*corev1.Secret, error) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentSignalSecretName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNodePowerAgent(agent)
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		return controllerutil.SetControllerReference(agent, secret, r.Scheme)
	})
	return secret, err
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
	SignalSecretName   string
	SelectedUPSDevices []string
	MountServerCA      bool
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
	hostPoweroff := nodePowerAgentRequiresHostPoweroff(agent)
	daemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentDaemonSetName(agent), Namespace: namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, daemonSet, func() error {
		daemonSet.Labels = labels
		daemonSet.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		daemonSet.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
			Type: appsv1.RollingUpdateDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{
				MaxUnavailable: ptrIntOrStringFromInt32(1),
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
				Name: "nut-client-config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: spec.ConfigMapName},
						DefaultMode:          ptrInt32(0440),
					},
				},
			},
			{
				Name: "upsmon-config",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  spec.SecretName,
						DefaultMode: ptrInt32(0440),
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
				Name: "power-agent-signals",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  spec.SignalSecretName,
						DefaultMode: ptrInt32(0440),
						Optional:    ptrBool(true),
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
				Args:            []string{"-D"},
				Resources:       agent.Spec.Resources.Upsmon,
				SecurityContext: restrictedContainerSecurityContext(),
				ReadinessProbe:  upsmonReadinessProbe(),
				LivenessProbe:   upsmonLivenessProbe(),
				Env:             nodePowerAgentSignalEnv(agent, spec.ConfigHash, spec.SelectedUPSDevices),
				VolumeMounts: []corev1.VolumeMount{
					{Name: "nut-client-config", MountPath: "/etc/nut/nut.conf", SubPath: nodePowerAgentConfigFile, ReadOnly: true},
					{Name: "upsmon-config", MountPath: "/etc/nut/" + upsmonConfigFile, SubPath: upsmonConfigFile, ReadOnly: true},
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
							Name:      "upsmon-config",
							MountPath: nodePowerAgentServerCASourcePath,
							SubPath:   nodePowerAgentServerCAFile,
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
		if spec.ActuatorImage != "" {
			daemonSet.Spec.Template.Spec.Containers = append(daemonSet.Spec.Template.Spec.Containers, corev1.Container{
				Name:            "actuator",
				Image:           spec.ActuatorImage,
				ImagePullPolicy: spec.ActuatorPullPolicy,
				Resources:       agent.Spec.Resources.Actuator,
				Env: []corev1.EnvVar{
					{Name: "POWER_AGENT_MODE", Value: string(nodePowerAgentMode(agent))},
					{Name: "POWER_ACTUATOR_POLICY", Value: string(nodePowerAgentActuatorPolicy(agent))},
					{
						Name: "POWER_NODE_NAME",
						ValueFrom: &corev1.EnvVarSource{
							FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "spec.nodeName"},
						},
					},
					{Name: "POWER_SIGNAL_PATH", Value: nodePowerAgentSignalPath(agent)},
					{Name: "POWER_SIGNAL_PATHS", Value: nodePowerAgentSignalPath(agent) + "," + nodePowerAgentProjectedSignalPath},
					{Name: "POWER_SIGNAL_TTL", Value: durationString(agent.Spec.Shutdown.SignalTTL, "2m")},
				},
				SecurityContext: actuatorContainerSecurityContext(hostPoweroff),
				ReadinessProbe:  actuatorReadinessProbe(),
				VolumeMounts: []corev1.VolumeMount{
					{Name: "power-agent-run", MountPath: "/run/power-agent"},
					{Name: "power-agent-signals", MountPath: nodePowerAgentProjectedSignalDirectory, ReadOnly: true},
				},
			})
		}
		return controllerutil.SetControllerReference(agent, daemonSet, r.Scheme)
	})
	return daemonSet, err
}

// nodePowerAgentServerCARehashScript builds the CApath upsmon needs.
//
// openssl rehash writes the <subject-hash>.N symlinks OpenSSL looks for when it
// walks a CApath. Without them the directory reads as empty and every certificate
// verification fails, which is the F-40 failure mode. The bundle may hold several
// CAs when an agent monitors several servers, and rehash handles that by splitting
// on subject rather than on file.
func nodePowerAgentServerCARehashScript() string {
	return fmt.Sprintf(
		"set -e; umask 022; cp %s %s/server-ca.pem; openssl rehash %s",
		nodePowerAgentServerCASourcePath,
		nodePowerAgentServerCAPath,
		nodePowerAgentServerCAPath,
	)
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

func actuatorContainerSecurityContext(hostPoweroff bool) *corev1.SecurityContext {
	if !hostPoweroff {
		return restrictedContainerSecurityContext()
	}
	// No seccomp override: the pod's RuntimeDefault applies (F-62).
	//
	// This carried SeccompProfile: Unconfined on the assumption that the runtime's default profile
	// would block reboot(2). It does not. Measured on kind, with the capability actually held after
	// the F-61 fix:
	//
	//   RuntimeDefault + CAP_SYS_BOOT  -> EINVAL, the kernel's reboot handler ran and rejected the
	//                                    argument, so the capability check passed
	//   RuntimeDefault, no capability  -> EPERM, refused on the capability
	//
	// The two errnos are what separate the explanations. Seccomp denials surface as EPERM before the
	// handler runs; EINVAL can only come from the handler itself. The capability is the gate, and
	// Unconfined was buying nothing while removing every other syscall filter from the one container
	// that can halt the machine.
	//
	// hostPID still places this pod outside Pod Security "baseline" on its own, so dropping the
	// override narrows the container without changing where it can be admitted.
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptrBool(false),
		ReadOnlyRootFilesystem:   ptrBool(true),
		Capabilities: &corev1.Capabilities{
			Add:  []corev1.Capability{"SYS_BOOT"},
			Drop: []corev1.Capability{"ALL"},
		},
	}
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

func upsmonReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{
					"sh",
					"-ec",
					`test -r /etc/nut/upsmon.conf && awk '/^MONITOR[[:space:]]/ {print $2}' /etc/nut/upsmon.conf | while IFS= read -r target; do server="${target#*@}"; test "$server" != "$target"; upsc -l "$server" >/dev/null; done`,
				},
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       30,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}
}

func actuatorReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/node-actuator", "--version"},
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       30,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}
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
		{Name: "POWER_PLAN_CONFIG_HASH", Value: configHash},
		{Name: "POWER_SELECTED_UPS_DEVICES", Value: strings.Join(selectedUPSDevices, ",")},
		{Name: "POWER_SHUTDOWN_FLOW", Value: nodePowerAgentShutdownFlowName(agent)},
		{Name: "POWER_SIGNAL_REASON", Value: nodePowerAgentSignalReason},
	}
}

func nodePowerAgentTolerations(agent *powerv1alpha1.NodePowerAgent) []corev1.Toleration {
	tolerations := []corev1.Toleration{
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
	}
	for _, toleration := range agent.Spec.Placement.Tolerations {
		if !hasToleration(tolerations, toleration) {
			tolerations = append(tolerations, toleration)
		}
	}
	return tolerations
}

func hasToleration(tolerations []corev1.Toleration, candidate corev1.Toleration) bool {
	for _, toleration := range tolerations {
		if toleration.Key == candidate.Key &&
			toleration.Operator == candidate.Operator &&
			toleration.Value == candidate.Value &&
			toleration.Effect == candidate.Effect &&
			toleration.TolerationSeconds == candidate.TolerationSeconds {
			return true
		}
	}
	return false
}

func nodePowerAgentPriorityClassName(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.Placement.PriorityClassName != "" {
		return agent.Spec.Placement.PriorityClassName
	}
	return nodePowerAgentDefaultPriorityClassName
}

func nodePowerAgentShutdownFlowName(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.ShutdownFlowRef != nil && agent.Spec.ShutdownFlowRef.Name != "" {
		return agent.Spec.ShutdownFlowRef.Name
	}
	return "upsmon-local"
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

func (r *NodePowerAgentReconciler) nodePowerAgentNodeStatuses(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, selectedNodes []string) ([]powerv1alpha1.NodePowerAgentNodeStatus, int32, int32, error) {
	podsByNode, err := r.nodePowerAgentPodsByNode(ctx, agent, namespace, selectedNodes)
	if err != nil {
		return nil, 0, 0, err
	}

	statuses := make([]powerv1alpha1.NodePowerAgentNodeStatus, 0, len(selectedNodes))
	var readyCount int32
	var unavailableCount int32
	for _, nodeName := range selectedNodes {
		pod, found := podsByNode[nodeName]
		if !found {
			unavailableCount++
			statuses = append(statuses, powerv1alpha1.NodePowerAgentNodeStatus{
				NodeName: nodeName,
				Ready:    false,
				Reason:   "AgentPodMissing",
				Message:  "no NodePowerAgent pod is currently observed on this selected node",
			})
			continue
		}
		status := nodePowerAgentNodeStatusFromPod(nodeName, pod)
		if status.Ready {
			readyCount++
		} else {
			unavailableCount++
		}
		statuses = append(statuses, status)
	}
	return statuses, readyCount, unavailableCount, nil
}

func (r *NodePowerAgentReconciler) nodePowerAgentPodsByNode(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, selectedNodes []string) (map[string]corev1.Pod, error) {
	selected := make(map[string]struct{}, len(selectedNodes))
	for _, nodeName := range selectedNodes {
		selected[nodeName] = struct{}{}
	}

	var pods corev1.PodList
	if err := r.List(
		ctx,
		&pods,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{Selector: labels.SelectorFromSet(labelsForNodePowerAgent(agent))},
	); err != nil {
		return nil, fmt.Errorf("list NodePowerAgent pods: %w", err)
	}

	byNode := make(map[string]corev1.Pod, len(selectedNodes))
	for _, pod := range pods.Items {
		nodeName := pod.Spec.NodeName
		if _, selected := selected[nodeName]; nodeName == "" || !selected {
			continue
		}
		existing, found := byNode[nodeName]
		if !found || preferredNodePowerAgentPod(pod, existing) {
			byNode[nodeName] = pod
		}
	}
	return byNode, nil
}

func nodePowerAgentNodeStatusFromPod(nodeName string, pod corev1.Pod) powerv1alpha1.NodePowerAgentNodeStatus {
	readyCondition := podReadyCondition(pod)
	lastHeartbeatTime := readyConditionTime(readyCondition, pod.Status.StartTime)
	status := powerv1alpha1.NodePowerAgentNodeStatus{
		NodeName:          nodeName,
		Ready:             false,
		PodName:           pod.Name,
		Phase:             pod.Status.Phase,
		LastHeartbeatTime: lastHeartbeatTime,
	}
	switch {
	case podIsDeleting(pod):
		status.Reason = "AgentPodDeleting"
		status.Message = "observed NodePowerAgent pod is deleting"
	case readyCondition != nil && readyCondition.Status == corev1.ConditionTrue:
		status.Ready = true
		status.Reason = "AgentPodReady"
		status.Message = "ready NodePowerAgent pod is observed on this node"
	case readyCondition != nil && readyCondition.Reason != "":
		status.Reason = readyCondition.Reason
		status.Message = readyCondition.Message
	default:
		status.Reason = "AgentPodNotReady"
		status.Message = "observed NodePowerAgent pod is not ready"
	}
	if status.Message == "" {
		status.Message = "observed NodePowerAgent pod is not ready"
	}
	return status
}

func preferredNodePowerAgentPod(candidate, existing corev1.Pod) bool {
	candidateReady := podIsReady(candidate)
	existingReady := podIsReady(existing)
	if candidateReady != existingReady {
		return candidateReady
	}
	return podObservedTime(candidate).After(podObservedTime(existing))
}

func podIsReady(pod corev1.Pod) bool {
	if podIsDeleting(pod) {
		return false
	}
	condition := podReadyCondition(pod)
	return condition != nil && condition.Status == corev1.ConditionTrue
}

func podIsDeleting(pod corev1.Pod) bool {
	return pod.DeletionTimestamp != nil && !pod.DeletionTimestamp.IsZero()
}

func podReadyCondition(pod corev1.Pod) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == corev1.PodReady {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

func readyConditionTime(condition *corev1.PodCondition, startTime *metav1.Time) *metav1.Time {
	if condition != nil && !condition.LastTransitionTime.IsZero() {
		heartbeat := condition.LastTransitionTime
		return &heartbeat
	}
	if startTime != nil && !startTime.IsZero() {
		heartbeat := *startTime
		return &heartbeat
	}
	return nil
}

func podObservedTime(pod corev1.Pod) time.Time {
	if condition := podReadyCondition(pod); condition != nil && !condition.LastTransitionTime.IsZero() {
		return condition.LastTransitionTime.Time
	}
	if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
		return pod.Status.StartTime.Time
	}
	return pod.CreationTimestamp.Time
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

func nodePowerAgentSignalKey(nodeName string) string {
	return nodeName + ".json"
}

func nodePowerAgentServiceAccountName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}

func nodePowerAgentNetworkPolicyName(agent *powerv1alpha1.NodePowerAgent) string {
	return agent.Name + "-node-power-agent"
}
