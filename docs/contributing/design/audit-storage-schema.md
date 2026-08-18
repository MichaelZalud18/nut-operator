# Audit Storage Schema

Components: Storage & Audit.

Production durable state is PostgreSQL, with CloudNativePG as the preferred in-cluster provider.
The project packages PostgreSQL migrations and the PostgreSQL-shaped writer boundary in
`internal/audit`. `internal/storage` connects controller reconciliation to Secret-backed
PostgreSQL/CNPG credentials through the pgx `database/sql` driver.

## Why an operator has a database at all

Most operators have exactly one datastore: the Kubernetes API server. That is not an oversight, it
is the reconciliation model working as intended. A controller reads desired state from `spec`,
observes the world, and writes what it found to `status`. If it crashes, restarts, or loses
everything it knew, the next reconcile re-derives current state from scratch. **History is
disposable because the present is recomputable.** What happened along the way is emitted as Events
(1 hour TTL by default) and metrics (scraped elsewhere), and neither is durable, because the
question an operator normally answers is "is this correct now," which reconciliation answers for
free. etcd reinforces the same conclusion from the other direction: roughly 1.5 MiB per object,
every write a Raft round trip, every update waking every watcher, and compaction discarding
revision history regardless. It is a coordination store, not an accumulator.

This operator does not fit that model, for two reasons.

**The present is not recomputable here.** The events worth recording happen while the cluster is
going down, and the state they leave behind is absence. There is no `status` to read afterward
because the nodes are off. Which nodes released, in what order, against what battery reading, and
why a wave stopped early cannot be reconstructed from a cluster that no longer exists — and those
are precisely the questions a power product exists to answer.

**The volume is wrong for etcd.** Telemetry snapshots land one row per device per poll, every
5–15 seconds, indefinitely. Accumulating that in CR status would stress etcd in exactly the way it
punishes hardest.

So the split in GP-3 is not a preference: Kubernetes holds desired state and current summaries,
PostgreSQL holds history. See `docs/contributing/design/scope-boundaries.md` for GP-3 and SB-11.

The cost of the deviation is real and worth stating plainly. This binary is a PostgreSQL client —
connection pool, TLS, credentials, an owned schema, versioned migrations, retention enforcement,
failover behavior — which is application-tier machinery living inside an operator, and it is where
most of this component's failure modes come from. Sharper still: **the database is most likely to be
unavailable at the exact moment it matters most.** The outage is the event being recorded, and under
the recommended CNPG deployment the database is inside the cluster being shut down. That single fact
is why audit failure must never block power response (SB-11), and why the spool below exists at all.

The pattern is not unprecedented, only uncommon at this layer. Argo Workflows persists completed
workflows to a PostgreSQL archive for the same reason — the CRs are pruned and the history is not
recoverable from them — and Tekton Results is the same shape. It shows up wherever the Kubernetes
objects are ephemeral but the record of them is not. Note the distinction from database operators
such as CloudNativePG: those talk to PostgreSQL as the workload they manage, not as their own store.

## Tables

- `audit_schema_migrations`: applied schema versions.
- `power_events`: operator decisions and power-domain events.
- `ups_telemetry_snapshots`: raw UPS status snapshots and selected normalized fields.
- `capability_profile_matches`: profile resolution outcomes and diagnostics.
- `capability_profile_verifications`: probe-history records tying observed NUT variables, provider
  identity, firmware, and drift diagnostics to one matched profile version.
- `shutdownflow_compilations`: accepted or rejected planner compilations and compiled waves.
- `shutdownflow_decisions`: dry-run or enforce decisions for power-event triggers.
- `shutdownflow_executions`: executor runs tied to a compiled plan and trigger decision.
- `shutdownflow_execution_waves`: wave-level execution progress and timing.
- `shutdownflow_execution_groups`: group-level execution progress, selected targets, and timing.
- `shutdownflow_action_attempts`: individual dry-run or effectful executor action outcomes.
- `node_release_records`: executor release decisions for node shutdown handoff.
- `node_signal_handoffs`: signal-file evidence passed to node power agents.
- `executor_resume_states`: compact restart state for idempotent executor resume.

## Boundary

CR status remains the current summary and review surface. PostgreSQL holds history, telemetry
streams, profile-match records, profile verification/probe-history records, accepted and rejected
planner compilation records, shutdown decision records, and durable executor progress.

