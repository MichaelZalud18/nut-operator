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

Last reviewed: 2026-08-14

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

- Carry derived domain membership across the resolver boundary. `plannerPowerDomains`
  (`resolver/planner.go:79`) maps `Name` and `UPSDevices` only, so the compiler's derived
  `domain.Nodes` and `domain.Infrastructure` are dropped before the planner ever sees them, and
  `planner.PowerDomainMembership` has no field to receive them. `ControlPlane` and
  `ControlPlaneQuorumMember` reach `inventory.Entity` (`inventory/types.go:52`) and stop there —
  `plannerNodeTiers` (`resolver/planner.go:44`) carries name and tier only. Data plumbing, no policy.
- Wire domain membership into wave compilation once it arrives, so a plan can be scoped to a domain
  (`OD-14`). Wanted in v1: this is a planner capability, not a policy question, and it is sequenced
  after the plumbing line above rather than blocked on a decision.
- Feed trigger evaluation from the derived closure instead of live telemetry snapshots.
- `F-81` correct `snapshotage.go:33` — it claims the declarative provider restamps `ObservedAt` every
  resolve, and nothing sets that field at all. Pin the hazard with a test in the same change:
  `Compile` hashes the normalized snapshot including `ObservedAt` (`compiler.go:45`, `compiler.go:336`),
  so stamping it churns the topology hash and plan identity on every reconcile. Not yet written up in
  an audit.
- `F-82` emit a cycle diagnostic from `feedsClosure` (`compiler.go:312`). The visited set prevents a
  hang, so a cyclic `feeds` graph compiles silently — this is missing diagnosis, not a crash. Not yet
  written up in an audit.

---

### Capability Profiles

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/provenance design surface. Design docs:
`docs/design/capability-profiles.md`.

#### Built

`UPSCapabilityProfile` and `PDUCapabilityProfile` CRDs over one shared five-tier match precedence
chain, firmware-scoped quirks, telemetry aliasing, bundled UPS and PDU catalogs at `1.0.0` with drift
tests, and `UPSCapabilityProbe` advisory drafting with probe history. A device publishes the profile
it resolves to on `status.capability` — identity, tier, the quirks in force after firmware scoping,
and the matcher's own reason when the match is anything but a clean product hit — so a device that
fell back to the universal floor is distinguishable from one that matched its product profile.
A PDU profile set that cannot resolve — duplicate ids, two universal profiles — is
reported on every profile in the set. PDU device matching itself is scaffolding per `OD-25`, with no
device kind and no inventory entity kind to match against. `OD-21` is decided as the code already
behaves: driver configuration is owned by `UPSDevice` spec in NUT's own vocabulary.

Closed: `F-25`, `F-26`, `F-79`, `F-80`, `RS-7`–`RS-10`, `PL-30`, `OD-21`, `OD-22`, `OD-23`, `OD-25`,
`OD-31`. `OD-26` dropped — the `provenance` field whose semantics it decides was never
built, and `ProfileSource` already draws the only distinction that exists.

#### Open Work

- `F-83` sort `Quirks` in `normalizeProfiles` (`matcher.go:374`) alongside `TelemetryVariables` and
  `ActuationBehaviors`. Without it, reordering quirks with no semantic change moves the published
  `status.capability.profileHash`. Not yet written up in an audit.
- `F-84` state the PDU kind's v1 scaffolding status in the CRD description. It reaches the catalog
  YAML and the `PDUCapabilityProfileSpec` godoc, so `kubectl explain pducapabilityprofile` — where a
  user meets the kind — is the one surface that does not say it actuates nothing. Not yet written up
  in an audit.
- Record `OD-21` in [decision-index.md](design/decision-index.md), `scope-boundaries.md`, and
  `capability-profiles.md`, with its decline: a profile-supplied *defaulting* layer for driver
  configuration is refused, because it would make the rendered `ups.conf` depend on which profile
  matched and reintroduce the second source of truth `OD-21` just removed. Validation is already
  served by the driver allowlist (`RB-2`) and the `ups.conf` token/value checks.
