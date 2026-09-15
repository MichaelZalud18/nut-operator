package v1alpha1

import (
	"context"
	"encoding/json"
	"slices"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Preserve exact retirement requests so defaults cannot turn them into spec updates.
// oldObj must be an empty object of the same kind as newObj.
func legacyOperandFinalizerRemovalRequest(ctx context.Context, oldObj, newObj client.Object, finalizer string) bool {
	req, err := admission.RequestFromContext(ctx)
	if err != nil || req.Operation != admissionv1.Update {
		return false
	}
	if err := json.Unmarshal(req.OldObject.Raw, oldObj); err != nil {
		return false
	}
	return legacyOperandFinalizerRemoval(oldObj, newObj, finalizer)
}

// legacyOperandFinalizerRemoval permits retirement even when an old spec no
// longer passes admission. It grants no permission to alter spec, status,
// approvals, ownership, or another controller's finalizers.
func legacyOperandFinalizerRemoval(oldObj, newObj client.Object, finalizer string) bool {
	if !slices.Contains(oldObj.GetFinalizers(), finalizer) {
		return false
	}
	want := slices.DeleteFunc(slices.Clone(oldObj.GetFinalizers()), func(value string) bool { return value == finalizer })
	if !slices.Equal(want, newObj.GetFinalizers()) {
		return false
	}
	oldCopy := oldObj.DeepCopyObject().(client.Object)
	newCopy := newObj.DeepCopyObject().(client.Object)
	oldCopy.SetFinalizers(newCopy.GetFinalizers())
	// Managed fields are API-server bookkeeping, not caller-controlled intent.
	oldCopy.SetManagedFields(nil)
	newCopy.SetManagedFields(nil)
	return equality.Semantic.DeepEqual(oldCopy, newCopy)
}
