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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nodePowerAgentRequestsForSecret enqueues NodePowerAgents whose Talos actuator names this Secret.
//
// Owns(&corev1.Secret{}) covers the operator-managed config and signal Secrets, but a talosconfig
// Secret is user-supplied and intentionally has no owner reference back to the agent. Without this
// watch, creating or rotating that Secret would only reach the DaemonSet when an unrelated reconcile
// happened to run.
func (r *NodePowerAgentReconciler) nodePowerAgentRequestsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var agents powerv1alpha1.NodePowerAgentList
	if err := r.List(ctx, &agents); err != nil {
		log.Error(err, "Failed to list NodePowerAgent resources after Secret change", "secret", secret.Name, "namespace", secret.Namespace)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		agent := &agents.Items[i]
		if nodePowerAgentUsesTalosConfigSecret(agent, secret) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: agent.Name}})
		}
	}
	return requests
}

func nodePowerAgentUsesTalosConfigSecret(agent *powerv1alpha1.NodePowerAgent, secret *corev1.Secret) bool {
	if agent.Spec.Shutdown.ActuatorPolicy != powerv1alpha1.ActuatorPolicyTalosShutdown ||
		agent.Spec.Shutdown.Talos == nil {
		return false
	}
	ref := agent.Spec.Shutdown.Talos.TalosConfigSecretKeyRef
	return ref.Namespace == secret.Namespace && ref.Name == secret.Name
}
