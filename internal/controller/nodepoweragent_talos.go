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
	"net"
	"sort"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
)

type nodePowerAgentTalosConfig struct {
	ConfigSecretName  string
	ConfigSecretKey   string
	Endpoints         []string
	NodeAddressSource powerv1alpha1.TalosNodeAddressSource
	ShutdownTimeout   string
}

func (c *nodePowerAgentTalosConfig) enabled() bool {
	return c != nil
}

func (r *NodePowerAgentReconciler) resolveNodePowerAgentTalosConfig(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string) (*nodePowerAgentTalosConfig, []networkingv1.NetworkPolicyEgressRule, error) {
	if !nodePowerAgentUsesTalosAPI(agent) {
		return nil, nil, nil
	}
	talos := agent.Spec.Shutdown.Talos
	if talos == nil {
		return nil, nil, fmt.Errorf("TalosShutdown actuator rendering requires spec.shutdown.talos")
	}
	ref := talos.TalosConfigSecretKeyRef
	if ref.Namespace != namespace {
		return nil, nil, fmt.Errorf("TalosShutdown talosconfig Secret must be in operand namespace %q, got %q", namespace, ref.Namespace)
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &secret); err != nil {
		return nil, nil, fmt.Errorf("get talosconfig Secret %s/%s for NodePowerAgent %q: %w", ref.Namespace, ref.Name, agent.Name, err)
	}
	if len(secret.Data[ref.Key]) == 0 {
		return nil, nil, fmt.Errorf("talosconfig Secret %s/%s for NodePowerAgent %q requires a non-empty data[%q]", ref.Namespace, ref.Name, agent.Name, ref.Key)
	}

	endpoints, egressRule, err := nodePowerAgentTalosEgressRule(talos.Endpoints)
	if err != nil {
		return nil, nil, err
	}
	if len(endpoints) == 0 {
		return nil, nil, fmt.Errorf("TalosShutdown actuator rendering requires at least one spec.shutdown.talos.endpoints entry")
	}

	nodeAddressSource := talos.NodeAddressSource
	if nodeAddressSource == "" {
		nodeAddressSource = powerv1alpha1.TalosNodeAddressSourceHostIP
	}
	return &nodePowerAgentTalosConfig{
		ConfigSecretName:  ref.Name,
		ConfigSecretKey:   ref.Key,
		Endpoints:         endpoints,
		NodeAddressSource: nodeAddressSource,
		ShutdownTimeout:   durationString(talos.ShutdownTimeout, "30s"),
	}, []networkingv1.NetworkPolicyEgressRule{egressRule}, nil
}

func nodePowerAgentTalosEgressRule(endpoints []string) ([]string, networkingv1.NetworkPolicyEgressRule, error) {
	resolved := make([]string, 0, len(endpoints))
	peers := make([]networkingv1.NetworkPolicyPeer, 0, len(endpoints))
	seen := map[string]struct{}{}
	for _, endpoint := range endpoints {
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}

		cidr, err := talosEndpointCIDR(endpoint)
		if err != nil {
			return nil, networkingv1.NetworkPolicyEgressRule{}, err
		}
		resolved = append(resolved, endpoint)
		peers = append(peers, networkingv1.NetworkPolicyPeer{
			IPBlock: &networkingv1.IPBlock{CIDR: cidr},
		})
	}
	sort.Strings(resolved)
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].IPBlock.CIDR < peers[j].IPBlock.CIDR
	})

	return resolved, networkingv1.NetworkPolicyEgressRule{
		To: peers,
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: ptrProtocol(corev1.ProtocolTCP),
				Port:     ptrIntOrStringFromInt32(nodePowerAgentTalosAPIPort),
			},
		},
	}, nil
}

func talosEndpointCIDR(endpoint string) (string, error) {
	ip := net.ParseIP(endpoint)
	if ip == nil {
		return "", fmt.Errorf("TalosShutdown endpoint %q must be an IP literal so the generated NetworkPolicy can allow only that Talos API endpoint", endpoint)
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return (&net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}).String(), nil
}
