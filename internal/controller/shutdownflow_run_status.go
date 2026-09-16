package controller

import (
	"context"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func markExecutionPending(flow *power.ShutdownFlow, accepted bool, evaluation *power.ShutdownTriggerEvaluationStatus) {
	if accepted && evaluation != nil && (evaluation.Eligible || shutdownFlowRehearsalRequest(flow).Requested) {
		setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, false, "ExecutionPending", "waiting for an execution worker and execution-time checks")
	}
}

func (r *ShutdownFlowReconciler) publishOwnedRun(ctx context.Context, flow, base *power.ShutdownFlow) (bool, ctrl.Result, error) {
	run := r.runs.snapshot(flow.Name)
	if run == nil {
		return false, ctrl.Result{}, nil
	}
	stale := run.flow.UID != flow.UID || run.flow.Generation != flow.Generation || !flow.DeletionTimestamp.IsZero()
	if stale {
		r.runs.cancel(flow.Name)
		if run.done {
			return false, ctrl.Result{}, nil
		}
		setExecutionReadyCondition(&flow.Status.Conditions, flow.Generation, false, "ExecutionStopping", "waiting for the superseded execution to release its resources")
	} else {
		flow.Status = *run.flow.Status.DeepCopy()
	}
	cadence := run.cadence
	if !stale && flow.Status.LastExecution != nil && flow.Status.LastExecution.Phase == power.ShutdownExecutionPhaseRunning {
		cluster, err := r.getManagementCluster(ctx, flow)
		if err != nil {
			logf.FromContext(ctx).Error(err, "Failed to read publish cadence, using default active cadence", "shutdownflow", flow.Name)
			cluster = nil
		}
		// Rehearsals can start while triggers are idle, so the submitted cadence
		// may no longer describe the owned execution's activity.
		cadence = publishCadence(flow, cadenceParameters(cluster))
	}
	publishedAt := metav1.NewTime(r.now())
	flow.Status.LastPublishTime = &publishedAt
	err := r.Status().Patch(ctx, flow, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if err != nil {
		return true, ctrl.Result{}, err
	}
	metrics.ShutdownFlowPublishTimestampSeconds.WithLabelValues(flow.Name).Set(float64(publishedAt.Unix()))
	if run.done {
		r.runs.acknowledge(flow.Name)
	}
	if stale {
		return true, ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return true, ctrl.Result{RequeueAfter: cadence}, nil
}
