# Kubernetes Inventory Adapter

`kubeinventory` reads declarative inventory, Kubernetes nodes, agent coverage, and capability
profiles through a caller-supplied `client.Reader`. It validates and converts API resources into
provider-neutral inputs, then calls the pure `internal/resolver` package. It also exposes the
device-scoped profile lookup used by telemetry polling and probing.

## Boundaries

- Controllers own reconciliation, resource lifecycle, status/conditions, and metrics publication.
- This package owns Kubernetes inventory reads, API conversions, and the existing inventory/profile
  validation shared by compilation and reconciliation. Admission validation remains separate.
- `internal/resolver`, `internal/inventory`, and `internal/capability` perform pure resolution,
  topology compilation, and matching without Kubernetes clients or other I/O.
- `internal/shutdownflow` converts resolved inputs into planner inputs, validates the authored plan,
  applies eligible execution scope, and selects duration history by the resulting plan hash.

The supplied reader determines cache behavior. A returned structural bundle is a compile-time
snapshot, not proof of current authorization, target membership, readiness, telemetry, or quorum.
Execution still requires the controller's uncached wave and per-release checks. This package does
not acquire actuation authority or add a cache of its own.

## Tests

Run `go test ./internal/kubeinventory ./internal/shutdownflow` for the adapters without a cluster
or controller test suite. These tests cover conversion, read failures/cancellation, validation
diagnostics, deterministic topology, node roles, communication dependencies, execution scope,
and history identity. Controller/executor regression tests cover their integration and fresh
release gates; adapter tests do not establish guest-shutdown behavior.

See [resolver requirements](../../docs/contributing/design/resolver-requirements.md) and the
[inventory contract](../../docs/contributing/design/inventory-provider-contract.md).
