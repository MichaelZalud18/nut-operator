# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Architecture, security, API, and design documents describe the system in its finalized form. Current
build state, open implementation work, and validation gates live here so the architecture docs do
not become a progress diary.

Work is organized by component so it can be picked up independently. Each component section lists
what is built and what remains open, with design-doc identifiers (`OD-n`, `PL-n`, etc.) and audit
findings (`F-n`) cited so the reasoning behind an item is one click away rather than re-litigated.
Items that genuinely span two components are listed under their primary owner with a cross-reference.

Last reviewed: 2026-08-05

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/design/inventory-provider-contract.md` (`IN-n`).

#### Built

- `internal/inventory`: pure compiler — validation, power-domain closure over `feeds`, carrier
  ordering from `carries`, orphan rule with exemptions (`IN-3`/`IN-5`/`IN-7`/`IN-9`/`IN-11`/`IN-12`).
- `PowerInventoryNode`/`PowerInventoryEdge`/`PowerInfrastructure` CRDs and the domain label
  (`IN-1`/`IN-10`); `PowerInventoryEdge` is the sole topology author.
- Admission webhooks and controller validators for all four inventory CRDs.
- Compiler wired into `ShutdownFlow` reconciliation; an invalid graph blocks or degrades acceptance.
- Compiled topology hash folds into plan identity (`IN-14`).
- Numbered shutdown tiers on `spec.roles.shutdownTier` (`OD-4`).
- Coverage: compiler determinism, domain derivation, orphan rules, resolver end-to-end, webhooks.

#### Open Work

- **Wire the derived topology into the planner.** Partially started 2026-08-05: `Topology.Domains`
  now reaches the planner and is consumed by `PL-19` trigger-capability validation. What still has no
  consumer is the part that shapes the plan itself — wave compilation does not read derived domain
  membership, trigger *evaluation* still runs off live telemetry snapshots rather than the derived
  closure, and `carries`-based ordering (`PL-21`) has no consumer at all. This is the single highest-value remaining item in this
  component; see the matching entry under Planning & Execution Logic.
- No cross-check that a `PowerInventoryNode`/edge reference names a real `corev1.Node` — identity is
  trusted by convention (`IN-13`), never verified against the cluster.
- `IN-16` snapshot age ceiling has no implementation (no config field, no staleness condition).
- NetBox as a second topology provider (`SB-8`) is docs-only — zero code exists. Decide whether and
  when to build it, or drop it from the design docs if it's not actually planned.
- `OD-16` (missing `carries` coverage) is effectively resolved in code as warning + exemption, but
  `scope-boundaries.md` still lists it open — close the registry entry to match the implementation.
- `OD-18` tier inversion (lower-tier workload on a higher-tier node) has no compile-time validation,
  opt-in migration, or blocking behavior implemented.
- Controller-level regression tests for multi-object graph failures (orphan rejection, duplicate
  edges) end-to-end through `ShutdownFlowReconciler` — today this is only tested at the
  compiler/resolver layer, not proven through the actual reconcile path.
- `isSupportedInventoryEntityKind` is implemented twice (`internal/controller/validation.go` and the
  edge webhook) — worth factoring into one shared helper.

---

### Capability Profiles

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/provenance design surface. Design docs:
`docs/design/capability-profiles.md`.

#### Built

- `UPSCapabilityProfile` CRD and the five-tier match precedence chain.
- Bundled catalog (`config/catalog/upscapabilityprofiles.yaml`) at `1.0.0`; Ubiquiti quirks
  firmware-scoped against field evidence. Manifest drift test covers variables and aliases.
- Matched profiles carry declared content, not just identity, and that content folds into plan
  identity (`PL-30`).
- `F-25` closed: `spec.telemetry.aliases` (a map) applied at normalization, with precedence rules
  and diagnostics settled (`OD-23`).
- `UPSCapabilityProbe`: drafts a profile and an issue report from a real device, advisory only
  (`RS-7`–`RS-10`).
- `OD-31` closed: unidentified devices block `Enforce` unless `spec.safety.allowUnidentifiedDevices`.
- `ProviderModelMissing` diagnostic when a missing model actually costs a match.
- Settled scope: profiles influence `upsd` config but never sizing; queried devices get profiles,
  topological devices do not; provenance and upgrade-safety guarantees drafted.

#### Open Work

- **`F-26` firmware-gated quirks can't expire.** `Quirks` is a flat `[]string` with no firmware
  constraint, so a device on current firmware inherits every historical quirk of its model family
  permanently. Decide between structured quirk objects and firmware-ranged selectors (`OD-22`).
  The concrete case that exposed it (2026-08-04, see `docs/design/capability-profiles.md` Field
  Verification) has since had its *data* corrected — see Built above — but the structural question
  is untouched: quirk strings still carry firmware scope only by naming convention, and nothing
  validates or enforces it. Needs a decision, not just another data fix.
- **`F-27` verification lifecycle is undefined** for actuation commands: what counts as verified,
  where the result is recorded, how a verified result becomes a profile change, and whether a
  locally-verified user profile can declare support without a catalog release.
- `OD-19` FSD usage decision (declare non-use explicitly, or adopt as the release signal) —
  implementation would live in NUT Server / upsd, but the decision belongs to the capability/actuation
  design.
- `OD-20` instant-command scope and gating (`upscmd`/`upsrw` surface), starting with the
  `shutdown.return` handshake and `test.battery.start`.
- `OD-21` driver configuration ownership — profile vs. `UPSDevice` spec. A hybrid (profile default,
  spec override) matches the existing `RS-5` precedence pattern and is the likely shape.
- `OD-24` non-NUT power device actuation (UniFi RPS-class devices) — currently topological-only by
  design; revisit alongside `OD-10` (USB support), since both concern control surfaces outside the
  NUT-network-only posture.
- `OD-25` PDU capability profile kind — scaffolding only, not started.
- `OD-26` provenance field semantics — advisory metadata today; decide whether it should ever gate
  resolution (e.g. `Community` profiles requiring opt-in).

---

### Telemetry & Triggers

Owns: NUT protocol polling (`internal/nut`), normalization (`internal/telemetry`), poll composition
(`internal/polling`), and trigger evaluation (`internal/trigger`). Design docs:
`telemetry-and-triggers.md`, `resiliency-and-partitions.md`.

#### Built

- `internal/nut`: real NUT protocol client (`LIST VAR` framing, `ERR` handling), not an `upsc` wrapper.
- `internal/telemetry`: pure normalization to phase, charge, runtime, load, plus diagnostics.
  Profile-declared aliases resolve here (`F-25` runtime half).
- `internal/polling`: transport + normalizer per target, with the `ups_telemetry_snapshots` adapter.
- `internal/trigger`: pure evaluator — eligibility, selected devices, hold state, diagnostics.
- `UPSDevice` reconciliation polls, updates status, and records snapshots when storage is ready.
- `ShutdownFlow` trigger evaluation wired end to end, deduplicated by `deduplicationKey`.
- `dummy-ups` repeater mode for upstream NUT appliances (`spec.upstreamNUT`), correcting `F-22`.

#### Open Work

- `PL-19` trigger-capability degrade mechanics (`OD-9`): the reject-vs-degrade split (some devices
  vs. all devices in a domain) is decided in principle, but the actual coarser-trigger substitution
  table isn't built.
- `OD-14` partial-domain outage plan scope (cluster-wide vs. domain-scoped) — blocks how trigger
  firing should interact with multi-domain topology once inventory-derived domains are wired into
  the planner (shared with Inventory System and Planning & Execution Logic).
- `AE-6` capability gating for mid-flow adaptive execution needs a declared "firmware recomputes
  runtime against present load" capability check before the tier-pointer/timing-mode work in
  Planning & Execution Logic can safely turn adaptation on for a given device.

---

### Planning & Execution Logic

Owns: `internal/planner` (pure compile), `internal/executor` (wave execution/evidence),
`internal/kubeactions` (action runner), and `internal/shutdownflow` plus the `ShutdownFlow`
controller wiring that connects them. Design docs: `planner-requirements.md`,
`executor-requirements.md`, `shutdown-flow.md`, `adaptive-execution-tier-pointer.md`.

#### Built

- `internal/planner`: pure graph compilation — edges, cycle detection, waves, deterministic plan
  hash, structured diagnostics, degradation handling.
- `PL-19` trigger-capability validation wired, using the some/all split (reject vs degrade).
- Derived power domains reach the planner as trigger-validation scope; wave compilation still does
  not read them (see Open Work).
- `OD-4` closed: tier policy, resolution precedence, derived tier edges, tier status on steps/waves.
- Planner artifacts (`internal/planner/artifacts.go`): normalized graph, provenance-tagged
  explanations, advisory startup projection, Mermaid/Graphviz/D2 exports.
- `ShutdownFlow` dry-run dispatch into `internal/executor` with `status.lastExecution` and
  `ExecutionReady`.
- `internal/executor`: ordered wave evidence, action attempts, releases, handoffs, resume state,
  restart-safe (`EX-14`).
- `internal/kubeactions`: enforce-mode `ScaleWorkload`, `CordonNodes`, `DrainNodes`, `RunWorkflow`,
  `AgentShutdown`.
- Node-agent coverage gating: enforce blocks releases without a ready agent, dry-run records the
  same facts.
- `F-31` closed operator-wide (status writes are patches). The unpredicated `UPSDevice` watch it
  exposed is still open below.

#### Open Work

- **Consume the inventory-derived topology.** The planner still doesn't read
  `Topology.Domains`/`Topology.CommunicationOrders` from the inventory compiler — this is the
  execution-side half of the Inventory System's top open item.
- **Fold the provisional adaptive-execution design into real identifiers.** Tier-pointer
  descent/ascent, the three timing modes (`Relaxed`/`Nominal`/`Urgent`), and asymmetric hysteresis
  are fully specified in `adaptive-execution-tier-pointer.md` (`AE-1`–`AE-6`) but **none of it is
  implemented**. Folding into real `PL`/`EX` numbers is a documentation step; building the pointer,
  mode selection, and hysteresis logic is the actual engineering work.
- `OD-27`–`OD-30`: hysteresis count/margin, relationship to `OD-12`, tier-ascent trigger condition,
  and cadence intervals — all open parameters the adaptive-execution build needs decided first.
- **`Wait` and `Gate` are declared API actions that execute as no-ops.** The enum accepts them,
  `spec.groups[].duration` documents itself as "used by Wait steps", and `spec.groups[].timeout`
  exists — but `kubeactions.Runner.runAction` returns `noop: true` for `Notify`/`Wait`/`Gate` and no
  executor code reads either duration. Same "looks load-bearing, does nothing" shape as `F-25` and
  `F-33`. Either implement them or remove them from the enum; a declared action that silently does
  nothing is worse than an unsupported one.
- **Make the pre-shutdown hook engine-neutral, and write the scope limit down.** The user-facing
  need is ordinary: run a workload's own pre-shutdown routine (a database snapshot, a quiesce) at a
  high tier while the workload keeps serving, and shut it down at a lower tier. Ordering already
  expresses this — two groups at two tiers. What does not hold up is the hook itself.
  `workflowObject` parameterizes the GVK (`workflow.apiVersion`/`workflow.kind`, defaulting to
  `argoproj.io/v1alpha1 Workflow`) but always builds an Argo-shaped body —
  `workflowTemplateRef`, `entrypoint`, `serviceAccountName`, `arguments.parameters` — and RBAC only
  grants `argoproj.io/workflows`. So a non-Argo target is nominally addressable and practically
  unusable. Owning workflow orchestration is out of scope (`GP-4`, `GP-7`): the operator should
  invoke a hook and publish the fact, never become the engine that runs it. This boundary should
  become a numbered scope entry (`SB-15` is next) rather than living only in a task note.

  **Direction decided 2026-08-06: prefer a transport-generic hook over Kubernetes-native ones.**
  Custom hooks are most likely to target things the operator does not manage and Kubernetes cannot
  address — a NAS, a bare-metal database, a hypervisor, a switch. None of those have a CRD, so a
  Kubernetes-object hook cannot reach the systems that most need one. Proposed shape, to be designed
  before building:

  - A dedicated `ShutdownHook` resource referenced by name from a group, rather than today's
    `params` map. Reusable across flows, reviewable in Git on its own, and it keeps `ShutdownFlow`
    readable — the "write the invocation elsewhere and reference it" shape.
  - HTTP delivery with a CloudEvents-shaped body as the primary transport, carrying execution ID,
    plan hash, flow, group, tier, wave, and trigger context. This is the interop lingua franca:
    Tekton emits CloudEvents to a configured sink, Alertmanager posts to webhook receivers, Argo
    Events consumes them. Anything that accepts an HTTP POST becomes reachable, in or out of the
    cluster.
  - A Kubernetes-object transport kept as a second option, taking a user-supplied object body so any
    GVK works — `batch/v1` `Job` needs no engine at all, and Argo becomes one example rather than
    the assumption.
  - Failure-path constraints, which are what separate this from an ordinary notifier: hooks are
    declared ahead of time (`GP-5`, nothing discovered mid-outage); every call is bounded by a short
    timeout; a failed or slow hook degrades and is recorded but never holds the wave; secrets are
    referenced, never inline; TLS verification stays on; and outbound endpoints likely want an
    allowlist on `PowerManagementCluster` per `GP-2`, since this would be the operator's only
    outbound egress to arbitrary hosts.
  - Dry-run needs a deliberate answer, TBD. The runner is never invoked in dry-run
    (`internal/executor`'s `if !dryRun` guard), so hooks structurally cannot fire — correct, but it
    means dry-run currently proves nothing about a hook: a wrong URL, a rotated credential, or an
    endpoint that has moved is discovered during the outage. At minimum dry-run should record the
    request it *would* have sent. The candidate worth designing against: let the hook declare its
    own dry-run invocation alongside the real one, so the author decides what a safe rehearsal means
    for their system — a different endpoint, the same endpoint with a rehearsal flag, or a read-only
    call that proves reachability and auth without side effects. That keeps the operator out of
    guessing which calls are safe to repeat, which it cannot know for a system it does not manage.
- **Waiting on a hook is deliberately undecided (2026-08-06).** Whether the executor blocks on a
  hook's completion is TBD. What is decided: **the default is that shutdown proceeds anyway.** A
  hook that has not finished never becomes a reason to keep nodes up while battery runtime drains —
  a one-hour snapshot may simply not be affordable during the outage it was meant to protect
  against. Any future waiting mechanism is opt-in, bounded, and must state what happens when the
  budget runs out first.
- `OD-12` infeasible-plan policy field default and options (reject/warn/truncate), referenced by
  `EX-3`, not yet decided or implemented.
- `OD-18` tier inversion validation (shared with Inventory System — this is the planner
  tier-compilation half). Detection falls out of the same label scan as the defaulted-tier
  diagnostic below: resolve every node's tier and every workload's tier, then report workloads whose
  tier is lower than the node they sit on, since that node cannot clear under `PL-20` while they
  run. Worth publishing as a count so it can be watched rather than only read at compile time. Note
  what users can do about it without operator support: `nodeAffinity` accepts `Gt`/`Lt` on
  integer-valued node labels, so a workload can already require a node at a more desirable tier.
- **Report tiers that were defaulted rather than declared.** A group with no resolvable tier falls
  back to `spec.shutdownTiers.defaultTier`, which is legitimate configuration and currently silent.
  The tier compiler diagnoses malformed tiers (`ShutdownTierInvalid`, `DuplicateShutdownTier`,
  `ShutdownTierZeroTargeted`, `ShutdownTierDefaultReserved`) but never says "this fell back," so a
  typo'd label key is indistinguishable from a deliberate default — and something the user never
  intended to be ordinary silently becomes tier 4. Emit an informational diagnostic naming the
  target and why no tier resolved.
- **Node targeting cannot express tier ranges.** `spec.groups[].target.nodeSelector` is a
  `metav1.LabelSelector`, which supports only `In`/`NotIn`/`Exists`/`DoesNotExist` — no numeric
  comparison. `corev1.NodeSelector` (what `nodeAffinity` uses) does support `Gt`/`Lt` against
  integer-valued labels. Consider accepting node-selector *requirements* for node targeting so a
  group can say "every node above tier 2" directly instead of enumerating values or restating the
  range in `spec.shutdownTiers.selectorRules`. Namespace and workload targeting cannot gain this —
  Kubernetes has no `Gt`/`Lt` for those selector types.
- `OD-14` partial-domain outage plan scope (shared with Telemetry & Triggers).
- Controller/envtest coverage for executor resume behavior (restart mid-flow) — asserted by design
  (`EX-14`) but not covered by an actual restart test yet.
- **`ShutdownFlow`'s unpredicated `UPSDevice` watch causes reconcile churn.** Confirmed via a 10h
  production log pull (2026-08-04, evidence trail in the private deployment repo): 1,516
  `"the object has been modified"` errors spread evenly across the whole window (~1/48s) — 744 against
  `ShutdownFlow` specifically. The conflict-error symptom is now fixed (`F-31`, see Built above and
  Operator Maturity & Hardening), but the root cause of the churn itself is unchanged:
  `SetupWithManager` watches `UPSDevice` with no predicate, so every telemetry tick (5–15s per device)
  still re-enqueues a `ShutdownFlow` reconcile, each doing a Postgres audit-store round trip via
  `recordShutdownFlowAudit` regardless of whether anything the trigger logic reads actually changed.
  Fix: scope the `UPSDevice` watch predicate to the fields the trigger logic actually reads (phase,
  charge %, runtime seconds).

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`);
relevant findings from `docs/audits/nut-usage-audit.md` (`F-20`–`F-22`, `F-24`).

