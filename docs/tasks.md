# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Work is organized by component so it can be picked up independently. Items spanning two components
are listed under their primary owner with a cross-reference.

**This file is for doing work, not for explaining it.** Each component gets what it owns and what is
left on it, and nothing else. Rationale lives where it was worked out: design docs say what a thing
*is*, [decision-index.md](design/decision-index.md) holds settled decisions, and `docs/audits/` holds
findings and evidence. An entry that needs a paragraph to justify itself belongs in one of those
files with a one-line pointer left behind.

Completed work is not tracked here at all. Design docs are written as implemented, so a requirement
described in one is a requirement that exists — that is the record, and repeating it here would be a
second copy to keep in sync. Closed decisions are in
[scope-boundaries.md](design/scope-boundaries.md); findings and their fixes are in `docs/audits/`. A
component with nothing outstanding says `None.` and stops, with no notes about what future work might
look like, because that is speculation wearing a task's clothes.

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

None.

---

### Capability Profiles

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/profile-source design surface. Design docs:
`docs/design/capability-profiles.md`.

None.

---

### Planning & Execution Logic

Owns: `internal/planner` (pure compile), `internal/executor` (wave execution/evidence),
`internal/kubeactions` (action runner), and `internal/shutdownflow` plus the `ShutdownFlow`
controller wiring that connects them. Design docs: `planner-requirements.md`,
`executor-requirements.md`, `shutdown-flow.md`, `adaptive-execution-tier-pointer.md`,
`settled-questions.md`.

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
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`).

None.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`. Design doc: `docs/design/node-agent-operand.md` (`NA-n`). Audits:
`docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`, `F-33`–`F-36`, `F-54`–`F-92`, `OD-37`).

None.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

- Publish communication-ordering artifacts once the planner consumes `carries` ordering (see Planning
  & Execution Logic).

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/design/audit-storage-schema.md`.

None.

---

### Operator Maturity & Hardening

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

- Enable branch protection on `main` at release. Deliberately off during build: every CI check
  exists and passes, and requiring them would only add a merge round-trip to a single-maintainer
  repository that is still changing shape daily. This is a release gate, not a gap — the checks to
  require are already there, so turning it on is a repository-settings change and nothing else.
  Recorded here because this section previously described it as already in place.

---

### Telemetry & Triggers

Owns: NUT protocol polling (`internal/nut`), normalization (`internal/telemetry`), poll composition
(`internal/polling`), and trigger evaluation (`internal/trigger`). Design docs:
`telemetry-and-triggers.md`, `resiliency-and-partitions.md`.

None.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

The `docs/` set is reorganized around the reader: `guides/` for doing things, `decisions/` for the
judgement calls only the operator can make, `reference/` for lookup, and `design/` plus `audits/` for
contributors. [docs/README.md](README.md) is the landing page and carries a first-hour path. Modeled
on the Cluster API Book for structure and cert-manager for the front door. What is left:

- Decide the delivery format. A published site (Cluster API uses mdBook) versus rendered-on-GitHub
  markdown is a real trade — a site gives navigation and search, and adds a build, a deploy target,
  and a way for docs to break independently of the code. Not obviously worth it pre-v1.
- Adopt a per-page audience tag, the way each page already carries `Components:`, so a page states
  who it is for and drift is visible at the point of reading.
- Revisit `install.md` once the guides section has more than one page in it. At 443 lines it is
  prerequisites, two install paths, a configuration walkthrough, upgrade and uninstall order,
  troubleshooting, and the actuation-verification procedure in one file, which is more than one
  guide's worth of reader.

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
