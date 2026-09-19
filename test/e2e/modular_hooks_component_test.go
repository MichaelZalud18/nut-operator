//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

func TestModularHookFixture(t *testing.T) {
	var flow power.ShutdownFlow
	if err := yaml.Unmarshal([]byte(logicalMixedFlowManifest("worker", "DryRun", false)), &flow); err != nil {
		t.Fatal(err)
	}
	if len(flow.Spec.Groups) != 10 {
		t.Fatalf("groups=%d", len(flow.Spec.Groups))
	}
	for i, name := range modularHookNames {
		g := flow.Spec.Groups[i]
		if g.Name != name || g.Action != power.ShutdownStepRunHook || g.HookRef.Name != name || len(g.Target.AgentRefs) != 0 || g.Target.NodeSelector != nil {
			t.Fatalf("invalid static external target: %+v", g)
		}
		if len(g.Before) != 1 || g.Before[0] != flow.Spec.Groups[i+1].Name {
			t.Fatalf("missing hook order: %+v", g)
		}
	}
	var list corev1.List
	if err := json.Unmarshal([]byte(modularReceiverManifest("survivor")), &list); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := json.Unmarshal(list.Items[0].Raw, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.NodeSelector["kubernetes.io/hostname"] != "survivor" || *pod.Spec.AutomountServiceAccountToken || pod.Spec.HostPID || pod.Spec.Containers[0].Image != snmpsimFixtureImage || !*pod.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem {
		t.Fatal("receiver isolation changed")
	}
	var policy networkingv1.NetworkPolicy
	if err := json.Unmarshal(list.Items[2].Raw, &policy); err != nil {
		t.Fatal(err)
	}
	peer := policy.Spec.Ingress[0].From[0]
	if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != namespace || peer.PodSelector.MatchLabels["control-plane"] != "controller-manager" {
		t.Fatal("receiver ingress is not restricted to the manager")
	}
	for i, raw := range list.Items[5:] {
		var hook power.ShutdownHook
		if err := json.Unmarshal(raw.Raw, &hook); err != nil {
			t.Fatal(err)
		}
		if hook.Name != modularHookNames[i] || hook.Spec.Invocation.HTTP.Data["host"] != "external-a" || (hook.Spec.DryRun != nil) != (i == 0) {
			t.Fatalf("wrong hook configuration: %+v", hook)
		}
	}
}
