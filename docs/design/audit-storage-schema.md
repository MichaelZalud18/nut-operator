# Audit Storage Schema

Production durable state is PostgreSQL, with CloudNativePG as the preferred in-cluster provider.
The current code packages the first PostgreSQL migration and PostgreSQL-shaped writer boundary in
`internal/audit`; it does not yet connect controller reconciliation to a live database.

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
database driver wiring.

The next storage step is controller-owned connection wiring that obtains the DSN/credentials for
CNPG or external PostgreSQL, applies migrations through the audit store, and records events without
putting PostgreSQL on the critical shutdown decision path.
