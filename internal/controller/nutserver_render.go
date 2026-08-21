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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

const (
	defaultOperandNamespace = "power-system"
	nutServerPortName       = "upsd"

	// upsdReadinessProbe timing (F-17): a local upsc query proves the driver has registered with
	// upsd, not merely that the TCP port is accepting connections.
	upsdReadinessInitialDelaySeconds = 5
	upsdReadinessPeriodSeconds       = 10
	upsdReadinessTimeoutSeconds      = 5
	upsdReadinessFailureThreshold    = 3

	// nutServerUpsdContainerName is the container that serves the NUT protocol. It is referenced by
	// name rather than by index because the pod now holds more than one container.
	nutServerUpsdContainerName = "upsd"

	// driverWatchdogContainerName supervises the drivers upsdrvctl starts once and never revisits
	// (F-49). It is a sidecar rather than a container per driver so that the container list stays
	// independent of the device set: making it a function of the devices would turn adding or
	// removing a UPSDevice into a pod recreate, which is the outcome F-48 exists to eliminate.
	driverWatchdogContainerName = "driver-watchdog"

	// driverWatchdogIntervalSeconds is deliberately slower than the readiness probe. Readiness has
	// to report the fault promptly; the watchdog has to fix it without churning, and every restart
	// it performs costs a short telemetry gap for that device.
	//
	// It also bounds how long a reloadable config change takes to reach upsd (F-48), on top of the
	// kubelet's own projected-volume sync period. Both are far inside any window that matters:
	// the alternative it replaces was replacing the pod.
	driverWatchdogIntervalSeconds = 30

	// NUT TLS paths inside the upsd operand. The kubernetes.io/tls Secret is projected read-only
	// at nutServerCertificateMountPath; the init container concatenates it into a single PEM on a
	// writable emptyDir because upsd's CERTFILE takes one file holding chain-then-key.
	nutServerCertificateMountPath = "/etc/nut/tls"
	nutServerClientCAMountPath    = "/etc/nut/client-ca"
	nutServerCombinedCertDir      = "/run/nut-tls"
	nutServerCombinedCertPath     = nutServerCombinedCertDir + "/nut-server.pem"
	nutServerClientCAPath         = nutServerClientCAMountPath + "/ca.crt"

	nutServerTLSCertificateKey = "tls.crt"
	nutServerTLSPrivateKeyKey  = "tls.key"
	nutServerCABundleKey       = "ca.crt"

	nutServerTLSInitContainerName = "nut-tls-assemble"
	nutServerTLSVolumeName        = "nut-tls"
	nutServerClientCAVolumeName   = "nut-client-ca"
	nutServerCombinedTLSVolume    = "nut-tls-assembled"
)

type renderedNUTServer struct {
	Namespace        string
	SelectedDevices  []string
	DesiredReplicas  int32
	ReadyReplicas    int32
	ServiceEndpoints []powerv1alpha1.ServiceEndpointStatus
	UpstreamNUT      []powerv1alpha1.NUTUpstreamStatus
	ConfigHash       string
	ManagedResources []powerv1alpha1.ManagedResourceStatus
}

func (r *NUTServerReconciler) reconcileNUTServerOperands(ctx context.Context, server *powerv1alpha1.NUTServer) (renderedNUTServer, error) {
	cluster, err := r.getManagementCluster(ctx, server)
	if err != nil {
		return renderedNUTServer{}, err
	}
	namespace := nutServerNamespace(server, cluster)
	image, err := nutServerImage(server, cluster)
	if err != nil {
		return renderedNUTServer{}, err
	}

	devices, err := r.selectUPSDevices(ctx, server)
	if err != nil {
		return renderedNUTServer{}, err
	}
	// ensureOperandNamespace runs before any lookup scoped to that namespace (credentialSecretRef,
	// simulation.sequenceConfigMapRef): both are user-supplied objects the operand namespace is
	// expected to already contain, but for a standalone NUTServer (no PowerManagementCluster
	// pre-creating operandNamespace) this reconciler is also what creates that namespace in the
	// first place. Resolving those refs first would mean the very first reconcile always fails with
	// NotFound before the namespace it needs ever gets created, and the resource could never
	// converge without an out-of-band namespace creation.
	if _, err := ensureOperandNamespace(ctx, r.Client, namespace, operandNamespaceCreateAllowed(cluster)); err != nil {
		return renderedNUTServer{}, err
	}
	credentials, err := resolveUPSDeviceCredentials(ctx, r.Client, namespace, devices)
	if err != nil {
		return renderedNUTServer{}, err
	}
	simulationFixtures, err := resolveUPSDeviceSimulationFixtures(ctx, r.Client, namespace, devices)
	if err != nil {
		return renderedNUTServer{}, err
	}
	if err := r.validateNUTServerTLSSecrets(ctx, server, namespace); err != nil {
		return renderedNUTServer{}, err
	}
	configData, err := renderNUTServerConfig(server, devices, credentials, simulationFixtures)
	if err != nil {
		return renderedNUTServer{}, err
	}
	// The pod-template annotation carries only the config upsd cannot adopt on reload (F-48).
	//
	// It used to carry a digest of everything rendered, so any change at all replaced the pod --
	// dropping every upsmon session and NUT's login accounting, which is the damage F-15 and F-16
	// exist to prevent. Adding one device should not cost the other devices their clients.
	//
	// The split is drawn from what upsd actually does, established by running it rather than from
	// the documentation: `upsd -c reload` registers a device added to ups.conf (upsc -l reports it
	// immediately afterwards) and re-reads upsd.users, but a changed LISTEN address or port is
	// ignored -- the reload returns success, logs nothing about it, and upsd stays bound to the old
	// port. Silent non-adoption is why upsd.conf stays on the restart path.
	//
	// TLS material is on the restart path for the same reason and is included by digest, because it
	// lives in referenced Secrets rather than in configData: upsd builds its SSL context at startup,
	// so a rotated certificate reaches the pod's volume and is not served until the process restarts.
	tlsDigest, err := r.nutServerTLSMaterialDigest(ctx, server, namespace)
	if err != nil {
		return renderedNUTServer{}, err
	}
	restartHash := hashStringMap(map[string]string{
		"upsd.conf": configData["upsd.conf"],
		"tls":       tlsDigest,
	})
	upsConf := configData["ups.conf"]
	delete(configData, "ups.conf")
	configHash := hashStringMap(configData)

	configMap, err := r.ensureNUTServerConfigMap(ctx, server, namespace, configData)
	if err != nil {
		return renderedNUTServer{}, err
	}
	driverConfigSecret, err := r.ensureNUTServerDriverConfigSecret(ctx, server, namespace, upsConf)
	if err != nil {
		return renderedNUTServer{}, err
	}
	secretRef, managedSecret, err := r.ensureNUTUsersSecret(ctx, server, namespace)
	if err != nil {
		return renderedNUTServer{}, err
	}
	service, err := r.ensureNUTServerService(ctx, server, namespace)
	if err != nil {
		return renderedNUTServer{}, err
	}
	networkPolicy, err := r.ensureNUTServerNetworkPolicy(ctx, server, namespace, devices)
	if err != nil {
		return renderedNUTServer{}, err
	}
	deployment, err := r.ensureNUTServerDeployment(ctx, server, namespace, image, configMap.Name, driverConfigSecret.Name, secretRef, restartHash, devices)
	if err != nil {
		return renderedNUTServer{}, err
	}
	pdb, err := r.ensureNUTServerPodDisruptionBudget(ctx, server, namespace)
	if err != nil {
		return renderedNUTServer{}, err
	}
	upstreamStatus := r.probeUpstreamNUTDevices(ctx, devices)

	selected := make([]string, 0, len(devices))
	for _, device := range devices {
		selected = append(selected, device.Name)
	}
	sort.Strings(selected)

	desiredReplicas := int32(1)
	if server.Spec.Replicas != nil {
		desiredReplicas = *server.Spec.Replicas
	}

	managed := []powerv1alpha1.ManagedResourceStatus{
		{APIVersion: "v1", Kind: "Namespace", Name: namespace},
		{APIVersion: "v1", Kind: "ConfigMap", Namespace: namespace, Name: configMap.Name, Hash: configHash},
		// No Hash here, matching the F-24 precedent for the upsd.users Secret: this Secret can carry
		// real driver credentials (SNMP community/SNMPv3 passwords), and status is broadly readable.
		{APIVersion: "v1", Kind: "Secret", Namespace: namespace, Name: driverConfigSecret.Name},
		{APIVersion: "v1", Kind: "Service", Namespace: namespace, Name: service.Name},
		{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Namespace: namespace, Name: networkPolicy.Name},
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: namespace, Name: deployment.Name},
		{APIVersion: "policy/v1", Kind: "PodDisruptionBudget", Namespace: namespace, Name: pdb.Name},
	}
	if managedSecret != "" {
		managed = append(managed, powerv1alpha1.ManagedResourceStatus{
			APIVersion: "v1",
			Kind:       "Secret",
			Namespace:  namespace,
			Name:       managedSecret,
		})
	}

	return renderedNUTServer{
		Namespace:       namespace,
		SelectedDevices: selected,
		DesiredReplicas: desiredReplicas,
		ReadyReplicas:   deployment.Status.ReadyReplicas,
		ServiceEndpoints: []powerv1alpha1.ServiceEndpointStatus{
			{
				Name:      service.Name,
				Namespace: namespace,
				DNSName:   fmt.Sprintf("%s.%s.svc.cluster.local", service.Name, namespace),
				// Published so agents can monitor the server without cluster DNS (F-71). Empty
				// for a headless Service and before allocation, which is why the DNS name stays.
				ClusterIP: serviceClusterIP(service),
				Port:      servicePort(server),
			},
		},
		UpstreamNUT:      upstreamStatus,
		ConfigHash:       configHash,
		ManagedResources: managed,
	}, nil
}