#### Built

- `NUTServer` operand rendering: Namespace, ConfigMap, Secrets, Service, NetworkPolicy, Deployment,
  and `dummy-ups` relay mode.
- `upsd.conf`/`upsd.users` rendering with injection-validated values; hardened container security
  context; project-owned image with pinned NUT packages.
- Protocol fidelity confirmed by audit (`docs/audits/nut-usage-audit.md`).
- `F-15` replicas pinned to 1; `F-16` `Recreate` strategy; `F-18` priority class and PDB.
- `F-17` readiness probe proves live driver connectivity, not just registration; driver-start
  failures are non-fatal to the container.
- `F-21` `upssched` non-use and `F-23` privileged-user separation are recorded decisions
  (`docs/audits/nut-usage-audit.md`, `docs/audits/nutserver-pod-audit.md`); `F-24` confirmed no
  credential leak path.
- `UPSDevice.spec.credentialSecretRef` wired; `ups.conf` moved into a dedicated Secret.
- Scripted `dummy-ups` transitions via `spec.simulation.sequenceConfigMapRef`
  (`docs/examples/simulation/`), plus an `snmpsim` driver-conformance fixture
  (`images/snmpsim-fixture/`).
- Fixed: operand-namespace creation ordering, and a cross-namespace `NetworkPolicy` gap that left
  telemetry polling silently unreachable.
