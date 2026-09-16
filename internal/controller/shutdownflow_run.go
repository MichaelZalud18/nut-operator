package controller

import (
	"context"
	"errors"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// runShutdownFlow belongs to the manager-owned worker. It owns the evidence store
// until execution (including overlapped action cleanup) has returned.
func (r *ShutdownFlowReconciler) runShutdownFlow(ctx context.Context, flow *powerv1alpha1.ShutdownFlow, result validationResult, diagnostics []resolver.Diagnostic, plannerDiagnostics []planner.Diagnostic, bundle resolver.StructuralBundle, compiledWaves []powerv1alpha1.CompiledShutdownWave, publishedArtifact *powerv1alpha1.PublishedPlannerArtifactStatus, configHash string, triggerEvaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) (runErr error) {
	if flow == nil {
		return nil
	}
	cluster, err := r.getManagementCluster(ctx, flow)
	if err != nil || cluster == nil || (!managementClusterStorageReady(cluster) && !cluster.Spec.Storage.AuditSpool.Enabled) {
		if result.accepted && triggerEvaluation != nil && triggerEvaluation.Eligible {
			setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, false,
				"AuditStoreUnavailable", "shutdown flow execution requires a ready PostgreSQL audit store")
		}
		return err
	}
	store, err := r.openExecutionAuditStore(ctx, cluster)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	writer, spoolWriter, err := shutdownAuditWriter(cluster, store)
	if err != nil {
		return err
	}
	if spoolWriter != nil {
		defer func() { r.reportAuditSpoolFallback(flow, spoolWriter.Stats(), triggerEvaluation) }()
	}
	r.drainAuditSpool(ctx, cluster, store)
	observedAt := r.now()
	auditErr := r.recordShutdownFlowAudit(ctx, writer, flow, observedAt, result, diagnostics, plannerDiagnostics, bundle, compiledWaves, publishedArtifact, configHash, triggerEvaluation)

	// Evidence errors do not decide execution eligibility. Static acceptance and
	// the executor's trigger/rehearsal, approval, and fresh-target gates do.
	var executionErr error
	if result.accepted {
		executionErr = r.executeShutdownFlow(ctx, writer, store, flow, observedAt, bundle.Hash, configHash, triggerEvaluation, bundle)
	}
	return errors.Join(auditErr, executionErr)
}