func (r *NUTServerReconciler) getManagementCluster(ctx context.Context, server *powerv1alpha1.NUTServer) (*powerv1alpha1.PowerManagementCluster, error) {
	if server.Spec.ManagementClusterRef == nil || server.Spec.ManagementClusterRef.Name == "" {
		return nil, nil
	}

	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, types.NamespacedName{Name: server.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
		return nil, fmt.Errorf("get PowerManagementCluster %q: %w", server.Spec.ManagementClusterRef.Name, err)
	}
	return &cluster, nil
}

func nutServerNamespace(server *powerv1alpha1.NUTServer, cluster *powerv1alpha1.PowerManagementCluster) string {
	if server.Spec.Namespace != "" {
		return server.Spec.Namespace
	}
	if cluster != nil && cluster.Spec.OperandNamespace != nil && cluster.Spec.OperandNamespace.Name != "" {
		return cluster.Spec.OperandNamespace.Name
	}
	return defaultOperandNamespace
}

func nutServerImage(server *powerv1alpha1.NUTServer, cluster *powerv1alpha1.PowerManagementCluster) (string, error) {
	image := server.Spec.Image
	if image.Repository == "" && cluster != nil {
		image = cluster.Spec.Images.NUTServer
	}
	if image.Repository == "" {
		return "", fmt.Errorf("NUTServer operand rendering requires spec.image.repository or spec.managementClusterRef with spec.images.nutServer.repository")
	}

	ref := image.Repository
	if image.Tag != "" {
		ref += ":" + image.Tag
	}
	if image.Digest != "" {
		ref += "@" + image.Digest
	}
	return ref, nil
}

func (r *NUTServerReconciler) selectUPSDevices(ctx context.Context, server *powerv1alpha1.NUTServer) ([]powerv1alpha1.UPSDevice, error) {
	return selectUPSDevices(ctx, r.Client, server)
}

func selectUPSDevices(ctx context.Context, c client.Client, server *powerv1alpha1.NUTServer) ([]powerv1alpha1.UPSDevice, error) {
	byName := map[string]powerv1alpha1.UPSDevice{}

	for _, ref := range server.Spec.DeviceRefs {
		var device powerv1alpha1.UPSDevice
		if err := c.Get(ctx, types.NamespacedName{Name: ref.Name}, &device); err != nil {
			return nil, fmt.Errorf("get UPSDevice %q: %w", ref.Name, err)
		}
		byName[device.Name] = device
	}

	if server.Spec.DeviceSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(server.Spec.DeviceSelector)
		if err != nil {
			return nil, fmt.Errorf("parse deviceSelector: %w", err)
		}
		if selector != labels.Nothing() {
			var list powerv1alpha1.UPSDeviceList
			if err := c.List(ctx, &list); err != nil {
				return nil, fmt.Errorf("list UPSDevices: %w", err)
			}
			for _, device := range list.Items {
				if selector.Matches(labels.Set(device.Labels)) {
					byName[device.Name] = device
				}
			}
		}
	}

	devices := make([]powerv1alpha1.UPSDevice, 0, len(byName))
	for _, device := range byName {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].Name < devices[j].Name
	})
	return devices, nil
}

