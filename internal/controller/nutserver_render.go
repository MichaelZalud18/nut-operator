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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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
	configData, err := renderNUTServerConfig(server, devices)
	if err != nil {
		return renderedNUTServer{}, err
	}
	configHash := hashStringMap(configData)

	if err := r.ensureOperandNamespace(ctx, namespace); err != nil {
		return renderedNUTServer{}, err
	}

	configMap, err := r.ensureNUTServerConfigMap(ctx, server, namespace, configData)
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
	deployment, err := r.ensureNUTServerDeployment(ctx, server, namespace, image, configMap.Name, secretRef, configHash, devices)
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
		{APIVersion: "v1", Kind: "Service", Namespace: namespace, Name: service.Name},
		{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy", Namespace: namespace, Name: networkPolicy.Name},
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: namespace, Name: deployment.Name},
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

func renderNUTServerConfig(server *powerv1alpha1.NUTServer, devices []powerv1alpha1.UPSDevice) (map[string]string, error) {
	upsConf, err := renderUPSConf(devices)
	if err != nil {
		return nil, err
	}

	config := map[string]string{
		"nut.conf":  "MODE=netserver\n",
		"ups.conf":  upsConf,
		"upsd.conf": fmt.Sprintf("LISTEN %s %d\n", listenAddress(server), servicePort(server)),
	}
	for _, device := range devices {
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

func renderUPSConf(devices []powerv1alpha1.UPSDevice) (string, error) {
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
		if filename, ok, err := dummyUPSDefinitionFileName(device); err != nil {
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

		keys := make([]string, 0, len(device.Spec.DriverOptions))
		for key := range device.Spec.DriverOptions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := device.Spec.DriverOptions[key]
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
	filename := nutDeviceName(device) + ".dev"
	if err := validateNUTConfigToken(filename); err != nil {
		return "", false, fmt.Errorf("invalid dummy UPS definition filename for UPSDevice %q: %w", device.Name, err)
	}
	return filename, true, nil
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

func networkPolicyName(server *powerv1alpha1.NUTServer) string {
	return server.Name + "-nut-server"
}

func (r *NUTServerReconciler) ensureOperandNamespace(ctx context.Context, name string) error {
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

func (r *NUTServerReconciler) ensureNUTServerConfigMap(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string, data map[string]string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labelsForNUTServer(server)
		cm.Data = data
		return controllerutil.SetControllerReference(server, cm, r.Scheme)
	})
	return cm, err
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

func (r *NUTServerReconciler) ensureNUTServerDeployment(ctx context.Context, server *powerv1alpha1.NUTServer, namespace, image, configName string, secretRef powerv1alpha1.NamespacedNameReference, configHash string, devices []powerv1alpha1.UPSDevice) (*appsv1.Deployment, error) {
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
		for _, projection := range upstreamAuthProjections {
			deployment.Spec.Template.Spec.Volumes[0].Projected.Sources = append(
				deployment.Spec.Template.Spec.Volumes[0].Projected.Sources,
				projection,
			)
		}
		deployment.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:            "upsd",
				Image:           image,
				ImagePullPolicy: pullPolicy,
				Ports: []corev1.ContainerPort{
					{
						Name:          nutServerPortName,
						ContainerPort: servicePort(server),
						Protocol:      corev1.ProtocolTCP,
					},
				},
				Resources: server.Spec.Resources,
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
		if server.Spec.TLS.Mode != "" && server.Spec.TLS.Mode != powerv1alpha1.NUTTLSDisabled && server.Spec.TLS.ServerCertificateRef != nil {
			deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "nut-tls",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  server.Spec.TLS.ServerCertificateRef.Name,
						DefaultMode: ptrInt32(0440),
					},
				},
			})
			deployment.Spec.Template.Spec.Containers[0].VolumeMounts = append(deployment.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "nut-tls",
				MountPath: "/etc/nut/tls",
				ReadOnly:  true,
			})
		}
		return controllerutil.SetControllerReference(server, deployment, r.Scheme)
	})
	return deployment, err
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
