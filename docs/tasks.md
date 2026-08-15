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

Last reviewed: 2026-08-15

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/design/inventory-provider-contract.md` (`IN-n`).

#### Built

The `internal/inventory` pure compiler, all four inventory CRDs with webhooks and validators,
numbered shutdown tiers, and compilation wired into `ShutdownFlow` with the topology hash in plan
identity. The resolver carries full derived power-domain closure — UPS roots, members, nodes, and
infrastructure — into planner inputs, and runtime trigger evaluation consumes that same closure, so
domain-scoped triggers select devices from topology membership rather than raw `UPSDevice` labels.
The declarative provider leaves snapshot `ObservedAt` unstamped to avoid per-reconcile topology hash
churn, and cyclic `feeds` graphs are rejected with a `FeedsCycle` diagnostic before closure
derivation. Planner wave compilation consumes that same membership for `OD-14`, pruning groups
proved wholly outside the affected domain while keeping ambiguous or mixed-domain groups.

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
chain, firmware-scoped quirks, telemetry aliasing, bundled UPS and PDU catalogs at `1.0.0` with drift
tests, deterministic profile hashing across reordered telemetry, actuation, quirk, and firmware-match
sets, and `UPSCapabilityProbe` advisory drafting with probe history. A device publishes the profile
it resolves to on `status.capability` — identity, tier, the quirks in force after firmware scoping,
and the matcher's own reason when the match is anything but a clean product hit — so a device that
fell back to the unidentified-device profile is distinguishable from one that matched its product
profile.
A PDU profile set that cannot resolve — duplicate ids, two universal profiles — is
reported on every profile in the set. PDU device matching itself is scaffolding per `OD-25`, and
the CRD description says so: no device kind, inventory entity, render path, or actuation path
consumes it. `OD-21` is decided as the code already behaves: driver configuration is owned by
`UPSDevice` spec in NUT's own vocabulary.

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
node-agent coverage, planner artifacts with diagram exports, and `ShutdownFlow` dry-run dispatch.
`internal/adaptive` is the pure tier-pointer and timing-mode model (`EX-25`–`EX-30`), wired into the
executor: power is re-read at every wave boundary, the pointer follows each compiled wave's tier, and
declared timeouts and `Wait` durations are compressed by a ratio measured from remaining runtime over
remaining declared plan. Pointer and mode persist in `executor_resume_states` and publish on
`status.lastExecution.adaptive`. Power returning mid-flow is recorded and nothing else: the execution
runs to completion, the pointer ascends as bookkeeping, and whether a new execution starts is settled
by trigger eligibility a level up. `Gate` is
removed from the action enum; `Notify` emits a Kubernetes Event. Tier inversion is published as
`nutoperator_shutdownflow_tier_inversions`, and the EX-29 cadence heartbeat as
`nutoperator_shutdownflow_publish_timestamp_seconds` plus `status.lastPublishTime`. `EX-31` is built:
`spec.tierOverrunPolicy` defaults to `Wait`, `Overlap` starts the next lower tier on schedule while
the overrun tier continues, `Preempt` cancels the tier action context when the next lower tier becomes
due, and overruns are recorded in wave/final audit details, `status.lastExecution.tierOverruns`,
`nutoperator_shutdownflow_tier_overruns_total`, and
`nutoperator_shutdownflow_tier_overrun_seconds`. Node clearance is re-derived at execution
against the pods actually on the node, read uncached, and node-oriented targets accept Kubernetes
`nodeSelectorRequirements` so authored flows can express native `Gt`/`Lt` label ranges without
inventing a custom selector language. The provisional `AE-n` identifiers are folded into
`EX-25`–`EX-30`, and the runtime-estimate gate that shared `AE-6` is now `CR-4`. `EX-32`
estimates are informed by what previous outages actually took: observed group durations are read from
the audit tables scoped by plan config hash, injected as a resolved planner input, and published per
group with provenance and sample counts. `OD-12` is surfaced as
`status.planFeasibility` — plan estimate against reported runtime, warning and never blocking.
`EX-14` restart resume is covered by envtest: a second reconciler instance holding no
in-process state resumes the persisted tier and timing mode instead of re-reporting descended tiers as
new work. `EX-33` rehearsal execution is built as a generic one-way request: changing the
`power.zalud.io/rehearsal-request` annotation to a new token runs one approved enforce-mode sample,
labels status and audit details as rehearsal, and feeds those real durations into estimates unless
`spec.rehearsal.includeInEstimates: false` opts out. `ShutdownHook`/`RunHook` replaces the removed
Argo-shaped `RunWorkflow` route: HTTP CloudEvents is the primary transport for non-Kubernetes
systems, generic Kubernetes objects are the secondary transport, hook dry-runs are either authored
rehearsals or recorded request summaries, and hook failures mark the flow degraded without holding
waves or engaging `abortPolicy`. `OD-14`
partial-domain scope is compiled in `internal/planner`: domain- or UPS-scoped triggers prune only
groups proved wholly outside affected domains, with ambiguous and mixed-domain groups retained.

Closed: `PL-19`, `PL-20`, `PL-43`, `CR-4`, `EX-9`, `EX-11`, `EX-14`, `EX-22`–`EX-33`, `OD-4`,
`OD-11`, `OD-12`, `OD-14`, `OD-17`, `OD-18`, `OD-29`, `OD-30`, `OD-33`, `OD-34`, `SB-15`,
`HK-1`–`HK-10`, `F-31`, `F-42`, `F-44`.

#### Open Work

- `OD-27` confirm the adaptive defaults against a real outage. The compression amount is measured, so
  what is left to settle is the 20% runtime reserve (it stands in for a handoff tail nobody has
  timed) and the 10% minimum compression (the point at which the plan is declared not to fit).
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

`NUTServer` operand rendering with injection-validated NUT config, `credentialSecretRef` wiring, NUT
protocol TLS proven end to end against operands built from source on OpenSSL, scripted `dummy-ups`
simulation, and the `snmpsim` driver-conformance fixture. e2e covers `dummy-ups`, `snmp-ups`, and
`NUTServer`; real actuation stays out of scope for `kind`. The reconciler watches `UPSDevice` through
a predicate scoped to spec and labels, and maps credential `Secret` and simulation `ConfigMap` changes
back to the servers whose selected devices reference them, so a driver, port, credential, or fixture
edit re-renders instead of waiting for an unrelated reconcile. Readiness reads `upsdrvctl status`,
NUT's own driver-state report, rather than inferring driver health from `upsc` failures, and the
Docker `HEALTHCHECK` runs that same check instead of the `upsd -V` that could never fail.
`verifyClientCertificates` is refused at admission because no released
OpenSSL `upsd` honors CERTREQUEST. The entrypoint runs `upsd -FF`, so the operand is foregrounded
because that is what was asked for rather than as a side effect of debug logging, and it leaves the
PID file `upsd -c reload` needs; the smoke test asserts the file rather than the flag. The driver
allowlist is pinned to the image from both sides — the smoke test asserts every admitted driver is
present, and a Go test asserts the two lists agree — so admission cannot accept a driver the operand
cannot run. A `driver-watchdog` sidecar restarts drivers that stop answering, so a driver dying after
startup no longer leaves the pod permanently out of the Service endpoints. A server whose selector
matches nothing starts idle and reports NotReady instead of crash-looping. Adding or removing a
device reloads `upsd` in place instead of replacing the pod; only `LISTEN`, port, and certificate
changes still roll it, because those are the changes `upsd` ignores on reload — silently, in the
case of `LISTEN`. The pod shares a process namespace, which is what lets the sidecar signal `upsd`
and what gives the pause container the orphaned drivers to reap. Verified on `kind`: a killed driver
is recovered and leaves no zombie, and a device added by patching the ConfigMap is served without
either container restarting. Driver configuration follows NUT's own vocabulary on `UPSDevice` spec,
with `spec.driverOptions` as the `ups.conf` escape hatch for anything the typed fields do not cover
(`OD-21`). The escape hatch cannot reach around the allowlist it sits behind: `driver` is reserved
on both the direct and `upstreamNUT` paths, so a device cannot pass admission declaring one driver
and render another.

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
`SHUTDOWNCMD` binary, `internal/nodeagent` signal validation enforcing TTL and node name, and
`cmd/node-actuator`'s syscall-backed poweroff, proven on `kind` within the configured TTL. The
authorization boundary (`OD-37`), the signal lifecycle including revocation (`F-87`) and the
delivery-channel marker (`F-86`), the permitted-only `CAP_SYS_BOOT` model (`F-61`), the readiness
contract (`F-59`, `F-64`), and the mid-episode write deferral (`F-92`) are described as built in
[node-agent-operand.md](design/node-agent-operand.md) (`NA-1`–`NA-8`).

Closed: `F-8`–`F-14`, `F-24`, `F-33`–`F-36`, `F-54`–`F-60`, `F-61`–`F-65`,
`F-66`, `F-67`–`F-71`, `F-72`, `F-73`–`F-75`, `F-86`–`F-88`, `F-90`–`F-92`, `OD-37`. `F-89` declined — the signal Secret
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
controller-runtime's registry. Delivery is Kubernetes API watch over `ShutdownFlow.status` for the
current artifact stream, plus Events, logs, and PostgreSQL for transitions, operator detail, and
durable history. Kubernetes-first interface only — CRDs, status, Events, logs, PostgreSQL, no UI and
no bundled broker. Metrics now cover telemetry polls, capability-match attempts, inventory compiler
counts, domain counts, orphan nodes, and unmodeled communication paths.

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
multi-arch operand images with SBOM, provenance, and scanning; CI with distinct check names, path
filters, tidy-drift, ASH, an RFC1918 scan, and CRD-schema validation of every shipped sample and
example; and the no-cert-manager install path (`config/byo-cert`, `hack/webhook-cert.sh`) verified on
`kind`.

Sample and example manifests are validated against the generated CRD schemas by
`make validate-samples`, on every commit and with no path filter, because the CRDs are generated
from the Go types while the manifests are hand-written and nothing else connects the two. The
RFC1918 scan moved alongside it into `Repo Hygiene` for the same reason: both guard documentation
and examples, and both previously sat behind a `docs/**` path filter that skipped exactly the
commits they exist to check.

Serving-certificate expiry is published as `nutoperator_certificate_not_after_timestamp_seconds`; the
byo-cert install path is covered end to end on `kind`, including rotation; and the ASH scan runs
every scanner that can contribute here — `grype` and `syft` installed from pinned checksum-verified
archives, `cfn-nag`/`cdk-nag`/`opengrep` excluded by decision.

The manager image no longer carries a Docker `HEALTHCHECK` that only ran `--version`; Kubernetes
`/healthz` and `/readyz` probes are the manager readiness contract, and the Dockerfile carries the
corresponding `CKV_DOCKER_2` skip rationale. `docs/images.md` and `docs/security.md` now distinguish
current source-build controls from open release-hardening targets.
The Images workflow signs non-PR published image digests with keyless Sigstore/cosign after the
published-image vulnerability scan, and `docs/images.md` documents digest verification. The `main`
tag is applied only to a digest the e2e suite and the NUT TLS smoke test have both run against: the
build job publishes immutable `sha-` references, `test-e2e` is invoked with those digests through
`workflow_call`, and a promote job floats the tag afterwards (`F-77`). A failing ASH scan names its
actionable findings in the job log and the run summary instead of only counting them, and the
extraction is reconciled against ASH's own verdict so it cannot report clean while the scan fails.

Closed: `F-1`–`F-5`, `F-7`, `F-28`–`F-32`, `F-38`, `F-52`, `F-77`, `F-78`.

#### Open Work

- Enable branch protection on `main`. This section previously recorded it as built with every
  check required and `enforce_admins` on; the GitHub API reports no classic protection and no
  rulesets on the branch, checked with an admin-scoped token, so the control is not in place
  whatever its history. Every CI check the claim depended on does exist and passes — what is
  missing is the enforcement that makes any of them required. Needs a repository-settings
  change, not a code change.

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
pure evaluation wired into `ShutdownFlow`, domain-scoped trigger evaluation against resolver-derived
power-domain membership, planner compilation of that same partial-domain scope, and `dummy-ups`
repeater mode for upstream NUT appliances.

Closed: `F-22` relay half, `F-25` runtime half, `OD-9`, `OD-14`, `CR-4`.

#### Open Work

None.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

#### Built

Component-scoped design docs with stable identifier namespaces, governing principles and scope
boundaries, the decision index, the references under `docs/`, the audit records under
`docs/audits/`, and the diagrams under `docs/diagrams/`. Example node naming is role-based per
CONTRIBUTING.md — no new decision, that is the example policy applied.

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
- **Open:** whether a live plug-pull is also a v1 gate, or whether the dry-run above is the bar.
  Undecided in either direction — do not assume one while planning against it.
