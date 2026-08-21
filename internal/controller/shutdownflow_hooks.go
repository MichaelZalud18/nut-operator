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
	"net/url"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

const defaultShutdownHookTimeout = 10 * time.Second

func (r *ShutdownFlowReconciler) shutdownFlowHookDigests(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, cluster *powerv1alpha1.PowerManagementCluster) ([]planner.HookDigest, []powerv1alpha1.ShutdownFlowDiagnosticStatus, error) {
	refs := shutdownFlowHookRefs(flow)
	if len(refs) == 0 {
		return nil, nil, nil
	}
	digests := make([]planner.HookDigest, 0, len(refs))
	var diagnostics []powerv1alpha1.ShutdownFlowDiagnosticStatus
	for _, ref := range refs {
		var hook powerv1alpha1.ShutdownHook
		key := types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}
		if err := r.Get(ctx, key, &hook); err != nil {
			return nil, nil, fmt.Errorf("get ShutdownHook %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		diagnostics = append(diagnostics, disallowedHookEndpointDiagnostics(&hook, ref, cluster)...)
		digests = append(digests, planner.HookDigest{
			Namespace: ref.Namespace,
			Name:      ref.Name,
			Hash:      stableHookSpecHash(hook.Spec),
		})
	}
	return digests, diagnostics, nil
}

// disallowedHookEndpointDiagnostics reports hook URLs the cluster allowlist does not permit.
//
// F-113: the allowlist was enforced at delivery time only, so a flow referencing a hook pointing
// somewhere `spec.hooks.allowedEndpoints` does not cover compiled clean and discovered the problem
// mid-outage — which is the one moment `HK-9` exists to have settled in Git beforehand. Editing a
// host out of the allowlist had the same effect on every hook still pointing at it, silently.
//
// Warning, not rejection. `OD-34` makes hooks advisory and `HK-7` says a hook never holds a wave, so
// a hook that cannot deliver must not stop a shutdown plan from existing. It degrades the flow and
// says why, which is the difference between finding this while reading `kubectl` output and finding
// it while the battery drains.
func disallowedHookEndpointDiagnostics(hook *powerv1alpha1.ShutdownHook, ref powerv1alpha1.NamespacedNameReference, cluster *powerv1alpha1.PowerManagementCluster) []powerv1alpha1.ShutdownFlowDiagnosticStatus {
	if cluster == nil {
		return nil
	}
	allowed := cluster.Spec.Hooks.AllowedEndpoints
	subject := fmt.Sprintf("ShutdownHook/%s/%s", ref.Namespace, ref.Name)

	var diagnostics []powerv1alpha1.ShutdownFlowDiagnosticStatus
	for label, invocation := range map[string]*powerv1alpha1.ShutdownHookInvocationSpec{
		"invocation": &hook.Spec.Invocation,
		"dryRun":     hook.Spec.DryRun,
	} {
		if invocation == nil || invocation.HTTP == nil || invocation.HTTP.URL == "" {
			continue
		}
		parsed, err := url.Parse(invocation.HTTP.URL)
		if err != nil {
			diagnostics = append(diagnostics, powerv1alpha1.ShutdownFlowDiagnosticStatus{
				Severity: resolver.DiagnosticWarning,
				Source:   "Hook",
				Reason:   "HookURLUnparseable",
				Subject:  subject,
				Message:  fmt.Sprintf("spec.%s.http.url could not be parsed: %v", label, err),
			})
			continue
		}
		if kubeactions.HookURLAllowed(parsed, allowed) {
			continue
		}
		diagnostics = append(diagnostics, powerv1alpha1.ShutdownFlowDiagnosticStatus{
			Severity: resolver.DiagnosticWarning,
			Source:   "Hook",
			Reason:   "HookEndpointNotAllowed",
			Subject:  subject,
			Message: fmt.Sprintf(
				"spec.%s.http.url %q is not permitted by PowerManagementCluster %q spec.hooks.allowedEndpoints, "+
					"so this hook will fail at delivery during a shutdown",
				label, invocation.HTTP.URL, cluster.Name,
			),
		})
	}
	// Map iteration is unordered and this reaches published status, so a stable order keeps the
	// same configuration from reporting two different diagnostic lists between reconciles.
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].Reason != diagnostics[j].Reason {
			return diagnostics[i].Reason < diagnostics[j].Reason
		}
		return diagnostics[i].Message < diagnostics[j].Message
	})
	return diagnostics
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