- Record the `OD-26` drop in [decision-index.md](design/decision-index.md) and
  `capability-profiles.md`: `internal/capability/types.go` has no `Provenance` field, so the question
  decides the semantics of something that was never built. `ProfileSource` (`CRD` vs `Bundled`)
  already draws the only distinction that matters, including in match precedence, and the bundled
  catalog is entirely first-party — there is no `Community` tier to hold apart. Revisit when an
  external contributor catalog exists.

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
`nutoperator_shutdownflow_publish_timestamp_seconds` plus `status.lastPublishTime`. Node clearance is
re-derived at execution against the pods actually on the node, read uncached. The provisional `AE-n`
identifiers are folded into `EX-25`–`EX-30`, and the runtime-estimate gate that shared `AE-6` is now
`CR-4`. `EX-32` estimates are informed by what previous outages actually took: observed group
durations are read from the audit tables scoped by plan config hash, injected as a resolved planner
input, and published per group with provenance and sample counts. `OD-12` is surfaced as
`status.planFeasibility` — plan estimate against reported runtime, warning and never blocking.
`EX-14` restart resume is covered by envtest: a second reconciler instance holding no
in-process state resumes the persisted tier and timing mode instead of re-reporting descended tiers as
new work. `ShutdownHook`/`RunHook` replaces the removed Argo-shaped `RunWorkflow` route: HTTP
CloudEvents is the primary transport for non-Kubernetes systems, generic Kubernetes objects are the
secondary transport, hook dry-runs are either authored rehearsals or recorded request summaries, and
hook failures mark the flow degraded without holding waves or engaging `abortPolicy`.

Closed: `PL-19`, `PL-20`, `PL-43`, `CR-4`, `EX-9`, `EX-11`, `EX-14`, `EX-22`–`EX-30`, `OD-4`,
`OD-11`, `OD-12`, `OD-17`, `OD-18`, `OD-29`, `OD-30`, `OD-33`, `OD-34`, `EX-32`, `SB-15`,
`HK-1`–`HK-10`, `F-31`, `F-42`, `F-44`.

#### Open Work

- `OD-27` confirm the adaptive defaults against a real outage. The compression amount is measured, so
  what is left to settle is the 20% runtime reserve (it stands in for a handoff tail nobody has
  timed) and the 10% minimum compression (the point at which the plan is declared not to fit).
- Build `EX-31` tier-overrun policy: `spec.tierOverrunPolicy` of `Wait` (default, current behavior),
  `Overlap`, or `Preempt`, plus metrics recording which tier overran, by how much, and what the
  policy did.
- Build `EX-33` rehearsal runs so history exists before the first real outage: an on-demand or
  scheduled enforce-mode execution, labelled as a rehearsal in the audit trail and includable or
  excludable from estimates. Dry-run cannot serve this — it skips effects and so produces no honest
  durations. `status.planFeasibility.thinGroups` already names what a rehearsal would improve; what
  is missing is the run itself and the label on it.
- Feed node metrics into the estimates alongside execution history — draw and capacity readings
  sharpen the runtime side of the comparison the same way observed durations sharpen the plan side.
- `OD-14` decide partial-domain outage plan scope, then wire domain membership into wave compilation
  (shared with Inventory System and Telemetry & Triggers).
- Accept node-selector *requirements* for node targeting so a group can express a tier range.
  `metav1.LabelSelector` has no numeric comparison; `corev1.NodeSelector` supports `Gt`/`Lt`.
  Namespace and workload targeting cannot gain this.
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
`internal/nodeagent`. Audits: `docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-75`, `OD-37`). The task lines below are pointers; the evidence and the
recommended order are in the audit. The privilege group it sequenced first is closed, and `OD-37` is
now decided — the operator path is the authoritative shutdown path, so `F-55`–`F-57` are unblocked
and become the implementation of that lockdown.

#### Built

