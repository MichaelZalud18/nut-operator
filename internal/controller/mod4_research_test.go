package controller

import (
	"context"
	"strings"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// This is a dependency probe, not an installation or manager-startup test.
// Unrelated power kinds are deliberately absent from the scheme.
func TestMOD4NUTServerRendersWithOnlyDeviceAndServerKinds(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypes(power.GroupVersion, &power.UPSDevice{}, &power.UPSDeviceList{}, &power.NUTServer{}, &power.NUTServerList{})
	metav1.AddToGroupVersion(scheme, power.GroupVersion)
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, policyv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	device := &power.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: "research-ups"}, Spec: power.UPSDeviceSpec{Driver: "dummy-ups", DriverOptions: map[string]string{"desc": "research"}}}
	server := &power.NUTServer{ObjectMeta: metav1.ObjectMeta{Name: "research", UID: "research-server"}, Spec: power.NUTServerSpec{
		Namespace: "research-power", DeviceRefs: []power.ObjectNameReference{{Name: device.Name}}, Replicas: ptr.To(int32(1)),
		TLS:          power.NUTTLSSpec{Mode: power.NUTTLSDisabled},
		ClientAccess: []power.NUTClientPeer{{NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"nut-client": "allowed"}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "consumer"}}}},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(server).WithObjects(device, server).Build()
	r := NUTServerReconciler{Client: c, Scheme: scheme, NUTOnly: true, DefaultImage: "example.com/nut-server:research"}
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: server.Name}}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, types.NamespacedName{Name: server.Name}, server); err != nil {
			t.Fatal(err)
		}
		if strings.EqualFold(string(server.Status.Phase), "Error") {
			t.Fatalf("render failed: %+v", server.Status)
		}
		var deployments appsv1.DeploymentList
		if err := c.List(ctx, &deployments); err != nil {
			t.Fatal(err)
		}
		if len(deployments.Items) != 1 {
			t.Fatalf("deployments = %d, status = %+v", len(deployments.Items), server.Status)
		}
		containers := deployments.Items[0].Spec.Template.Spec.Containers
		if len(containers) != 2 {
			t.Fatalf("expected upsd and supervisor, got %+v", containers)
		}
		if containers[0].Image != r.DefaultImage || containers[1].Image != r.DefaultImage {
			t.Fatal("profile operand default not applied")
		}
		var policy networkingv1.NetworkPolicy
		if err := c.Get(ctx, types.NamespacedName{Name: networkPolicyName(server), Namespace: "research-power"}, &policy); err != nil {
			t.Fatal(err)
		}
		peers := policy.Spec.Ingress[0].From
		if len(peers) != 3 || peers[2].NamespaceSelector.MatchLabels["nut-client"] != "allowed" || peers[2].PodSelector.MatchLabels["app"] != "consumer" {
			t.Fatal("explicit AND client selector missing")
		}
		var configs corev1.SecretList
		if err := c.List(ctx, &configs); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, config := range configs.Items {
			if strings.Contains(string(config.Data["ups.conf"]), "[research-ups]") {
				found = true
			}
		}
		if !found {
			t.Fatal("device spec without reconciled status did not render")
		}
	}
	server.Spec.ManagementClusterRef = &power.ObjectNameReference{Name: "forbidden"}
	if _, err := r.reconcileNUTServerOperands(ctx, server); err == nil || !strings.Contains(err.Error(), "unavailable in the nut-only profile") {
		t.Fatalf("omitted API reference must fail without access: %v", err)
	}
}