func renderNUTServerConfig(server *powerv1alpha1.NUTServer, devices []powerv1alpha1.UPSDevice, credentials map[string]map[string]string, simulationFixtures map[string]string) (map[string]string, error) {
	upsConf, err := renderUPSConf(devices, credentials)
	if err != nil {
		return nil, err
	}

	config := map[string]string{
		"nut.conf":  "MODE=netserver\n",
		"ups.conf":  upsConf,
		"upsd.conf": renderUPSDConf(server),
	}
	for _, device := range devices {
		if filename, ok, err := dummyUPSSimulationFileName(device); err != nil {
			return nil, err
		} else if ok {
			fixture, hasFixture := simulationFixtures[device.Name]
			if !hasFixture {
				return nil, fmt.Errorf("UPSDevice %q simulation.sequenceConfigMapRef resolved no fixture content", device.Name)
			}
			config[filename] = fixture
			continue
		}
		filename, ok, err := dummyUPSDefinitionFileName(device)
		if err != nil {
			return nil, err
		}
		if ok {
			definition, err := renderDummyUPSDefinition(device)
			if err != nil {
				return nil, err
			}
			config[filename] = definition
		}
	}
	return config, nil
}

// renderUPSDConf emits upsd.conf. LISTEN alone only opens a plaintext socket: upsd negotiates
// STARTTLS solely when CERTFILE (OpenSSL builds) names a usable key pair, so a spec.tls block
// that mounts a certificate without emitting CERTFILE serves cleartext NUT — including the
// MONITOR password every upsmon sends on connect.
func renderUPSDConf(server *powerv1alpha1.NUTServer) string {
	var out strings.Builder
	fmt.Fprintf(&out, "LISTEN %s %d\n", listenAddress(server), servicePort(server))

	// ALLOW_NO_DEVICE is rendered unconditionally, not only when the selector currently matches
	// nothing (F-51).
	//
	// Without it upsd calls fatalx on a device-less ups.conf -- verified: "Fatal error: at least
	// one UPS must be defined in ups.conf", exit 1 -- so a NUTServer whose selector matches nothing
	// yet, or stops matching, cannot start. That turns an ordinary empty state into a crash loop.
	//
	// Rendering it conditionally would be worse than rendering it always: the directive would then
	// appear and disappear as devices come and go, so the transition from one device to zero would
	// itself require a config change to survive, which is the situation the directive exists to
	// avoid. Upstream's own message names the intended lifecycle -- "please configure the file and
	// reload the service".
	//
	// Nothing is hidden by this. A server with no responsive driver reports NotReady through NS-1
	// and leaves the Service endpoints, so an empty server is visibly idle rather than silently
	// serving nothing.
	out.WriteString("ALLOW_NO_DEVICE true\n")

	if !nutServerTLSEnabled(server) {
		return out.String()
	}

	// NUT wants the chain first and the private key last in one file; the init container
	// assembles it because a kubernetes.io/tls Secret projects tls.crt and tls.key separately.
	fmt.Fprintf(&out, "CERTFILE %s\n", nutServerCombinedCertPath)
	if nutServerDisableWeakProtocols(server) {
		out.WriteString("DISABLE_WEAK_SSL true\n")
	}
	if nutServerVerifiesClientCertificates(server) {
		fmt.Fprintf(&out, "CERTPATH %s\n", nutServerClientCAPath)
		out.WriteString("CERTREQUEST 2\n")
	}
	return out.String()
}

// nutServerTLSEnabled reports whether upsd should be configured to offer TLS at all. Both
// Required and Opportunistic serve the same certificate; the modes differ in what clients are
// told to accept, which is an upsmon.conf concern rather than an upsd.conf one.
func nutServerTLSEnabled(server *powerv1alpha1.NUTServer) bool {
	return server.Spec.TLS.Mode != powerv1alpha1.NUTTLSDisabled && server.Spec.TLS.ServerCertificateRef != nil
}

func nutServerVerifiesClientCertificates(server *powerv1alpha1.NUTServer) bool {
	return server.Spec.TLS.VerifyClientCertificates != nil &&
		*server.Spec.TLS.VerifyClientCertificates &&
		server.Spec.TLS.ClientCARef != nil
}

func nutServerDisableWeakProtocols(server *powerv1alpha1.NUTServer) bool {
	return server.Spec.TLS.DisableWeakProtocols == nil || *server.Spec.TLS.DisableWeakProtocols
}

func renderUPSConf(devices []powerv1alpha1.UPSDevice, credentials map[string]map[string]string) (string, error) {
	var out strings.Builder
	for _, device := range devices {
		if result := validateUPSDevice(&device); !result.accepted {
			return "", fmt.Errorf("invalid selected UPSDevice %q: %s", device.Name, result.message)
		}

		name := nutDeviceName(device)
		if err := validateNUTConfigToken(name); err != nil {
			return "", fmt.Errorf("invalid NUT device name for UPSDevice %q: %w", device.Name, err)
		}
		driver := renderedUPSDriver(device)
		if err := validateNUTConfigValue(driver); err != nil {
			return "", fmt.Errorf("invalid driver for UPSDevice %q: %w", device.Name, err)
		}

		fmt.Fprintf(&out, "[%s]\n", name)
		fmt.Fprintf(&out, "  driver = %s\n", driver)
		if device.Spec.UpstreamNUT != nil {
			target := upstreamNUTTarget(device)
			if err := validateNUTConfigValue(target); err != nil {
				return "", fmt.Errorf("invalid upstream NUT target for UPSDevice %q: %w", device.Name, err)
			}
			fmt.Fprintf(&out, "  port = %s\n", target)
			fmt.Fprintf(&out, "  mode = repeater\n")
			fmt.Fprintf(&out, "  authconf = %s\n", upstreamNUTAuthConf(device))
			if !upstreamNUTStrictStart(device) {
				fmt.Fprintf(&out, "  repeater_disable_strict_start = true\n")
			}
		}
		if filename, ok, err := dummyUPSSimulationFileName(device); err != nil {
			return "", err
		} else if ok {
			fmt.Fprintf(&out, "  port = %s\n", filename)
		} else if filename, ok, err := dummyUPSDefinitionFileName(device); err != nil {
			return "", err
		} else if ok {
			fmt.Fprintf(&out, "  port = %s\n", filename)
		}
		if device.Spec.Endpoint != nil {
			endpoint := device.Spec.Endpoint.Host
			if device.Spec.Endpoint.Port != nil {
				endpoint = fmt.Sprintf("%s:%d", endpoint, *device.Spec.Endpoint.Port)
			}
			if err := validateNUTConfigValue(endpoint); err != nil {
				return "", fmt.Errorf("invalid endpoint for UPSDevice %q: %w", device.Name, err)
			}
			if _, hasPort := device.Spec.DriverOptions["port"]; !hasPort {
				fmt.Fprintf(&out, "  port = %s\n", endpoint)
			}
		}

		options := make(map[string]string, len(device.Spec.DriverOptions)+1)
		if hasSimulationSequence(device) {
			// Explicit, even though the .seq extension alone already selects dummy-loop mode
			// (belt-and-suspenders); a user-supplied mode below still wins on key collision.
			options["mode"] = "dummy-loop"
		}
		for key, value := range device.Spec.DriverOptions {
			options[key] = value
		}
		// Credential-Secret-sourced values win on key collision: they are the operator-verified
		// source of truth for driver auth fields, whereas driverOptions is user-authored plaintext.
		for key, value := range credentials[device.Name] {
			options[key] = value
		}

		keys := make([]string, 0, len(options))
		for key := range options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := options[key]
			if err := validateNUTConfigToken(key); err != nil {
				return "", fmt.Errorf("invalid driver option key for UPSDevice %q: %w", device.Name, err)
			}
			if err := validateNUTConfigValue(value); err != nil {
				return "", fmt.Errorf("invalid driver option value for UPSDevice %q: %w", device.Name, err)
			}
			fmt.Fprintf(&out, "  %s = %s\n", key, value)
		}
		out.WriteString("\n")
	}
	return out.String(), nil
}