The writer uses a narrow generic SQL executor interface, so CNPG and external PostgreSQL are
connection-management choices rather than separate domain models. The storage resolver in
`internal/storage` keeps `Disabled`, `ExternalPostgres`, and `CNPG` selection separate from
domain validation and controller status. `spec.storage.auditSpool` adds a local JSONL fallback
journal for shutdown-time audit records generated after PostgreSQL has already been opened but then
stops accepting writes. The spool is not a replacement database: it requires CNPG or
ExternalPostgres storage, an explicit absolute in-container path, and a deployment-supplied durable
volume at that path.

`PowerManagementCluster` reconciliation opens the configured audit store, pings PostgreSQL,
applies bundled migrations, evaluates configured retention, and records durable reconciliation
events after status updates. Accepted `ShutdownFlow` reconciliations record planner compilation
rows with the compiled waves, dependency graph, advisory startup waves, planner explanations, and
diagram exports, plus capability profile match rows, capability profile verification rows, trigger
decisions, and eligible dry-run execution evidence through the referenced `PowerManagementCluster`
storage backend. Rejected `ShutdownFlow` reconciliations record a compilation row with diagnostics
and no accepted plan hash. Executor implementations use the execution, wave, group,
action-attempt, release, handoff, and resume-state tables to make shutdown progress auditable and
resumable without putting PostgreSQL on the host actuation boundary. External PostgreSQL requires
TLS by default. CNPG mode reads the generated application credential Secret and prefers the FQDN URI
when present.

## Shutdown-Time Spool

When `spec.storage.auditSpool.enabled` is true, the `ShutdownFlow` audit writer first attempts the
normal PostgreSQL write. If that write fails, it appends one JSON line to
`<spec.storage.auditSpool.path>/audit-spool.jsonl` and lets execution continue. Each spool record
contains the audit record kind, a stable replay key such as `executionID` or
`executionID/waveIndex`, the spool timestamp, the primary PostgreSQL error, and the original audit
payload. A successful fallback sets the `Degraded` and `ExecutionReady` conditions to
`AuditSpoolFallback` on the `ShutdownFlow`.

The journal is bounded by `spec.storage.auditSpool.maxSize` (default `64Mi`, minimum `1Mi`). A
PostgreSQL outage has no bounded duration, so an uncapped journal grows until the durable volume is
full — and a full volume during a power event is a worse failure than a truncated audit trail,
because it can take the operator's own writes down with it. At the cap the journal stops accepting
records rather than evicting older ones: the first records of a degradation explain it, the
thousandth mostly repeats it. Refused records are counted, reported under the distinct
`AuditSpoolFull` condition reason, and exported as
`nutoperator_audit_spool_records_total{outcome="dropped"}`. They are never silently discarded, and
they never fail the reconcile — a delayed audit trail and a permanently incomplete one are different
operator problems, but neither outranks power response (SB-11).

### Replay

`audit.ReplaySpool` drains the journal back into the primary writer, and `ShutdownFlow`
reconciliation runs it immediately after the audit store opens — the one point the operator reliably
knows PostgreSQL is accepting writes. A spool that captures records but never returns them does not
preserve the audit trail; it loses it more slowly.

Replay is safe to repeat. Every spooled record carries the same identity the primary writer uses and
every primary insert is an upsert on that identity, so re-applying a record is a no-op rather than a
duplicate. The journal is removed only after a fully clean drain: a record that fails, or one this
build does not recognize, leaves the file in place. A drain failure is logged, never returned.

The behavior has direct precedent in the telemetry tier — Fluent Bit filesystem buffering, the
OpenTelemetry Collector's persistent sending queue, Vector disk buffers, and the Prometheus WAL all
buffer locally when the downstream is unavailable and drain on recovery. All of them bound the
buffer, too (`storage.total_limit_size`, `queue_size`, `max_size` respectively), which is why
`maxSize` above is not optional in practice.

## Retention

Retention is configured on `PowerManagementCluster.spec.storage.retention`.

- `events` prunes operator/audit event families: `power_events`, `capability_profile_matches`,
  `capability_profile_verifications`, `shutdownflow_compilations`, `shutdownflow_decisions`, and
  `shutdownflow_executions`.
- `telemetry` prunes raw UPS telemetry snapshots in `ups_telemetry_snapshots`.

Executor child tables use PostgreSQL `ON DELETE CASCADE` from `shutdownflow_executions`, so wave,
group, action-attempt, node-release, signal-handoff, and resume-state rows expire with their parent
execution. Unset or zero retention keeps that record family indefinitely. Negative retention values
are rejected by storage resolution before a store is opened.
