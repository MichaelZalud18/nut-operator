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

Last reviewed: 2026-08-17

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/design/inventory-provider-contract.md` (`IN-n`).

#### Built

The `internal/inventory` pure compiler, all four inventory CRDs with webhooks and validators,
numbered shutdown tiers, derived power-domain closure carried into planner inputs and runtime trigger
evaluation, and compilation wired into `ShutdownFlow` with the topology hash in plan identity.
Contract and rationale: [inventory-provider-contract.md](design/inventory-provider-contract.md).

Closed: `IN-1`, `IN-3`, `IN-5`, `IN-7`, `IN-9`–`IN-14`, `IN-16`, `OD-4`, `OD-16`, `F-81`,
`F-82`, `OD-14`.

#### Open Work

None.

---

### Capability Profiles

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/profile-source design surface. Design docs:
`docs/design/capability-profiles.md`.

#### Built

`UPSCapabilityProfile` and `PDUCapabilityProfile` CRDs over one shared five-tier match precedence
chain, firmware-scoped quirks, telemetry aliasing, bundled catalogs at `1.0.0` with drift tests,
order-independent profile hashing, `status.capability` publication, and `UPSCapabilityProbe` advisory
drafting with probe history. PDU matching is scaffolding per `OD-25`. Full treatment:
[capability-profiles.md](design/capability-profiles.md).

Closed: `F-25`, `F-26`, `F-79`, `F-80`, `F-83`, `F-84`, `RS-7`–`RS-10`, `PL-30`, `OD-21`, `OD-22`,
`OD-23`, `OD-25`, `OD-31`. `OD-26` dropped — the `provenance` field whose semantics it decides was never
built, and `ProfileSource` already draws the only distinction that exists.

#### Open Work

None currently tracked. Future capability-profile work should start from new device evidence or
from the deferred instant-command/PDU actuation decisions rather than from this section.

---

### Planning & Execution Logic

Owns: `internal/planner` (pure compile), `internal/executor` (wave execution/evidence),
`internal/kubeactions` (action runner), and `internal/shutdownflow` plus the `ShutdownFlow`
controller wiring that connects them. Design docs: `planner-requirements.md`,
`executor-requirements.md`, `shutdown-flow.md`, `adaptive-execution-tier-pointer.md`,
`settled-questions.md`.

#### Built

`internal/planner` pure compilation with diagnostics reaching status and audit, `internal/executor`
wave execution with restart-safe resume, `internal/kubeactions` enforce-mode actions gated on
node-agent coverage, planner artifacts with diagram exports, and `ShutdownFlow` dispatch in both
modes. `internal/adaptive` is the pure tier-pointer and timing-mode model wired into the executor,
persisting to `executor_resume_states` and publishing on `status.lastExecution.adaptive`. Tier-overrun
policy, rehearsal execution, history-informed estimates, node-clearance revalidation, partial-domain
scope, and `ShutdownHook`/`RunHook` delivery are all built. `spec.groups[].phase` is removed, so
ordering is tiers plus `requires`/`before`/`after` and a wave holds every group whose dependencies
are satisfied.

Requirements and rationale: [planner-requirements.md](design/planner-requirements.md),
[executor-requirements.md](design/executor-requirements.md),
[shutdown-flow.md](shutdown-flow.md),
[adaptive-execution-tier-pointer.md](design/adaptive-execution-tier-pointer.md),
[shutdown-hooks.md](design/shutdown-hooks.md).

Closed: `PL-19`, `PL-20`, `PL-43`, `CR-4`, `EX-9`, `EX-11`, `EX-14`, `EX-22`–`EX-33`, `OD-4`,
`OD-11`, `OD-12`, `OD-14`, `OD-17`, `OD-18`, `OD-29`, `OD-30`, `OD-33`, `OD-34`, `OD-38`, `SB-15`,
`HK-1`–`HK-10`, `F-31`, `F-42`, `F-44`.

#### Open Work

- `OD-27` confirm the adaptive defaults against a real outage. The compression amount is measured, so
  what is left to settle is the 20% runtime reserve (it stands in for a handoff tail nobody has
  timed) and the 10% minimum compression (the point at which the plan is declared not to fit).
  Measured `sync(2)` duration on the halt path is now direct evidence for that tail — one input, not
  a closure: confirming the reserve still needs a real outage. Both halves of that evidence now
  exist: the actuator's own precise measurement, which dies with the node, and the operator-side
  `nutoperator_halt_duration_seconds` reconstruction, which is coarser and survives it.
- Feed node metrics into the estimates alongside execution history — draw and capacity readings
  sharpen the runtime side of the comparison the same way observed durations sharpen the plan side.
- `PL-21` communication-path edges stay unwired until a network device can be an actuation target
  (`OD-24` makes switches topological-only). Revisit with PDU outlet control.

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`,
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`, `F-85`); relevant findings from `docs/audits/nut-usage-audit.md`
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`). The task lines below are pointers; the evidence and the
recommended order are in the audits.

