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
	"sort"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

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
		for _, peer := range server.Spec.ClientAccess {
			policy.Spec.Ingress[0].From = append(policy.Spec.Ingress[0].From, networkingv1.NetworkPolicyPeer{
				NamespaceSelector: peer.NamespaceSelector.DeepCopy(),
				PodSelector:       peer.PodSelector.DeepCopy(),
			})
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