`NodePowerAgent` DaemonSet rendering in `MonitorOnly`/`DryRun`/`Actuate`, `power-signal-writer` as
the `SHUTDOWNCMD` binary, `internal/nodeagent` signal validation with TTL and node-name enforcement,
and `cmd/node-actuator`'s syscall-backed poweroff. Handoff proven on `kind` within the configured
TTL. The actuator carries `CAP_SYS_BOOT` as a file capability and raises it only for the syscall,
which is what makes `mode=Actuate` able to halt a node at all; it refuses to arm without it, runs
under the pod's `RuntimeDefault` seccomp profile, and keeps `hostPID`, without which `reboot(2)`
kills the container and reports success. An operand namespace whose Pod Security level would reject
the actuating pod is reported on the agent's `Degraded` condition rather than relabelled. Both
containers' readiness probes can fail: the actuator's reflects its watch loop, and `upsmon`'s
queries every `<ups>@<server>` it monitors instead of anonymously listing the host.

Closed: `F-8`–`F-14`, `F-24`, `F-33`–`F-36`, `F-58`, `F-60`, `F-61`–`F-65`, `F-67`–`F-71`,
`F-73`, `F-74`.

#### Open Work

##### Signal authority and the two-component boundary

`OD-37` is decided: the operator path is the authoritative shutdown path — ordered, planned,
tier-aware, originating from the projected Secret. The local `upsmon` `SHUTDOWNCMD` path stays in the
codebase as scaffolding and is locked down: disabled by default, with no supported way to enable it
in v1. `F-54` resolves as a consequence, and the three items below are that lockdown's
implementation.

- **`F-57` · Close the tmpfs trust boundary.** Mount the actuator's `power-agent-run` copy
  `ReadOnly`, and take the local path out of `POWER_SIGNAL_PATHS` — or gate it behind the
  locked-down scaffold. It is read-write in both containers today, and `signalPaths` evaluates it
  before the API-gated projected Secret, so the network-facing container can halt the host by writing
  one file.

- **`F-56` · Stop carrying `DryRun` in the signal file.** The writer sets it from `POWER_AGENT_MODE`
  and the actuator gates on it, so the actuator reads its own configuration back out of a file.
  Authorization derives from the projected Secret's provenance and the actuator's own env instead.

- **`F-55` · Validate signal fields by value, not presence.** Inject `POWER_PLAN_CONFIG_HASH` and
  `POWER_SHUTDOWN_FLOW` into the actuator container — they reach `upsmon` only today — and compare
  against them. `PlanConfigHash`, `ExecutionID`, and `ShutdownFlow` are checked non-empty and no
  further.

- Record `OD-37` in [decision-index.md](design/decision-index.md), `scope-boundaries.md`, and the
  DaemonSet design and audit docs. The audit currently reads `OD-37` as an open choice with a
  last-resort backstop as one arm; it resolved the other way, and the text must say so rather than
  stay ambiguous. Record `F-89` in the same pass, as declined: the signal Secret mounts whole on
  every node, so a node can read another node's signal, and the node-name check keeps that to
  exposure rather than actuation. Accepted — the payload carries no credentials, per-node Secrets
  multiply objects by node count, and the `subPathExpr` alternative would stop the volume updating
  in place.

##### Signal delivery and dedupe

- **`F-86` · A dead delivery channel reads as healthy.** `power-agent-signals` is a Secret volume
  with `Optional: true` (`nodepoweragent_render.go:911`), so an operator that never creates or stops
  updating it leaves kubelet mounting a readable empty directory: `unreadableSignalDirs` stays empty
  and readiness passes. With `OD-37` making this the only authorized channel, its silence has to be
  distinguishable from "no flow running". The audit's 2026-08-12 "not findings" list credits
  `Optional` with the in-place updates that actually come from the absence of `subPath` — correct
  that entry in the same change. Not yet written up in an audit.

- **`F-87` · A rollout mid-flow can re-actuate a signal inside its TTL.** `F-58`'s actuated-key state
  lives on a per-pod emptyDir, while `F-72`'s `maxSurge: 1` brings up a second pod with an empty
  `seen` set watching the same projected signal — which is exactly what `F-58` was closed to prevent.
  Unrecorded interaction between the two; resolve it with the `F-72` rollout-suppression remainder
  below rather than separately. Not yet written up in an audit.

- **`F-72` remainder · nothing suppresses a rollout during a flow.** The shape is fixed; the guard is
  the open half — a paused DaemonSet, an admission check, or a flow-active condition. `F-87` is what
  makes this a defect rather than a preference.

