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
	// ShutdownFlowCompileTotal counts planner compilations by result: "Accepted", the first planner
	// diagnostic reason, or "PlannerFailed" when no diagnostic was available.
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

	// AuditSpoolRecordsTotal counts audit records handled by the shutdown-time fallback journal, by
	// outcome: "spooled" (PostgreSQL refused the write and the journal took it) or "dropped" (the
	// journal was at its size cap, so the record exists nowhere). Any non-zero rate means PostgreSQL
	// was failing writes during execution; a non-zero "dropped" rate means audit evidence was lost.
	AuditSpoolRecordsTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "audit",
		Name:      "spool_records_total",
		Help:      "Total audit records written to or refused by the shutdown-time spool journal, by outcome.",
	}, []string{"outcome"})

	// AuditSpoolReplayRecordsTotal counts records drained from the journal back into PostgreSQL, by
	// outcome: "replayed" (accepted by the primary writer) or "skipped" (unparseable or of a record
	// kind this build does not recognize, so left in the journal).
	AuditSpoolReplayRecordsTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "audit",
		Name:      "spool_replay_records_total",
		Help:      "Total spooled audit records returned to PostgreSQL, by outcome.",
	}, []string{"outcome"})

	// AuditSpoolJournalBytes is the journal size observed at the last spool write. It is the metric to
	// alert on ahead of loss: records are dropped once it reaches spec.storage.auditSpool.maxSize.
	AuditSpoolJournalBytes = promauto.With(metrics.Registry).NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "audit",
		Name:      "spool_journal_bytes",
		Help:      "Size in bytes of the shutdown-time audit spool journal at the last spool write.",
	})

	// ShutdownFlowTierInversions is how many nodes a flow is currently withholding from power-off
	// because a group scheduled to keep working runs on them (OD-18).
	//
	// A gauge rather than a counter, and published every compile including when it is zero, because
	// inversion is a condition that develops over time as workloads reschedule. A compile-time
	// diagnostic is read once by whoever ran it; this is what makes the condition watchable between
	// compiles, which is when it actually appears.
	ShutdownFlowTierInversions = promauto.With(metrics.Registry).NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "tier_inversions",
		Help:      "Nodes currently withheld from power-off because a lower-tier group runs on them.",
	}, []string{"shutdownflow"})

	// ShutdownFlowPublishTimestampSeconds is when this flow's state was last published, as a Unix
	// timestamp. It is the EX-29 cadence heartbeat.
	//
	// Change emission alone leaves a subscriber unable to distinguish "nothing is happening" from
	// "the publisher died" -- both look like silence. This is re-published on a fixed cadence whether
	// or not anything changed, so staleness in it means the operator stopped, not that the power did
	// not move.
	//
	// A timestamp rather than a counter, so a single scrape answers "how long since this flow was
	// last evaluated" without needing a rate over a window:
	//
	//	time() - nutoperator_shutdownflow_publish_timestamp_seconds > 180
	ShutdownFlowPublishTimestampSeconds = promauto.With(metrics.Registry).NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "shutdownflow",
		Name:      "publish_timestamp_seconds",
		Help:      "When this shutdown flow's state was last published, as a Unix timestamp.",
	}, []string{"shutdownflow"})

	// CertificateNotAfterTimestampSeconds is the expiry of a mounted serving certificate, as a Unix
	// timestamp. Alert on it with `... - time() < <lead time>`; a timestamp rather than a remaining
	// duration so the value does not have to be re-published to stay accurate.
	//
	// This is the warning the no-cert-manager install path had no way to give: rotation there is a
	// deliberate hack/webhook-cert.sh re-run, so nothing renews the certificate on its own.
	CertificateNotAfterTimestampSeconds = promauto.With(metrics.Registry).NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: "certificate",
		Name:      "not_after_timestamp_seconds",
		Help:      "Expiry of a mounted serving certificate, as a Unix timestamp.",
	}, []string{"certificate"})

	// CertificateReadErrorsTotal counts failures to read or parse a mounted serving certificate. The
	// gauge above is deleted on failure rather than left stale, so this counter and the gauge's
	// absence are the two signals that the expiry is currently unknown.
	CertificateReadErrorsTotal = promauto.With(metrics.Registry).NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: "certificate",
		Name:      "read_errors_total",
		Help:      "Total failures to read or parse a mounted serving certificate.",
	}, []string{"certificate"})
)

// BoolToFloat renders a boolean as a Prometheus gauge value (1 for true, 0 for false).
func BoolToFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
