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
tests, and `UPSCapabilityProbe` advisory drafting with probe history. A device publishes the profile
it resolves to on `status.capability` — identity, tier, the quirks in force after firmware scoping,
and the matcher's own reason when the match is anything but a clean product hit — so a device that
fell back to the universal floor is distinguishable from one that matched its product profile.
A PDU profile set that cannot resolve — duplicate ids, two universal profiles — is
reported on every profile in the set. PDU device matching itself is scaffolding per `OD-25`, with no
device kind and no inventory entity kind to match against.

Closed: `F-25`, `F-26`, `F-79`, `F-80`, `RS-7`–`RS-10`, `PL-30`, `OD-22`, `OD-23`, `OD-25`, `OD-31`.

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
new work.

Closed: `PL-19`, `PL-20`, `PL-43`, `CR-4`, `EX-9`, `EX-11`, `EX-14`, `EX-22`–`EX-30`, `OD-4`,
`OD-11`, `OD-12`, `OD-17`, `OD-18`, `OD-29`, `OD-30`, `EX-32`, `SB-15`, `F-31`, `F-42`, `F-44`.

#### Open Work

- Build the `ShutdownHook` resource and its HTTP/CloudEvents transport
  ([shutdown-hooks.md](design/shutdown-hooks.md), `HK-1`–`HK-10`). `RunWorkflow` currently refuses any
  GVK but Argo, so nothing advertises neutrality it lacks in the meantime.
- `OD-33` decide whether an opt-in bounded wait on hook completion exists, and what happens when the
  runtime budget expires first.
- `OD-34` decide whether a failed hook can mark the flow degraded, or stays purely advisory.
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
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`); relevant findings from `docs/audits/nut-usage-audit.md`
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
either container restarting.

Closed: `F-15`–`F-18`, `F-21`, `F-23`, `F-24`, `F-37`, `F-39`–`F-41`, `F-43`, `F-46`, `F-47`,
`F-48`–`F-51`, `F-53`, `F-76`, `NS-1`–`NS-9`, `OD-32`, `OD-36`. `F-19`
declined — it only matters with an HA `upsd` topology, which is not designed.

#### Open Work

- Advanced driver-specific configuration for the operand render path.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`. Audits: `docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-75`, `OD-37`); `F-45` from `docs/audits/nut-usage-audit.md`. The task lines
below are pointers; the evidence and the recommended order are in the audit. The privilege group it
sequenced first is closed, so `F-54` with `OD-37` is next — and the audit records that `F-54`,
`F-56`, and `F-57` are pre-commitment decisions to lock before actuation ships rather than live
exposure.

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

- `F-45` expose the `MONITOR` power value and `MINSUPPLIES` so a host whose supplies are all fed by
  one UPS can say so ([nut-usage-audit.md](audits/nut-usage-audit.md)). The hardcoded `1`/`1` is
  correct for every topology currently modeled, so this is a limit, not a defect.

##### Signal authority and the two-component boundary

- **`F-54` · The local `upsmon` path self-authorizes an unordered fleet-wide halt.** Every agent
  renders `MONITOR ... 1 ... secondary` with `MINSUPPLIES 1`, so one UPS reaching OB+LB fires
  `SHUTDOWNCMD` on every node it covers, unordered, with an execution ID nothing distinguishes from
  the executor's. Resolve with `OD-37`; gates `F-55`–`F-57`
  ([node-agent-daemonset-audit.md](audits/node-agent-daemonset-audit.md)).

- **`OD-37` · Decide and record what the local path is for.** Either an intentional last-resort
  backstop — in which case it needs a name, a spec field, a distinct `reason`, and a written
  statement of what ordering guarantee is surrendered when it fires — or it is a bypass and should
  be bound to prior authorization per `F-57`. Leaving it undeclared is what makes `F-54` a surprise.

- **`F-55` · Signal fields are validated for presence, never for value.** `PlanConfigHash`,
  `ExecutionID`, and `ShutdownFlow` are checked non-empty and no further, and the actuator's env
  does not carry the values needed to check them, so the fix needs a render change. Blocked on
  `F-54`/`OD-37`.

- **`F-56` · `DryRun` looks like authorization and carries no independent information.** The writer
  sets it from `POWER_AGENT_MODE` and the actuator gates on it, so the actuator is reading its own
  configuration back out of a file. Whatever replaces it must originate outside the pod. Blocked on
  `F-54`/`OD-37`.

- **`F-57` · The trust boundary between the two containers is a shared writable tmpfs.**
  `power-agent-run` is read-write in both, and `signalPaths` evaluates it before the API-gated
  projected Secret, so the network-facing container can halt the host by writing one file. Mount the
  actuator's copy `ReadOnly` regardless. Blocked on `F-54`/`OD-37`.

- **`F-59` · TTL spans two clocks with no stated assumption.** The executor stamps `Timestamp`; the
  actuator compares against the node clock. Past the window every operator-issued signal is rejected
  fleet-wide, evidenced only by a container log line. Needs a stated NTP assumption plus a condition
  or metric for "this node rejects what I send it".

##### Checks that cannot fail

- **`F-68` remainder · nothing consumes the recorded notifications beyond readiness.** The events are
  written and read by `power-notify-writer --check`; publishing them upward is `F-60`'s channel
  question, which is closed as designed.

##### NUT mechanisms inert or unused

- **`F-66` remainder · `POWERDOWNFLAG` is rendered and vestigial.** Recorded as inert by design; drop
  it or leave it, but do not go looking for a consumer
  ([node-agent-daemonset-audit.md](audits/node-agent-daemonset-audit.md)).

##### Event-time coupling

- **`F-72` remainder · nothing suppresses a rollout during a flow.** The shape is fixed; the guard is
  a design question — a paused DaemonSet, an admission check, or a flow-active condition.

##### Naming and hygiene

- **`F-75` remainder · rename `SystemdPoweroff`, and drop the duplicate signal-path env pair.** Both
  change the published CRD enum or the container contract, so they belong with a deliberate alpha API
  revision rather than a fix pass — renaming the enum silently breaks every CR that sets it.

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
