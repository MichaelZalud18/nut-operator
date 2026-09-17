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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	defaultOperandNamespace = "power-system"
	nutServerPortName       = "upsd"

	// Timing for the readiness probe's upsdrvctl status flag check.
	upsdReadinessInitialDelaySeconds = 5
	upsdReadinessPeriodSeconds       = 10
	upsdReadinessTimeoutSeconds      = 5
	upsdReadinessFailureThreshold    = 3

	// nutServerUpsdContainerName is the container that serves the NUT protocol. It is referenced by
	// name rather than by index because the pod holds more than one container.
	nutServerUpsdContainerName = "upsd"

	// driverSupervisorContainerName owns NUT driver processes. It is a sidecar rather than
	// one container per driver so that adding or removing a UPSDevice does not force a pod recreate
	// and drop every existing upsmon session, but inside that stable sidecar each configured
	// UPS still gets its own foreground upsdrvctl worker. That mirrors NUT's service-instance model
	// without pretending Kubernetes can add containers to a running pod.
	driverSupervisorContainerName = "driver-supervisor"

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
	// The pod-template annotation carries only config upsd cannot adopt on reload, preserving
	// upsmon sessions and NUT login accounting for reloadable changes.
	// `upsd -c reload` registers devices added to ups.conf and re-reads upsd.users, but silently
	// ignores a changed LISTEN address or port. That keeps upsd.conf on the restart path.
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
		// No Hash here: this Secret can carry
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
				// Published so agents can monitor the server without cluster DNS. Empty
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
