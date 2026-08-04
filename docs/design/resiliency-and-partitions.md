# Resiliency and Partitions

Components: Cross-cutting.

`nut-operator` treats loss of connectivity as an explicit degraded state, not as proof that a node,
UPS, database, or network path is safe to ignore.

## Design Contract

- Missing or stale UPS telemetry never produces an optimistic shutdown decision. The planner marks
  feasibility as `Unknown` or degraded and publishes the reason.
- A node that becomes unreachable is not assumed to be powered off, drained, or safe. Executor
  progress records the last confirmed Kubernetes action and leaves the node release state explicit.
- The controller uses Kubernetes leader election so one active planner/executor instance owns
  decisions during normal API-server availability.
- Node agents are passive by default. They do not invent local shutdown policy when the API server,
  NUT server, or operator is unreachable.
- Node shutdown signals are structured, node-bound, plan-hash-bound, execution-bound, and TTL-bound.
  The actuator watches both the local `upsmon` handoff file and the executor-projected Secret path,
  then rejects stale signals and signals intended for another node.
- Loss of the API path after a valid node-local signal is accepted does not grant new authority. The
  actuator may only complete the already-approved local action represented by that fresh signal.
- PostgreSQL outages degrade audit durability but do not silently change the power plan. The
  shutdown-time audit spool records fallback JSONL evidence for writes that fail after the audit
  store was opened.
- Recovery is outside the operator boundary. Recovery orchestrators consume published plan and
  execution facts; they do not become part of the shutdown decision path.

## Partition Patterns

| Partition | Operator behavior |
|---|---|
| Operator to Kubernetes API | No new cluster actions can be issued. Existing CR status and audit records remain the last durable review surface. Leader election prevents a second controller from acting until the API path is coherent. |
| Operator to PostgreSQL | Reconciliation continues only where policy allows audit degradation. The condition and audit backend state show degraded durability. Shutdown-time local spool support preserves replayable JSONL records when enabled. |
| Operator to NUT server or UPS endpoint | Telemetry becomes stale after `UPSDevice.spec.telemetry.staleAfter`. Trigger and planner outputs carry stale/unknown reasons instead of assuming the UPS is healthy. |
| Operator to node | The executor does not infer local completion. Cordon, drain, release, and handoff evidence remain separately recorded so subscribers can see exactly where certainty ended. |
| Node agent to NUT server | `upsmon` handles NUT monitor behavior locally, while the actuator remains bounded by its configured mode and signal contract. |
| Node agent to operator/API | The node agent does not fetch new policy. It acts only on a fresh local signal already delivered into its pod boundary. |

## Implementation Hooks

- Per-node heartbeat/status records from `NodePowerAgent` pods.
- Partition-aware executor progress reasons for unreachable nodes and API write failures.
- Projected Secret signal delivery from the executor to node-agent pods.
- Replay tooling that drains shutdown-time audit spool records into PostgreSQL when connectivity
  returns.
- Optional policy fields for fail-closed versus continue-with-last-approved-plan behavior when a
  compiled plan is fresh but one dependency path is degraded.