- **`F-88` · Dedupe is per-source, not per-episode.** `watchSignals` (`cmd/node-actuator/main.go:204`)
  iterates every configured path without breaking, and `SignalKey`
  (`internal/nodeagent/signal.go:93`) is built from the payload, so two sources produce two keys for
  one episode. The `OD-37` lockdown leaves one source and resolves most of it; break out of the loop
  after actuating so the remainder cannot return. Not yet written up in an audit.

##### Clock and propagation assumptions

- **`F-59` · The TTL window rests on two unstated assumptions.** The executor stamps `Timestamp` and
  the actuator compares against the node clock, so skew past the window rejects every
  operator-issued signal fleet-wide, evidenced only by a container log line. `F-90` is the second
  assumption on the same window: signal TTL versus kubelet Secret propagation delay. State both —
  NTP and propagation — and add the condition or metric for "this node rejects what I send it".

##### Naming and hygiene

- **`F-75` · Rename the `ActuatorPolicy` enum and drop the duplicate signal-path env pair.**
  `Disabled`/`Stub`/`SystemdPoweroff` becomes `Disabled`/`Simulate`/`PowerOff`. `SystemdPoweroff` is
  factually wrong — the actuator calls `reboot(2)` with `LINUX_REBOOT_CMD_POWER_OFF` and there is no
  systemd anywhere in that path. A deliberate alpha API revision that breaks every CR setting the
  enum, and it lands in v1 rather than shipping a misleading name.

- **`F-66` remainder · drop the vestigial `POWERDOWNFLAG` from the render.** Inert by design, and a
  rendered NUT directive with no consumer is the class of thing this audit exists to remove.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

#### Built

A single structured planner artifact, the dependency graph as normalized vertices/edges with
provenance and explanations, deterministic Mermaid/Graphviz/D2 renderers, advisory startup wave
projections, and `internal/metrics` on controller-runtime's registry. Delivery is Kubernetes API
watch over `ShutdownFlow.status` for the current artifact stream, plus Events, logs, and PostgreSQL
for transitions, operator detail, and durable history. Kubernetes-first interface only — CRDs,
status, Events, logs, PostgreSQL, no UI and no bundled broker.

Closed: `PL-45`–`PL-48`, `OD-1`, `OD-5`, `F-3`, `F-6`.

#### Open Work

- Add telemetry-poll, capability-match, and inventory-compiler metrics (`docs/metrics.md` Open Work).
- Publish the richer graph/domain artifacts once the planner consumes derived domains and
  communication ordering (see Planning & Execution Logic).

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
- `F-77` gate image publication on the e2e run and have the suite pull the digest it built rather
  than building its own, so the published image is one that was tested
  ([operator-maturity-benchmarks.md](audits/operator-maturity-benchmarks.md)).
- `F-78` decide whether the manager image gains a readiness subcommand for its `HEALTHCHECK` or
  drops the instruction; `--version` cannot fail and distroless leaves no in-image alternative
  ([operator-maturity-benchmarks.md](audits/operator-maturity-benchmarks.md)).
- Correct `docs/images.md` to the source-build reality and close the two supply-chain claims it makes
  that the build does not meet (`F-52`, recorded in
  [operator-maturity-benchmarks.md](audits/operator-maturity-benchmarks.md)). It still states that the operand Dockerfiles package NUT
  "from pinned distribution packages" and that `nut-server` "installs `nut`" — both images have built
  from source since `F-39`. It also lists checksum *and signature* verification of NUT source inputs,
  where the Dockerfiles verify `sha256` only, and a pinned base image digest, where both use
  `alpine:3.22` as a tag. The description is a correction; the digest pin and signature verification
  are real work — do them or restate them as target state, and say which.

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

Closed: `F-22` relay half, `F-25` runtime half, `OD-9`, `CR-4`.

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
- A dry-run runs against real UPS hardware in a real cluster, not against `kind` and `dummy-ups`.
  Not reachable yet, and not expected to be until the sections above close.
- **Open:** whether a live plug-pull is also a v1 gate, or whether the dry-run above is the bar.
  Undecided in either direction — do not assume one while planning against it.
