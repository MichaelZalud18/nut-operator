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
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
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
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	storageconfig "github.com/MichaelZalud18/nut-operator/internal/storage"
)

// ShutdownFlowReconciler reconciles a ShutdownFlow object
type ShutdownFlowReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	StorageConnector storageconfig.AuditStoreConnector
	ExecutorRunner   executorpkg.ActionRunner
	Clock            func() time.Time
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters;powerinfrastructures;powerinventoryedges;powerinventorynodes;upscapabilityprofiles;upsdevices,verbs=get;list;watch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces;nodes;pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;replicasets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=argoproj.io,resources=workflows,verbs=create;get;list;watch

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
	base := flow.DeepCopy()

	observedAt := r.now()
	reconcileResult := ctrl.Result{}
	result := validateShutdownFlow(&flow)
	var bundle resolver.StructuralBundle
	var resolverDiagnostics []resolver.Diagnostic
	var managementCluster *powerv1alpha1.PowerManagementCluster
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
	if result.accepted {
		cluster, err := r.getManagementCluster(ctx, &flow)
		if err != nil {
			if apierrors.IsNotFound(err) {
				result = rejected("ManagementClusterNotFound", "referenced PowerManagementCluster could not be found")
			} else {
				return ctrl.Result{}, err
			}
		}
		managementCluster = cluster
	}

	flow.Status.ObservedGeneration = flow.Generation
	evaluationTime := metav1.NewTime(observedAt)
	flow.Status.LastEvaluationTime = &evaluationTime
	var compiled []powerv1alpha1.CompiledShutdownStep
	var compiledWaves []powerv1alpha1.CompiledShutdownWave
	var estimatedDuration *metav1.Duration
	var configHash string
	var publishedArtifact *powerv1alpha1.PublishedPlannerArtifactStatus
	var triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus
	if result.accepted {
		compileStart := time.Now()
		compiled, compiledWaves, estimatedDuration, configHash, publishedArtifact = compileShutdownFlowWithResolvedInputsAndTierPolicy(&flow, bundle, shutdownFlowTierPolicy(managementCluster))
		metrics.ShutdownFlowCompileDurationSeconds.WithLabelValues(flow.Name).Observe(time.Since(compileStart).Seconds())
		compileResult := "Accepted"
		if configHash == "" {
			result = rejected("PlannerFailed", "shutdown flow planner failed after resolver inputs were attached")
			compileResult = "PlannerFailed"
		}
		metrics.ShutdownFlowCompileTotal.WithLabelValues(flow.Name, compileResult).Inc()
	}
	if result.accepted {
		evaluation, status, holdStates, err := evaluateShutdownFlowTriggers(ctx, r.Client, &flow, observedAt, configHash)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluate ShutdownFlow %q triggers: %w", flow.Name, err)
		}
		triggerEvaluation = status
		metrics.ShutdownFlowTriggerEvaluationsTotal.WithLabelValues(flow.Name, strconv.FormatBool(status.Eligible)).Inc()
		if requeueAfter := triggerRequeueAfter(evaluation, observedAt); requeueAfter > 0 {
			reconcileResult.RequeueAfter = requeueAfter
		}
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseCompiled
		flow.Status.CompiledSteps = compiled
		flow.Status.CompiledWaves = compiledWaves
		flow.Status.PublishedArtifact = publishedArtifact
		flow.Status.EstimatedDuration = estimatedDuration
		if configHash != "" && configHash != base.Status.ConfigHash {
			metrics.ShutdownFlowPlanHashChangesTotal.WithLabelValues(flow.Name).Inc()
		}
		flow.Status.ConfigHash = configHash
		flow.Status.ResolvedInputHash = bundle.Hash
		flow.Status.TopologyHash = bundle.Topology.Hash
		flow.Status.InventoryEntityCount = int32(len(bundle.Topology.Entities))
		flow.Status.InventoryEdgeCount = int32(len(bundle.Topology.Edges))
		flow.Status.CapabilityMatchCount = int32(len(bundle.CapabilityMatches))
		flow.Status.TriggerEvaluation = triggerEvaluation
		flow.Status.TriggerHoldStates = holdStates
	} else {
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseError
		flow.Status.CompiledSteps = nil
		flow.Status.CompiledWaves = nil
		flow.Status.PublishedArtifact = nil
		flow.Status.EstimatedDuration = nil
		flow.Status.ConfigHash = ""
		flow.Status.ResolvedInputHash = ""
		flow.Status.TopologyHash = ""
		flow.Status.InventoryEntityCount = 0
		flow.Status.InventoryEdgeCount = 0
		flow.Status.CapabilityMatchCount = 0
		flow.Status.TriggerEvaluation = nil
		flow.Status.TriggerHoldStates = nil
		deactivateLastExecution(&flow.Status.LastExecution)
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
		} else if diagnostic := firstTriggerDiagnostic(triggerEvaluation, "Error"); diagnostic != nil {
			degraded = true
			degradedReason = diagnostic.Reason
			degradedMessage = diagnostic.Message
			readyMessage = "shutdown flow compiled with trigger evaluation errors"
		} else if diagnostic := firstTriggerDiagnostic(triggerEvaluation, "Warning"); diagnostic != nil {
			degraded = true
			degradedReason = diagnostic.Reason
			degradedMessage = diagnostic.Message
			readyMessage = "shutdown flow compiled with trigger evaluation warnings"
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
	metrics.ShutdownFlowDegraded.WithLabelValues(flow.Name).Set(metrics.BoolToFloat(degraded))
	if triggerEvaluation != nil {
		setTriggerEligibleCondition(
			&flow.Status.Conditions,
			flow.Generation,
			triggerEvaluation.Eligible,
			triggerEvaluation.Reason,
			triggerEligibleMessage(triggerEvaluation),
		)
	} else {
		setTriggerEligibleCondition(
			&flow.Status.Conditions,
			flow.Generation,
			false,
			"FlowNotAccepted",
			"shutdown flow triggers were not evaluated because the flow was not accepted",
		)
	}
	setExecutionReadyCondition(
		&flow.Status.Conditions,
		flow.Generation,
		false,
		"TriggerNotEligible",
		"shutdown flow execution has not started because no trigger is eligible",
	)

	if err := r.recordShutdownFlowAudit(ctx, &flow, result, resolverDiagnostics, bundle, compiledWaves, publishedArtifact, configHash, triggerEvaluation); err != nil {
		log.Error(err, "failed to record ShutdownFlow audit records", "shutdownflow", flow.Name)
	}

	if err := r.Status().Patch(ctx, &flow, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update ShutdownFlow status")
		return ctrl.Result{}, err
	}

	return reconcileResult, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ShutdownFlowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	specChanged := builder.WithPredicates(predicate.GenerationChangedPredicate{})
	flowChanged := builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.AnnotationChangedPredicate{}))
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.ShutdownFlow{}, flowChanged).
		Watches(&powerv1alpha1.UPSDevice{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange)).
		Watches(&powerv1alpha1.PowerManagementCluster{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
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

func (r *ShutdownFlowReconciler) recordShutdownFlowAudit(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, result validationResult, diagnostics []resolver.Diagnostic, bundle resolver.StructuralBundle, compiledWaves []powerv1alpha1.CompiledShutdownWave, publishedArtifact *powerv1alpha1.PublishedPlannerArtifactStatus, configHash string, triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) error {
	if flow == nil {
		return nil
	}
	cluster, err := r.getManagementCluster(ctx, flow)
	if err != nil || cluster == nil || !managementClusterStorageReady(cluster) {
		if result.accepted && triggerEvaluation != nil && triggerEvaluation.Eligible {
			setExecutionReadyCondition(
				&flow.Status.Conditions,
				flow.Generation,
				false,
				"AuditStoreUnavailable",
				"shutdown flow execution requires a ready PostgreSQL audit store",
			)
		}
		return err
	}

	store, err := r.storageConnector().OpenAuditStore(ctx, cluster)
	if err != nil {
		return err
	}
	writer, spoolWriter, err := shutdownAuditWriter(cluster, store)
	if err != nil {
		closeErr := store.Close()
		return errors.Join(err, closeErr)
	}

	observedAt := r.now()
	recordErr := writer.RecordShutdownFlowCompilation(ctx, audit.ShutdownFlowCompilation{
		CompilationID:      uuid.NewString(),
		ObservedAt:         observedAt,
		ShutdownFlow:       flow.Name,
		ResourceGeneration: flow.Generation,
		ConfigHash:         configHash,
		InputHash:          bundle.Hash,
		Accepted:           result.accepted,
		Diagnostics:        auditDiagnosticsForCompilation(result, diagnostics),
		CompiledWaves:      compiledWaves,
		DependencyGraph:    auditDependencyGraph(publishedArtifact),
		StartupWaves:       auditStartupWaves(publishedArtifact),
		Explanations:       auditExplanations(publishedArtifact),
		DiagramExports:     auditDiagramExports(publishedArtifact),
	})
	if result.accepted {
		for _, match := range bundle.CapabilityMatches {
			err := writer.RecordCapabilityProfileMatch(ctx, audit.CapabilityProfileMatch{
				MatchID:        uuid.NewString(),
				ObservedAt:     observedAt,
				UPSDevice:      match.DeviceID,
				ProfileID:      match.ProfileID,
				ProfileVersion: match.ProfileVersion,
				ProfileSource:  string(match.ProfileSource),
				MatchTier:      string(match.Tier),
				Fallback:       match.Fallback,
				Diagnostics:    auditDiagnosticsForCapabilityMatch(diagnostics, match.DeviceID),
			})
			if err != nil {
				recordErr = errors.Join(recordErr, err)
			}
		}
		recordErr = errors.Join(recordErr, recordShutdownFlowDecisions(ctx, writer, flow, observedAt, configHash, triggerEvaluation))
		recordErr = errors.Join(recordErr, r.recordShutdownFlowExecution(ctx, writer, flow, observedAt, bundle.Hash, configHash, triggerEvaluation))
	}
	if spoolWriter != nil {
		stats := spoolWriter.Stats()
		if stats.FallbackWrites > 0 {
			message := fmt.Sprintf("%d audit records were spooled to %s after PostgreSQL audit writes failed", stats.FallbackWrites, stats.LastSpoolPath)
			setDegradedCondition(&flow.Status.Conditions, flow.Generation, true, "AuditSpoolFallback", message)
			if triggerEvaluation != nil && triggerEvaluation.Eligible && flow.Status.LastExecution != nil && flow.Status.LastExecution.TriggerActive {
				setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, true, "AuditSpoolFallback", message)
				flow.Status.LastExecution.Message = message
			}
		}
	}
	closeErr := store.Close()
	if recordErr != nil || closeErr != nil {
		return errors.Join(recordErr, closeErr)
	}
	return nil
}

func shutdownAuditWriter(cluster *powerv1alpha1.PowerManagementCluster, store audit.Store) (audit.Writer, *audit.SpoolWriter, error) {
	if cluster == nil || store == nil {
		return store, nil, nil
	}
	if !cluster.Spec.Storage.AuditSpool.Enabled {
		return store, nil, nil
	}
	backend, err := storageconfig.Resolve(cluster.Spec.Storage)
	if err != nil {
		return nil, nil, err
	}
	writer, err := audit.NewSpoolWriter(store, audit.SpoolOptions{Directory: backend.AuditSpool.Path})
	if err != nil {
		return nil, nil, err
	}
	return writer, writer, nil
}

func (r *ShutdownFlowReconciler) getManagementCluster(ctx context.Context, flow *powerv1alpha1.ShutdownFlow) (*powerv1alpha1.PowerManagementCluster, error) {
	if flow.Spec.ManagementClusterRef == nil || flow.Spec.ManagementClusterRef.Name == "" {
		return nil, nil
	}

	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, types.NamespacedName{Name: flow.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
		return nil, fmt.Errorf("get PowerManagementCluster %q: %w", flow.Spec.ManagementClusterRef.Name, err)
	}
	return &cluster, nil
}

func (r *ShutdownFlowReconciler) storageConnector() storageconfig.AuditStoreConnector {
	if r.StorageConnector != nil {
		return r.StorageConnector
	}
	return storageconfig.NewKubernetesConnector(r.Client, storageconfig.ConnectorOptions{})
}

func (r *ShutdownFlowReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

func managementClusterStorageReady(cluster *powerv1alpha1.PowerManagementCluster) bool {
	if cluster == nil {
		return false
	}
	mode := cluster.Status.Storage.Mode
	if mode == "" {
		mode = storageconfig.EffectiveMode(cluster.Spec.Storage)
	}
	return mode != powerv1alpha1.PowerStorageDisabled && cluster.Status.Storage.Ready
}

func shutdownFlowTierPolicy(cluster *powerv1alpha1.PowerManagementCluster) powerv1alpha1.PowerShutdownTierPolicySpec {
	if cluster == nil {
		return powerv1alpha1.PowerShutdownTierPolicySpec{}
	}
	return cluster.Spec.ShutdownTiers
}

func auditDiagnosticsFromResolver(diagnostics []resolver.Diagnostic) []audit.DiagnosticRecord {
	records := make([]audit.DiagnosticRecord, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		records = append(records, audit.DiagnosticRecord{
			Severity: diagnostic.Severity,
			Source:   diagnostic.Source,
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	return records
}

func auditDiagnosticsForCompilation(result validationResult, diagnostics []resolver.Diagnostic) []audit.DiagnosticRecord {
	records := auditDiagnosticsFromResolver(diagnostics)
	if result.accepted {
		return records
	}
	for _, record := range records {
		if record.Severity == resolver.DiagnosticError && record.Reason == result.reason {
			return records
		}
	}
	return append(records, audit.DiagnosticRecord{
		Severity: resolver.DiagnosticError,
		Source:   "Validation",
		Reason:   result.reason,
		Message:  result.message,
	})
}

func auditDiagnosticsForCapabilityMatch(diagnostics []resolver.Diagnostic, deviceID string) []audit.DiagnosticRecord {
	var filtered []resolver.Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Source == resolver.DiagnosticSourceCapability && diagnostic.Subject == deviceID {
			filtered = append(filtered, diagnostic)
		}
	}
	return auditDiagnosticsFromResolver(filtered)
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

func firstTriggerDiagnostic(evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, severity string) *powerv1alpha1.ShutdownTriggerDiagnosticStatus {
	if evaluation == nil {
		return nil
	}
	for i := range evaluation.Diagnostics {
		if evaluation.Diagnostics[i].Severity == severity {
			return &evaluation.Diagnostics[i]
		}
	}
	return nil
}

func triggerEligibleMessage(evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) string {
	if evaluation == nil {
		return "shutdown flow triggers were not evaluated"
	}
	if evaluation.Eligible {
		return fmt.Sprintf("shutdown flow trigger evaluation is eligible on %d UPS device(s)", len(evaluation.SelectedUPSDevices))
	}
	return fmt.Sprintf("shutdown flow trigger evaluation is not eligible: %s", evaluation.Reason)
}

func auditDependencyGraph(artifact *powerv1alpha1.PublishedPlannerArtifactStatus) any {
	if artifact == nil {
		return powerv1alpha1.PlannerGraphStatus{}
	}
	return artifact.Graph
}

func auditStartupWaves(artifact *powerv1alpha1.PublishedPlannerArtifactStatus) any {
	if artifact == nil {
		return []powerv1alpha1.CompiledShutdownWave{}
	}
	return artifact.StartupWaves
}

func auditExplanations(artifact *powerv1alpha1.PublishedPlannerArtifactStatus) any {
	if artifact == nil {
		return []powerv1alpha1.PlannerExplanationStatus{}
	}
	return artifact.Explanations
}

func auditDiagramExports(artifact *powerv1alpha1.PublishedPlannerArtifactStatus) any {
	if artifact == nil {
		return powerv1alpha1.PlannerDiagramExportsStatus{}
	}
	return artifact.Diagrams
}

func recordShutdownFlowDecisions(ctx context.Context, writer audit.Writer, flow *powerv1alpha1.ShutdownFlow, observedAt time.Time, configHash string, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) error {
	if writer == nil || flow == nil || evaluation == nil {
		return nil
	}
	var recordErr error
	for _, decision := range evaluation.Decisions {
		err := writer.RecordShutdownFlowDecision(ctx, audit.ShutdownFlowDecision{
			DecisionID:         uuid.NewString(),
			ObservedAt:         observedAt,
			ShutdownFlow:       flow.Name,
			TriggerType:        string(decision.Type),
			Mode:               string(evaluation.Mode),
			Approved:           decision.Eligible,
			Decision:           shutdownFlowDecisionName(decision),
			Reason:             decision.Reason,
			SelectedUPSDevices: decision.SelectedUPSDevices,
			PlanConfigHash:     configHash,
			Details:            shutdownFlowDecisionDetails(evaluation, decision),
		})
		if err != nil {
			recordErr = errors.Join(recordErr, err)
		}
	}
	return recordErr
}

func shutdownFlowDecisionName(decision powerv1alpha1.ShutdownTriggerDecisionStatus) string {
	switch {
	case decision.Eligible:
		return "Eligible"
	case decision.Matched:
		return "HoldPending"
	default:
		return "NotMatched"
	}
}

func shutdownFlowDecisionDetails(evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus, decision powerv1alpha1.ShutdownTriggerDecisionStatus) map[string]any {
	details := map[string]any{
		"flowEligible":        evaluation.Eligible,
		"evaluationReason":    evaluation.Reason,
		"matchedTriggerCount": evaluation.MatchedTriggerCount,
	}
	if decision.HoldStartedAt != nil {
		details["holdStartedAt"] = decision.HoldStartedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if decision.EligibleAt != nil {
		details["eligibleAt"] = decision.EligibleAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if len(evaluation.Diagnostics) > 0 {
		details["diagnostics"] = evaluation.Diagnostics
	}
	return details
}
