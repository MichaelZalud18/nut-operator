package controller

import (
	"context"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNUTServerDisablesAutomaticAPITokenOnEveryReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{power.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	r := NUTServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	server := &power.NUTServer{ObjectMeta: metav1.ObjectMeta{Name: "token-boundary", UID: "fixture-server"}}
	for _, drift := range []*bool{nil, ptr.To(true)} {
		deployment, err := r.ensureNUTServerDeployment(ctx, server, "fixture", "nut-server:fixture", "config", "drivers",
			power.NamespacedNameReference{Name: "users", Namespace: "fixture"}, "hash", nil)
		if err != nil {
			t.Fatal(err)
		}
		if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken {
			t.Fatal("NUT operand inherits or enables an unnecessary Kubernetes API token")
		}
		deployment.Spec.Template.Spec.AutomountServiceAccountToken = drift
		if err := r.Update(ctx, deployment); err != nil {
			t.Fatal(err)
		}
		deployment, err = r.ensureNUTServerDeployment(ctx, server, "fixture", "nut-server:fixture", "config", "drivers",
			power.NamespacedNameReference{Name: "users", Namespace: "fixture"}, "hash", nil)
		if err != nil {
			t.Fatal(err)
		}
		if deployment.Spec.Template.Spec.AutomountServiceAccountToken == nil || *deployment.Spec.Template.Spec.AutomountServiceAccountToken {
			t.Fatal("reconciliation did not repair automatic API token mount drift")
		}
	}
}