func dummyUPSDefinitionFileName(device powerv1alpha1.UPSDevice) (string, bool, error) {
	if device.Spec.Driver != "dummy-ups" {
		return "", false, nil
	}
	if device.Spec.UpstreamNUT != nil {
		return "", false, nil
	}
	if explicitPort := device.Spec.DriverOptions["port"]; explicitPort != "" {
		return "", false, nil
	}
	if hasSimulationSequence(device) {
		return "", false, nil
	}
	filename := nutDeviceName(device) + ".dev"
	if err := validateNUTConfigToken(filename); err != nil {
		return "", false, fmt.Errorf("invalid dummy UPS definition filename for UPSDevice %q: %w", device.Name, err)
	}
	return filename, true, nil
}

// hasSimulationSequence reports whether a UPSDevice requests dummy-ups scripted
// state-transition simulation via a referenced .seq fixture ConfigMap.
func hasSimulationSequence(device powerv1alpha1.UPSDevice) bool {
	return device.Spec.Simulation != nil && device.Spec.Simulation.SequenceConfigMapRef.Name != ""
}

// dummyUPSSimulationFileName names the dummy-ups `.seq` scripted-transition fixture file for a
// device requesting spec.simulation, mirroring dummyUPSDefinitionFileName's static `.dev` case.
// The `.seq` extension alone selects NUT's dummy-loop driver mode; renderUPSConf also writes an
// explicit `mode = dummy-loop` driver option for clarity.
func dummyUPSSimulationFileName(device powerv1alpha1.UPSDevice) (string, bool, error) {
	if device.Spec.Driver != "dummy-ups" {
		return "", false, nil
	}
	if device.Spec.UpstreamNUT != nil {
		return "", false, nil
	}
	if explicitPort := device.Spec.DriverOptions["port"]; explicitPort != "" {
		return "", false, nil
	}
	if !hasSimulationSequence(device) {
		return "", false, nil
	}
	filename := nutDeviceName(device) + ".seq"
	if err := validateNUTConfigToken(filename); err != nil {
		return "", false, fmt.Errorf("invalid dummy UPS simulation filename for UPSDevice %q: %w", device.Name, err)
	}
	return filename, true, nil
}

// resolveUPSDeviceSimulationFixtures fetches spec.simulation.sequenceConfigMapRef for each
// selected device so renderNUTServerConfig can render a dummy-ups `.seq` scripted-transition
// fixture instead of the static `.dev` file, letting tests drive real OnBattery/LowBattery
// transitions without hand-patching cluster state. Same-namespace-only, matching
// resolveUPSDeviceCredentials.
func resolveUPSDeviceSimulationFixtures(ctx context.Context, c client.Client, namespace string, devices []powerv1alpha1.UPSDevice) (map[string]string, error) {
	fixtures := make(map[string]string)
	for _, device := range devices {
		if !hasSimulationSequence(device) {
			continue
		}
		ref := device.Spec.Simulation.SequenceConfigMapRef
		if ref.Namespace != namespace {
			return nil, fmt.Errorf("UPSDevice %q simulation.sequenceConfigMapRef must be in operand namespace %q", device.Name, namespace)
		}
		key := device.Spec.Simulation.SequenceKey
		if key == "" {
			key = "sequence.seq"
		}
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &cm); err != nil {
			return nil, fmt.Errorf("get simulation.sequenceConfigMapRef ConfigMap for UPSDevice %q: %w", device.Name, err)
		}
		content, ok := cm.Data[key]
		if !ok {
			return nil, fmt.Errorf("UPSDevice %q simulation.sequenceConfigMapRef ConfigMap %q missing key %q", device.Name, ref.Name, key)
		}
		fixtures[device.Name] = content
	}
	return fixtures, nil
}

func renderedUPSDriver(device powerv1alpha1.UPSDevice) string {
	if device.Spec.UpstreamNUT != nil {
		return "dummy-ups"
	}
	return device.Spec.Driver
}

func upstreamNUTTarget(device powerv1alpha1.UPSDevice) string {
	upstream := device.Spec.UpstreamNUT
	target := fmt.Sprintf("%s@%s", upstream.UPSName, upstream.Host)
	port := upstreamNUTPort(device)
	if port != 3493 || upstream.Port != nil {
		target = fmt.Sprintf("%s:%d", target, port)
	}
	return target
}

func upstreamNUTPort(device powerv1alpha1.UPSDevice) int32 {
	if device.Spec.UpstreamNUT != nil && device.Spec.UpstreamNUT.Port != nil {
		return *device.Spec.UpstreamNUT.Port
	}
	return 3493
}

func upstreamNUTStrictStart(device powerv1alpha1.UPSDevice) bool {
	if device.Spec.UpstreamNUT == nil || device.Spec.UpstreamNUT.StrictStart == nil {
		return true
	}
	return *device.Spec.UpstreamNUT.StrictStart
}

func upstreamNUTAuthConf(device powerv1alpha1.UPSDevice) string {
	switch upstreamAuthMode(device.Spec.UpstreamNUT) {
	case powerv1alpha1.UPSUpstreamNUTAuthDefault:
		return "default"
	case powerv1alpha1.UPSUpstreamNUTAuthSecret:
		return "/etc/nut/upstream-auth/" + nutDeviceName(device) + ".nutauth.conf"
	default:
		return "none"
	}
}

// resolveUPSDeviceCredentials fetches spec.credentialSecretRef for each selected device (SNMP
// community, SNMPv3 secName/authPassword/privPassword, etc.) so renderUPSConf can merge real
// driver auth fields into ups.conf instead of leaving them unwired. Same-namespace-only, matching
// the existing upstreamNUTAuthProjections convention.
func resolveUPSDeviceCredentials(ctx context.Context, c client.Client, namespace string, devices []powerv1alpha1.UPSDevice) (map[string]map[string]string, error) {
	credentials := make(map[string]map[string]string)
	for _, device := range devices {
		ref := device.Spec.CredentialSecretRef
		if ref == nil {
			continue
		}
		if ref.Namespace != namespace {
			return nil, fmt.Errorf("UPSDevice %q credentialSecretRef must be in operand namespace %q", device.Name, namespace)
		}
		var secret corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &secret); err != nil {
			return nil, fmt.Errorf("get credentialSecretRef Secret for UPSDevice %q: %w", device.Name, err)
		}
		values := make(map[string]string, len(secret.Data))
		for key, value := range secret.Data {
			values[key] = string(value)
		}
		credentials[device.Name] = values
	}
	return credentials, nil
}

