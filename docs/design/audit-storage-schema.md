# Audit Storage Schema

Production durable state is PostgreSQL, with CloudNativePG as the preferred in-cluster provider.
The current code packages the first PostgreSQL migration and PostgreSQL-shaped writer boundary in
`internal/audit`, and `internal/storage` connects controller reconciliation to Secret-backed
PostgreSQL/CNPG credentials through the pgx `database/sql` driver.

## Tables

- `audit_schema_migrations`: applied schema versions.
- `power_events`: operator decisions and power-domain events.
- `ups_telemetry_snapshots`: raw UPS status snapshots and selected normalized fields.
- `capability_profile_matches`: profile resolution outcomes and diagnostics.
- `shutdownflow_compilations`: accepted or rejected planner compilations and compiled waves.
- `shutdownflow_decisions`: dry-run or enforce decisions for power-event triggers.

## Boundary

CR status remains the current summary and review surface. PostgreSQL holds history, telemetry
streams, profile-match records, planner compilation records, and shutdown decision records.

The writer uses a narrow generic SQL executor interface, so CNPG and external PostgreSQL are
connection-management choices rather than separate domain models. The storage resolver in
`internal/storage` keeps `Disabled`, `ExternalPostgres`, and `CNPG` selection separate from
domain validation and controller status.

`PowerManagementCluster` reconciliation now opens the configured audit store, pings PostgreSQL,
applies bundled migrations, and records durable reconciliation events after status updates. Accepted
`ShutdownFlow` reconciliations record planner compilation rows and capability profile match rows
through the referenced `PowerManagementCluster` storage backend. External PostgreSQL requires TLS by
default. CNPG mode reads the generated application credential Secret and prefers the FQDN URI when
present.

The next storage step is expanding audit writes into telemetry, rejected planner compilation, and
shutdown decision paths without putting PostgreSQL on the critical host-actuation path.
