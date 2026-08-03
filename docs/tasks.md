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
- Planner artifact publication for `ShutdownFlow` status: normalized dependency graph,
  edge/source explanations, advisory startup wave projection, and deterministic Mermaid,
  Graphviz/DOT, and D2 exports.
- `ShutdownFlow` trigger evaluation wired into reconciliation from `UPSDevice` telemetry status,
  including compact hold-state persistence, `TriggerEligible` status condition, status-visible
  per-trigger decisions, UPS status-change watches, approval-annotation watches, and
  `shutdownflow_decisions` audit writes.
- `ShutdownFlow` dry-run execution dispatch wired from eligible trigger decisions to
  `internal/executor`, with `status.lastExecution`, active-trigger deduplication, durable execution
  evidence, and `ExecutionReady` condition updates.
- Kubernetes-first interface decision: no dedicated v1 UI; CRDs, status, Events, logs, GitOps, and
  PostgreSQL records are the project interface.
- Resiliency contract for API, PostgreSQL, NUT, telemetry, and node-agent partitions documented as
  planned behavior: lost connectivity degrades certainty rather than granting optimistic action.
- PostgreSQL audit schema and storage boundary for power events, telemetry snapshots, capability
  matches, capability profile verification/probe history, accepted and rejected planner
  compilations, shutdown decisions, executor runs, wave/group progress, action attempts, node
  release records, signal-file handoff evidence, and executor resume state.
- Planner compilation audit records persist compiled waves, dependency graph, advisory startup
  waves, explanations, and diagram exports as structured PostgreSQL JSONB payloads.
- PostgreSQL retention enforcement for audit/event records and telemetry snapshots from
  `spec.storage.retention`, evaluated by the `PowerManagementCluster` storage readiness path.
- Standalone `internal/executor` package for ordered wave execution evidence, dry-run action
  attempts, node release records, signal-file handoff records, and resume-state updates.
- Kubernetes action runner boundary for enforce-mode `ScaleWorkload`, `CordonNodes`, `DrainNodes`,
  provider-neutral `RunWorkflow` hooks, and `AgentShutdown` release validation. The
  `ShutdownFlow` controller enumerates concrete workloads, nodes, and namespaces at execution time.
- Node actuator signal handling validates structured shutdown JSON, enforces signal TTL and
  node-name matching, skips dry-run `SystemdPoweroff` signals, and supports command-backed
  poweroff execution behind the still-blocked host actuation rendering gate.
- Node-agent rendering gives `upsmon` a writable runtime mount at `/run` while keeping the root
  filesystem read-only and preserving the shared `/run/power-agent` handoff directory.
- Storage backend resolution for `Disabled`, `ExternalPostgres`, and `CNPG` modes without locking
  callers to a CNPG-only database path.
- Project-maintained capability profile catalog under `config/catalog/`, including Ubiquiti UniFi
  UPS Tower and UPS 2U product-family profiles.
- Project-owned operand Dockerfiles for `nut-server`, `upsmon-agent`, and `node-actuator`.
- Source hardening pass for ASH/Checkov-authored findings: explicit helper admin RBAC verbs,
  non-default base namespaces for leader-election RBAC, manager pull policy, documented manager
  service-account token/digest exceptions, Kustomize image placeholder repair, and Dockerfile
  healthcheck instructions.
- Local AWS Labs ASH security scan configuration and `make security-scan` target.
- Public-safe sample manifests and the Orion example topology.

## Open Build Items

- Resolve shutdown-time audit durability: local spool, audit-store last-ditch placement, or
  documented preference and test coverage for `ExternalPostgres`.
- Implement partition-aware node-agent heartbeat/status and executor progress reasons for
  unreachable nodes.
- Complete real host-actuation deployment: cluster-to-node signal delivery, approved
  `SystemdPoweroff` rendering, and the minimal host access profile for real poweroff.
- Harden release images with signing policy, cosign verification docs, and immutable digest
  production examples.
- Re-run ASH after each hardening pass and triage every unsuppressed medium-or-higher finding.
- Decide whether full ASH coverage runs through container mode or locally installed `grype`, `syft`,
  `opengrep`, `cfn-nag`, and `cdk-nag` dependencies.
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
- Day-to-day operation works with CRDs, GitOps, `kubectl`, Events, logs, and audit records; no
  embedded dashboard is required for v1.
