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
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type agentMonitorTarget struct {
	UPSName   string
	ServerDNS string
	Port      int32
	Username  string
	Password  string
	TLSMode   powerv1alpha1.NUTTLSMode
	ServerCA  []byte
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

// nutServerDNSName is the address agents monitor a server at.
//
// The ClusterIP is preferred over the DNS name when the server publishes one (F-71). CoreDNS is an
// ordinary workload inside the flow's own path: when it goes, every agent resolving
// <name>.<ns>.svc.cluster.local loses the ability to reconnect and flips NotReady together, and the
// readiness probe cannot tell that apart from the server being down. A ClusterIP is stable for the
// life of the Service and needs nothing running to resolve.
//
// The DNS name remains the fallback rather than being removed, because a server that has not yet
// published an endpoint still has to be addressable, and a name that resolves later is better than
// no target at all.
func nutServerDNSName(server *powerv1alpha1.NUTServer, namespace string) string {
	for _, endpoint := range server.Status.ServiceEndpoints {
		if endpoint.ClusterIP != "" {
			return endpoint.ClusterIP
		}
	}
	for _, endpoint := range server.Status.ServiceEndpoints {
		if endpoint.DNSName != "" {
			return endpoint.DNSName
		}
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local", server.Name, namespace)
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

// uncoveredInventoryNodes names nodes the power inventory describes that this agent does not select.
//
// The inverse of readiness (F-74). Every other count the agent publishes is computed over nodes
// spec.nodeSelector already matched, so a placement mistake -- a selector that misses a rack, a
// label that was never applied -- produces no unavailable pod and no degraded node. It produces
// silence, and an agent reporting fully ready over a fleet it only partly covers.
//
// Nodes marked powerPlanningExempt are excluded: the inventory has already said they are outside
// the planning model, and reporting them as uncovered would train the reader to ignore this field.
func (r *NodePowerAgentReconciler) uncoveredInventoryNodes(ctx context.Context, selectedNodes []string) ([]string, error) {
	var inventory powerv1alpha1.PowerInventoryNodeList
	if err := r.List(ctx, &inventory); err != nil {
		return nil, fmt.Errorf("list PowerInventoryNode resources: %w", err)
	}

	selected := make(map[string]struct{}, len(selectedNodes))
	for _, node := range selectedNodes {
		selected[node] = struct{}{}
	}

	var uncovered []string
	for i := range inventory.Items {
		record := &inventory.Items[i]
		if record.Spec.PowerPlanningExempt != nil && *record.Spec.PowerPlanningExempt {
			continue
		}
		if _, covered := selected[record.Spec.NodeName]; !covered {
			uncovered = append(uncovered, record.Spec.NodeName)
		}
	}
	sort.Strings(uncovered)
	return uncovered, nil
}
