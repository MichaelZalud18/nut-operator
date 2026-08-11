# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Work is organized by component so it can be picked up independently. Items spanning two components
are listed under their primary owner with a cross-reference.

**This file is for doing work, not for explaining it.** Entries are actionable lines — what to do,
what blocks it, and the identifier to read for why. Rationale lives where it was worked out: design
docs say what a thing *is*, [decision-index.md](design/decision-index.md) holds settled decisions,
and `docs/audits/` holds findings, evidence, and proposed directions. Built is a receipt: one
paragraph plus the identifiers it closed. An entry here that needs a paragraph to justify itself
belongs in one of those files with a one-line pointer left behind.

Work deliberately targeted after v1 lives in [tasks-post-v1.md](tasks-post-v1.md) so this file
stays answerable to one question: what is left before v1. Items move there only when something
outside the project gates them or scope-boundaries places them beyond v1 — never merely because
they are hard or unscheduled. Declined work is recorded where it was declined, not parked here.

Last reviewed: 2026-08-10

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/design/inventory-provider-contract.md` (`IN-n`).

#### Built

The `internal/inventory` pure compiler, all four inventory CRDs with webhooks and validators,
numbered shutdown tiers, and compilation wired into `ShutdownFlow` with the topology hash in plan
identity.

Closed: `IN-1`, `IN-3`, `IN-5`, `IN-7`, `IN-9`–`IN-14`, `IN-16`, `OD-4`, `OD-16`.

#### Open Work

- Wire derived domain membership into wave compilation. Blocked on `OD-14`.
- Feed trigger evaluation from the derived closure instead of live telemetry snapshots.
- Wire `carries`-based ordering (`PL-21`). Blocked — see Planning & Execution Logic.
- `SB-8` NetBox provider. Deliberately last: nothing depends on it, and an integration against an
  external source of truth is worth little until the rest is stable.

---

### Capability Profiles

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/provenance design surface. Design docs:
`docs/design/capability-profiles.md`.

#### Built

`UPSCapabilityProfile` and `PDUCapabilityProfile` CRDs over one shared five-tier match precedence
chain, firmware-scoped quirks, telemetry aliasing, bundled UPS and PDU catalogs at `1.0.0` with drift
tests, and `UPSCapabilityProbe` advisory drafting with probe history.

Closed: `F-25`, `F-26`, `RS-7`–`RS-10`, `PL-30`, `OD-22`, `OD-23`, `OD-25`, `OD-31`.

#### Open Work

- `F-27` define the actuation verification lifecycle
  ([quirks-aliasing-firmware.md](audits/quirks-aliasing-firmware.md)).
- `OD-21` decide driver configuration ownership — profile vs. `UPSDevice` spec, then implement.
- `OD-26` decide whether provenance ever gates resolution, then implement.

---

### Planning & Execution Logic

Owns: `internal/planner` (pure compile), `internal/executor` (wave execution/evidence),
`internal/kubeactions` (action runner), and `internal/shutdownflow` plus the `ShutdownFlow`
controller wiring that connects them. Design docs: `planner-requirements.md`,
`executor-requirements.md`, `shutdown-flow.md`, `adaptive-execution-tier-pointer.md`.

#### Built

`internal/planner` pure compilation with diagnostics reaching status and audit, `internal/executor`
wave execution with restart-safe resume, `internal/kubeactions` enforce-mode actions gated on
node-agent coverage, planner artifacts with diagram exports, and `ShutdownFlow` dry-run dispatch.
`internal/adaptive` is the pure tier-pointer and timing-mode model (`AE-1`–`AE-6`), wired into the
executor: power is re-read at every wave boundary, the pointer follows each compiled wave's tier, and
declared timeouts and `Wait` durations are compressed by a ratio measured from remaining runtime over
remaining declared plan. Pointer and mode persist in `executor_resume_states` and publish on
`status.lastExecution.adaptive`. Power returning mid-flow suspends the execution without restoring
anything, leaving the pointer unlatched at its depth so a second dip resumes from there. `Gate` is
removed from the action enum; `Notify` emits a Kubernetes Event. Tier inversion is published as
`nutoperator_shutdownflow_tier_inversions`, and the AE-5 cadence heartbeat as
`nutoperator_shutdownflow_publish_timestamp_seconds` plus `status.lastPublishTime`.

Closed: `PL-19`, `PL-20`, `AE-5`, `EX-11`, `EX-14`, `EX-22`, `EX-23`, `EX-24`, `OD-4`, `OD-17`,
`OD-18`, `F-31`, `F-42`.

#### Open Work

- `F-44` make the pre-shutdown hook engine-neutral, and record the scope limit as `SB-15`
  ([pre-shutdown-hook-transport.md](audits/pre-shutdown-hook-transport.md)). Design the `ShutdownHook`
  resource before building; the dry-run and hook-waiting questions are open there.
- Revalidate node clearance at execution, not only at compile time (`OD-11`, `PL-43`).
- `OD-27` confirm the adaptive defaults against a real outage. The compression amount is measured, so
  what is left to settle is the 20% runtime reserve (it stands in for a handoff tail nobody has
  timed) and the 10% minimum compression (the point at which the plan is declared not to fit).
- `OD-29` decide whether suspending on power recovery should require the condition to hold. Ascent is
  free, but suspension stops the remaining waves, so a brief flicker back to mains currently pauses a
  shutdown that should continue.
- `OD-30` decide whether cadence is global or per-flow.
- Fold the `AE-n` identifiers into real `PL`/`EX` numbers.
- `OD-12` decide the infeasible-plan policy (reject/warn/truncate), then implement (`EX-3`).
- `OD-14` decide partial-domain outage plan scope, then wire domain membership into wave compilation
  (shared with Inventory System and Telemetry & Triggers).
- Accept node-selector *requirements* for node targeting so a group can express a tier range.
  `metav1.LabelSelector` has no numeric comparison; `corev1.NodeSelector` supports `Gt`/`Lt`.
  Namespace and workload targeting cannot gain this.
- Envtest coverage for executor resume across a restart mid-flow (`EX-14` is asserted by design, not
  by test).
- `PL-21` communication-path edges stay unwired until a network device can be an actuation target
  (`OD-24` makes switches topological-only). Revisit with PDU outlet control.

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`);
relevant findings from `docs/audits/nut-usage-audit.md` (`F-20`–`F-22`, `F-24`).