func upstreamNUTEgressRules(devices []powerv1alpha1.UPSDevice) []networkingv1.NetworkPolicyEgressRule {
	portsByNumber := map[int32]struct{}{}
	for _, device := range devices {
		if device.Spec.UpstreamNUT == nil {
			continue
		}
		portsByNumber[upstreamNUTPort(device)] = struct{}{}
	}
	if len(portsByNumber) == 0 {
		return nil
	}

	ports := make([]int, 0, len(portsByNumber))
	for port := range portsByNumber {
		ports = append(ports, int(port))
	}
	sort.Ints(ports)

	rules := make([]networkingv1.NetworkPolicyEgressRule, 0, len(ports)+1)
	for _, port := range ports {
		rules = append(rules, networkingv1.NetworkPolicyEgressRule{
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: ptrProtocol(corev1.ProtocolTCP),
					Port:     ptrIntOrStringFromInt32(int32(port)),
				},
			},
		})
	}
	rules = append(rules, networkingv1.NetworkPolicyEgressRule{
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
	})
	return rules
}

func upstreamNUTAuthProjections(devices []powerv1alpha1.UPSDevice, namespace string) ([]corev1.VolumeProjection, error) {
	projections := make([]corev1.VolumeProjection, 0)
	for _, device := range devices {
		if device.Spec.UpstreamNUT == nil ||
			upstreamAuthMode(device.Spec.UpstreamNUT) != powerv1alpha1.UPSUpstreamNUTAuthSecret {
			continue
		}
		ref := device.Spec.UpstreamNUT.Auth.SecretKeyRef
		if ref == nil {
			return nil, fmt.Errorf("UPSDevice %q upstream NUT auth mode Secret requires secretKeyRef", device.Name)
		}
		if ref.Namespace != namespace {
			return nil, fmt.Errorf("UPSDevice %q upstream NUT auth Secret must be in operand namespace %q", device.Name, namespace)
		}
		projections = append(projections, corev1.VolumeProjection{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Items: []corev1.KeyToPath{
					{Key: ref.Key, Path: "upstream-auth/" + nutDeviceName(device) + ".nutauth.conf"},
				},
			},
		})
	}
	return projections, nil
}

func renderDummyUPSDefinition(device powerv1alpha1.UPSDevice) (string, error) {
	name := nutDeviceName(device)
	displayName := device.Spec.DisplayName
	if displayName == "" {
		displayName = name
	}
	if err := validateNUTConfigValue(displayName); err != nil {
		return "", fmt.Errorf("invalid dummy UPS display name for UPSDevice %q: %w", device.Name, err)
	}
	return fmt.Sprintf(`device.mfr: nut-operator
device.model: %s
device.serial: %s
ups.mfr: nut-operator
ups.model: %s
ups.status: OL
battery.charge: 100
battery.runtime: 3600
ups.load: 10
`, displayName, name, displayName), nil
}

func nutDeviceName(device powerv1alpha1.UPSDevice) string {
	if device.Status.NUTName != "" {
		return sanitizeNUTName(device.Status.NUTName)
	}
	return sanitizeNUTName(device.Name)
}

func sanitizeNUTName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateNUTConfigToken(value string) error {
	if value == "" {
		return fmt.Errorf("value is empty")
	}
	return validateNUTConfigValue(value)
}

func validateNUTConfigValue(value string) error {
	if strings.ContainsAny(value, "\r\n[]") {
		return fmt.Errorf("value contains unsupported control or section characters")
	}
	return nil
}

func listenAddress(server *powerv1alpha1.NUTServer) string {
	if server.Spec.Config.ListenAddress != "" {
		return server.Spec.Config.ListenAddress
	}
	return "0.0.0.0"
}

func servicePort(server *powerv1alpha1.NUTServer) int32 {
	if server.Spec.Service.Port != nil {
		return *server.Spec.Service.Port
	}
	return 3493
}

// upsdReadinessProbeScript proves at least one configured device has a live, connected driver
// (F-17), using NUT's own driver-state report rather than inferring it (F-46).
//
// `upsdrvctl status` is the built-in answer to exactly this question. It prints one TAB-separated
// row per device in ups.conf with an S_RESPONSIVE column holding RESPONSIVE or NOT_RESPONSIVE,
// determined by probing the driver's own socket. Ready means at least one row says RESPONSIVE.
//
// `upsc -l` cannot answer it (verified empirically): it lists every name defined in ups.conf
// whether or not the driver ever connected, so a fully-disconnected driver still reports as
// present. The earlier probe worked around that by querying a variable per name and reading the
// failure, which reimplemented in shell what upsdrvctl reports directly.
//
// The match is field-exact and deliberately so: NOT_RESPONSIVE contains RESPONSIVE as a
// substring, so a grep for the token would report every dead driver as healthy -- a readiness
// probe that can never fail. Comparing whole awk fields also skips the header row for free,
// since its token is S_RESPONSIVE.
func upsdReadinessProbeScript() string {
	return `upsdrvctl status 2>/dev/null | ` +
		`awk '{for (i = 1; i <= NF; i++) if ($i == "RESPONSIVE") { found = 1; exit } } END { exit !found }'`
}

