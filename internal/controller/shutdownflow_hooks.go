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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

const defaultShutdownHookTimeout = 10 * time.Second

func (r *ShutdownFlowReconciler) shutdownFlowHookDigests(ctx context.Context, flow *powerv1alpha1.ShutdownFlow) ([]planner.HookDigest, error) {
	refs := shutdownFlowHookRefs(flow)
	if len(refs) == 0 {
		return nil, nil
	}
	digests := make([]planner.HookDigest, 0, len(refs))
	for _, ref := range refs {
		var hook powerv1alpha1.ShutdownHook
		key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
		if err := r.Get(ctx, key, &hook); err != nil {
			return nil, fmt.Errorf("get ShutdownHook %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		digests = append(digests, planner.HookDigest{
			Namespace: ref.Namespace,
			Name:      ref.Name,
			Hash:      stableHookSpecHash(hook.Spec),
		})
	}
	return digests, nil
}

func shutdownFlowHookRefs(flow *powerv1alpha1.ShutdownFlow) []powerv1alpha1.NamespacedNameReference {
	if flow == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var refs []powerv1alpha1.NamespacedNameReference
	add := func(action powerv1alpha1.ShutdownStepType, ref *powerv1alpha1.NamespacedNameReference) {
		if action != powerv1alpha1.ShutdownStepRunHook || ref == nil || ref.Namespace == "" || ref.Name == "" {
			return
		}
		key := ref.Namespace + "/" + ref.Name
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		refs = append(refs, *ref)
	}
	for i := range flow.Spec.Groups {
		group := flow.Spec.Groups[i]
		add(group.Action, group.HookRef)
	}
	for i := range flow.Spec.Steps {
		step := flow.Spec.Steps[i]
		add(step.Type, step.HookRef)
	}
	sort.SliceStable(refs, func(left, right int) bool {
		if refs[left].Namespace == refs[right].Namespace {
			return refs[left].Name < refs[right].Name
		}
		return refs[left].Namespace < refs[right].Namespace
	})
	return refs
}

func stableHookSpecHash(spec powerv1alpha1.ShutdownHookSpec) string {
	encoded, err := json.Marshal(spec)
	if err != nil {
		panic(fmt.Sprintf("ShutdownHook spec could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (r *ShutdownFlowReconciler) shutdownHookTimeout(ctx context.Context, ref *powerv1alpha1.NamespacedNameReference, cluster *powerv1alpha1.PowerManagementCluster, dryRun bool) (time.Duration, error) {
	if ref == nil {
		return 0, nil
	}
	var hook powerv1alpha1.ShutdownHook
	if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &hook); err != nil {
		return 0, fmt.Errorf("get ShutdownHook %s/%s for timeout: %w", ref.Namespace, ref.Name, err)
	}
	invocation := hook.Spec.Invocation
	if dryRun && hook.Spec.DryRun != nil {
		invocation = *hook.Spec.DryRun
	}
	if invocation.Timeout != nil {
		return invocation.Timeout.Duration, nil
	}
	if cluster != nil && cluster.Spec.Hooks.DefaultTimeout != nil {
		return cluster.Spec.Hooks.DefaultTimeout.Duration, nil
	}
	return defaultShutdownHookTimeout, nil
}

func (r *ShutdownFlowReconciler) shutdownFlowRequestsForHookChange(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	var flows powerv1alpha1.ShutdownFlowList
	if err := r.List(ctx, &flows); err != nil {
		log.Error(err, "Failed to list ShutdownFlow resources after ShutdownHook change", "name", obj.GetName(), "namespace", obj.GetNamespace())
		return nil
	}

	var requests []reconcile.Request
	for _, flow := range flows.Items {
		for _, ref := range shutdownFlowHookRefs(&flow) {
			if ref.Namespace == obj.GetNamespace() && ref.Name == obj.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: flow.Name}})
				break
			}
		}
	}
	return requests
}
