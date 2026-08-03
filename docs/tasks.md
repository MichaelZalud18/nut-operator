# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Architecture, security, API, and design documents describe the system in its finalized form. Current
build state, open implementation work, and validation gates live here so the architecture docs do
not become a progress diary.

Last reviewed: 2026-08-02

## Built

- Kubebuilder/controller-runtime scaffold with Apache-2.0 licensing and public project metadata.
- Nine cluster-scoped `power.zalud.io/v1alpha1` CRDs with status subresources:
  `PowerManagementCluster`, `UPSDevice`, `PowerInfrastructure`, `PowerInventoryNode`,
  `PowerInventoryEdge`, `UPSCapabilityProfile`, `NUTServer`, `NodePowerAgent`, and `ShutdownFlow`.
- Validation/status reconcilers for the CRD set, plus admission webhook code for the core safety
  combinations.
- `NUTServer` operand rendering for Namespace, ConfigMap, operator-managed Secret, Service,
  NetworkPolicy, Deployment, and upstream NUT relay mode through `dummy-ups`.
- `NodePowerAgent` operand rendering for Namespace, ServiceAccount, ConfigMap, Secret-backed
  `upsmon.conf`, egress NetworkPolicy, and non-privileged monitor/dry-run/stub DaemonSet modes.
- Pure packages for planner graph compilation, declarative inventory, capability matching, resolver
  assembly, telemetry normalization, NUT polling, and trigger evaluation.
- `ShutdownFlow` trigger evaluation wired into reconciliation from `UPSDevice` telemetry status,
  including compact hold-state persistence, `TriggerEligible` status condition, status-visible
  per-trigger decisions, UPS status-change watches, approval-annotation watches, and
  `shutdownflow_decisions` audit writes.
- `ShutdownFlow` dry-run execution dispatch wired from eligible trigger decisions to
  `internal/executor`, with `status.lastExecution`, active-trigger deduplication, durable execution
  evidence, and `ExecutionReady` condition updates.
- PostgreSQL audit schema and storage boundary for power events, telemetry snapshots, capability
  matches, accepted and rejected planner compilations, shutdown decisions, executor runs,
  wave/group progress, action attempts, node release records, signal-file handoff evidence, and
  executor resume state.
- Standalone `internal/executor` package for ordered wave execution evidence, dry-run action
  attempts, node release records, signal-file handoff records, and resume-state updates.
- Storage backend resolution for `Disabled`, `ExternalPostgres`, and `CNPG` modes without locking
  callers to a CNPG-only database path.
- Project-maintained capability profile catalog under `config/catalog/`, including Ubiquiti UniFi
  UPS Tower and UPS 2U product-family profiles.
- Project-owned operand Dockerfiles for `nut-server`, `upsmon-agent`, and `node-actuator`.
- Public-safe sample manifests and the Orion example topology.

## Open Build Items

- Add Kubernetes action runners for workload scaling, node cordon/drain, workflow hooks, and
  enforce-mode node-agent signal handoff.
- Resolve shutdown-time audit durability: local spool, audit-store last-ditch placement, or
  documented preference and test coverage for `ExternalPostgres`.
- Add probe-history persistence for capability drift checks, including firmware/profile verification
  records.
- Implement retention enforcement for PostgreSQL audit tables.
- Harden release images with smoke tests, SBOM generation, signing, scan policy, provenance, and
  immutable digest examples.
- Expand NUT operand rendering for credential rotation and advanced driver-specific config.
- Add controller/envtest coverage for PostgreSQL degradation and executor resume behavior.

## Validation Gates

- Pure packages pass deterministic unit tests without Kubernetes, NUT, PostgreSQL, or filesystem
  dependencies.
- Controller and webhook tests pass against envtest.
- Operand image smoke tests prove the packaged NUT binaries, entrypoints, users, root filesystems,
  and network-only defaults.
- Public-readiness scans show no private hostnames, private addresses, credentials, or site-specific
  topology.
- Alpha deployments run in dry-run by default and expose compiled plans, telemetry status, audit
  records, and approval-gate state before any host action is possible.