- e2e target state met for `dummy-ups`/`snmp-ups`/`NUTServer` coverage. Real actuation stays out of
  scope for `kind`.

#### Open Work

- **`spec.tls` mounts a certificate and never tells NUT to use it.** The webhook defaults
  `spec.tls.mode` to `Required` and validation rejects `Required` without a
  `serverCertificateRef`; the render mounts that Secret read-only at `/etc/nut/tls`. Nothing then
  emits `CERTFILE` into `upsd.conf`, or `CERTVERIFY`/`FORCESSL` into `upsd.conf`/`upsmon.conf` —
  `renderNUTServerConfig` writes `upsd.conf` as a bare `LISTEN <address> <port>`, and the image's
  entrypoint adds nothing. So a `NUTServer` that reports TLS `Required` serves plaintext NUT on
  3493, and `upsmon.conf`'s `MONITOR <ups> 1 <user> <pass> secondary` login crosses the wire in the
  clear. Confined to in-cluster traffic and constrained by the operand `NetworkPolicy`, but the API
  claims a protection that is not in effect — the same "declared field that does nothing" class as
  `F-25` and `F-33`, and the only instance of it that is security-relevant. NUT's own guidance
  (`docs/security.txt` upstream) treats `CERTVERIFY 1` plus `FORCESSL 1` as the pair that makes TLS
  real. Fix the render, and assert the directives in `nutserver_render_test.go` so the claim cannot
  silently regress again.
