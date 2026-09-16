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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

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
