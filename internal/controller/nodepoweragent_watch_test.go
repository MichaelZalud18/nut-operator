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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func watchTestNodePowerAgent(name string, talosSecret powerv1alpha1.SecretKeyReference) *powerv1alpha1.NodePowerAgent {
	return &powerv1alpha1.NodePowerAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: powerv1alpha1.NodePowerAgentSpec{
			NUTServerRefs: []powerv1alpha1.ObjectNameReference{{Name: "rack-a"}},
			Shutdown: powerv1alpha1.AgentShutdownSpec{
				ActuatorPolicy: powerv1alpha1.ActuatorPolicyTalosShutdown,
				Talos: &powerv1alpha1.TalosShutdownSpec{
					TalosConfigSecretKeyRef: talosSecret,
					Endpoints:               []string{"192.0.2.10"},
				},
			},
		},
	}
}

func TestNodePowerAgentRequestsForSecretFollowsTalosConfigReference(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "talosconfig", Namespace: "power-system"}}
	referencing := watchTestNodePowerAgent("talos-agent", powerv1alpha1.SecretKeyReference{
		Namespace: "power-system",
		Name:      "talosconfig",
		Key:       "config",
	})
	unrelated := watchTestNodePowerAgent("other-agent", powerv1alpha1.SecretKeyReference{
		Namespace: "power-system",
		Name:      "other-talosconfig",
		Key:       "config",
	})

	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(nutServerWatchScheme(t)).
			WithObjects(secret, referencing, unrelated).
			Build(),
	}

	assertSameServers(t, reconciler.nodePowerAgentRequestsForSecret(context.Background(), secret),
		[]string{"talos-agent"})
}

func TestNodePowerAgentRequestsForSecretIgnoresSameNamedSecretElsewhere(t *testing.T) {
	agent := watchTestNodePowerAgent("talos-agent", powerv1alpha1.SecretKeyReference{
		Namespace: "power-system",
		Name:      "talosconfig",
		Key:       "config",
	})
	impostor := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "talosconfig", Namespace: "default"}}

	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(nutServerWatchScheme(t)).
			WithObjects(agent, impostor).
			Build(),
	}

	if got := reconciler.nodePowerAgentRequestsForSecret(context.Background(), impostor); len(got) != 0 {
		t.Fatalf("requests = %v, want none for a Secret outside the operand namespace", requestNames(t, got))
	}
}