- **`NUTServer` reconciler doesn't watch `UPSDevice` or unowned credential `Secret`s.**
  `SetupWithManager` (`nutserver_controller.go`) only watches `NUTServer` itself plus resources it
  owns (`Owns(&corev1.Secret{})` only matches secrets with an owner reference back to it — not a
  user-supplied `credentialSecretRef` target, which has none). A `UPSDevice.spec.credentialSecretRef`
  change, a `driverOptions` change, or the referenced Secret's contents changing all silently do
  nothing until some unrelated reconcile happens to fire. `ShutdownFlow` has the opposite problem
  (watches `UPSDevice` with no predicate, reconciling far too often — see Planning & Execution
  Logic); `NUTServer` needs the predicate-scoped version of that same watch, not zero watch at all.
- `OD-20` instant command scope, narrowed and deprioritized (2026-08-03): the operator's actuator
  already owns real shutdown (nodes and workloads). The only remaining use case for NUT instant
  commands is the tail end after the operator has already finished — `shutdown.return` stops the UPS
  discharging into a dead load and auto-restores power when line power returns. Redundant with the
  actuator for anything actually running in the cluster; only matters for non-cluster hardware on
  the same UPS or battery-waste cleanup. Not pursued unless that narrow case becomes a real need.
- Credential rotation and advanced driver-specific config for the NUT operand render path.
- SNMPv3 coverage for the `snmpsim` driver-conformance fixture (currently SNMPv2c community-auth
  only, see Built above) — would need `snmpsim`'s USM configuration wired to match a `credentialSecretRef`
  fixture Secret. Not pursued: the conformance question (OID/decode correctness) is orthogonal to the
  auth mode, and production hardware's SNMPv3 path is already exercised for real in
  `docs/audits/nut-usage-audit.md`'s alpha-hardware findings.

