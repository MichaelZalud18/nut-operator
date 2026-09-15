package controller

import (
	"os"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func managedOperand(ref power.ManagedResourceStatus) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ref.APIVersion)
	obj.SetKind(ref.Kind)
	obj.SetNamespace(ref.Namespace)
	obj.SetName(ref.Name)
	return obj
}

func assertOperandOwnership(owner client.Object, resources []power.ManagedResourceStatus) {
	Expect(resources).NotTo(BeEmpty())
	for _, ref := range resources {
		obj := managedOperand(ref)
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
		if ref.Kind == "Namespace" {
			Expect(metav1.IsControlledBy(obj, owner)).To(BeFalse(), "shared operand namespace must survive")
		} else {
			Expect(metav1.IsControlledBy(obj, owner)).To(BeTrue(), "%s/%s must be garbage collectible", ref.Kind, ref.Name)
		}
	}
}

func assertOperandGarbageCollection(resources []power.ManagedResourceStatus) {
	// envtest has no garbage collector. The isolated Kind entrypoint runs these
	// same deletion specs against a real controller manager, without our operator.
	if os.Getenv("OPERAND_GC_ACCEPTANCE") != "true" {
		return
	}
	Expect(os.Getenv("USE_EXISTING_CLUSTER")).To(Equal("true"))
	for _, ref := range resources {
		obj := managedOperand(ref)
		if ref.Kind == "Namespace" {
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
			continue
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj))
		}, 30*time.Second, 200*time.Millisecond).Should(BeTrue(), "%s/%s should be garbage collected without operator reconciliation", ref.Kind, ref.Name)
	}
}
