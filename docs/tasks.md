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

Last reviewed: 2026-08-29

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
- `PL-21` communication-path edges stay unwired until a network device can be an actuation target
  (`OD-24` makes switches topological-only). Revisit with PDU outlet control.

Closed locally 2026-08-29:

- Generic UPS telemetry now feeds the runtime side of `ShutdownFlow.status.planFeasibility` from
  public `UPSDevice.status` fields, not from site-local metrics. The status publishes trusted
  shortest runtime, lowest selected-device charge, highest selected-device load, and the observed
  versus declared execution-history provenance. The UPSDevice watch predicate now admits
  `loadPercent` changes while still dropping pure poll timestamp/raw-status churn.

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/contributing/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`,
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`, `F-85`); relevant findings from `docs/contributing/audits/nut-usage-audit.md`
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`).

- Settle how UPS drivers behave under the new foreground supervisor in a real cluster. Local image
  probes now rule out the bad design: one `upsdrvctl -FF start` bundle for all drivers exits the
  whole bundle when one configured driver fails. The rendered operand instead keeps a stable
  `driver-supervisor` sidecar and starts one foreground `upsdrvctl -FF start <ups>` worker per
  configured UPS, matching NUT's service-instance model while preserving `F-48` reload semantics.
  Remaining proof is runtime evidence from Kubernetes rather than more process-model design.
- `F-97` find out why the driver stops answering new probes in the minutes after a pod start. The
  recovery half is done and now measured: a killed driver is back in 9.75s against a 30s budget
  (`test/e2e/driver_recovery_test.go`). The question changed shape on 2026-08-24 — the exit rate is a
  startup burst, not a steady state, and eight of ten restarts happened while `upsd` still held a
  working session. See the correction of that date in `operator-maturity-benchmarks.md`.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`, plus the operator-side halt evidence in `internal/haltwatch` and
`internal/controller/nodehalt_controller.go`. Design doc: `docs/contributing/design/node-agent-operand.md`
(`NA-n`). Audits: `docs/contributing/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-92`, `OD-37`) and `operator-maturity-benchmarks.md` (`F-94`).

No open v1 DaemonSet tasks remain.

Closed locally 2026-08-29:

- Talos actuator support is implemented as a separate `TalosShutdown` policy, not as a hidden variant
  of `PowerOff`. It uses the Talos machine API through a mounted talosconfig Secret, targets the
  local node explicitly, and force-shutdowns because the operator has already handled the Kubernetes
  drain/ordering decision.
- The actuator boundary is now policy-specific. Linux `PowerOff` is still the only shape that renders
  `hostPID` and `CAP_SYS_BOOT`; `TalosShutdown` keeps the restricted container profile, no host
  namespace, no Linux capabilities, no Kubernetes service-account token, one read-only talosconfig
  Secret, and egress only to configured Talos API endpoint IPs on TCP 50000. No new OD was opened:
  `NA-1`/`OD-37` already settled the halt authorization path, and `NA-11` records the Talos-specific
  boundary.

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

- `F-110` run something for longer than a few minutes. Failure injection now exists: a spec kills
  the driver through its own PID file and asserts recovery inside a budget below the smallest
  `DEADTIME` the operator renders, and it measured 9.75s on its first run. That bounds the
  driver-outage path, not `F-105`, which the 2026-08-24 correction separated out. Still nothing
  partitions the network or stalls the apiserver, and nothing runs long
  enough to catch what only appears over hours -- the agent restart loop needed 23 of them before it
  was visible as anything but a restart count.
- Set a retention policy on the four GHCR packages. About 1,780 of ~2,180 versions carry no tag at
  all as of 2026-08-25 -- superseded digests and attestation layers from 550+ builds -- and nothing
  prunes them.
- Delete the one tag on the `nut-operator` package that is neither `main` nor a digest reference,
  pushed by hand on 2026-07-31. It is the only human-readable tag on a public package that the
  promote job did not put there.
- `F-112` run and verify the first `v*.*.*` release through the existing tag-promotion workflow.
  Local upgrade coverage now checks CRD/deployment reapply plus manager replacement over an existing
  resource. True previous-release schema compatibility starts after there is a previous released API
  to install.

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
- ASH grype low finding `GO-2026-5932` is tracked and triaged: `golang.org/x/crypto v0.55.0`
  currently has no available module update from `go list -m -u`, and `go list -deps ./...` does
  not import the affected `golang.org/x/crypto/openpgp` package. The OpenPGP package in the current
  dependency graph is `github.com/ProtonMail/go-crypto/openpgp`; recheck before v1 or when
  `golang.org/x/crypto` publishes a newer release.
- Alpha deployments run in dry-run by default and expose compiled plans, telemetry status, audit
  records, and approval-gate state before any host action is possible.
- Day-to-day operation works with CRDs, GitOps, `kubectl`, Events, logs, and audit records; no
  embedded dashboard is required for v1.
- A dry-run runs against real UPS hardware in a real cluster, not against `kind` and `dummy-ups`.
  Not reachable yet, and not expected to be until the sections above close.
- One node halted through a real actuator policy. `PowerOff` uses `make verify-actuation`; Talos
  clusters need the equivalent `TalosShutdown` proof against a sacrificial node. Distinct from the
  dry-run gate above, not a replacement for it: a dry-run never renders the actuate configuration,
  so that gate can pass without the selected shutdown boundary ever having been exercised on a real
  kubelet.
- **Open:** whether a live plug-pull is also a v1 gate, or whether the dry-run above is the bar.
  Undecided in either direction — do not assume one while planning against it.
