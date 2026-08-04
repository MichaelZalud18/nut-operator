# Audit Storage Schema

Components: Storage & Audit.

Production durable state is PostgreSQL, with CloudNativePG as the preferred in-cluster provider.
The project packages PostgreSQL migrations and the PostgreSQL-shaped writer boundary in
`internal/audit`. `internal/storage` connects controller reconciliation to Secret-backed
PostgreSQL/CNPG credentials through the pgx `database/sql` driver.

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

Replay into PostgreSQL is a recovery/subscriber concern. The spool preserves the original IDs so a
future replayer can use the same upsert and insert semantics as the primary audit writer.

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