// driverWatchdogScript restarts drivers that have stopped answering (F-49).
//
// upsdrvctl start runs once at startup and nothing retries. A driver that dies later leaves upsd
// alive, so the container is never restarted; readiness then fails correctly and pulls the pod from
// the Service endpoints, where it stays indefinitely with every agent in DEADTIME. Readiness
// reports the fault accurately and nothing acts on it. This is what acts on it.
//
// Three behaviors verified by running the operand image rather than assumed, because each one
// decides part of the script:
//
//   - After `kill -9` on a driver, `upsdrvctl status` reports RUNNING=N/A and
//     S_RESPONSIVE=NOT_RESPONSIVE, and the stale PID file is left behind.
//   - `upsdrvctl start <ups>` recovers from exactly that state on its own: it detects the stale PID
//     file, terminates the phantom, and starts a fresh driver, exiting 0. No stop-then-start dance
//     is needed, and adding one would introduce a window where the driver is deliberately down.
//   - `upsdrvctl start <ups>` against a *healthy* driver terminates and replaces it. That is why
//     the non-responsive reading is confirmed with a second probe before acting: a transient miss
//     would otherwise cost a working driver a restart, and this loop runs unattended forever.
//
// The RESPONSIVE match is field-exact for the same reason upsdReadinessProbeScript's is --
// NOT_RESPONSIVE contains RESPONSIVE as a substring, so a substring test would find every dead
// driver healthy and the watchdog would never do anything. The header row is skipped by name
// rather than by line number, since its own token is S_RESPONSIVE and matching it would try to
// start a device called UPSNAME.
func driverWatchdogScript() string {
	return `set -u
notResponsive() {
  upsdrvctl status 2>/dev/null | ` + driverWatchdogNonResponsiveSelector() + `
}
configDigest() {
  cat /etc/nut/ups.conf /etc/nut/upsd.users 2>/dev/null | md5sum | cut -d' ' -f1
}
lastDigest="$(configDigest)"
while true; do
  # The interval is taken at the top of the loop, not the bottom, so the first pass happens after a
  # full interval rather than immediately. The watchdog container and the upsd container start
  # together, and checking straight away races the entrypoint's own driver start: the drivers are
  # legitimately not responsive yet, both the check and its confirmation agree on that, and the
  # watchdog restarts a driver that was seconds from being healthy. Observed, not hypothesized.
  sleep ` + strconv.Itoa(driverWatchdogIntervalSeconds) + `

  currentDigest="$(configDigest)"
  if [ "$currentDigest" != "$lastDigest" ]; then
    echo "driver-watchdog: reloadable configuration changed, reloading upsd"
    if upsd -c reload; then
      lastDigest="$currentDigest"
    else
      echo "driver-watchdog: upsd reload failed, will retry"
    fi
  fi
  for ups in $(notResponsive); do
    if notResponsive | grep -qx "$ups"; then
      echo "driver-watchdog: $ups is not responsive, restarting its driver"
      upsdrvctl start "$ups" || echo "driver-watchdog: restart of $ups failed, will retry"
    fi
  done
done`
}

// driverWatchdogNonResponsiveSelector prints the name of every device whose driver is not
// answering. It is split out from the loop so it can be run against captured `upsdrvctl status`
// output in a test, because every way this can be wrong is silent.
//
// It selects rows that state NOT_RESPONSIVE rather than rows that fail to state RESPONSIVE, and
// the difference is not stylistic. `upsdrvctl status` prints a version banner to stdout above the
// header -- "Network UPS Tools upsdrvctl - UPS driver controller 2.8.5 release" -- and a selector
// keyed on the absence of RESPONSIVE reads that line as a device named "Network" and tries to
// start it on every tick. That was the first implementation, and it took running the watchdog
// against the operand image to see it: the driver still recovered, so every test passed while the
// loop spent each pass failing to start a device that does not exist.
//
// Matching on the positive token also handles the header for free, since its own token is
// S_RESPONSIVE, and it cannot invent a device name out of prose the way absence-matching can.
//
// The match is field-exact for the reason NS-2 gives in reverse: NOT_RESPONSIVE contains
// RESPONSIVE, so anything less than whole-field comparison confuses the two states in one
// direction or the other.
func driverWatchdogNonResponsiveSelector() string {
	return `awk '{
    for (i = 1; i <= NF; i++) if ($i == "NOT_RESPONSIVE") { print $1; next }
  }'`
}

// upsdResources returns what the upsd container asks for, defaulting it when spec.resources says
// nothing.
//
// The container ran unbounded while the 10m/32Mi sidecar beside it was sized, which is the
// backwards half of the pair: upsd is the process that must survive a power event, and an
// unrequested container is the first thing the kubelet evicts under node pressure -- exactly the
// condition a rack losing power tends to produce.
//
// Requests equal limits for the same reason they do on the watchdog: the pod is Guaranteed, so it
// sits in the last eviction class rather than the first.
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

// defaultUpsdResources sizes upsd plus its drivers. upsd itself is a small single-threaded server;
// the variable part is one driver process per device, each a few MB of RSS polling on an interval.
// 128Mi leaves room for a double-digit device count without putting an OOM kill on the path that
// has to report the outage.
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

// driverWatchdogResources sizes the sidecar without spending the user's budget on it.
//
// spec.resources describes upsd. Reusing it here would silently double whatever the user declared
// for the server, which is the kind of surprise that shows up as an unschedulable pod on a full
// node rather than as an error. The watchdog is a shell loop that execs upsdrvctl twice a minute,
// so its footprint is a property of this implementation rather than a decision the user should be
// asked to make.
//
// Requests equal limits deliberately. Without them the sidecar would drag a pod whose upsd is
// Guaranteed down to Burstable, changing the eviction ordering of the server on the observability
// path for every agent -- an operand-shape change nobody asked for.
func driverWatchdogResources() corev1.ResourceRequirements {
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	}
	return corev1.ResourceRequirements{Requests: requests, Limits: limits}
}

func serviceType(server *powerv1alpha1.NUTServer) corev1.ServiceType {
	if server.Spec.Service.Type != "" {
		return server.Spec.Service.Type
	}
	return corev1.ServiceTypeClusterIP
}

func labelsForNUTServer(server *powerv1alpha1.NUTServer) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "nut-operator",
		"app.kubernetes.io/component":  "nut-server",
		"app.kubernetes.io/managed-by": "nut-operator",
		"power.zalud.io/nutserver":     server.Name,
	}
}

func deploymentName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-server"
}

func configMapName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-config"
}

func operatorManagedUsersSecretName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-users"
}

// driverConfigSecretName holds the ups.conf keys that carry real driver credentials
// (spec.credentialSecretRef contents merged in), kept out of the plain ConfigMap.
func driverConfigSecretName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-driver-config"
}

func networkPolicyName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-server"
}

func podDisruptionBudgetName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-server"
}

func (r *NUTServerReconciler) ensureNUTServerConfigMap(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string, data map[string]string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labelsForNUTServer(server)
		cm.Data = data
		return controllerutil.SetControllerReference(server, cm, r.Scheme)
	})
	return cm, err
}

func (r *NUTServerReconciler) ensureNUTServerDriverConfigSecret(ctx context.Context, server *powerv1alpha1.NUTServer, namespace, upsConf string) (*corev1.Secret, error) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: driverConfigSecretName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNUTServer(server)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"ups.conf": []byte(upsConf)}
		return controllerutil.SetControllerReference(server, secret, r.Scheme)
	})
	return secret, err
}

