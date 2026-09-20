// Package fixture creates disposable Kubernetes fixtures with retained identities.
// It does not infer cluster ownership or adopt resources by name.
package fixture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const ownerLabel = "test.power.zalud.io/fixture-owner"

// Namespace retains the server-assigned UID and random per-creation owner token.
// Construct it only with CreateNamespace; zero-value handles cannot mutate a cluster.
type Namespace struct {
	client       kubernetes.Interface
	cluster, uid types.UID
	name, owner  string
	timeout      time.Duration
}

func (n *Namespace) Name() string {
	if n == nil {
		return ""
	}
	return n.name
}

// CreateNamespace checks a trusted, previously recorded kube-system UID before
// creating a generated-name namespace. An ambiguous create error is not retried or
// followed by name-based adoption/deletion. Callers must retain diagnostics then.
// A nonnil handle returned with a context error still owns the confirmed object.
func CreateNamespace(parent context.Context, client kubernetes.Interface, cluster types.UID, timeout time.Duration) (*Namespace, error) {
	if client == nil || cluster == "" || timeout <= 0 {
		return nil, fmt.Errorf("explicit client, trusted cluster UID and positive timeout required")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := kube.CheckIdentity(ctx, client, cluster, timeout); err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	owner := hex.EncodeToString(token[:])
	ns, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		GenerateName: "vm-fixture-", Labels: map[string]string{ownerLabel: owner},
	}}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if ns.UID == "" || !strings.HasPrefix(ns.Name, "vm-fixture-") || ns.Labels[ownerLabel] != owner {
		return nil, fmt.Errorf("namespace creation did not return a verifiable owned identity")
	}
	return &Namespace{client: client, cluster: cluster, uid: ns.UID, name: ns.Name, owner: owner, timeout: timeout}, ctx.Err()
}

// Check revalidates cluster and namespace identity before a caller's fixture work.
// This observation is not a transaction with subsequent caller-owned API writes.
func (n *Namespace) Check(parent context.Context) error {
	if err := n.valid(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, n.timeout)
	defer cancel()
	if err := kube.CheckIdentity(ctx, n.client, n.cluster, n.timeout); err != nil {
		return err
	}
	_, err := n.current(ctx)
	return err
}

// Delete rechecks identity, issues a UID/resourceVersion-preconditioned deletion,
// then observes disappearance within the same budget. It never removes finalizers.
// A timeout retains the handle for retry; a replacement namespace is never deleted.
// Context cancellation is honored; lifecycle owners supply an independent cleanup ctx.
func (n *Namespace) Delete(parent context.Context) error {
	if err := n.valid(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, n.timeout)
	defer cancel()
	if err := kube.CheckIdentity(ctx, n.client, n.cluster, n.timeout); err != nil {
		return err
	}
	ns, err := n.current(ctx)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if ns.ResourceVersion == "" {
		return fmt.Errorf("namespace resource version required for deletion")
	}
	if ns.DeletionTimestamp == nil {
		err = n.client.CoreV1().Namespaces().Delete(ctx, n.name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &n.uid, ResourceVersion: &ns.ResourceVersion}})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := n.current(ctx)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (n *Namespace) valid() error {
	if n == nil || n.client == nil || n.cluster == "" || n.uid == "" || n.name == "" || n.owner == "" || n.timeout <= 0 {
		return fmt.Errorf("verified namespace handle required")
	}
	return nil
}
func (n *Namespace) current(ctx context.Context) (*corev1.Namespace, error) {
	ns, err := n.client.CoreV1().Namespaces().Get(ctx, n.name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if ns.UID != n.uid || ns.Labels[ownerLabel] != n.owner {
		return nil, fmt.Errorf("namespace ownership changed")
	}
	return ns, ctx.Err()
}
