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
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// ShutdownFlowReconciler reconciles a ShutdownFlow object
type ShutdownFlowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=powerinfrastructures;powerinventoryedges;powerinventorynodes;upscapabilityprofiles;upsdevices,verbs=get;list;watch

// Reconcile validates shutdown flow safety and records compiled plan status
// against the current declarative inventory and UPS capability profile bundle.
func (r *ShutdownFlowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var flow powerv1alpha1.ShutdownFlow
	if err := r.Get(ctx, req.NamespacedName, &flow); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result := validateShutdownFlow(&flow)
	var bundle resolver.StructuralBundle
	var resolverDiagnostics []resolver.Diagnostic
	if result.accepted {
		resolvedBundle, diagnostics, err := resolveDeclarativeStructuralBundle(ctx, r.Client)
		resolverDiagnostics = diagnostics
		if err != nil {
			if errors.Is(err, resolver.ErrRejected) {
				result = validationResultFromResolverDiagnostics(diagnostics)
			} else {
				return ctrl.Result{}, err
			}
		} else {
			bundle = resolvedBundle
		}
	}

	flow.Status.ObservedGeneration = flow.Generation
	flow.Status.LastEvaluationTime = ptrNow()
	var compiled []powerv1alpha1.CompiledShutdownStep
	var compiledWaves []powerv1alpha1.CompiledShutdownWave
	var estimatedDuration *metav1.Duration
	var configHash string
	if result.accepted {
		compiled, compiledWaves, estimatedDuration, configHash = compileShutdownFlowWithResolvedInputs(&flow, bundle)
		if configHash == "" {
			result = rejected("PlannerFailed", "shutdown flow planner failed after resolver inputs were attached")
		}
	}
	if result.accepted {
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseCompiled
		flow.Status.CompiledSteps = compiled
		flow.Status.CompiledWaves = compiledWaves
		flow.Status.EstimatedDuration = estimatedDuration
		flow.Status.ConfigHash = configHash
		flow.Status.ResolvedInputHash = bundle.Hash
		flow.Status.TopologyHash = bundle.Topology.Hash
		flow.Status.InventoryEntityCount = int32(len(bundle.Topology.Entities))
		flow.Status.InventoryEdgeCount = int32(len(bundle.Topology.Edges))
		flow.Status.CapabilityMatchCount = int32(len(bundle.CapabilityMatches))
	} else {
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseError
		flow.Status.CompiledSteps = nil
		flow.Status.CompiledWaves = nil
		flow.Status.EstimatedDuration = nil
		flow.Status.ConfigHash = ""
		flow.Status.ResolvedInputHash = ""
		flow.Status.TopologyHash = ""
		flow.Status.InventoryEntityCount = 0
		flow.Status.InventoryEdgeCount = 0
		flow.Status.CapabilityMatchCount = 0
	}
	setAcceptedCondition(&flow.Status.Conditions, flow.Generation, result)
	degraded := !result.accepted
	degradedReason := result.reason
	degradedMessage := result.message
	readyMessage := "shutdown flow compiled for dry-run review"
	if result.accepted {
		if warning := firstResolverDiagnostic(resolverDiagnostics, resolver.DiagnosticWarning); warning != nil {
			degraded = true
			degradedReason = warning.Reason
			degradedMessage = warning.Message
			readyMessage = "shutdown flow compiled with resolver warnings"
		} else {
			degradedReason = "NotDegraded"
			degradedMessage = "shutdown flow compiled without resolver warnings"
		}
	}
	setReadyCondition(
		&flow.Status.Conditions,
		flow.Generation,
		result.accepted,
		"Compiled",
		readyMessage,
	)
	setDegradedCondition(&flow.Status.Conditions, flow.Generation, degraded, degradedReason, degradedMessage)

	if err := r.Status().Update(ctx, &flow); err != nil {
		log.Error(err, "failed to update ShutdownFlow status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func ptrNow() *metav1.Time {
	now := metav1.Now()
	return &now
}

// SetupWithManager sets up the controller with the Manager.
func (r *ShutdownFlowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	specChanged := builder.WithPredicates(predicate.GenerationChangedPredicate{})
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.ShutdownFlow{}, specChanged).
		Watches(&powerv1alpha1.UPSDevice{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.PowerInfrastructure{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.PowerInventoryNode{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.PowerInventoryEdge{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.UPSCapabilityProfile{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Named("shutdownflow").
		Complete(r)
}

func (r *ShutdownFlowReconciler) shutdownFlowRequestsForInventoryChange(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	var flows powerv1alpha1.ShutdownFlowList
	if err := r.List(ctx, &flows); err != nil {
		log.Error(err, "Failed to list ShutdownFlow resources after declarative inventory change", "kind", obj.GetObjectKind().GroupVersionKind().Kind, "name", obj.GetName())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(flows.Items))
	for _, flow := range flows.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: flow.Name},
		})
	}
	return requests
}

func validationResultFromResolverDiagnostics(diagnostics []resolver.Diagnostic) validationResult {
	if diagnostic := firstResolverDiagnostic(diagnostics, resolver.DiagnosticError); diagnostic != nil {
		return rejected(diagnostic.Reason, "%s", diagnostic.Message)
	}
	return rejected("ResolverRejected", "declarative inventory resolver rejected structural inputs")
}

func firstResolverDiagnostic(diagnostics []resolver.Diagnostic, severity string) *resolver.Diagnostic {
	for i := range diagnostics {
		if diagnostics[i].Severity == severity {
			return &diagnostics[i]
		}
	}
	return nil
}