func (r *NUTServerReconciler) ensureNUTUsersSecret(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (powerv1alpha1.NamespacedNameReference, string, error) {
	if server.Spec.Auth.Mode == powerv1alpha1.NUTAuthExistingSecret && server.Spec.Auth.ExistingSecretRef != nil {
		if server.Spec.Auth.ExistingSecretRef.Namespace != namespace {
			return powerv1alpha1.NamespacedNameReference{}, "", fmt.Errorf("ExistingSecret auth for NUTServer %q must reference a Secret in operand namespace %q", server.Name, namespace)
		}
		return *server.Spec.Auth.ExistingSecretRef, "", nil
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorManagedUsersSecretName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNUTServer(server)
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		if len(secret.Data["admin-password"]) == 0 {
			password, err := randomPassword()
			if err != nil {
				return err
			}
			secret.Data["admin-password"] = []byte(password)
		}
		if len(secret.Data["monitor-password"]) == 0 {
			password, err := randomPassword()
			if err != nil {
				return err
			}
			secret.Data["monitor-password"] = []byte(password)
		}
		secret.Data["upsd.users"] = []byte(renderUPSDUsers(
			string(secret.Data["admin-password"]),
			string(secret.Data["monitor-password"]),
		))
		return controllerutil.SetControllerReference(server, secret, r.Scheme)
	})
	if err != nil {
		return powerv1alpha1.NamespacedNameReference{}, "", err
	}
	return powerv1alpha1.NamespacedNameReference{Namespace: namespace, Name: secret.Name}, secret.Name, nil
}

func renderUPSDUsers(adminPassword, monitorPassword string) string {
	return fmt.Sprintf(`[admin]
  password = %s
  actions = SET
  instcmds = ALL

[monitor]
  password = %s
  upsmon secondary
`, adminPassword, monitorPassword)
}

func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate operator-managed NUT credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
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

func (r *NUTServerReconciler) ensureNUTServerNetworkPolicy(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string, devices []powerv1alpha1.UPSDevice) (*networkingv1.NetworkPolicy, error) {
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: networkPolicyName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		labels := labelsForNUTServer(server)
		policy.Labels = labels
		policy.Spec.PodSelector = metav1.LabelSelector{MatchLabels: labels}
		policy.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
		policy.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{
			{
				From: []networkingv1.NetworkPolicyPeer{
					{
						PodSelector: &metav1.LabelSelector{},
					},
					{
						// The operator's own manager pod polls upsd for UPSDevice telemetry
						// (UPSDeviceReconciler) but does not live in this operand namespace, so the
						// same-namespace peer above never covers it. Verified against a real kind
						// cluster: with only the same-namespace rule, a real NetworkPolicy-enforcing
						// CNI (confirmed here, and true for the Cilium CNI this project's own
						// reference deployment uses) silently blocks the manager's poll traffic --
						// telemetry never updates, with no error surfaced anywhere except a generic
						// connect timeout. NamespaceSelector is intentionally unscoped (the manager's
						// own namespace is deployment-configurable, e.g. via kustomize namePrefix);
						// the manager pod's labels are the actual constant, matching the same
						// selector config/network-policy's allow-metrics-traffic/allow-webhook-traffic
						// policies already use to identify it from outside its namespace.
						NamespaceSelector: &metav1.LabelSelector{},
						PodSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"control-plane":          "controller-manager",
								"app.kubernetes.io/name": "nut-operator",
							},
						},
					},
				},
				Ports: []networkingv1.NetworkPolicyPort{
					{
						Protocol: ptrProtocol(corev1.ProtocolTCP),
						Port:     ptrIntOrStringFromInt32(servicePort(server)),
					},
				},
			},
		}
		egress := upstreamNUTEgressRules(devices)
		if len(egress) > 0 {
			policy.Spec.PolicyTypes = append(policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
			policy.Spec.Egress = egress
		} else {
			policy.Spec.Egress = nil
		}
		return controllerutil.SetControllerReference(server, policy, r.Scheme)
	})
	return policy, err
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
		// Recreate (F-16): upsd is a singleton with long-lived client TCP sessions and NUT's own
		// login accounting. RollingUpdate would briefly run two instances and split that
		// accounting, the same failure mode F-15 pins replicas to 1 to avoid. A short outage
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
		// F-48: the watchdog signals upsd with `upsd -c reload`, and signalling across the
		// container boundary needs a shared PID namespace. Without it upsd is PID 1 in its own
		// container, so the PID file reads "1" and upsd refuses it -- "Ignoring invalid pid number
		// 1". The reload is not merely blocked but blocked in a way that resembles success, since
		// the command still exits 0.
		//
		// The isolation cost is small here in a way it would not be elsewhere: both containers run
		// the same image as the same non-root UID and are peers. This is not the node agent's
		// split, where F-57 records a real trust boundary between a container that parses network
		// responses and one holding CAP_SYS_BOOT.
		//
		// It also makes the pause container PID 1, which reaps the orphaned drivers upsd never
		// reaped (F-76).
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
				// The watchdog runs the same image and reaches the drivers the same way upsd does:
				// through /run/nut, which holds the driver sockets and PID files, and /etc/nut,
				// which is where upsdrvctl reads the device list from. It needs no privilege upsd
				// does not already have, and it carries no probes -- a restart of the supervisor
				// must never take the server down with it, which is the whole reason it is a
				// separate container.
				Name:            driverWatchdogContainerName,
				Image:           image,
				ImagePullPolicy: pullPolicy,
				Command:         []string{"sh", "-c", driverWatchdogScript()},
				Resources:       driverWatchdogResources(),
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

// validateNUTServerTLSSecrets fails the reconcile before the Deployment is written when the
// referenced TLS Secrets cannot produce a working upsd. Without it the only symptom is an init
// container that crash-loops on a missing file, or worse, an upsd that starts and quietly falls
// back to plaintext, which is exactly the failure this whole code path exists to prevent.
func (r *NUTServerReconciler) validateNUTServerTLSSecrets(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) error {
	if !nutServerTLSEnabled(server) {
		return nil
	}
	if err := r.requireSecretKeys(ctx, namespace, *server.Spec.TLS.ServerCertificateRef, "spec.tls.serverCertificateRef",
		nutServerTLSCertificateKey, nutServerTLSPrivateKeyKey); err != nil {
		return err
	}
	if !nutServerVerifiesClientCertificates(server) {
		return nil
	}
	return r.requireSecretKeys(ctx, namespace, *server.Spec.TLS.ClientCARef, "spec.tls.clientCARef", nutServerCABundleKey)
}

