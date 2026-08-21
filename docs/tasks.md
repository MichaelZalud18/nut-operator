# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Work is organized by component so it can be picked up independently. Items spanning two components
are listed under their primary owner with a cross-reference.

**This file is for doing work, not for explaining it.** Each component gets what it owns and what is
left on it, and nothing else. Rationale lives where it was worked out: design docs say what a thing
*is*, [decision-index.md](contributing/design/decision-index.md) holds settled decisions, and `docs/contributing/audits/` holds
findings and evidence. An entry that needs a paragraph to justify itself belongs in one of those
files with a one-line pointer left behind.

Completed work is not tracked here at all. Design docs are written as implemented, so a requirement
described in one is a requirement that exists — that is the record, and repeating it here would be a
second copy to keep in sync. Closed decisions are in
[scope-boundaries.md](contributing/design/scope-boundaries.md); findings and their fixes are in `docs/contributing/audits/`. A
component with nothing outstanding says `None.` and stops, with no notes about what future work might
look like, because that is speculation wearing a task's clothes.

Work deliberately targeted after v1 lives in [tasks-post-v1.md](tasks-post-v1.md) so this file
stays answerable to one question: what is left before v1. Items move there only when something
outside the project gates them or scope-boundaries places them beyond v1 — never merely because
they are hard or unscheduled. Declined work is recorded where it was declined, not parked here.

Last reviewed: 2026-08-20

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/contributing/design/inventory-provider-contract.md` (`IN-n`).

None.

---

### Capability Profiles

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/profile-source design surface. Design docs:
`docs/contributing/design/capability-profiles.md`.

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
- Read `executor_resume_states` back on startup. The rows now persist, but nothing loads them, so an
  executor interrupted mid-plan still restarts from the beginning. What it should do with the state
  it finds is an `OD-27` evidence-model question, not a lookup — see the `F-100` closure in
  `operator-maturity-benchmarks.md`.

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/contributing/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`,
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`, `F-85`); relevant findings from `docs/contributing/audits/nut-usage-audit.md`
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`).

- `F-97` find out why `dummy-ups` exits, and make the watchdog beat `DEADTIME`. The driver process
  terminates on its own every 4-176 seconds; `PF_PID` persisting is a stale PID file, not a live
  process. The watchdog is the only thing that restarts it and polls every 30s, so every exit
  becomes `F-105` before recovery lands. An isolated driver on the same fixture ran 236s untouched,
  so look at `upsd` and its reconnecting clients. Evidence: `operator-maturity-benchmarks.md`.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`, plus the operator-side halt evidence in `internal/haltwatch` and
`internal/controller/nodehalt_controller.go`. Design doc: `docs/contributing/design/node-agent-operand.md`
(`NA-n`). Audits: `docs/contributing/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-92`, `OD-37`) and `operator-maturity-benchmarks.md` (`F-94`).

- `F-94` decide whether halt evidence survives a manager restart. Attempts live only in
  `haltwatch.Observer`'s map, so a restart or leadership handoff between the signal write and the
  node stopping records no outcome at all. Re-seeding from the signal Secrets would close it; the
  open question is what an already-`NotReady` node with a live key means, which is an `OD-27`
  evidence-model decision rather than a patch.
- `F-105` decide what an agent does after it has signalled. `upsmon` exits 0 once it runs
  `SHUTDOWNCMD`, which in a DaemonSet is a container restart, and the restarted `upsmon` re-fires as
  soon as comms are still bad — each cycle writing a fresh timestamp-derived execution ID the
  actuator's `seen` set cannot match. Observed at 61-67 restarts per agent over seven hours,
  triggered by the `F-97` driver exits. `NA-3` revocation covers the operator-written Secret, not
  this node-local path.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/contributing/design/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

- Publish communication-ordering artifacts once the planner consumes `carries` ordering (see Planning
  & Execution Logic).

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/contributing/design/audit-storage-schema.md`.

None.

---

### Operator Maturity & Hardening

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/contributing/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

- Enable branch protection on `main` at release. Deliberately off during build: every CI check
  exists and passes, and requiring them would only add a merge round-trip to a single-maintainer
  repository that is still changing shape daily. This is a release gate, not a gap — the checks to
  require are already there, so turning it on is a repository-settings change and nothing else.
  Recorded here because this section previously described it as already in place.

- `F-113` validate the rendered manifests against an API server before the e2e job. Three
  `ShutdownHook` helper `ClusterRole`s shipped with an empty `resources:` list, which every local
  gate accepted -- envtest never applies them, `validate-samples` only checks CRs against CRD
  schemas, and installer-freshness only diffs `dist/` against itself. The apiserver rejects them
  outright, so the first thing to notice was the e2e job, six minutes and four image builds in. A
  `kubectl apply --dry-run=server -f dist/` in Repo Hygiene catches it in seconds.
- `F-108` test against more than one Kubernetes version. `ENVTEST_K8S_VERSION` follows the
  `k8s.io/api` minor in `go.mod`, and the e2e cluster takes whatever `kind` `latest` defaults to,
  unpinned. Pin the node image and run a matrix before making any compatibility claim.
- Write the multi-node specs the e2e cluster can now carry. `F-109` gave it three nodes and a CNI
  that enforces `NetworkPolicy`, and the only spec using either is the enforcement guard. Wave
  ordering, tier descent across nodes, and agent self-exclusion are now testable and still untested.
- `F-110` induce failures in the suite, and run something for longer than a few minutes. Nothing
  deletes a pod, partitions the network, or stalls the apiserver; the only restart assertions check
  that a restart did *not* happen. `F-97` and `F-105` are both in the class this would catch.
- `F-112` add upgrade coverage and a release workflow. Nothing tests that a cluster converges after
  the operator is replaced, or that CRD schemas stay compatible across versions. Both become gates
  when a v1 exists.

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

The `docs/` set follows the section layout the established Kubernetes operators use — Concepts,
Installation, Guides, Reference, Troubleshooting, Contributing — modeled on the Cluster API Book for
structure and cert-manager for the front door. [docs/README.md](README.md) is the landing page and
carries a first-hour path.

Each layer has a stated job, and material is placed by that job rather than by where it was written:
the root README is product, model, safety boundaries, and an install entry point; `concepts/` is how
the system works; `guides/` holds the judgement calls only the operator can make, in the order a
reader hits them; `installation/` is procedure; `reference/` is exact fact — API, metrics, security,
glossary; `contributing/` holds the design set and the audits behind it. Delivery is
rendered-on-GitHub markdown; a published site was considered and declined pre-v1. Every page carries
`Components:` and `Audience:` under its title, so both are visible at the point of reading.

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
