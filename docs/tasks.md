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
NUT's own driver-state report, rather than inferring driver health from `upsc` failures; the inert
Docker `HEALTHCHECK` is gone. `verifyClientCertificates` is refused at admission because no released
OpenSSL `upsd` honors CERTREQUEST. The entrypoint runs `upsd -FF`, so the operand is foregrounded
because that is what was asked for rather than as a side effect of debug logging, and it leaves the
PID file `upsd -c reload` needs; the smoke test asserts the file rather than the flag. The driver
allowlist is pinned to the image from both sides — the smoke test asserts every admitted driver is
present, and a Go test asserts the two lists agree — so admission cannot accept a driver the operand
cannot run. A `driver-watchdog` sidecar restarts drivers that stop answering, so a driver dying after
startup no longer leaves the pod permanently out of the Service endpoints. A server whose selector
matches nothing starts idle and reports NotReady instead of crash-looping.

Closed: `F-15`–`F-18`, `F-21`, `F-23`, `F-24`, `F-37`, `F-39`–`F-41`, `F-43`, `F-46`, `F-47`,
`F-49`–`F-51`, `NS-1`–`NS-7`, `OD-32`, `OD-36`. `F-19`
declined — it only matters with an HA `upsd` topology, which is not designed.

#### Open Work

- Add a config-reload path instead of recreating the pod on every change (`F-48`). `upsd -c reload`
  re-reads `ups.conf`, `upsd.conf`, and `upsd.users` and registers devices added since startup;
  `upsdrvctl -c reload` / `reload-or-error` / `reload-or-exit` covers the driver side. Today the
  `power.zalud.io/config-hash` annotation forces a `Recreate` on any change, dropping every `upsmon`
  session and NUT's login accounting — the damage `F-15` and `F-16` exist to prevent. Projected
  volumes update in place, so the config reaches the container without a restart. `F-47` has landed,
  so the PID file reload signals through now exists, and `F-49` resolved as a sidecar rather than a
  container per driver, so the container list stays independent of the device set. Scope explicitly
  what still requires a restart: `LISTEN`, port, and certificate changes.
- Reap dead driver processes (`F-76`). The entrypoint `exec`s `upsd`, so `upsd` is PID 1 and never
  reaps the drivers reparented to it — verified as `State: Z (zombie)`, `PPid: 1`. Before `F-49` that
  leaked one entry per driver death; now the watchdog restarts the driver, so a flapping device
  leaks one per flap with no ceiling and nothing reporting it. Remedy is an init shim ahead of
  `upsd`, or `shareProcessNamespace: true`, which also collapses the PID-namespace asymmetry the
  watchdog works around — but that weakens the container boundary, so it is a decision rather than a
  swap. Not urgent: the symptom is process-table growth, not service failure.