#### Deferred / Declined (2026-08-03)

- `OD-19` FSD usage — deferred. Staying on the executor's own signal file; no plan to also wire up
  NUT's native forced-shutdown broadcast.
- `F-17` follow-on (per-device telemetry-freshness readiness) — declined. Proving the driver
  connected (2026-08-04: `upsdReadinessProbeScript`, a real `ups.status` query) is the right
  stopping point. Tying pod readiness to live telemetry *freshness* on top of that would drop every
  connected node agent into DEADTIME on a single flaky poll — worse than the gap it would close.
- `F-19` `topologySpreadConstraints`/anti-affinity — confirmed low value at current scale. Only
  matters with multiple `upsd` instances spread thin across nodes, and a colocated failure just
  degrades the affected domain to `Unknown` feasibility rather than doing anything unsafe.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`. Audit: `docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`, `F-33`–`F-36`).

#### Built

- `NodePowerAgent` DaemonSet rendering with `MonitorOnly`/`DryRun`/`Actuate` modes; `F-8`–`F-12`
  closed (rollout strategy, priority, tolerations, probes).
- `power-signal-writer`: project-owned `SHUTDOWNCMD` binary writing to a projected signal Secret.
- `internal/nodeagent`: signal validation, TTL and node-name enforcement, dry-run skip, both handoff
  paths watched.
- `cmd/node-actuator`: syscall-backed poweroff with a non-Linux stub.
- `F-13` narrow privilege model: `hostPID` + `CAP_SYS_BOOT` only, no service-account token.
- `F-14` self-exclusion enforced structurally in the executor (`protectedNamespaces`).
- `F-24` confirmed safe, including the `upsmon.conf` Secret hash.
- `F-33` closed: `requireFreshTelemetry` is enforced per agent, failing closed.
- `F-34` closed: non-zero resource defaults for both tier-0 containers.
- `F-35` closed: `upsmon` liveness probe; the actuator's deliberate lack of one is a test invariant.
- Signal handoff proven on a real `kind` cluster within the configured TTL.

#### Open Work

- **`F-36` `node-actuator`'s `command` poweroff method is implemented but unreachable through the
  CRD.** `cmd/node-actuator/main.go`'s `runPoweroff` fully supports `POWER_POWEROFF_METHOD=command`
  (`runPoweroffCommand`, arbitrary command + args), but
  `nodepoweragent_render.go`'s `nodePowerAgentPoweroffMethod` hardcodes `"reboot-syscall"` and ignores
  its `*NodePowerAgent` parameter entirely — there is no `spec` field that can select the `command`
  method. Not fixed: this needs a real design decision (a new spec field, its validation, and whether
  exposing an arbitrary host command from a CRD is even desired) rather than a unilateral API addition.
  The `reboot-syscall` default is also the narrower, `F-13`-preferred privilege model
  (`CAP_SYS_BOOT` alone vs. a broader `systemctl`-invoking path), so this may be intentionally
  unreachable rather than an oversight — needs a decision either way, not just code.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

#### Built

- Single structured planner artifact (`PL-45`–`PL-48`).
- Dependency graph as normalized vertices/edges with relation type, source refs, provenance, and
  explanations — answerable from structure, not logs.
- Deterministic Mermaid, Graphviz/DOT, and D2 exports as renderers only.
- Kubernetes-first interface complete: CRDs, status, Events, logs, PostgreSQL. No UI (`GP-7`).
- Advisory startup wave projections published for recovery subscribers (`OD-1`/`OD-5` closed).
- `F-3` closed: `internal/metrics` on controller-runtime's registry, instrumented at the impure
  boundary. Contract in `docs/metrics.md`.

#### Open Work

- Once the planner consumes inventory-derived domains and communication ordering (see Planning &
  Execution Logic), the published graph/domain artifacts should reflect that richer structure — right
  now the published graph is only as complete as the planner's current inputs.
- **Metrics coverage is not exhaustive.** `F-3`'s highest-value candidates are covered (see Built), but
  per-`UPSDevice` telemetry poll metrics, capability-match metrics, and inventory compiler metrics
  (domain/orphan counts) are not — see `docs/metrics.md`'s Open Work note. Also open: threading the
  planner's actual diagnostic classes through to `compile_total`'s `result` label instead of the
  coarser rejection-reason string, which needs `internal/shutdownflow`'s adapter functions to stop
  discarding `planner.Compile`'s diagnostics return value first.
- No worked example showing how an external subscriber (dashboard, recovery orchestrator) actually
  consumes the published artifacts in practice — the contract is documented, but there's no sample
  integration.
- `F-6`: document `ExecutionReady`/`TriggerEligible` as part of the public condition-type API
  surface, since users will alert on them and they're bespoke (not standard Kubernetes condition
  types).

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/design/audit-storage-schema.md`.