#### Built

`NUTServer` operand rendering with injection-validated NUT config, `credentialSecretRef` wiring, NUT
protocol TLS proven end to end against operands built from source on OpenSSL, scripted `dummy-ups`
simulation, and the `snmpsim` driver-conformance fixture. e2e covers `dummy-ups`, `snmp-ups`, and
`NUTServer`; real actuation stays out of scope for `kind`.

Closed: `F-15`–`F-18`, `F-21`, `F-23`, `F-24`, `F-37`, `F-39`, `F-40`, `OD-32`. `F-19` declined —
it only matters with an HA `upsd` topology, which is not designed.

#### Open Work

- `F-41` document `verifyClientCertificates` as inert, and decide whether admission should reject
  `true` ([nut-usage-audit.md](audits/nut-usage-audit.md)).
- `F-43` add the predicate-scoped `UPSDevice` watch and a mapping watch from credential Secrets
  ([reconciler-watch-scoping.md](audits/reconciler-watch-scoping.md)).
- Advanced driver-specific configuration for the operand render path.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`. Audit: `docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`, `F-33`–`F-36`).

#### Built

`NodePowerAgent` DaemonSet rendering in `MonitorOnly`/`DryRun`/`Actuate`, `power-signal-writer` as
the `SHUTDOWNCMD` binary, `internal/nodeagent` signal validation with TTL and node-name enforcement,
and `cmd/node-actuator`'s syscall-backed poweroff. Handoff proven on `kind` within the configured
TTL.

Closed: `F-8`–`F-14`, `F-24`, `F-33`–`F-36`.

#### Open Work

None.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

#### Built

A single structured planner artifact, the dependency graph as normalized vertices/edges with
provenance and explanations, deterministic Mermaid/Graphviz/D2 renderers, advisory startup wave
projections, and `internal/metrics` on controller-runtime's registry. Kubernetes-first interface
only — CRDs, status, Events, logs, PostgreSQL, no UI.

Closed: `PL-45`–`PL-48`, `OD-1`, `OD-5`, `F-3`.

#### Open Work

- Add telemetry-poll, capability-match, and inventory-compiler metrics (`docs/metrics.md` Open Work).
- Thread the planner's real diagnostic classes into `compile_total`'s `result` label.
- `F-6` document `ExecutionReady`/`TriggerEligible` as public condition types — bespoke, and users
  will alert on them.
- Publish the richer graph/domain artifacts once the planner consumes derived domains and
  communication ordering (see Planning & Execution Logic).
- Write a worked example of an external subscriber consuming the published artifacts.

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/design/audit-storage-schema.md`.