#### Built

`NUTServer` operand rendering with injection-validated NUT config, `credentialSecretRef` wiring,
NUT protocol TLS proven end to end against operands built from source on OpenSSL, scripted
`dummy-ups` simulation, and the `snmpsim` driver-conformance fixture. Health reporting, startup,
driver supervision, in-place reload, and the allowlist/image pinning are described as built in
[nut-server-operand.md](design/nut-server-operand.md) (`NS-1`–`NS-9`). e2e covers `dummy-ups`,
`snmp-ups`, and `NUTServer`; real actuation stays out of scope for `kind`.

Closed: `F-15`–`F-18`, `F-21`, `F-23`, `F-24`, `F-37`, `F-39`–`F-41`, `F-43`, `F-46`, `F-47`,
`F-48`–`F-51`, `F-53`, `F-76`, `F-85`, `NS-1`–`NS-9`, `OD-21`, `OD-32`, `OD-36`. `F-19`
declined — it only matters with an HA `upsd` topology, which is not designed.

#### Open Work

None.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`. Design doc: `docs/design/node-agent-operand.md` (`NA-n`). Audits:
`docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`, `F-33`–`F-36`, `F-54`–`F-92`, `OD-37`).
The design doc says what the operand is, the audit holds the evidence, and this section is a
receipt.

#### Built

The DaemonSet renders in `MonitorOnly`/`DryRun`/`Actuate` with `power-signal-writer` as the
`SHUTDOWNCMD` binary, `internal/nodeagent` signal validation, and `cmd/node-actuator`'s
syscall-backed poweroff, proven on `kind` within the configured TTL. The authorization boundary,
signal lifecycle, permitted-only `CAP_SYS_BOOT` model, readiness contract, bounded pre-halt `sync(2)`
with executor-driven `skipSync`, and the per-gate halt trace are described as built in
[node-agent-operand.md](design/node-agent-operand.md) (`NA-1`–`NA-9`). `make verify-actuation` proves
the path end to end on real hardware; see [install.md](install.md).

Closed: `F-8`–`F-14`, `F-24`, `F-33`–`F-36`, `F-54`–`F-60`, `F-61`–`F-65`,
`F-66`, `F-67`–`F-71`, `F-72`, `F-73`–`F-75`, `F-86`–`F-88`, `F-90`–`F-92`, `NA-1`–`NA-9`, `OD-37`. `F-89` declined — the signal Secret
mounts whole on every node, but the payload carries no credentials and the node-name check holds it
to exposure rather than actuation; recorded in
[node-agent-daemonset-audit.md](audits/node-agent-daemonset-audit.md).

#### Open Work

None.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

#### Built

A single structured planner artifact, the dependency graph as normalized vertices/edges with
provenance and explanations, resolver-derived power-domain closure artifacts, deterministic
Mermaid/Graphviz/D2 renderers, advisory startup wave projections, and `internal/metrics` on
controller-runtime's registry. Delivery is Kubernetes API watch over `ShutdownFlow.status`, plus
Events, logs, and PostgreSQL — no UI and no bundled broker. Operator-side halt verification
(`internal/haltwatch`, the `nodehalt` reconciler) records what a powered-off node cannot report
itself. Every metric and its rationale: [metrics.md](metrics.md).

Closed: `PL-45`–`PL-48`, `OD-1`, `OD-5`, `F-3`, `F-6`.

#### Open Work

- Publish communication-ordering artifacts once the planner consumes `carries` ordering (see Planning
  & Execution Logic).

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
multi-arch operand images with SBOM, provenance, digest-pinned bases, signature-verified NUT source,
and keyless cosign signing; the `main` tag applied only to a digest e2e and the TLS smoke test have
both run against; Kubernetes `/healthz`/`/readyz` as the manager readiness contract; and the
no-cert-manager install path verified on `kind` including rotation. CI carries distinct check names,
path filters, tidy-drift, ASH with actionable-finding extraction, and a `Repo Hygiene` workflow with
no path filter at all — sample-schema validation, an RFC1918 scan, and installer freshness. Evidence:
[operator-maturity-benchmarks.md](audits/operator-maturity-benchmarks.md), [images.md](images.md).

Closed: `F-1`–`F-5`, `F-7`, `F-28`–`F-32`, `F-38`, `F-52`, `F-77`, `F-78`.

#### Open Work

- Enable branch protection on `main` at release. Deliberately off during build: every CI check
  exists and passes, and requiring them would only add a merge round-trip to a single-maintainer
  repository that is still changing shape daily. This is a release gate, not a gap — the checks to
  require are already there, so turning it on is a repository-settings change and nothing else.
  Recorded here because this section previously described it as already in place.

---

### Telemetry & Triggers

*Sequenced last: every open item here waits on Planning & Execution Logic or Inventory
System landing first, so the section is placed at the end of the tracker deliberately.*

Owns: NUT protocol polling (`internal/nut`), normalization (`internal/telemetry`), poll composition
(`internal/polling`), and trigger evaluation (`internal/trigger`). Design docs:
`telemetry-and-triggers.md`, `resiliency-and-partitions.md`.

#### Built

`internal/nut` as a real NUT protocol client rather than an `upsc` wrapper, `internal/telemetry`
normalization with profile-declared aliases, `internal/polling` per-target transport,
`internal/trigger` pure evaluation wired into `ShutdownFlow`, domain-scoped trigger evaluation against
resolver-derived membership, and `dummy-ups` repeater mode for upstream NUT appliances. Boundary and
trigger semantics: [telemetry-and-triggers.md](design/telemetry-and-triggers.md),
[upstream-nut-relay.md](design/upstream-nut-relay.md).

Closed: `F-22` relay half, `F-25` runtime half, `OD-9`, `OD-14`, `CR-4`.

#### Open Work

None.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

#### Built

Component-scoped design docs with stable identifier namespaces, governing principles and scope
boundaries, the decision index and glossary, the references under `docs/`, the audit records under
`docs/audits/`, and the diagrams under `docs/diagrams/`. Worked examples are one tight
(`docs/examples/orion-cluster/`, every edge authored) and three loose
(`docs/examples/simulation/`, tiers only, wave structure derived); each carries its own README, and
every manifest in both is schema-validated in CI.

#### Open Work

None.

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
- A dry-run runs against real UPS hardware in a real cluster, not against `kind` and `dummy-ups`.
  Not reachable yet, and not expected to be until the sections above close.
- One node halted through `make verify-actuation`. Distinct from the dry-run gate above, not a
  replacement for it: a dry-run never renders the actuate configuration, so that gate can pass
  without `hostPID`, the file capability, or the host PID namespace ever having been exercised on a
  real kubelet.
- **Open:** whether a live plug-pull is also a v1 gate, or whether the dry-run above is the bar.
  Undecided in either direction — do not assume one while planning against it.
