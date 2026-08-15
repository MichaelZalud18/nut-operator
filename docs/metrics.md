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
| `compile_total` | Counter | `shutdownflow`, `result` | Planner compilation attempts. `result` is `Accepted`, the first planner error or warning reason, or `PlannerFailed` when planner compilation produced no diagnostic to name. Validation and resolver rejections that happen before planner compilation are surfaced on the `Accepted` condition and audit diagnostics instead. |
| `compile_duration_seconds` | Histogram | `shutdownflow` | Time spent in the `planner.Compile` call for one reconcile. |
| `plan_hash_changes_total` | Counter | `shutdownflow` | Incremented when a successful compile's plan hash differs from the previously observed one — how often the compiled plan is actually changing shape, not just re-confirming. |
| `trigger_evaluations_total` | Counter | `shutdownflow`, `eligible` (`true`/`false`) | Trigger evaluations, by eligibility outcome. |
| `degraded` | Gauge | `shutdownflow` | Mirrors the `Degraded` status condition (1/0), so it can be alerted on directly. |
| `tier_inversions` | Gauge | `shutdownflow` | Nodes currently withheld from power-off because a lower-tier group runs on them (`OD-18`). Published on every compile including zero, so the series exists before the first inversion. Inversion develops as workloads reschedule, so a compile-time diagnostic alone misses it. |
| `execution_duration_seconds` | Histogram | `shutdownflow`, `mode` (`DryRun`/`Enforce`) | Time spent recording one wave-execution run (`internal/executor.Executor.Execute`). |
| `tier_overruns_total` | Counter | `shutdownflow`, `tier`, `policy`, `action` | Tier transitions whose observed elapsed time exceeded the effective tier budget (`EX-31`). `policy` is `Wait` or `Preempt`; `action` records what the executor did. |
| `tier_overrun_seconds` | Histogram | `shutdownflow`, `tier`, `policy`, `action` | Seconds by which an overrun tier exceeded its effective budget (`EX-31`). Same labels as `tier_overruns_total`, so the count and amount describe the same event stream. |
| `publish_timestamp_seconds` | Gauge | `shutdownflow` | When this flow's state was last republished, as a Unix timestamp — the EX-29 cadence heartbeat. Refreshed every reconcile whether or not anything changed, on a cadence that is faster while a flow is active. |

## `nutoperator_actuator_*`

Not labeled by `shutdownflow`: every action from every flow passes through the same
`internal/kubeactions.Runner.RunAction` choke point, so these are the fleet-wide actuator view.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `action_attempts_total` | Counter | `action`, `mode` (`DryRun`/`Enforce`), `outcome` | Every `RunAction` call, by the executor action type (`ScaleWorkload`, `CordonNodes`, `DrainNodes`, `RunHook`, `AgentShutdown`, `Notify`, `Wait`), mode, and outcome (`Succeeded`, `Simulated`, `Blocked`, `TimedOut`, or `Error`). |
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

## Publisher liveness

Change emission alone cannot distinguish "nothing is happening" from "the publisher died" — both
look like silence. `publish_timestamp_seconds` is the other half of EX-29: republished on a fixed
cadence regardless of change, so staleness in it means the operator stopped rather than that the
power did not move.

```promql
time() - nutoperator_shutdownflow_publish_timestamp_seconds > 180
```

The same value is on the object as `status.lastPublishTime`, for anyone watching the resource rather
than scraping metrics.

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

## `nutoperator_upsdevice_*`

Telemetry polling metrics are recorded at the `UPSDevice` reconciler boundary, where the operator
has both the target and the result.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `telemetry_polls_total` | Counter | `upsdevice`, `result` (`Succeeded`/`Failed`) | Live NUT telemetry poll attempts by result. |
| `telemetry_poll_duration_seconds` | Histogram | `upsdevice`, `result` (`Succeeded`/`Failed`) | Time spent polling live NUT telemetry for one `UPSDevice`. |
| `telemetry_last_success_timestamp_seconds` | Gauge | `upsdevice` | Unix timestamp of the last successful telemetry poll for that `UPSDevice`. |

## `nutoperator_capability_*`

Capability metrics count profile-resolution attempts. A successful unidentified-device floor match
is still a match, with `unidentified="true"` and the match tier label naming the floor that produced
it.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `match_total` | Counter | `result`, `tier`, `unidentified` | Capability profile match attempts from UPSDevice status publication and ShutdownFlow structural resolution. `result` is `Matched` or `Failed`. |

## `nutoperator_inventory_*`

Inventory metrics publish the current accepted resolver/compiler shape and the diagnostic counts
that matter when the shape is rejected.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `compile_total` | Counter | `result` (`Accepted`/`Rejected`/`Failed`) | Declarative inventory compile attempts. `Rejected` means structural diagnostics rejected the snapshot; `Failed` means the controller could not read required inputs or the resolver failed outside ordinary rejection. |
| `entities` | Gauge | `kind` (`UPSDevice`/`Node`/`PowerInfrastructure`) | Current accepted inventory entity count by kind. |
| `edges` | Gauge | `relation` (`feeds`/`carries`) | Current accepted inventory edge count by relation. |
| `power_domains` | Gauge | — | Current accepted derived power-domain count. |
| `orphan_nodes` | Gauge | — | Nodes currently reported by the power-planning orphan rule. This is populated from diagnostics, so it survives rejected snapshots. |
| `communication_path_unmodeled_nodes` | Gauge | — | Nodes currently reported without a modeled `carries` path. |

## Design notes

- Collectors live in `internal/metrics`, registered via `promauto.With(metrics.Registry)` against
  controller-runtime's own registry — the pattern the [kubebuilder book
  documents](https://book.kubebuilder.io/reference/metrics.html#publishing-additional-metrics) for
  publishing additional metrics.
- Recording happens at the impure boundary (`internal/controller`, `internal/kubeactions`), not inside
  `internal/planner` or `internal/trigger`. Both are deliberately pure — no I/O, no wall-clock reads —
  and a global Prometheus counter is a side effect; keeping it out of those packages keeps their unit
  tests independent of global registry state.
- Every label set is a bounded enum or a `ShutdownFlow`/`UPSDevice` object name (small by the
  operator's own design). No workload, node, or namespace name is ever used as a label value.

## Open work

None.
