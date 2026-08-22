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
	"strings"
	"time"

	"github.com/go-logr/logr"
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
	"github.com/MichaelZalud18/nut-operator/internal/planner"
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

	// APIReader bypasses the informer cache for execution-time node clearance (EX-9).
	// A cache a few seconds behind is exactly long enough to miss a pod that just
	// landed on a node about to lose power.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=shutdownflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters;powerinfrastructures;powerinventoryedges;powerinventorynodes;shutdownhooks;upscapabilityprofiles;upsdevices,verbs=get;list;watch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=namespaces;nodes;pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=update;patch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// OD-38: when a PodDisruptionBudget refuses an eviction on an enforcing flow, the drain falls
// back to Delete. Without this verb that fall-back is a 403 and the drain aborts on exactly the
// budget it exists to override, so the grant is what makes OD-38 real rather than a log message.
// +kubebuilder:rbac:groups="",resources=pods,verbs=delete
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets;replicasets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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
		// Runs after resolution because it needs the capability matches: a flow
		// cannot enforce against a UPS nothing is known about.
		result = validateShutdownFlowDeviceIdentification(&flow, bundle)
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
	if result.accepted {
		// Age escalation runs here rather than inside the resolver because the
		// thresholds are cluster policy, and the cluster is only in hand once the
		// flow's reference resolves.
		if aging := resolver.EvaluateSnapshotAge(
			bundle.SnapshotObservedAt,
			observedAt,
			snapshotAgeLevels(managementCluster),
		); aging != nil {
			resolverDiagnostics = append(resolverDiagnostics, *aging)
		}
	}

	flow.Status.ObservedGeneration = flow.Generation
	evaluationTime := metav1.NewTime(observedAt)
	flow.Status.LastEvaluationTime = &evaluationTime
	var compiled []powerv1alpha1.CompiledShutdownStep
	var compiledWaves []powerv1alpha1.CompiledShutdownWave
	var estimatedDuration *metav1.Duration
	var configHash string
	var publishedArtifact *powerv1alpha1.PublishedPlannerArtifactStatus
	var plannerDiagnostics []planner.Diagnostic
	var blockedNodeReleases []powerv1alpha1.BlockedNodeReleaseStatus
	var hookDigests []planner.HookDigest
	var planFeasibility *powerv1alpha1.PlanFeasibilityStatus
	var planEstimate *time.Duration
	var estimateConfidence planner.EstimateConfidence
	var triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus
	var hookDiagnostics []powerv1alpha1.ShutdownFlowDiagnosticStatus
	if result.accepted {
		digests, diagnostics, err := r.shutdownFlowHookDigests(ctx, &flow, managementCluster)
		if err != nil {
			if apierrors.IsNotFound(err) {
				result = rejected("ShutdownHookNotFound", "referenced ShutdownHook could not be found")
			} else {
				return ctrl.Result{}, err
			}
		} else {
			hookDigests = digests
			hookDiagnostics = diagnostics
		}
	}
	if result.accepted {
		compileStart := time.Now()
		// Keyed off the hash already on status: a plan whose identity has not moved finds
		// its own history, and one that has moved finds none and keeps declared timeouts.
		history := r.flowExecutionHistory(ctx, managementCluster, &flow, flow.Status.ConfigHash)
		compiledFlow := compileShutdownFlowWithHistory(&flow, bundle, shutdownFlowTierPolicy(managementCluster), history, hookDigests)
		compiled = compiledFlow.Steps
		compiledWaves = compiledFlow.Waves
		estimatedDuration = compiledFlow.EstimatedDuration
		configHash = compiledFlow.ConfigHash
		publishedArtifact = compiledFlow.Artifact
		plannerDiagnostics = compiledFlow.Diagnostics
		blockedNodeReleases = compiledFlow.BlockedNodeReleases
		planEstimate = bestPlanEstimate(compiledFlow.ObservedDuration, compiledFlow.EstimatedDuration)
		estimateConfidence = compiledFlow.EstimateConfidence
		metrics.ShutdownFlowCompileDurationSeconds.WithLabelValues(flow.Name).Observe(time.Since(compileStart).Seconds())
		if configHash == "" {
			result = reportPlannerRejection(log, flow.Name, plannerDiagnostics)
		}
		metrics.ShutdownFlowCompileTotal.WithLabelValues(flow.Name, plannerCompileMetricResult(configHash, plannerDiagnostics)).Inc()
	}
	if result.accepted {
		evaluation, status, holdStates, err := evaluateShutdownFlowTriggers(ctx, r.Client, &flow, bundle, observedAt, configHash)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluate ShutdownFlow %q triggers: %w", flow.Name, err)
		}
		triggerEvaluation = status
		// Computed here rather than at compile time because it needs the selection this
		// evaluation just made: the warning compares against the devices that would
		// actually power this flow, not the ones a previous reconcile saw.
		planFeasibility = planFeasibilityStatus(planEstimate, r.flowRuntimeObservation(ctx, status, bundle), estimateConfidence)
		metrics.ShutdownFlowTriggerEvaluationsTotal.WithLabelValues(flow.Name, strconv.FormatBool(status.Eligible)).Inc()
		if requeueAfter := triggerRequeueAfter(evaluation, observedAt); requeueAfter > 0 {
			reconcileResult.RequeueAfter = requeueAfter
		}
		flow.Status.Phase = powerv1alpha1.ShutdownFlowPhaseCompiled
		flow.Status.CompiledSteps = compiled
		flow.Status.CompiledWaves = compiledWaves
		flow.Status.PublishedArtifact = publishedArtifact
		flow.Status.BlockedNodeReleases = blockedNodeReleases
		// Published on every compile, zero included, so the series exists before the first inversion
		// rather than appearing only once something is already wrong.
		metrics.ShutdownFlowTierInversions.WithLabelValues(flow.Name).Set(float64(len(blockedNodeReleases)))
		flow.Status.EstimatedDuration = estimatedDuration
		flow.Status.PlanFeasibility = planFeasibility
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
		flow.Status.BlockedNodeReleases = nil
		flow.Status.EstimatedDuration = nil
		flow.Status.PlanFeasibility = nil
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
	// Set on both branches, and outside them: a rejected compile is exactly when the reader needs
	// these, and clearing the rest of status must not clear the explanation for why it was cleared.
	flow.Status.CompileDiagnostics = compileDiagnosticStatuses(resolverDiagnostics, plannerDiagnostics, hookDiagnostics)
	setAcceptedCondition(&flow.Status.Conditions, flow.Generation, result)
	verdict := compileVerdict(result, resolverDiagnostics, plannerDiagnostics, hookDiagnostics, triggerEvaluation)
	degraded, degradedReason, degradedMessage, readyMessage := verdict.degraded, verdict.reason, verdict.message, verdict.readyMessage
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
		triggerNotEligibleReason,
		triggerNotEligibleMessage,
	)

	if err := r.recordShutdownFlowAudit(ctx, &flow, result, resolverDiagnostics, plannerDiagnostics, bundle, compiledWaves, publishedArtifact, configHash, triggerEvaluation); err != nil {
		log.Error(err, "failed to record ShutdownFlow audit records", "shutdownflow", flow.Name)
	}

	// EX-29 cadence: republish on a fixed interval whether or not anything changed, so
	// silence from this flow means nothing is happening rather than that the operator
	// stopped. Taken as the soonest of the two intervals, never the later one -- a
	// trigger hold about to expire must not wait for the next heartbeat.
	publishedAt := metav1.NewTime(r.now())
	flow.Status.LastPublishTime = &publishedAt
	metrics.ShutdownFlowPublishTimestampSeconds.WithLabelValues(flow.Name).Set(float64(publishedAt.Unix()))
	reconcileResult.RequeueAfter = soonestRequeue(
		reconcileResult.RequeueAfter,
		publishCadence(&flow, cadenceParameters(managementCluster)),
	)

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
		// F-42: scoped to the fields trigger evaluation reads. Unpredicated, every telemetry poll
		// re-enqueued every flow.
		Watches(&powerv1alpha1.UPSDevice{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange),
			builder.WithPredicates(upsDeviceTriggerRelevantPredicate())).
		Watches(&powerv1alpha1.PowerManagementCluster{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.PowerInfrastructure{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.PowerInventoryNode{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.PowerInventoryEdge{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForInventoryChange), specChanged).
		Watches(&powerv1alpha1.ShutdownHook{}, handler.EnqueueRequestsFromMapFunc(r.shutdownFlowRequestsForHookChange), specChanged).
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

func (r *ShutdownFlowReconciler) recordShutdownFlowAudit(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, result validationResult, diagnostics []resolver.Diagnostic, plannerDiagnostics []planner.Diagnostic, bundle resolver.StructuralBundle, compiledWaves []powerv1alpha1.CompiledShutdownWave, publishedArtifact *powerv1alpha1.PublishedPlannerArtifactStatus, configHash string, triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) error {
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

	// The store opened, so PostgreSQL is reachable: return anything the spool
	// captured while it was not, before writing this reconcile's own records.
	r.drainAuditSpool(ctx, cluster, store)

	observedAt := r.now()
	recordErr := writer.RecordShutdownFlowCompilation(ctx, audit.ShutdownFlowCompilation{
		CompilationID:      uuid.NewString(),
		ObservedAt:         observedAt,
		ShutdownFlow:       flow.Name,
		ResourceGeneration: flow.Generation,
		ConfigHash:         configHash,
		InputHash:          bundle.Hash,
		Accepted:           result.accepted,
		Diagnostics:        auditDiagnosticsForCompilation(result, diagnostics, plannerDiagnostics),
		CompiledWaves:      compiledWaves,
		DependencyGraph:    auditDependencyGraph(publishedArtifact),
		StartupWaves:       auditStartupWaves(publishedArtifact),
		Explanations:       auditExplanations(publishedArtifact),
		DiagramExports:     auditDiagramExports(publishedArtifact),
	})
	if result.accepted {
		for _, match := range bundle.CapabilityMatches {
			declaredModel, reportedModel := r.deviceIdentityEvidence(ctx, match.DeviceID)
			err := writer.RecordCapabilityProfileMatch(ctx, audit.CapabilityProfileMatch{
				MatchID:        uuid.NewString(),
				ObservedAt:     observedAt,
				UPSDevice:      match.DeviceID,
				ProfileID:      match.ProfileID,
				ProfileVersion: match.ProfileVersion,
				ProfileSource:  string(match.ProfileSource),
				MatchTier:      string(match.Tier),
				// The audit column stays `fallback`: it is persisted
				// PostgreSQL schema, and renaming it for vocabulary alone would
				// mean a migration over existing records for no behavior change.
				Fallback: match.Unidentified,
				// F-101: which profile a device resolved to is only half the record. Without the
				// two model strings beside it, history cannot say whether the profile describes
				// the device that was actually being read.
				DeclaredModel: declaredModel,
				ReportedModel: reportedModel,
				Diagnostics:   auditDiagnosticsForCapabilityMatch(diagnostics, match.DeviceID),
			})
			if err != nil {
				recordErr = errors.Join(recordErr, err)
			}
		}
		recordErr = errors.Join(recordErr, recordShutdownFlowDecisions(ctx, writer, flow, observedAt, configHash, triggerEvaluation))
		recordErr = errors.Join(recordErr, r.recordShutdownFlowExecution(ctx, writer, flow, observedAt, bundle.Hash, configHash, triggerEvaluation, bundle))
	}
	if spoolWriter != nil {
		r.reportAuditSpoolFallback(flow, spoolWriter.Stats(), triggerEvaluation)
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
	writer, err := audit.NewSpoolWriter(store, audit.SpoolOptions{
		Directory: backend.AuditSpool.Path,
		MaxBytes:  backend.AuditSpool.MaxBytes,
	})
	if err != nil {
		return nil, nil, err
	}
	return writer, writer, nil
}

// reportAuditSpoolFallback surfaces what the fallback journal did this
// reconcile.
//
// Two outcomes, deliberately distinct. A spooled record is degraded but intact:
// PostgreSQL refused it and the journal holds it until replay. A dropped record
// is gone -- the journal was at its size cap -- and that is reported under its
// own reason rather than folded into the same condition, because "the audit
// trail is delayed" and "the audit trail has a hole in it" are different
// operator problems.
func (r *ShutdownFlowReconciler) reportAuditSpoolFallback(
	flow *powerv1alpha1.ShutdownFlow,
	stats audit.SpoolStats,
	triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus,
) {
	if stats.FallbackWrites == 0 && stats.DroppedWrites == 0 {
		return
	}
	metrics.AuditSpoolRecordsTotal.WithLabelValues("spooled").Add(float64(stats.FallbackWrites))
	metrics.AuditSpoolRecordsTotal.WithLabelValues("dropped").Add(float64(stats.DroppedWrites))
	metrics.AuditSpoolJournalBytes.Set(float64(stats.JournalBytes))

	reason := "AuditSpoolFallback"
	message := fmt.Sprintf("%d audit records were spooled to %s after PostgreSQL audit writes failed", stats.FallbackWrites, stats.LastSpoolPath)
	switch {
	case stats.DroppedWrites > 0 && stats.FallbackWrites == 0:
		reason = "AuditSpoolFull"
		message = fmt.Sprintf("PostgreSQL audit writes failed and the spool journal at %s is at its size cap, so %d audit records were dropped and are not recorded anywhere",
			stats.LastSpoolPath, stats.DroppedWrites)
	case stats.DroppedWrites > 0:
		reason = "AuditSpoolFull"
		message = fmt.Sprintf("%s; %d further records were dropped because the journal reached its size cap and are not recorded anywhere",
			message, stats.DroppedWrites)
	}

	setDegradedCondition(&flow.Status.Conditions, flow.Generation, true, reason, message)
	if triggerEvaluation != nil && triggerEvaluation.Eligible && flow.Status.LastExecution != nil && flow.Status.LastExecution.TriggerActive {
		setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, true, reason, message)
		flow.Status.LastExecution.Message = message
	}
}

// drainAuditSpool returns spooled records to PostgreSQL once it is reachable
// again, closing the other half of OD-6.
//
// Called on the reconcile path with a healthy store in hand, which is the only
// point the operator reliably knows the database is accepting writes. A drain
// failure is logged, never returned: replay is evidence recovery, and failing a
// power-management reconcile because a historical record could not be re-filed
// would invert the priority the whole spool design exists to protect (SB-11).
func (r *ShutdownFlowReconciler) drainAuditSpool(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster, store audit.Store) {
	if cluster == nil || store == nil || !cluster.Spec.Storage.AuditSpool.Enabled {
		return
	}
	backend, err := storageconfig.Resolve(cluster.Spec.Storage)
	if err != nil {
		return
	}

	log := logf.FromContext(ctx)
	stats, err := audit.ReplaySpool(ctx, store, audit.ReplayOptions{Directory: backend.AuditSpool.Path})
	// Counted before the error check: a partial drain still returned records to
	// PostgreSQL, and those are exactly the ones worth seeing when the drain as
	// a whole did not succeed.
	metrics.AuditSpoolReplayRecordsTotal.WithLabelValues("replayed").Add(float64(stats.Replayed))
	metrics.AuditSpoolReplayRecordsTotal.WithLabelValues("skipped").Add(float64(stats.Skipped))
	if err != nil {
		log.Error(err, "Could not drain audit spool", "replayed", stats.Replayed, "skipped", stats.Skipped)
		return
	}
	if stats.Replayed > 0 || stats.Skipped > 0 {
		log.Info("Drained audit spool", "read", stats.Read, "replayed", stats.Replayed, "skipped", stats.Skipped)
	}
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

// auditDiagnosticsFromPlanner records what the planner found while compiling.
// Source is set explicitly so a reader can tell a structural finding about the
// plan apart from one about the inventory that fed it.
func auditDiagnosticsFromPlanner(diagnostics []planner.Diagnostic) []audit.DiagnosticRecord {
	records := make([]audit.DiagnosticRecord, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		records = append(records, audit.DiagnosticRecord{
			Severity: diagnostic.Severity,
			Source:   "Planner",
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	return records
}

// compileVerdict picks the one finding a compile reports on its Degraded condition.
//
// Ordered most structural first: an inventory problem explains a planning one, and both explain a
// trigger that cannot fire, so reporting the downstream symptom would send the reader to the wrong
// place. Only the first is published as the condition — the full set is on
// status.compileDiagnostics (F-99), which is what makes picking one here safe.
type compileVerdictResult struct {
	degraded     bool
	reason       string
	message      string
	readyMessage string
}

func compileVerdict(
	result validationResult,
	resolverDiagnostics []resolver.Diagnostic,
	plannerDiagnostics []planner.Diagnostic,
	hookDiagnostics []powerv1alpha1.ShutdownFlowDiagnosticStatus,
	triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus,
) compileVerdictResult {
	if !result.accepted {
		return compileVerdictResult{
			degraded:     true,
			reason:       result.reason,
			message:      result.message,
			readyMessage: "shutdown flow compiled for dry-run review",
		}
	}

	if warning := firstResolverDiagnostic(resolverDiagnostics, resolver.DiagnosticWarning); warning != nil {
		return compileVerdictResult{true, warning.Reason, warning.Message, "shutdown flow compiled with resolver warnings"}
	}
	if warning := firstPlannerDiagnostic(plannerDiagnostics, planner.DiagnosticWarning); warning != nil {
		return compileVerdictResult{true, warning.Reason, warning.Message, "shutdown flow compiled with planner warnings"}
	}
	// F-113. A hook that cannot deliver never holds a wave (HK-7), so this degrades rather than
	// rejects -- but it is published before the outage instead of discovered during it.
	if hook := firstDiagnosticStatus(hookDiagnostics, resolver.DiagnosticWarning); hook != nil {
		return compileVerdictResult{true, hook.Reason, hook.Message, "shutdown flow compiled with hook endpoint warnings"}
	}
	if diagnostic := firstTriggerDiagnostic(triggerEvaluation, "Error"); diagnostic != nil {
		return compileVerdictResult{true, diagnostic.Reason, diagnostic.Message, "shutdown flow compiled with trigger evaluation errors"}
	}
	if diagnostic := firstTriggerDiagnostic(triggerEvaluation, "Warning"); diagnostic != nil {
		return compileVerdictResult{true, diagnostic.Reason, diagnostic.Message, "shutdown flow compiled with trigger evaluation warnings"}
	}
	return compileVerdictResult{
		reason:       "NotDegraded",
		message:      "shutdown flow compiled without resolver warnings",
		readyMessage: "shutdown flow compiled for dry-run review",
	}
}

// firstDiagnosticStatus returns the first published diagnostic at a severity.
func firstDiagnosticStatus(diagnostics []powerv1alpha1.ShutdownFlowDiagnosticStatus, severity string) *powerv1alpha1.ShutdownFlowDiagnosticStatus {
	for i := range diagnostics {
		if diagnostics[i].Severity == severity {
			return &diagnostics[i]
		}
	}
	return nil
}

// firstPlannerDiagnostic returns the first planner diagnostic at a severity, so
// the condition names a specific finding rather than a generic count.
func firstPlannerDiagnostic(diagnostics []planner.Diagnostic, severity string) *planner.Diagnostic {
	for i := range diagnostics {
		if diagnostics[i].Severity == severity {
			return &diagnostics[i]
		}
	}
	return nil
}

func plannerCompileMetricResult(configHash string, diagnostics []planner.Diagnostic) string {
	if diagnostic := firstPlannerDiagnostic(diagnostics, planner.DiagnosticError); diagnostic != nil {
		return diagnostic.Reason
	}
	if configHash == "" {
		return "PlannerFailed"
	}
	if diagnostic := firstPlannerDiagnostic(diagnostics, planner.DiagnosticWarning); diagnostic != nil {
		return diagnostic.Reason
	}
	return "Accepted"
}

func auditDiagnosticsForCompilation(result validationResult, diagnostics []resolver.Diagnostic, plannerDiagnostics []planner.Diagnostic) []audit.DiagnosticRecord {
	records := append(auditDiagnosticsFromResolver(diagnostics), auditDiagnosticsFromPlanner(plannerDiagnostics)...)
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

// deviceIdentityEvidence reads the two model strings the F-101 check compares.
//
// The reported half is not in the structural bundle and must not be: the bundle is hashed into plan
// identity, and a value that changes when a device is re-read would make the plan hash churn on
// telemetry rather than on topology. It is read from the device's published status instead, which
// is where the UPSDevice reconciler puts it.
//
// A device that cannot be read returns two empty strings rather than an error. This is an audit
// annotation on a row that is worth writing either way, and failing the whole compilation record
// over a missing annotation would trade the useful part for the decorative one.
func (r *ShutdownFlowReconciler) deviceIdentityEvidence(ctx context.Context, deviceName string) (string, string) {
	if deviceName == "" {
		return "", ""
	}
	var device powerv1alpha1.UPSDevice
	if err := r.Get(ctx, types.NamespacedName{Name: deviceName}, &device); err != nil {
		return "", ""
	}
	declared := strings.TrimSpace(device.Spec.Identity.Model)
	reported := ""
	if device.Status.Identity != nil {
		reported = strings.TrimSpace(device.Status.Identity.ReportedModel)
	}
	return declared, reported
}

// reportPlannerRejection logs the planner's refusal and returns it as the compile's rejection.
//
// F-99: the planner already said why, in a diagnostic nobody read. It reached the audit writer and
// stopped there, so on a cluster with storage: Disabled the reason existed nowhere the operator
// could look. It goes to the log here and to status.compileDiagnostics alongside.
func reportPlannerRejection(log logr.Logger, flowName string, diagnostics []planner.Diagnostic) validationResult {
	if diagnostic := firstPlannerDiagnostic(diagnostics, planner.DiagnosticError); diagnostic != nil {
		log.Info("Shutdown flow planner rejected the compile",
			"shutdownflow", flowName,
			"reason", diagnostic.Reason,
			"subject", diagnostic.Subject,
			"message", diagnostic.Message,
		)
	} else {
		log.Info("Shutdown flow planner produced no plan and no diagnostic", "shutdownflow", flowName)
	}
	return plannerRejection(diagnostics)
}

// plannerRejection names the planner's own reason for refusing to produce a plan.
//
// F-99: a rejected compile reported reason PlannerFailed with the message "shutdown flow planner
// failed after resolver inputs were attached", which says only that the thing that failed was the
// planner. The diagnostics naming the actual cause went to the audit writer, which returns early
// when spec.managementClusterRef is unset and writes nothing at all under storage: Disabled -- so
// on the configuration the install guide recommends for evaluation, the cause was unreachable.
//
// The generic reason survives as the fallback. A planner that produces neither a plan nor a
// diagnostic is its own bug, and flattening that case into a specific-sounding reason would hide it.
func plannerRejection(diagnostics []planner.Diagnostic) validationResult {
	if diagnostic := firstPlannerDiagnostic(diagnostics, planner.DiagnosticError); diagnostic != nil {
		if diagnostic.Subject != "" {
			return rejected(diagnostic.Reason, "%s: %s", diagnostic.Subject, diagnostic.Message)
		}
		return rejected(diagnostic.Reason, "%s", diagnostic.Message)
	}
	return rejected("PlannerFailed", "shutdown flow planner produced no plan and no diagnostic explaining why")
}

// compileDiagnosticStatuses flattens both diagnostic sets into the published list, tagged by the
// stage that produced each one so a reader can tell an inventory problem from a planning one.
func compileDiagnosticStatuses(resolverDiagnostics []resolver.Diagnostic, plannerDiagnostics []planner.Diagnostic, extra []powerv1alpha1.ShutdownFlowDiagnosticStatus) []powerv1alpha1.ShutdownFlowDiagnosticStatus {
	if len(resolverDiagnostics) == 0 && len(plannerDiagnostics) == 0 && len(extra) == 0 {
		return nil
	}
	statuses := make([]powerv1alpha1.ShutdownFlowDiagnosticStatus, 0, len(resolverDiagnostics)+len(plannerDiagnostics)+len(extra))
	for _, diagnostic := range resolverDiagnostics {
		statuses = append(statuses, powerv1alpha1.ShutdownFlowDiagnosticStatus{
			Severity: diagnostic.Severity,
			Source:   diagnostic.Source,
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	for _, diagnostic := range plannerDiagnostics {
		statuses = append(statuses, powerv1alpha1.ShutdownFlowDiagnosticStatus{
			Severity: diagnostic.Severity,
			Source:   "Planner",
			Reason:   diagnostic.Reason,
			Subject:  diagnostic.Subject,
			Message:  diagnostic.Message,
		})
	}
	statuses = append(statuses, extra...)
	return statuses
}