#### Built

- Backend resolution (`internal/storage`) for `Disabled`/`ExternalPostgres`/`CNPG`, with connection
  management kept out of domain validation.
- Full audit schema (`internal/audit`) through migration 5, including retention indexes; executor
  child tables cascade from their parent execution.
- Planner compilations persist waves, graph, startup projection, explanations, and diagram exports
  as JSONB.
- Retention enforcement for the `events` and `telemetry` families.
- Pooled connections expire (30m lifetime, 5m idle) so a CNPG failover cannot strand them.
- `OD-6` closed both halves: the shutdown-time spool and `audit.ReplaySpool`, which drains it on the
  first reconcile that can write again.
- Spool journal bounded by `spec.storage.auditSpool.maxSize`; a full journal reports `AuditSpoolFull`
  rather than growing or failing the reconcile.
- Spool and replay metrics (`nutoperator_audit_spool_*`), documented in `docs/metrics.md`.
- Coverage: outage-and-recovery round trip and a capped journal, both through real reconciliation.
- Why this component exists at all — the deviation from the reconciliation model, its cost, and its
  precedent — is written up in `docs/design/audit-storage-schema.md`.

#### Open Work

- Confirm the `capability_profile_verifications` schema (`OD-15`, closed in design) is actually
  populated end-to-end once the Capability Profiles drift-detection path (`RS-7`–`RS-10`) exists —
  right now the table exists ahead of its writer.

