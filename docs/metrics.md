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
| `execution_duration_seconds` | Histogram | `shutdownflow`, `mode` (`DryRun`/`Enforce`) | Time spent recording one wave-execution run (`internal/executor.Executor.Execute`). |

## `nutoperator_actuator_*`

Not labeled by `shutdownflow`: every action from every flow passes through the same
`internal/kubeactions.Runner.RunAction` choke point, so these are the fleet-wide actuator view.

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `action_attempts_total` | Counter | `action`, `mode` (`DryRun`/`Enforce`), `outcome` | Every `RunAction` call, by the executor action type (`ScaleWorkload`, `CordonNodes`, `DrainNodes`, `RunWorkflow`, `AgentShutdown`, `Notify`, `Wait`, `Gate`), mode, and outcome (`Succeeded`, `Simulated`, `Blocked`, or `Error`). |
| `action_duration_seconds` | Histogram | `action` | Time spent on one `RunAction` call. |

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
