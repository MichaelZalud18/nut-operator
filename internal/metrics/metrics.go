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

// Package metrics declares this operator's Prometheus collectors (F-3). Collectors register
// themselves against controller-runtime's own metrics.Registry -- the standard kubebuilder pattern
// (https://book.kubebuilder.io/reference/metrics.html#publishing-additional-metrics) -- so they are
// served on the same /metrics endpoint, behind the same authn/authz filter, as controller-runtime's
// built-in reconcile/workqueue metrics. No new endpoint, port, or RBAC is required.
//
// Labels are kept low-cardinality by construction: "shutdownflow" is bounded by the number of
// ShutdownFlow objects in the cluster (a handful, by design -- this operator orchestrates whole-cluster
// shutdown, not per-workload policies), and "action"/"mode"/"outcome"/"result" are all bounded enums.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const namespace = "nutoperator"

var (
	// ShutdownFlowCompileTotal counts planner compilations by result: "Accepted" or the same rejection
	// reason string already surfaced on the Accepted condition (e.g. "PlannerFailed",
	// "ManagementClusterNotFound", or a resolver diagnostic reason).
	ShutdownFlowCompileTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "compile_total",
		Help:      "Total ShutdownFlow plan compilation attempts, by outcome.",
	}, []string{"shutdownflow", "result"})

	// ShutdownFlowCompileDurationSeconds times the planner.Compile call for one reconcile.
	ShutdownFlowCompileDurationSeconds = promauto.With(metrics.Registry).NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "compile_duration_seconds",
		Help:      "Time spent compiling a ShutdownFlow plan.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"shutdownflow"})

	// ShutdownFlowPlanHashChangesTotal counts how often a successful compile produces a plan hash that
	// differs from the previously observed one -- a proxy for how often the compiled plan is actually
	// changing shape (spec edit, topology change, capability match change), not just re-confirming.
	ShutdownFlowPlanHashChangesTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "plan_hash_changes_total",
		Help:      "Total times a ShutdownFlow's compiled plan hash changed from its previously observed value.",
	}, []string{"shutdownflow"})

	// ShutdownFlowTriggerEvaluationsTotal counts trigger evaluations by eligibility outcome.
	ShutdownFlowTriggerEvaluationsTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "trigger_evaluations_total",
		Help:      "Total ShutdownFlow trigger evaluations, by eligibility.",
	}, []string{"shutdownflow", "eligible"})

	// ShutdownFlowDegraded is a level gauge (1/0) mirroring the Degraded status condition, so it can be
	// alerted on directly rather than only read from `kubectl get`.
	ShutdownFlowDegraded = promauto.With(metrics.Registry).NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "degraded",
		Help:      "Whether a ShutdownFlow's Degraded condition is currently true (1) or false (0).",
	}, []string{"shutdownflow"})

	// ShutdownFlowExecutionDurationSeconds times one call into internal/executor.Executor.Execute,
	// which records ordered wave-execution evidence for one eligible trigger episode (dry-run or
	// enforce).
	ShutdownFlowExecutionDurationSeconds = promauto.With(metrics.Registry).NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "execution_duration_seconds",
		Help:      "Time spent recording one ShutdownFlow wave-execution run.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"shutdownflow", "mode"})

	// ActuatorActionAttemptsTotal counts every internal/kubeactions.Runner.RunAction call -- the single
	// choke point every executor action (real or dry-run) passes through -- by action type, mode, and
	// outcome.
	ActuatorActionAttemptsTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "actuator",
		Name:      "action_attempts_total",
		Help:      "Total action-runner attempts, by action type, mode, and outcome.",
	}, []string{"action", "mode", "outcome"})

	// ActuatorActionDurationSeconds times one RunAction call.
	ActuatorActionDurationSeconds = promauto.With(metrics.Registry).NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Subsystem: "actuator",
		Name:      "action_duration_seconds",
		Help:      "Time spent on one action-runner attempt.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"action"})
)

// BoolToFloat renders a boolean as a Prometheus gauge value (1 for true, 0 for false).
func BoolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