#### Built

Backend resolution for `Disabled`/`ExternalPostgres`/`CNPG`, the full audit schema through migration
5 with retention enforcement, planner compilations persisted as JSONB, and the shutdown-time spool
with replay, bounded by `spec.storage.auditSpool.maxSize`.

Closed: `OD-6`, `OD-15`.

#### Open Work

None.

---

## Cross-Cutting

Work that doesn't belong to one component — either because it spans several, or because it's about
the operator as a whole. Components whose remaining work is entirely blocked on another section are
parked here too, so the tracker reads front to back in roughly the order work can actually be done.

### Operator Maturity & Hardening

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

#### Built

`observedGeneration`, enum validation, and spec/status separation across all CRDs; project-owned
multi-arch operand images with SBOM, provenance, and scanning; CI with distinct check names, path
filters, tidy-drift, ASH, and an RFC1918 scan; `main` protected with every check required and
`enforce_admins` on; and the no-cert-manager install path (`config/byo-cert`, `hack/webhook-cert.sh`)
verified on `kind`.

Serving-certificate expiry is published as `nutoperator_certificate_not_after_timestamp_seconds`; the
byo-cert install path is covered end to end on `kind`, including rotation; and the ASH scan runs
every scanner that can contribute here — `grype` and `syft` installed from pinned checksum-verified
archives, `cfn-nag`/`cdk-nag`/`opengrep` excluded by decision.

Closed: `F-1`–`F-5`, `F-7`, `F-28`–`F-32`, `F-38`.

#### Open Work

- Wire keyless Sigstore signing as a release gate, with cosign verification docs and digest-pinned
  examples (`docs/images.md` describes the target state).
- Automate triage of new unsuppressed medium-or-higher ASH findings.

---

### Telemetry & Triggers

*Sequenced last: every open item here waits on Planning & Execution Logic or Inventory
System landing first, so the section is placed at the end of the tracker deliberately.*

Owns: NUT protocol polling (`internal/nut`), normalization (`internal/telemetry`), poll composition
(`internal/polling`), and trigger evaluation (`internal/trigger`). Design docs:
`telemetry-and-triggers.md`, `resiliency-and-partitions.md`.

#### Built

`internal/nut` as a real protocol client rather than an `upsc` wrapper, `internal/telemetry`
normalization with profile-declared aliases, `internal/polling` per-target transport, `internal/trigger`
pure evaluation wired into `ShutdownFlow`, and `dummy-ups` repeater mode for upstream NUT appliances.

Closed: `F-22` relay half, `F-25` runtime half, `OD-9`, `AE-6`.

#### Open Work

- `OD-14` partial-domain outage plan scope — owned in Planning & Execution Logic, blocks trigger
  firing against multi-domain topology.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

#### Built

Component-scoped design docs with stable identifier namespaces, governing principles and scope
boundaries, the decision index, the references under `docs/`, and the audit records under
`docs/audits/`.

#### Open Work

- Redraw the example pod placement diagram into `docs/diagrams/`. Blocked on deciding node naming.
- Define how the Orion example's string tier labels (`application`/`data`/`storage`) coexist with
  `OD-4` numbered tiers. Numbered tiers win, but named tags still occur in practice.

---

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
