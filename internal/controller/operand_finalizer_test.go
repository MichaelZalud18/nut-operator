package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func operandFinalizerCases() []struct {
	name, legacy string
	object       func() client.Object
	reconciler   func(client.Client) reconcile.Reconciler
} {
	return []struct {
		name, legacy string
		object       func() client.Object
		reconciler   func(client.Client) reconcile.Reconciler
	}{
		{"NUTServer", nutServerFinalizer, func() client.Object { return &power.NUTServer{} }, func(c client.Client) reconcile.Reconciler { return &NUTServerReconciler{Client: c, Scheme: c.Scheme()} }},
		{"NodePowerAgent", nodePowerAgentFinalizer, func() client.Object { return &power.NodePowerAgent{} }, func(c client.Client) reconcile.Reconciler {
			return &NodePowerAgentReconciler{Client: c, Scheme: c.Scheme()}
		}},
	}
}

func TestOperandFinalizerRetirement(t *testing.T) {
	for _, tc := range operandFinalizerCases() {
		for _, legacy := range []bool{false, true} {
			for _, deleting := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/legacy=%t/deleting=%t", tc.name, legacy, deleting), func(t *testing.T) {
					scheme := runtime.NewScheme()
					if err := power.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					obj := tc.object()
					obj.SetName("legacy")
					obj.SetFinalizers([]string{"example.org/foreign-cleanup"})
					if legacy {
						obj.SetFinalizers(append(obj.GetFinalizers(), tc.legacy))
					}
					if deleting {
						now := metav1.Now()
						obj.SetDeletionTimestamp(&now)
					}
					c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
					r := tc.reconciler(c)
					for range 2 {
						if _, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(obj)}); err != nil {
							t.Fatal(err)
						}
					}
					got := tc.object()
					if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), got); err != nil {
						t.Fatal(err)
					}
					if len(got.GetFinalizers()) != 1 || got.GetFinalizers()[0] != "example.org/foreign-cleanup" {
						t.Fatalf("finalizers=%v; want only foreign finalizer", got.GetFinalizers())
					}
					if !deleting {
						got.SetFinalizers(nil)
						if err := c.Update(context.Background(), got); err != nil {
							t.Fatal(err)
						}
						if err := c.Delete(context.Background(), got); err != nil {
							t.Fatal(err)
						}
						if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), tc.object()); !apierrors.IsNotFound(err) {
							t.Fatalf("deletion without a subsequent reconcile: %v", err)
						}
					}
				})
			}
		}
	}
}

type finalizerPatchClient struct {
	client.Client
	beforePatch func(context.Context, client.Object) error
}

func (c finalizerPatchClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.beforePatch(ctx, obj); err != nil {
		return err
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

func TestOperandFinalizerRetirementConflictsPreserveConcurrentChanges(t *testing.T) {
	for _, tc := range operandFinalizerCases() {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := power.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			obj := tc.object()
			obj.SetName("legacy")
			obj.SetFinalizers([]string{tc.legacy})
			base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
			once := true
			c := finalizerPatchClient{Client: base, beforePatch: func(ctx context.Context, _ client.Object) error {
				if !once {
					return nil
				}
				once = false
				current := tc.object()
				if err := base.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
					return err
				}
				current.SetFinalizers(append(current.GetFinalizers(), "example.org/concurrent"))
				current.SetLabels(map[string]string{"concurrent": "retained"})
				return base.Update(ctx, current)
			}}
			r := tc.reconciler(c)
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
			if _, err := r.Reconcile(context.Background(), request); !apierrors.IsConflict(err) {
				t.Fatalf("stale patch should conflict: %v", err)
			}
			if _, err := r.Reconcile(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			got := tc.object()
			if err := base.Get(context.Background(), request.NamespacedName, got); err != nil {
				t.Fatal(err)
			}
			if len(got.GetFinalizers()) != 1 || got.GetFinalizers()[0] != "example.org/concurrent" || got.GetLabels()["concurrent"] != "retained" {
				t.Fatalf("concurrent metadata lost: %+v", got)
			}
		})
	}
}

func TestOperandFinalizerRetirementPatchErrors(t *testing.T) {
	for _, tc := range operandFinalizerCases() {
		for _, cause := range []error{context.Canceled, apierrors.NewForbidden(schema.GroupResource{Group: power.GroupVersion.Group, Resource: tc.name}, "legacy", errors.New("denied")), apierrors.NewNotFound(schema.GroupResource{Resource: tc.name}, "legacy")} {
			t.Run(tc.name+"/"+cause.Error(), func(t *testing.T) {
				scheme := runtime.NewScheme()
				if err := power.AddToScheme(scheme); err != nil {
					t.Fatal(err)
				}
				obj := tc.object()
				obj.SetName("legacy")
				obj.SetFinalizers([]string{tc.legacy})
				base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()
				c := finalizerPatchClient{Client: base, beforePatch: func(context.Context, client.Object) error { return cause }}
				result, err := tc.reconciler(c).Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(obj)})
				if apierrors.IsNotFound(cause) {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, cause) {
					t.Fatalf("error=%v want %v", err, cause)
				}
				if result.RequeueAfter != 0 {
					t.Fatalf("error path scheduled rendering: %+v", result)
				}
				got := tc.object()
				if err := base.Get(context.Background(), client.ObjectKeyFromObject(obj), got); err != nil {
					t.Fatal(err)
				}
				if len(got.GetFinalizers()) != 1 || got.GetFinalizers()[0] != tc.legacy {
					t.Fatal("failed patch changed finalizers")
				}
			})
		}
	}
}
