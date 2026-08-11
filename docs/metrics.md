# Metrics

Components: Outputs & Publishing, Operator Maturity & Hardening.

The manager exposes Prometheus metrics on the same `/metrics` endpoint controller-runtime already
serves — no new port, Service, or RBAC. `internal/metrics` declares this operator's own collectors
alongside controller-runtime's built-in reconcile/workqueue metrics
(`controller_runtime_reconcile_total`, `workqueue_*`, and friends), which need no project-specific
code to be useful on their own.

## Enabling scrape

The metrics endpoint itself is on by default (`config/default/manager_metrics_patch.yaml`, HTTPS on
`:8443`, behind the `metrics: enabled` namespace-label `NetworkPolicy` — see `docs/security.md`). The
Prometheus Operator `ServiceMonitor` (`config/prometheus/monitor.yaml`) is **not** enabled by default —
`config/default/kustomization.yaml` leaves `../prometheus` commented out, matching the kubebuilder
scaffold convention of not assuming the Prometheus Operator CRDs are installed. Uncomment that line to
enable it in a cluster that has them.

## `nutoperator_shutdownflow_*`

All labeled by `shutdownflow` (the `ShutdownFlow` object name) — bounded cardinality by design, since
this operator orchestrates whole-cluster shutdown as a handful of named flows, not a per-workload
policy engine.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `compile_total` | Counter | `shutdownflow`, `result` | Planner compilation attempts. `result` is `Accepted` or the same rejection reason already surfaced on the `Accepted` condition (`PlannerFailed`, `ManagementClusterNotFound`, or a resolver diagnostic reason). |
| `compile_duration_seconds` | Histogram | `shutdownflow` | Time spent in the `planner.Compile` call for one reconcile. |
| `plan_hash_changes_total` | Counter | `shutdownflow` | Incremented when a successful compile's plan hash differs from the previously observed one — how often the compiled plan is actually changing shape, not just re-confirming. |
| `trigger_evaluations_total` | Counter | `shutdownflow`, `eligible` (`true`/`false`) | Trigger evaluations, by eligibility outcome. |
| `degraded` | Gauge | `shutdownflow` | Mirrors the `Degraded` status condition (1/0), so it can be alerted on directly. |
| `tier_inversions` | Gauge | `shutdownflow` | Nodes currently withheld from power-off because a lower-tier group runs on them (`OD-18`). Published on every compile including zero, so the series exists before the first inversion. Inversion develops as workloads reschedule, so a compile-time diagnostic alone misses it. |
| `execution_duration_seconds` | Histogram | `shutdownflow`, `mode` (`DryRun`/`Enforce`) | Time spent recording one wave-execution run (`internal/executor.Executor.Execute`). |

## `nutoperator_actuator_*`

Not labeled by `shutdownflow`: every action from every flow passes through the same
`internal/kubeactions.Runner.RunAction` choke point, so these are the fleet-wide actuator view.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `action_attempts_total` | Counter | `action`, `mode` (`DryRun`/`Enforce`), `outcome` | Every `RunAction` call, by the executor action type (`ScaleWorkload`, `CordonNodes`, `DrainNodes`, `RunWorkflow`, `AgentShutdown`, `Notify`, `Wait`), mode, and outcome (`Succeeded`, `Simulated`, `Blocked`, `TimedOut`, or `Error`). |
| `action_duration_seconds` | Histogram | `action` | Time spent on one `RunAction` call. |

## `nutoperator_audit_*`

The shutdown-time audit spool (`spec.storage.auditSpool`). Any non-zero rate here means PostgreSQL
was refusing audit writes during execution — the flow continued, which is the intent (SB-11), but
the audit trail is degraded until the journal drains.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `spool_records_total` | Counter | `outcome` (`spooled`/`dropped`) | Audit records the fallback journal took (`spooled`) or refused because it was at its size cap (`dropped`). A `dropped` record exists nowhere: PostgreSQL rejected it and the journal would not hold it. |
| `spool_replay_records_total` | Counter | `outcome` (`replayed`/`skipped`) | Spooled records returned to PostgreSQL. `skipped` means unparseable or of a record kind this build does not recognize, so it stays in the journal. |
| `spool_journal_bytes` | Gauge | — | Journal size at the last spool write. Alert on this ahead of loss: records are dropped once it reaches `spec.storage.auditSpool.maxSize`. |

Useful alerts: `increase(nutoperator_audit_spool_records_total{outcome="dropped"}[1h]) > 0` for lost
evidence, and `nutoperator_audit_spool_journal_bytes` approaching the configured cap for evidence
about to be lost.

## `nutoperator_certificate_*`

Expiry of the serving certificates the manager has mounted, read from the files controller-runtime
actually serves rather than from the Secret through the API — so a stale mount reports its own stale
certificate instead of the cluster's current one.

This matters most on the no-cert-manager install path ([install.md](install.md)), where rotation is a
deliberate `hack/webhook-cert.sh` re-run. Nothing renews the certificate on its own there, and a
1-year lifetime makes expiry a once-a-year surprise — the kind nobody has the procedure in mind for
when it fires.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `not_after_timestamp_seconds` | Gauge | `certificate` (`webhook`/`metrics`) | Expiry as a Unix timestamp. The soonest expiry in the file: a bundle whose intermediate expires before its leaf goes down on the intermediate's date. |
| `read_errors_total` | Counter | `certificate` | Failures to read or parse the file. |

A timestamp rather than a remaining duration, so the value stays accurate between scrapes without
being re-published. Alert with a lead time that fits the rotation procedure:

```promql
nutoperator_certificate_not_after_timestamp_seconds - time() < 30 * 24 * 3600
```

On a read failure the gauge is **deleted**, not left at its last value — a stale reading of "expires
in eight months" answers the alert's question wrongly. Absence plus a rising `read_errors_total` is
what an unknown expiry looks like, so alert on that too:

```promql
absent(nutoperator_certificate_not_after_timestamp_seconds{certificate="webhook"})
```

Every replica publishes its own mount's expiry; the reporter deliberately does not take the leader
election, since a standby whose mount has gone stale is exactly the condition worth seeing.

## Design notes

- Collectors live in `internal/metrics`, registered via `promauto.With(metrics.Registry)` against
  controller-runtime's own registry — the pattern the [kubebuilder book
  documents](https://book.kubebuilder.io/reference/metrics.html#publishing-additional-metrics) for
  publishing additional metrics.
- Recording happens at the impure boundary (`internal/controller`, `internal/kubeactions`), not inside
  `internal/planner` or `internal/trigger`. Both are deliberately pure — no I/O, no wall-clock reads —
  and a global Prometheus counter is a side effect; keeping it out of those packages keeps their unit
  tests independent of global registry state.
- Every label set is a bounded enum or a `ShutdownFlow` object name (small by the operator's own
  design). No workload, node, or namespace name is ever used as a label value.

## Open work

Not yet covered: per-`UPSDevice` telemetry poll metrics, capability-match metrics, and inventory
compiler metrics (domain/orphan counts). See `docs/tasks.md`'s Outputs & Publishing section.
