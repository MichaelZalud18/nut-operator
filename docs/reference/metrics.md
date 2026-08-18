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
| `tier_overruns_total` | Counter | `shutdownflow`, `tier`, `policy`, `action` | Tier transitions whose observed elapsed time exceeded the effective tier budget (`EX-31`). `policy` is `Wait`, `Overlap`, or `Preempt`; `action` records what the executor did. |
| `tier_overrun_seconds` | Histogram | `shutdownflow`, `tier`, `policy`, `action` | Seconds by which an overrun tier exceeded its effective budget (`EX-31`). Same labels as `tier_overruns_total`, so the count and amount describe the same event stream. |
| `publish_timestamp_seconds` | Gauge | `shutdownflow` | When this flow's state was last republished, as a Unix timestamp — the EX-29 cadence heartbeat. Refreshed every reconcile whether or not anything changed, on a cadence that is faster while a flow is active. |

## `nutoperator_actuator_*`

Not labeled by `shutdownflow`: every action from every flow passes through the same
`internal/kubeactions.Runner.RunAction` choke point, so these are the fleet-wide actuator view.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `action_attempts_total` | Counter | `action`, `mode` (`DryRun`/`Enforce`), `outcome` | Every `RunAction` call, by the executor action type (`ScaleWorkload`, `CordonNodes`, `DrainNodes`, `RunHook`, `AgentShutdown`, `Notify`, `Wait`), mode, and outcome (`Succeeded`, `Simulated`, `Blocked`, `TimedOut`, or `Error`). |
| `action_duration_seconds` | Histogram | `action` | Time spent on one `RunAction` call. |

## `nutoperator_halt_*`

Whether nodes asked to power off actually stopped. This is the one part of the system whose evidence
cannot come from the thing being measured: the actuator logs its own syscall and then halts, taking
the log with it, so success would otherwise be inferred from a machine being dark. These are
reconstructed operator-side from two facts the operator can see on its own — it wrote the signal, and
it watched the `Node` stop reporting `Ready`.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `attempts_total` | Counter | `shutdownflow`, `outcome` (`Halted`/`TimedOut`) | One per node per signal. `Halted` means the node stopped reporting after being signalled; `TimedOut` means it was still `Ready` at the deadline, so the signal was delivered and the machine kept running. A wave that releases six nodes and halts four reads as four and two. |
| `last_verified_timestamp_seconds` | Gauge | `node` | When this operator last watched this node actually stop, as a Unix timestamp. Only a `Halted` outcome writes it — a failed attempt must not turn "proven to halt" into "asked to halt". |
| `duration_seconds` | Gauge | `node` | Reconstructed seconds from signal write to the node going away. |

The load-bearing query is the one nobody can otherwise answer without powering a machine off to find
out:

```promql
time() - nutoperator_halt_last_verified_timestamp_seconds > 86400 * 180
```

Absence of a series is itself the finding: that node has never been proven to halt. Only a real
execution writes one — the halt clock starts in the executor, at the signal write. `make
verify-actuation` deliberately does not produce a sample: it hand-delivers the signal so that planner
correctness cannot fail the test, which also puts it outside the path that records the metric. It
proves the node *can* halt; the series proves this operator *saw* it halt. See
[Enabling actuation](../guides/enable-actuation.md).

`duration_seconds` is coarser than the actuator's own `sync(2)` timing, deliberately. It spans
projection, poll, flush, syscall, and detection latency, and it takes the *earlier* of the `Ready`
condition's `lastHeartbeatTime` and `lastTransitionTime` as the moment the node went away — the
transition alone is written only after the control plane's node-monitor grace period, which would
inflate every measurement by that lag. What remains is a bias low, bounded by kubelet's status-post
interval. This is the durable half of the `OD-27` handoff-tail evidence; the container log is the
precise half, and it may not survive the machine long enough to be collected.

A gauge per node rather than a histogram: the value is observed a handful of times per node per year,
the interesting query is per-node anyway (some machines have far more to flush than others), and a
histogram carrying the same label would multiply series by bucket count to summarize what scrapes
already retain. Series are dropped when a `Node` object is deleted — a halted node keeps its object
at `NotReady`, so an actual deletion means the machine left the cluster.

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

This matters most on the no-cert-manager install path ([the installation guide](../installation/README.md)), where rotation is a
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
  operator's own design). No workload or namespace name is ever used as a label value.
- `nutoperator_halt_*` is the one exception, and it carries a node name. It is bounded by cluster
  size rather than by an enum, which is a weaker guarantee — accepted because the question those
  metrics answer is per-node and has no aggregate form: "can this cluster halt this machine" is not
  a fact about the fleet. The series are deleted when a `Node` object is deleted, so the bound holds
  across rebuilds rather than growing with them.

## Open work

None.