---

## Cross-Cutting

Work that doesn't belong to one component — either because it spans several, or because it's about
the operator as a whole.

### Operator Maturity & Hardening

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

#### Built

- `observedGeneration` across all ten CRDs, enum validation, single storage version, spec/status
  separation.
- Source hardening for ASH/Checkov findings; `make security-scan` runs ASH locally and in CI.
- `F-1` finalizers with `OperandTeardown` Events; `F-2` leader election on by default; `F-4`
  reserved operand namespaces rejected at both layers; `F-5` RBAC scope documented in
  `docs/security.md`.
- `F-7` convergence and idempotency tests for both rendering controllers; `F-28` CNPG watch gated on
  CRD presence; `F-29` e2e namespace labeled for the metrics NetworkPolicy; `F-30` manager protects
  itself from its own flows; `F-31` status writes are patches; `F-32` dedicated Pod cache for the
  agent watch.
- Project-owned multi-arch images for all four operands with SBOM, provenance, and scanning.
- CI: concurrency cancellation, path filters, timeouts, shared `bin/` cache, tidy-drift check,
  pre-push image scanning on PRs, and a `security.yml` running ASH plus an RFC1918 `private-ip-scan`.
- Resolutions with their root causes and verification are recorded per finding in `docs/audits/`.

#### Open Work