- Advanced driver-specific configuration for the operand render path.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`. Audits: `docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-75`, `OD-37`); `F-45` from `docs/audits/nut-usage-audit.md`. The task lines
below are pointers; the evidence and the recommended order are in the audit, which sequences `F-61`
first and records that `F-54`, `F-56`, and `F-57` are pre-commitment decisions to lock before
actuation ships rather than live exposure.

#### Built

`NodePowerAgent` DaemonSet rendering in `MonitorOnly`/`DryRun`/`Actuate`, `power-signal-writer` as
the `SHUTDOWNCMD` binary, `internal/nodeagent` signal validation with TTL and node-name enforcement,
and `cmd/node-actuator`'s syscall-backed poweroff. Handoff proven on `kind` within the configured
TTL.

Closed: `F-8`–`F-14`, `F-24`, `F-33`–`F-36`.

#### Open Work

- `F-45` expose the `MONITOR` power value and `MINSUPPLIES` so a host whose supplies are all fed by
  one UPS can say so ([nut-usage-audit.md](audits/nut-usage-audit.md)). The hardcoded `1`/`1` is
  correct for every topology currently modeled, so this is a limit, not a defect.

##### Signal authority and the two-component boundary

- **`F-54` · The local `upsmon` path self-authorizes an unordered fleet-wide halt.**
  `POWER_EXECUTION_ID` is never rendered, so `power-signal-writer` always falls through to a
  synthetic `upsmon-<node>-<nanos>`, and the actuator accepts it with the same authority as an
  executor-issued signal. The `shutdownFlow: upsmon-local` half of the original framing was wrong
  and is corrected in the audit: an agent bound to a `shutdownFlowRef` writes the *real* flow name,
  so for those agents the only distinguishing mark is the `upsmon-` prefix nothing inspects. Every
  agent renders `MONITOR ... 1 ...
  secondary` with `MINSUPPLIES 1`, so one UPS reaching OB+LB fires `SHUTDOWNCMD` on every node it
  covers within one `POLLFREQALERT`. Under `mode=Actuate` that is the whole fleet halting at once,
  unordered, at the moment the sequencer exists to prevent it. `SB-2b` says NUT's threshold model is
  an input and never the sequencer; this path makes it the sequencer, and no field disables it.
  `requireFreshTelemetry` (`F-33`) gates the executor's group release only and never sees this path.

- **`OD-37` · Decide and record what the local path is for.** Either an intentional last-resort
  backstop — in which case it needs a name, a spec field, a distinct `reason`, and a written
  statement of what ordering guarantee is surrendered when it fires — or it is a bypass and should
  be bound to prior authorization per `F-57`. Leaving it undeclared is what makes `F-54` a surprise.

- **`F-55` · `PlanConfigHash`, `ExecutionID`, and `ShutdownFlow` are validated for presence, never
  for value.** `InspectSignal` checks non-empty and stops. The actuator container's env carries only
  `POWER_AGENT_MODE`, `POWER_ACTUATOR_POLICY`, `POWER_NODE_NAME`, `POWER_SIGNAL_PATH`,
  `POWER_SIGNAL_PATHS`, and `POWER_SIGNAL_TTL` — `POWER_PLAN_CONFIG_HASH` and `POWER_SHUTDOWN_FLOW`
  go to the `upsmon` container only, so the check cannot be written without a render change.
  `resiliency-and-partitions.md` calls signals plan-hash-bound and execution-bound; at the
  enforcement point they are neither. Same class as `F-25`/`F-33`/`F-37`.

- **`F-56` · `DryRun` looks like authorization and carries no independent information.** The writer
  sets it from `POWER_AGENT_MODE`; the actuator gates on it; the same render injects that env var
  into both containers. The actuator is reading its own configuration back out of a file. Whatever
  replaces it has to originate outside the pod.

- **`F-57` · The trust boundary between the two containers is a shared writable tmpfs.**
  `power-agent-run` is an `emptyDir` mounted read-write into both containers, and `signalPaths`
  evaluates it before the API-gated projected Secret, so the less-trusted source wins the tick. Code
  execution in the `upsmon` container — the one that speaks a network protocol to a server and parses
  its responses — writes one JSON file and halts the host. Three structurally different fixes, pick
  deliberately: sign the payload with an operator-held key and ship the agent only the public half;
  accept a local-path signal only when it names an `executionID` already observed on the projected
  path; or drop the local path and route the `upsmon` decision through the operator. Mount the
  actuator's copy `ReadOnly` regardless.

- **`F-58` · A signal can actuate twice.** `seen` is an in-memory map in `watchSignals`; `emptyDir`
  is pod-scoped, not container-scoped. An actuator restart clears the map while the file survives, so
  a still-fresh signal actuates again. Nothing persists an actuated-key record, and nothing obliges
  the executor to retract its Secret key after a node goes down. Shutdown-side obligation, not a
  recovery concern — `OD-1` does not cover it.

- **`F-59` · TTL spans two clocks with no stated assumption.** The executor stamps `Timestamp`; the
  actuator compares against the node clock, tolerating ±`SignalTTL` in each direction. Inside that
  window real skew is invisible; past it every operator-issued signal is rejected fleet-wide as
  `SignalStale` or `SignalFromFuture`, evidenced only by a container log line. Needs a stated NTP
  assumption plus a condition or metric for "this node rejects what I send it".

- **`F-60` · The boundary is one-way — the actuator cannot report anything.** No receipt, metric, or
  event, and `AutomountServiceAccountToken: false` at both ServiceAccount and pod level means no
  channel exists. A signal delivered to a `Disabled` actuator sitting in `block()`'s `select{}` is
  indistinguishable from one that halted a machine, since the readiness probe is `--version`. The
  executor infers success from the node disappearing. `resiliency-and-partitions.md` lists per-node
  heartbeat records as an implementation hook; choosing its channel is a design decision, not a TODO.
  Note while deciding: the agent ServiceAccount exists with no Role bound, so flipping automount for
  a heartbeat activates whatever is bound at that time for both containers, including the one holding
  `CAP_SYS_BOOT`.

##### Privilege model

- **`F-61` · Verify `CAP_SYS_BOOT` survives the switch to UID 65532 — before anything else on this
  list.** The pod sets `RunAsNonRoot`, `RunAsUser: 65532`, `AllowPrivilegeEscalation: false`, and the
  actuator adds `SYS_BOOT`. Linux drops the permitted set across a UID transition unless the
  capability is ambient, and Kubernetes has no field to request ambient capabilities — whether it
  holds depends on runtime OCI spec generation. Actuation has only ever run stubbed, so this may
  never have reached the syscall. If it does not hold, the options are a root actuator with
  `CAP_SYS_BOOT` only, or a file capability on the binary.

- **`F-62` · `SeccompProfile: Unconfined` on the actuator is probably wider than needed.** The
  runtime default profile permits `reboot` conditionally on `CAP_SYS_BOOT` being present, so
  `RuntimeDefault` likely already allows it; test that first and fall back to a narrow
  `localhostProfile` rather than Unconfined. Separately, `hostPID` plus Unconfined puts the operand
  namespace outside Pod Security `baseline`, and no namespace labelling appears in the render path.

- **`F-63` · Record why `hostPID` is required.** `reboot(2)` called from a non-initial PID namespace
  signals that namespace's init instead of halting the host, so `hostPID` is load-bearing rather than
  defense-in-depth. `F-13` framed it as an either/or — `CAP_SYS_BOOT` alone *or* `hostPID` plus
  `nsenter` — and the code correctly does both for a reason `F-13` does not state. Someone will
  harden it away.

##### Checks that cannot fail

- **`F-64` · `F-46`'s rule never crossed to the agent side.** The actuator's readiness probe is
  `/node-actuator --version`, which a process blocked forever in `block()` passes.
  `images/upsmon-agent/Dockerfile` still carries `HEALTHCHECK CMD upsmon -V` and
  `images/node-actuator/Dockerfile` carries `--version`. Actuator readiness should reflect the watch
  loop: signal directory readable, TTL clock sane, mode and policy parsed.

- **`F-65` · The `upsmon` readiness probe is blind to authentication and TLS.** It runs
  `upsc -l <server>` per MONITOR line — an anonymous `LIST UPS` over plaintext that exercises neither
  the `MONITOR` credentials nor the TLS posture `upsmon` was told to enforce. It would have reported
  Ready throughout `F-40`, where `upsmon` logged `connect failed: SSL error` against a server `upsc`
  reached fine. It also passes trivially when the rendered secret contains zero MONITOR lines, since
  the `while` body never runs. With `F-35`'s `pgrep` liveness, nothing proves `upsmon` holds a live
  authenticated session. `F-68`'s `NOTIFYCMD` state file is the NUT-native source for a real check.

##### NUT mechanisms inert or unused

- **`F-66` · `POWERDOWNFLAG` is written and structurally unreadable, and the PID file is never
  written.** The flag points into `/run/power-agent`, an `emptyDir` that dies with the pod, and
  nothing ever runs `upsmon -K`. Separately the agent image never creates `/run/nut`
  (`--with-altpidpath=/run/nut`) and the DaemonSet mounts an `emptyDir` over `/run` anyway; `upsmon`
  writes its PID file unconditionally, unlike `upsd`, so this is a silent `writepid` failure that
  costs `-c reload`, `-c fsd`, and `-K` together.

- **`F-67` · `Args: ["-D"]` should be `-F`.** `-D` raises the debugging level and foregrounds as a
  side effect, so the agent runs at debug level permanently. `upsmon` has no `-FF` and does not need
  one.

- **`F-68` · The whole notification surface is unused.** `NOTIFYFLAG`, `NOTIFYCMD`, `NOTIFYMSG`,
  `RBWARNTIME`, `NOCOMMWARNTIME`, `SHUTDOWNEXIT`. This is the NUT-native way for a node to publish
  its own events, and `COMMOK`/`COMMBAD`/`NOCOMM` via `EXEC` into a state file is the fix `F-65`
  needs.

- **`F-69` · `subPath` mounts do not receive updates.** `upsmon-config` and `nut-client-config` are
  both mounted with `SubPath`, so Kubernetes never propagates Secret or ConfigMap changes into them.
  The config-hash rolling restart is therefore not merely the chosen path, it is the only one that
  works — adding `upsmon -c reload` requires directory mounts first, on top of `F-66`'s PID file.

##### Event-time coupling

- **`F-70` · Signal TTL is set beside the delivery bound rather than derived from it.** TTL defaults
  to 2m; measured projected-Secret delivery was ~44s, and kubelet sync period plus cache TTL can push
  worst case toward or past that. Derive TTL and the Urgent tier's budget from the delivery bound.

- **`F-71` · `MONITOR` targets and the readiness probe both depend on cluster DNS.** Both use
  `<name>.<ns>.svc.cluster.local`, and CoreDNS is an ordinary workload inside the flow's path — when
  it goes, every agent loses reconnect capability and flips NotReady together. Render the Service
  ClusterIP or add `hostAliases`; either way DNS needs an explicit tier position.

- **`F-72` · Rollout shape leaves nodes unmonitored and is not suppressed during a flow.**
  `maxUnavailable: 1` with no `maxSurge` and no `minReadySeconds` means every rollout leaves one node
  uncovered for a full pull-and-start window. `maxSurge: 1, maxUnavailable: 0` is the better shape —
  no hostPort or hostNetwork blocks it — and nothing currently prevents a rollout while a flow is
  active.

- **`F-73` · Agent image residency is unguarded.** `IfNotPresent` is the right default and nothing
  prevents a user setting `Always`, while the images may live in a registry inside the cluster being
  shut down. `ImageReference` supports digests and does not require them.

##### Coverage

- **`F-74` · Coverage is measured against the agent's own selector, not against the inventory.**
  Narrowed from its original framing while writing up the audit: an untolerated taint *is* already
  detected, as `AgentPodMissing` on a selected node. The gap is the frame of reference — every count
  is computed over nodes `spec.nodeSelector` already matched, so a node the inventory considers in
  scope but the selector excludes is absent from every count and the agent reports fully ready. The
  inverse of readiness, and the check that catches placement mistakes.

##### Naming and hygiene

- **`F-75` · `policySystemdPoweroff = "SystemdPoweroff"` is the enum a user sets to enable real
  actuation, and since `F-36` it performs `reboot(2)` and never touches systemd.** Reviewers asking
  what privileges the container needs are told dbus and host PID when the answer is `CAP_SYS_BOOT`.
  Rename with the CRD enum. Fold in while there: `signalPaths` splits on `:` as well as `,`, so any
  path containing a colon fragments into nonexistent paths and fails as `SignalMissing`, the one
  reason the watcher deliberately does not log; `POWER_SIGNAL_PATH` and `POWER_SIGNAL_PATHS` are both
  rendered with overlapping content; and `seen` grows unbounded over the pod's life.

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

- Reconcile the `HEALTHCHECK` statement (`F-53`). The NUT Server *Built* paragraph in
  `docs/tasks.md` says the inert Docker `HEALTHCHECK` "is gone", while `NS-3` and
  `images/nut-server/Dockerfile` both say it exists and runs the readiness probe's `upsdrvctl status`
  check verbatim. The receipt describes the removal and not the replacement that followed it.
  Recorded in [nutserver-pod-audit.md](audits/nutserver-pod-audit.md).
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
