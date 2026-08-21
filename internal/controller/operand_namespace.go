/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// operandNamespaceLabels are what this operator stamps on the operand namespace, whether it
// created the namespace or adopted one that was already there.
var operandNamespaceLabels = map[string]string{
	"app.kubernetes.io/managed-by":     "nut-operator",
	"power.zalud.io/operand-namespace": "true",
}

// operandNamespaceCreateAllowed reads spec.operandNamespace.create.
//
// F-104: the field had no reader at all. The webhook defaulted it to true, both operand reconcilers
// created the namespace unconditionally, and the cluster reconciler contained no namespace code --
// so create: false did not suppress creation, create: true did not cause it, and a
// PowerManagementCluster on its own reached Ready with the namespace still absent.
//
// A nil cluster is a standalone NUTServer or NodePowerAgent with no managementClusterRef. There is
// no field to read in that case, so creation stands: it is the webhook's default and the only
// answer under which a standalone operand can converge without an out-of-band namespace.
func operandNamespaceCreateAllowed(cluster *powerv1alpha1.PowerManagementCluster) bool {
	if cluster == nil || cluster.Spec.OperandNamespace == nil || cluster.Spec.OperandNamespace.Create == nil {
		return true
	}
	return *cluster.Spec.OperandNamespace.Create
}

// ensureOperandNamespace brings the operand namespace to the state the spec asks for and returns it
// as it now stands.
//
// The returned object carries whatever labels the namespace already had, including any
// pod-security.kubernetes.io/* set by whoever owns the cluster's admission policy. The mutation
// here adds this operator's own two labels and touches nothing else, which is what lets
// nodePowerAgentPodSecurityConflict read the enforced level without a second API call and without
// any risk of this operator being the thing that changed it.
//
// create: false suppresses creation, not adoption. The user is saying they own the namespace's
// existence -- its quotas, its admission labels, its lifecycle -- not that the operator should
// leave an existing namespace unlabelled and then fail to find its own operands in it.
func ensureOperandNamespace(ctx context.Context, c client.Client, name string, allowCreate bool) (*corev1.Namespace, error) {
	if err := rejectReservedOperandNamespace(name); err != nil {
		return nil, err
	}

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if !allowCreate {
		if err := c.Get(ctx, types.NamespacedName{Name: name}, namespace); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, fmt.Errorf(
					"operand namespace %q does not exist and spec.operandNamespace.create is false: "+
						"create the namespace, or set create to true to let the operator create it",
					name,
				)
			}
			return nil, err
		}
		if !applyOperandNamespaceLabels(namespace) {
			return namespace, nil
		}
		if err := c.Update(ctx, namespace); err != nil {
			return nil, err
		}
		return namespace, nil
	}

	if _, err := controllerutil.CreateOrUpdate(ctx, c, namespace, func() error {
		applyOperandNamespaceLabels(namespace)
		return nil
	}); err != nil {
		return nil, err
	}
	return namespace, nil
}

// applyOperandNamespaceLabels stamps this operator's labels and reports whether anything changed,
// so the create: false path does not issue an Update on every reconcile of a namespace that is
// already correct.
func applyOperandNamespaceLabels(namespace *corev1.Namespace) bool {
	changed := false
	if namespace.Labels == nil {
		namespace.Labels = map[string]string{}
	}
	for key, value := range operandNamespaceLabels {
		if namespace.Labels[key] != value {
			namespace.Labels[key] = value
			changed = true
		}
	}
	return changed
}
