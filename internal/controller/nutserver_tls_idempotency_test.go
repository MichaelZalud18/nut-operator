package controller

import (
	"context"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNUTServerTLSReconcileIsIdempotent(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{power.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	r := NUTServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	server := &power.NUTServer{ObjectMeta: metav1.ObjectMeta{Name: "tls-repeat", UID: "fixture"}, Spec: power.NUTServerSpec{TLS: power.NUTTLSSpec{ServerCertificateRef: &power.NamespacedNameReference{Namespace: "fixture", Name: "tls"}}}}
	var first *corev1.PodTemplateSpec
	for _, mode := range []power.NUTTLSMode{power.NUTTLSRequired, power.NUTTLSRequired, power.NUTTLSDisabled, power.NUTTLSRequired} {
		server.Spec.TLS.Mode = mode
		deployment, err := r.ensureNUTServerDeployment(context.Background(), server, "fixture", "nut:fixture", "config", "drivers", power.NamespacedNameReference{Name: "users", Namespace: "fixture"}, "hash", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if mode == power.NUTTLSDisabled {
			want = 0
		}
		if len(deployment.Spec.Template.Spec.InitContainers) != want {
			t.Fatalf("%s: expected %d init containers, got %d", mode, want, len(deployment.Spec.Template.Spec.InitContainers))
		}
		if mode == power.NUTTLSRequired {
			if first == nil {
				first = deployment.Spec.Template.DeepCopy()
			} else if !apiequality.Semantic.DeepEqual(*first, deployment.Spec.Template) {
				t.Fatal("TLS reconciliation changed the pod template without a spec/material change")
			}
		}
	}
}
