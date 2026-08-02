# Telemetry Normalization

`internal/telemetry` is the pure normalization layer for NUT variable snapshots. It does not own
TCP, `upsc`, Kubernetes status updates, database connections, or shutdown policy decisions.

## Boundary

Inputs are raw NUT variable maps plus the local UPS identity:

- `ups.status`
- `battery.charge`
- `battery.runtime`
- `ups.load`
- all other raw variables, preserved for audit

Outputs are stable policy/audit facts:

- normalized status symbols
- `Online`, `OnBattery`, `LowBattery`, `Stale`, `Unavailable`, or `Unknown` phase
- battery charge, runtime seconds, and load percentage when parseable
- non-fatal diagnostics for missing status, unknown status symbols, and bad numeric values
- an audit adapter for `ups_telemetry_snapshots`

## Status Handling

NUT exposes `ups.status` as space-separated status symbols. The normalizer treats the standard NUT
symbols as meaningful, preserves unknown symbols, and emits a warning instead of rejecting the
snapshot. That keeps future/vendor-specific statuses from breaking reconciliation.

## Next Work

The next layer should provide a transport-backed NUT poller that fetches variables from a selected
`NUTServer`, feeds this normalizer, updates `UPSDevice` status, and records telemetry snapshots
through the referenced `PowerManagementCluster` audit store.

## References

- [RFC 9271: UPS Management Protocol](https://www.rfc-editor.org/rfc/rfc9271.html)
- [NUT current driver developer guide](https://networkupstools.org/docs/developer-guide.chunked/new-drivers.html)
