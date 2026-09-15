package controller

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// retireOperandFinalizer migrates both live and terminating objects. Only our
// legacy key is removed; optimistic locking protects concurrent finalizer/spec
// updates and replacement objects. Owned resources use Kubernetes garbage collection.
func retireOperandFinalizer(ctx context.Context, c client.Client, obj client.Object, finalizer string) (ctrl.Result, error) {
	base := obj.DeepCopyObject().(client.Object)
	controllerutil.RemoveFinalizer(obj, finalizer)
	if err := c.Patch(ctx, obj, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	// Render only after another read, never from the pre-migration snapshot.
	return ctrl.Result{RequeueAfter: time.Second}, nil
}
