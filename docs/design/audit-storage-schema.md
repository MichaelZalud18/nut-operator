# Audit Storage Schema

Production durable state is PostgreSQL, with CloudNativePG as the preferred in-cluster provider.
The project packages PostgreSQL migrations and the PostgreSQL-shaped writer boundary in
`internal/audit`. `internal/storage` connects controller reconciliation to Secret-backed
PostgreSQL/CNPG credentials through the pgx `database/sql` driver.

## Tables

- `audit_schema_migrations`: applied schema versions.
- `power_events`: operator decisions and power-domain events.
- `ups_telemetry_snapshots`: raw UPS status snapshots and selected normalized fields.
- `capability_profile_matches`: profile resolution outcomes and diagnostics.
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
streams, profile-match records, accepted and rejected planner compilation records, shutdown
decision records, and durable executor progress.

The writer uses a narrow generic SQL executor interface, so CNPG and external PostgreSQL are
connection-management choices rather than separate domain models. The storage resolver in
`internal/storage` keeps `Disabled`, `ExternalPostgres`, and `CNPG` selection separate from
domain validation and controller status.

`PowerManagementCluster` reconciliation opens the configured audit store, pings PostgreSQL,
applies bundled migrations, and records durable reconciliation events after status updates. Accepted
`ShutdownFlow` reconciliations record planner compilation rows, capability profile match rows,
trigger decisions, and eligible dry-run execution evidence through the referenced
`PowerManagementCluster` storage backend. Rejected `ShutdownFlow` reconciliations record a
compilation row with diagnostics and no accepted plan hash. Executor implementations use the
execution, wave, group, action-attempt, release, handoff, and resume-state tables to make shutdown
progress auditable and resumable without putting PostgreSQL on the host actuation boundary.
External PostgreSQL requires TLS by default. CNPG mode reads the generated application credential
Secret and prefers the FQDN URI when present.