// nutServerTLSMaterialDigest summarizes the certificate material upsd serves, for the restart hash.
//
// The certificate is not part of the rendered config: it arrives through a referenced Secret and is
// mounted as a volume, so a rotation changes the pod's filesystem without changing anything the
// operator writes. upsd builds its SSL context once at startup, so the rotated certificate sits on
// disk unserved until the process restarts. Folding a digest of the material into the restart hash
// is what turns a rotation into a rollout.
//
// It is a digest rather than the material itself for the F-24 reason: the value ends up in a
// pod-template annotation, which is broadly readable, and a SHA-256 over the certificate and key
// cannot be reversed to recover them.
func (r *NUTServerReconciler) nutServerTLSMaterialDigest(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (string, error) {
	if !nutServerTLSEnabled(server) {
		return "", nil
	}

	material := map[string][]byte{}
	var serverSecret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: server.Spec.TLS.ServerCertificateRef.Name}
	if err := r.Get(ctx, key, &serverSecret); err != nil {
		return "", fmt.Errorf("get Secret %s for spec.tls.serverCertificateRef: %w", key, err)
	}
	material[nutServerTLSCertificateKey] = serverSecret.Data[nutServerTLSCertificateKey]
	material[nutServerTLSPrivateKeyKey] = serverSecret.Data[nutServerTLSPrivateKeyKey]

	if nutServerVerifiesClientCertificates(server) {
		var caSecret corev1.Secret
		caKey := types.NamespacedName{Namespace: namespace, Name: server.Spec.TLS.ClientCARef.Name}
		if err := r.Get(ctx, caKey, &caSecret); err != nil {
			return "", fmt.Errorf("get Secret %s for spec.tls.clientCARef: %w", caKey, err)
		}
		material["clientCA/"+nutServerCABundleKey] = caSecret.Data[nutServerCABundleKey]
	}
	return hashByteMap(material), nil
}

// requireSecretKeys enforces both that the Secret carries the expected keys and that it lives in
// the operand namespace. The namespace check is not redundant with the webhook: a pod can only
// mount Secrets from its own namespace, so a ref naming another namespace would otherwise render
// a volume silently pointed at a same-named Secret that may not exist or may be someone else's.
func (r *NUTServerReconciler) requireSecretKeys(ctx context.Context, namespace string, ref powerv1alpha1.NamespacedNameReference, fieldPath string, keys ...string) error {
	if ref.Namespace != "" && ref.Namespace != namespace {
		return fmt.Errorf("%s must name a Secret in operand namespace %q, got %q", fieldPath, namespace, ref.Namespace)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return fmt.Errorf("get Secret %s/%s for %s: %w", namespace, ref.Name, fieldPath, err)
	}
	for _, key := range keys {
		if len(secret.Data[key]) == 0 {
			return fmt.Errorf("secret %s/%s for %s requires a non-empty data[%q]", namespace, ref.Name, fieldPath, key)
		}
	}
	return nil
}

// applyNUTServerTLSOperand attaches everything upsd needs to actually terminate TLS: the
// certificate Secret, an init container that concatenates it into CERTFILE's expected
// chain-then-key layout, and the client CA when certificate-based client validation is on.
//
// The concatenation is an init container rather than an entrypoint change because the upsd image
// is caller-supplied (spec.image) and the operator does not control its entrypoint. It reuses the
// same image so no second image has to be pulled or trusted, and writes to an emptyDir because
// the operand runs with a read-only root filesystem.
func applyNUTServerTLSOperand(deployment *appsv1.Deployment, server *powerv1alpha1.NUTServer, image string, pullPolicy corev1.PullPolicy) {
	if !nutServerTLSEnabled(server) {
		return
	}
	podSpec := &deployment.Spec.Template.Spec

	podSpec.Volumes = append(podSpec.Volumes,
		corev1.Volume{
			Name: nutServerTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  server.Spec.TLS.ServerCertificateRef.Name,
					DefaultMode: ptrInt32(0440),
				},
			},
		},
		corev1.Volume{
			Name: nutServerCombinedTLSVolume,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resource.NewQuantity(1*1024*1024, resource.BinarySI),
				},
			},
		},
	)
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            nutServerTLSInitContainerName,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"sh", "-c", nutServerTLSAssembleScript()},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: nutServerTLSVolumeName, MountPath: nutServerCertificateMountPath, ReadOnly: true},
			{Name: nutServerCombinedTLSVolume, MountPath: nutServerCombinedCertDir},
		},
	})
	upsd := nutServerUpsdContainer(podSpec)
	if upsd == nil {
		return
	}
	upsd.VolumeMounts = append(upsd.VolumeMounts,
		corev1.VolumeMount{Name: nutServerCombinedTLSVolume, MountPath: nutServerCombinedCertDir, ReadOnly: true},
	)

	if !nutServerVerifiesClientCertificates(server) {
		return
	}
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: nutServerClientCAVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  server.Spec.TLS.ClientCARef.Name,
				DefaultMode: ptrInt32(0440),
			},
		},
	})
	upsd.VolumeMounts = append(upsd.VolumeMounts,
		corev1.VolumeMount{Name: nutServerClientCAVolumeName, MountPath: nutServerClientCAMountPath, ReadOnly: true},
	)
}

// nutServerUpsdContainer finds the server container by name rather than by position.
//
// These mounts carry the certificate and CA material upsd negotiates TLS with, and they were
// indexed as Containers[0] while upsd was the only container. Now that the pod also runs the F-49
// watchdog, position is no longer a safe way to name the one container that serves the protocol:
// reordering the slice would mount the CA into the sidecar and leave upsd serving plaintext while
// reporting TLS Required, which is F-37 and F-39 arriving a third time by a different route.
func nutServerUpsdContainer(podSpec *corev1.PodSpec) *corev1.Container {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == nutServerUpsdContainerName {
			return &podSpec.Containers[i]
		}
	}
	return nil
}

// nutServerTLSAssembleScript writes the CERTFILE upsd expects. Order matters: NUT documents the
// subject certificate first, then any intermediates, then the matching private key last.
func nutServerTLSAssembleScript() string {
	return fmt.Sprintf(
		"set -e; umask 077; cat %s/%s %s/%s > %s",
		nutServerCertificateMountPath, nutServerTLSCertificateKey,
		nutServerCertificateMountPath, nutServerTLSPrivateKeyKey,
		nutServerCombinedCertPath,
	)
}

// ensureNUTServerPodDisruptionBudget renders a PDB with minAvailable 1 (F-18). Paired with the
// F-15 replica pin, this blocks voluntary eviction of the sole upsd pod entirely, which is the
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

func ptrBool(value bool) *bool {
	return &value
}

func ptrInt32(value int32) *int32 {
	return &value
}

func ptrInt64(value int64) *int64 {
	return &value
}

func ptrProtocol(value corev1.Protocol) *corev1.Protocol {
	return &value
}

func ptrIntOrStringFromInt32(value int32) *intstr.IntOrString {
	port := intstr.FromInt32(value)
	return &port
}

func hashStringMap(data map[string]string) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	hasher := sha256.New()
	for _, key := range keys {
		hasher.Write([]byte(key))
		hasher.Write([]byte{0})
		hasher.Write([]byte(data[key]))
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func hashByteMap(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	hasher := sha256.New()
	for _, key := range keys {
		hasher.Write([]byte(key))
		hasher.Write([]byte{0})
		hasher.Write(data[key])
		hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
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
