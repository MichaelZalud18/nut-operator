# Telemetry and Triggers

Components: Telemetry & Triggers, Planning & Execution Logic.
Audience: contributors.

The detect-side pipeline, end to end: read NUT variables, normalize them into stable facts,
then evaluate `ShutdownFlow` trigger conditions against those facts. Normalization and trigger
evaluation are separate pure packages but one continuous path, and they were previously two
documents that had to be read together.

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
snapshot. That keeps vendor-specific statuses from breaking reconciliation.

## Polling

The transport layer speaks the NUT protocol directly rather than shelling out to `upsc`. It uses the
`LIST VAR <upsname>` command against TCP port 3493 and parses the documented `BEGIN LIST VAR`,
`VAR ... "<value>"`, `END LIST VAR` response. It never authenticates or issues administrative
commands.

A real client rather than a wrapper, because the wrapper cannot report what this needs. `upsc`
collapses every failure into an exit code and a line on stderr, so "the server refused the
connection", "the device is not in this server's list", and "the driver is not answering" arrive
indistinguishable — and those need different conditions on `UPSDevice.status`. It also puts a
subprocess on the polling path, which is the wrong shape for something that runs per device per
interval and must keep working while a cluster is losing power.

The polling layer accepts an already-resolved NUT target, calls the transport, normalizes the raw
variables, and exposes an audit adapter for `ups_telemetry_snapshots`.

## Controller Wiring

`UPSDevice` reconciliation resolves a ready selected `NUTServer`, polls its in-cluster Service
endpoint, updates `UPSDevice.status`, and records successful snapshots through the referenced
`PowerManagementCluster` audit store when storage is ready. Audit write failures are logged but do
not block status updates.

## Runtime Integration

Runtime integration evaluates telemetry against shutdown triggers, emits Kubernetes Events and
metrics, and records durable telemetry through a writer path suitable for high polling volume.


## Trigger Evaluation

`internal/trigger` evaluates `ShutdownFlow` trigger conditions against the normalized state
above. It is pure logic: callers provide the observation time, current UPS states, trigger
definitions, and any prior hold state.

### Trigger Boundary


Inputs:

- trigger definitions
- normalized UPS phase, charge, runtime, derived power-domain membership, and stale markers
- prior hold state for `spec.triggers[].for`
- caller-provided observation time

Outputs:

- one decision per trigger
- an overall eligible/not-eligible result
- selected UPS devices for eligible triggers
- next hold state
- structured diagnostics

The package does not read Kubernetes resources, poll NUT, write audit records, start executors, or
read the wall clock.

### Trigger Semantics

A trigger is eligible when at least one selected UPS satisfies the trigger condition and its optional
hold duration has elapsed. `upsDeviceRefs` and `powerDomains` narrow selection; an empty selector
means all supplied UPS states. Domain selection uses the resolver-derived `feeds` closure when the
controller has resolved topology, not the authored `UPSDevice.spec.powerDomains` root labels. The
authored labels are only a fallback for pure package callers without resolver context.

`RuntimeBelow` and `ChargeBelow` require both a configured threshold and matching telemetry. Missing
thresholds are errors. Missing telemetry is a warning and does not produce an optimistic match.

### Trigger Runtime Integration

The controller layer adapts `ShutdownFlow.spec.triggers` and current `UPSDevice.status` into this
package, persists hold state, records `shutdownflow_decisions`, and dispatches eligible trigger
episodes to the executor. `ShutdownFlow.status.lastExecution.deduplicationKey` prevents repeated
execution while the same trigger episode remains eligible; the key becomes reusable after the
trigger clears and later becomes eligible again. Execution stays dry-run unless the flow and
node-agent actuation gates are both approved.

## References

- [RFC 9271: UPS Management Protocol](https://www.rfc-editor.org/rfc/rfc9271.html)
- [NUT current driver developer guide](https://networkupstools.org/docs/developer-guide.chunked/new-drivers.html)