- **Drop the hard cert-manager dependency using `open-policy-agent/cert-controller`, with an opt-out
  for operators who already have PKI.** Two stages, in order: first validate it is a usable path
  (does it own the serving cert and reconcile `caBundle` into both webhook configurations, what RBAC
  on `*webhookconfigurations` it needs, whether its known caBundle-sync issues are current, and how
  it behaves alongside an existing cert-manager install), then adopt it as the default with a flag
  that hands cert ownership back to cert-manager or a user-supplied Secret. Motivation: "install
  cert-manager first" is a real adoption tax for an operator someone installs once, and Gatekeeper
  demonstrates the pattern at scale. Note the constraint documented in `docs/install.md`: removing
  the webhooks entirely is not an alternative, because the `NodePowerAgent` defaulter is the only
  thing that sets `spec.resources` and `spec.placement.priorityClassName`.
- Release image signing policy, cosign verification docs, and immutable digest production examples
  (`docs/images.md` describes the target state; keyless Sigstore signing as a release gate isn't
  confirmed wired into CI yet).
- Triaging new unsuppressed medium-or-higher ASH findings is still manual; the scan itself runs on
  every push/PR.
- Decide container-mode vs. locally-installed `grype`/`syft`/`opengrep`/`cfn-nag`/`cdk-nag` for full
  ASH coverage — confirmed still `MISSING` in the scan output, not just undecided in principle.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

#### Built

- Component-scoped design docs under `docs/design/` with stable identifier namespaces.
- Governing principles, scope boundaries, and the decision index (`docs/design/scope-boundaries.md`,
  `docs/design/decision-index.md`).
- Architecture, security, metrics, images, and shutdown-flow references under `docs/`.
- Audit records under `docs/audits/`, each owning its findings and their resolutions.

#### Open Work

- Refresh the example pod placement diagram and add it to `docs/diagrams/` — currently held back in
  the private planning folder pending a redraw; node naming (tree names vs. the Orion example's
  `orion-*` convention) is still undecided.
- Reconcile the Orion example's string shutdown-tier labels (`application`/`data`/`storage`) with the
  numbered tier scheme closed under `OD-4`. Numbered tiers take precedence, but named tags like
  `storage`/`data` may still occur in practice and need a defined mapping or coexistence rule rather
  than silently colliding.
- `OD-16` registry cleanup: mark closed in `scope-boundaries.md` to match what `internal/inventory`
  already implements (see Inventory System).

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
