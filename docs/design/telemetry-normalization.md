# Telemetry Normalization

`internal/telemetry` is the pure normalization layer for NUT variable snapshots. `internal/nut`
owns the narrow NUT protocol client for read-only variable polling. `internal/polling` composes
those packages into one target-oriented telemetry poll. None of these packages owns Kubernetes
status updates, database connections, or shutdown policy decisions.

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

## Polling

The transport layer uses the NUT `LIST VAR <upsname>` command against TCP port 3493 and parses the
documented `BEGIN LIST VAR`, `VAR ... "<value>"`, `END LIST VAR` response. It never authenticates
or issues administrative commands.

The polling layer accepts an already-resolved NUT target, calls the transport, normalizes the raw
variables, and exposes an audit adapter for `ups_telemetry_snapshots`.

## Controller Wiring

`UPSDevice` reconciliation resolves a ready selected `NUTServer`, polls its in-cluster Service
endpoint, updates `UPSDevice.status`, and records successful snapshots through the referenced
`PowerManagementCluster` audit store when storage is ready. Audit write failures are logged but do
not block status updates.

## Next Work

The next layer should evaluate telemetry against shutdown triggers, emit Kubernetes Events/metrics,
and move durable telemetry recording behind a long-lived writer/cache suitable for high polling
volume.

## References

- [RFC 9271: UPS Management Protocol](https://www.rfc-editor.org/rfc/rfc9271.html)
- [NUT current driver developer guide](https://networkupstools.org/docs/developer-guide.chunked/new-drivers.html)
