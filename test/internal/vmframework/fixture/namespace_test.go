package fixture

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func clientFixture() *fake.Clientset {
	client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster"}})
	next := 0
	client.PrependReactor("create", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		ns := a.(ktesting.CreateAction).GetObject().(*corev1.Namespace)
		next++
		ns.Name = fmt.Sprintf("vm-fixture-%d", next)
		ns.UID = types.UID(fmt.Sprint(next))
		ns.ResourceVersion = "1"
		return false, nil, nil
	})
	return client
}

func TestFreshNamespaceAndPreconditionedDeletion(t *testing.T) {
	client := clientFixture()
	first, err := CreateNamespace(context.Background(), client, "cluster", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateNamespace(context.Background(), client, "cluster", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name() == second.Name() || first.owner == second.owner {
		t.Fatal("fixtures shared identity")
	}
	client.PrependReactor("delete", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		opts := a.(ktesting.DeleteAction).GetDeleteOptions()
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != first.uid || opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion != "1" {
			t.Fatal("missing identity/version preconditions")
		}
		return false, nil, nil
	})
	if err := first.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.Check(context.Background()); err != nil {
		t.Fatalf("deleted another fixture: %v", err)
	}
}

func TestIdentityChangesRefuseDeletion(t *testing.T) {
	for _, change := range []string{"cluster", "uid", "owner"} {
		t.Run(change, func(t *testing.T) {
			client := clientFixture()
			n, err := CreateNamespace(context.Background(), client, "cluster", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			name := n.Name()
			if change == "cluster" {
				name = "kube-system"
			}
			ns, err := client.CoreV1().Namespaces().Get(context.Background(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if change == "owner" {
				ns.Labels[ownerLabel] = "foreign"
			} else {
				ns.UID = "replacement"
			}
			if _, err := client.CoreV1().Namespaces().Update(context.Background(), ns, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.ClearActions()
			if err := n.Check(context.Background()); err == nil {
				t.Fatal("identity change accepted")
			}
			if err := n.Delete(context.Background()); err == nil {
				t.Fatal("identity change accepted for delete")
			}
			for _, a := range client.Actions() {
				if a.GetVerb() == "delete" {
					t.Fatal("foreign object deletion attempted")
				}
			}
		})
	}
}

func TestDeleteConflictAndFinalizerTimeout(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			client := clientFixture()
			n, err := CreateNamespace(context.Background(), client, "cluster", 20*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			client.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
				if conflict {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "namespaces"}, n.Name(), errors.New("changed since get"))
				}
				return true, nil, nil // API accepted, but finalizers still hold the object.
			})
			err = n.Delete(context.Background())
			if conflict && !apierrors.IsConflict(err) {
				t.Fatal(err)
			}
			if !conflict && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if _, err := client.CoreV1().Namespaces().Get(context.Background(), n.Name(), metav1.GetOptions{}); err != nil {
				t.Fatalf("state lost: %v", err)
			}
		})
	}
}

func TestWrongClusterAndAmbiguousCreateNeverMutateFurther(t *testing.T) {
	client := clientFixture()
	if _, err := CreateNamespace(context.Background(), client, "wrong", time.Second); err == nil {
		t.Fatal("foreign cluster accepted")
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("foreign cluster mutated")
		}
	}
	failure := errors.New("connection lost after sending create")
	client.PrependReactor("create", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, failure })
	n, err := CreateNamespace(context.Background(), client, "cluster", time.Second)
	if n != nil || !errors.Is(err, failure) {
		t.Fatalf("%+v %v", n, err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "delete" {
			t.Fatal("ambiguous creation led to deletion")
		}
	}
	var zero Namespace
	if err := zero.Delete(context.Background()); err == nil {
		t.Fatal("zero handle accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client.ClearActions()
	if _, err := CreateNamespace(ctx, client, "cluster", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("cancelled create mutated")
		}
	}
}

func TestConfirmedCreateRetainsHandleAfterCancellation(t *testing.T) {
	client := clientFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.PrependReactor("create", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		ns := a.(ktesting.CreateAction).GetObject().(*corev1.Namespace)
		ns.Name = "vm-fixture-cancelled"
		ns.UID = "confirmed"
		ns.ResourceVersion = "1"
		if err := client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("namespaces"), ns, ""); err != nil {
			t.Fatal(err)
		}
		cancel()
		return true, ns, nil
	})
	n, err := CreateNamespace(ctx, client, "cluster", time.Second)
	if n == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("confirmed identity lost: %v %v", n, err)
	}
	if err := n.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReplacementDuringDeletionIsNotDeletedAgain(t *testing.T) {
	client := clientFixture()
	n, err := CreateNamespace(context.Background(), client, "cluster", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	client.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		deletes++
		replacement := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: n.Name(), UID: "replacement", ResourceVersion: "2"}}
		if err := client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("namespaces"), replacement, ""); err != nil {
			t.Fatal(err)
		}
		return true, nil, nil
	})
	if err := n.Delete(context.Background()); err == nil {
		t.Fatal("replacement reported as confirmed deletion")
	}
	if err := n.Delete(context.Background()); err == nil {
		t.Fatal("replacement accepted on retry")
	}
	if deletes != 1 {
		t.Fatalf("replacement deletion attempted: %d deletes", deletes)
	}
}
