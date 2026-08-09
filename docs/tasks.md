# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Architecture, security, API, and design documents describe the system in its finalized form. Current
build state, open implementation work, and validation gates live here so the architecture docs do
not become a progress diary.

Work is organized by component so it can be picked up independently. Each component section lists
what is built and what remains open, with design-doc identifiers (`OD-n`, `PL-n`, etc.) and audit
findings (`F-n`) cited so the reasoning behind an item is one click away rather than re-litigated.
Items that genuinely span two components are listed under their primary owner with a cross-reference.

Work deliberately targeted after v1 lives in [tasks-post-v1.md](tasks-post-v1.md) so this file
stays answerable to one question: what is left before v1. Items move there only when something
outside the project gates them or scope-boundaries places them beyond v1 — never merely because
they are hard or unscheduled.

Last reviewed: 2026-08-09

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
- **Declared node names are checked against the cluster (`IN-13`, 2026-08-07).** A
  `PowerInventoryNode` naming a node that does not exist raises `InventoryNodeNotInCluster`
  instead of silently producing a power domain covering a node nothing can shut down. Warns
  rather than rejects, since inventory is legitimately authored ahead of the hardware; stays
  silent when no nodes are visible at all, because there is nothing to check against.
- **`OD-16` closed in the registry (2026-08-07)** to match the warning-plus-exemption behavior
  `internal/inventory` already implemented.
- **`IN-16` snapshot age escalates rather than cutting off (2026-08-08).** Age raises the severity a
  snapshot is reported at, through thresholds on
  `PowerManagementCluster.spec.inventory.snapshotAgeLevels`; `Info` is recorded without changing
  conditions, `Warning` degrades the flows compiled from it, and an unconfigured cluster gets `Info`
  at one hour and `Warning` at six. There is no rejecting level: a ceiling would contradict `IN-15`
  at the worst possible moment, since the outage is exactly when the provider is unreachable and the
  shutdown still has to be planned. The declarative provider stamps no snapshot time — it is rebuilt
  from live resources every resolve — so evaluation stays silent for it rather than inventing
  staleness from a missing field. The escalation path therefore has no live producer until a provider
  that renders snapshots exists, which is `SB-8` below.
- **Graph failures are proven through the real reconcile path (2026-08-07).** Orphan rejection and
  duplicate-edge rejection now have envtest specs asserting the `ShutdownFlow` lands on the right
  `Accepted` reason with no compiled plan, rather than being tested only at the compiler layer.

#### Open Work

- **Finish wiring the derived topology into the planner.** `Topology.Domains` reaches the planner
  and is consumed by `PL-19` trigger-capability validation, and `PL-20` node clearance now shapes
  wave order (see Planning & Execution Logic). What still has no consumer: wave compilation does
  not read derived *domain membership*, trigger evaluation still runs off live telemetry snapshots
  rather than the derived closure, and `carries`-based ordering (`PL-21`) has no consumer.
- NetBox as a second topology provider (`SB-8`) is deferred, not dropped. It stays in the v1 design
  docs and is deliberately the last thing built: an integration against an external source of truth
  is worth little until the rest of the operator is stable, and every hour spent on it is an hour not
  spent on the paths that run during an outage. Nothing else in this section depends on it.

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
  (`RS-7`–`RS-10`). It also verifies the device against its matched profile and writes probe history
  to PostgreSQL (`OD-15`, see Storage & Audit); drift surfaces on the probe's Degraded condition so
  it is visible without querying the database.
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
- `OD-25` PDU capability profile kind — scaffolding only, not started.
- `OD-26` provenance field semantics — advisory metadata today; decide whether it should ever gate
  resolution (e.g. `Community` profiles requiring opt-in).

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
- **Planner diagnostics reach the flow (2026-08-07).** Every caller discarded `planner.Compile`'s
  diagnostics return value, so the planner's own findings ended at the function boundary: a rejection
  reason, a degraded trigger, a structural warning — none of it was visible in status or audit. The
  adapter now returns a `CompiledFlow` carrying them, planner warnings degrade the flow under their
  own reason, and every diagnostic including informational ones is recorded in the compilation audit
  row under source `Planner`. This is what makes the two diagnostics below observable rather than
  dead code, and it unblocks threading real diagnostic classes into `compile_total`'s result label.
- **`OD-18` tier inversion is detected and blocked, and the decision is closed (2026-08-08).** Tiers
  count down, so a node at tier 4 is meant to be gone while a group at tier 2 is still working; if
  that group runs on that node, the plan says something the cluster cannot do. Compilation reports
  `ShutdownTierInversion` naming the group, the node, and both tiers, and now also withholds that
  node from power-off for the whole flow — the withheld nodes reach `status.blockedNodeReleases` and
  are filtered out of the executor's release set, so the block changes what runs rather than only
  what is read. Detection uses the node membership `PL-20` already resolves plus each node's declared
  inventory tier.

  Blocking is the default because its failure mode is powering off less of the cluster than intended,
  while the alternative cuts power to work the author declared as still needed. `spec.groups[].`
  `tierInversionPolicy: Allow` opts a single group out; the inversion is still reported, as
  `ShutdownTierInversionAllowed`, because opting in accepts a risk rather than retiring it. One
  dissenting group holds a node up regardless of what its neighbors chose. Migration was rejected as
  a general remedy: node-local PVCs mean there is not always anywhere to migrate to, which is what
  kept the decision open.
- **Defaulted tiers are reported (2026-08-07).** A group inheriting `defaultTier` was silent, which
  made a mistyped tier label indistinguishable from a deliberate default — something never meant to
  be ordinary quietly became tier 4. `ShutdownTierDefaulted` names the group and the tier applied, at
  a new informational severity that never degrades a flow.
- **`PL-20` node-clearance edges are derived and shape wave order (2026-08-07).** The planner could
  not previously name a node: `PlannerTarget` reduces every selector to booleans and counts, so a
  group's node membership never reached compilation and `PL-20` had nothing to derive from. Group-to-
  node membership is now resolved before compiling — nodes a group *acts on* by matching its node
  selector against real node labels, nodes a group *releases* from
  `NodePowerAgent.status.selectedNodes` via `target.agentRefs`, which is the same resolution the
  executor performs at release time so the plan cannot disagree with execution. For each node, every
  group acting on it is ordered before the group releasing it.
  The edges go into the graph wave compilation already reads, so they change execution order rather
  than describing it. Concretely: a flow that drains rack-a and powers off rack-a with no declared
  ordering used to compile both into one wave — draining a node while cutting its power. It now
  compiles into two. Cluster reads live in the controller (`clusterNodeContext`), selector matching
  in the adapter, derivation in the pure planner; the membership folds into the structural hash while
  raw node labels deliberately do not, so an unrelated label edit cannot churn plan identity.
  Covered end to end: derivation, wave reordering, determinism under shuffled input, no self-edges,
  unknown groups ignored, cycles rejected, and an envtest reconcile against a real `Node` plus agent
  coverage asserting the published `NodeClearance` edge.

#### Open Work

- **`PL-21` communication-path edges have no actuation target yet.** `Topology.CommunicationOrders`
  is computed by the inventory compiler and reaches nothing. The constraint it expresses — a network
  device carrying node N's control-plane or NUT path cannot shut down before N — is only meaningful
  once the operator can actuate a network device, which today it cannot: switches are topological-only
  by design (`OD-24`). Wiring the data through now would add a planner input with no consumer, the
  same dead-field shape as `F-25` and `F-33`. Revisit alongside PDU outlet control (`OD-25`), which is
  what would make a network device a shutdown target in the first place.
- **Wave compilation still ignores derived domain membership.** `PL-20` clearance is derived per node;
  domain membership shapes nothing in the compiled plan. Blocked on `OD-14` (whether a partial-domain
  outage compiles a cluster-wide or domain-scoped plan), which decides the semantics before the wiring
  is worth doing.
- **Node clearance is derived at compile time and not revalidated at execution (`OD-11`, `PL-43`).**
  Group-to-node membership is resolved when the plan compiles; pods move afterward. The design already
  says clearance is revalidated at execution alongside instance resolution — that revalidation is not
  implemented.
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
- Publish tier inversion as a metric so it can be watched rather than only read at compile time. It
  develops over time as workloads reschedule, so a compile-time-only view misses it. Note what users
  can already do without operator support: `nodeAffinity` accepts `Gt`/`Lt` on integer-valued node
  labels, so a workload can require a node at a more desirable tier.
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
- **`F-39`/`F-40` fixed: the operands are built from source and proven to negotiate TLS
  (2026-08-09).** `images/nut-server` and `images/upsmon-agent` were `apk add nut` on Alpine, which
  is NUT 2.8.2 linked against **NSS**. `CERTFILE` is OpenSSL-only, so `upsd` rejected it as an
  invalid directive and `spec.tls.mode: Required` served plaintext on 3493 with the `MONITOR`
  password in it. Both images now build NUT 2.8.5 from a checksum-pinned tarball with
  `--with-openssl --without-nss`, and the build fails if the resulting binary is not linked against
  libcrypto — a base-image bump can no longer change the TLS backend underneath the operator. Only
  the network drivers are compiled in, which matches the API already rejecting USB and serial.

  `F-40` surfaced while proving the fix: `CERTPATH` was rendered as a PEM file, but NUT passes it to
  `SSL_CTX_load_verify_locations` as OpenSSL's `CApath`, which is a directory of hash-named
  certificates. A file there loads silently and verifies nothing, so `Required` would have taken
  every agent monitoring that server offline. An init container now rehashes the bundle into a
  memory-backed `emptyDir` and `CERTPATH` points at that.

  `hack/nut-tls-smoke.sh` is the gate that would have caught all of this: it starts both real images
  with the configuration the operator renders, requires `upsd` to accept `STARTTLS` and complete a
  handshake against a pinned CA, requires a protocol command to succeed inside the session, and
  requires `upsmon` to connect with `CERTVERIFY` and report that verification actually succeeded. It
  runs as the `NUT TLS handshake` CI job and fails on the previous images.

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
- NUT protocol TLS actually rendered: `CERTFILE`/`CERTPATH`/`CERTREQUEST`/`DISABLE_WEAK_SSL` in
  `upsd.conf`, `CERTPATH`/`CERTVERIFY`/`FORCESSL` in `upsmon.conf`. Previously `spec.tls` mounted a
  certificate and emitted nothing, so `mode: Required` served plaintext NUT including the `MONITOR`
  password. `verifyClientCertificates` now defaults off (was on) — it needs client certificates the
  operator does not issue, and NUT honors `CERTREQUEST` under OpenSSL only from 2.8.6.
- Scripted `dummy-ups` transitions via `spec.simulation.sequenceConfigMapRef`
  (`docs/examples/simulation/`), plus an `snmpsim` driver-conformance fixture
  (`images/snmpsim-fixture/`).
- Fixed: operand-namespace creation ordering, and a cross-namespace `NetworkPolicy` gap that left
  telemetry polling silently unreachable.
- e2e target state met for `dummy-ups`/`snmp-ups`/`NUTServer` coverage. Real actuation stays out of
  scope for `kind`.

#### Open Work

- **`F-41`: `verifyClientCertificates` is inert and should say so.** `upsd` 2.8.5 ends its OpenSSL
  initialization with `SSL_CTX_set_verify(ssl_ctx, SSL_VERIFY_NONE, NULL)` and never loads a client
  CA, so `CERTREQUEST` and the client-CA `CERTPATH` do nothing on any released NUT built this way.
  The field defaults to `false`, so nothing mis-serves today, but a user who sets it gets mutual TLS
  in the API and none on the wire. Until the NUT release that implements it exists, the field's
  documentation has to state plainly that it is inert — and admission should arguably reject `true`
  rather than accept a setting it cannot honor.
- **`NUTServer` reconciler doesn't watch `UPSDevice` or unowned credential `Secret`s.**
  `SetupWithManager` (`nutserver_controller.go`) only watches `NUTServer` itself plus resources it
  owns (`Owns(&corev1.Secret{})` only matches secrets with an owner reference back to it — not a
  user-supplied `credentialSecretRef` target, which has none). A `UPSDevice.spec.credentialSecretRef`
  change, a `driverOptions` change, or the referenced Secret's contents changing all silently do
  nothing until some unrelated reconcile happens to fire. `ShutdownFlow` has the opposite problem
  (watches `UPSDevice` with no predicate, reconciling far too often — see Planning & Execution
  Logic); `NUTServer` needs the predicate-scoped version of that same watch, not zero watch at all.
- Advanced driver-specific configuration for the NUT operand render path. Credential *rotation* is
  no longer carried as its own item: rotating a UPS password is an operations action, not an operator
  feature. The operator's only duty is to notice the referenced Secret changed and re-render both
  sides in an order that does not lock agents out mid-outage — which is the missing watch above, not
  separate work.

#### Deferred / Declined (2026-08-03)

- `OD-20` instant command scope, narrowed and deprioritized (2026-08-03): the operator's actuator
  already owns real shutdown (nodes and workloads). The only remaining use case for NUT instant
  commands is the tail end after the operator has already finished — `shutdown.return` stops the UPS
  discharging into a dead load and auto-restores power when line power returns. Redundant with the
  actuator for anything actually running in the cluster; only matters for non-cluster hardware on
  the same UPS or battery-waste cleanup. Not pursued unless that narrow case becomes a real need.
- SNMPv3 coverage for the `snmpsim` driver-conformance fixture (currently SNMPv2c community-auth
  only, see Built above) — would need `snmpsim`'s USM configuration wired to match a `credentialSecretRef`
  fixture Secret. Not pursued: the conformance question (OID/decode correctness) is orthogonal to the
  auth mode, and production hardware's SNMPv3 path is already exercised for real in
  `docs/audits/nut-usage-audit.md`'s alpha-hardware findings.
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
- `cmd/node-actuator`: syscall-backed poweroff with a non-Linux stub. The mechanism is fixed, not
  configurable — `F-36` closed by deleting the unreachable arbitrary-command path rather than adding
  a CRD field for it.
- `F-13` narrow privilege model: `hostPID` + `CAP_SYS_BOOT` only, no service-account token.
- `F-14` self-exclusion enforced structurally in the executor (`protectedNamespaces`).
- `F-24` confirmed safe, including the `upsmon.conf` Secret hash.
- `F-33` closed: `requireFreshTelemetry` is enforced per agent, failing closed.
- `F-34` closed: non-zero resource defaults for both tier-0 containers.
- `F-35` closed: `upsmon` liveness probe; the actuator's deliberate lack of one is a test invariant.
- Signal handoff proven on a real `kind` cluster within the configured TTL.

#### Open Work


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
- `OD-15` probe history is written, not just schematized. `UPSCapabilityProbe` reconciliation
  compares the matched profile against the device read (`capability.Verify`) and writes one
  `capability_profile_verifications` row per probe. The table previously existed ahead of its
  writer: schema, insert, spool path, and replay decoder were all present with no caller.

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
- `F-38` duplicate reconciler registration fixed; `cmd/main_wiring_test.go` guards it.
- Each workflow's job carries a distinct check name. They were all `Run on Ubuntu`, which made the
  checks indistinguishable to branch protection and impossible to require individually.
- `main` is protected with all five checks required **and `enforce_admins` on**, so nothing lands
  over a red gate — the path that let `F-38` through is closed, not just narrowed. Every change goes
  through a pull request; reviews are not required, so a passing PR can be merged by its author.
  Settings live in `.github/branch-protection.json` and the emergency-bypass procedure is in
  `CONTRIBUTING.md`, so lifting protection is deliberate and reversible in one command.
- The e2e suite restores `config/manager/kustomization.yaml`. `make deploy IMG=...` edits that
  tracked file in place and nothing put it back, so a suite run left the repository holding
  `example.com/nut-operator:v0.0.1` as the published operator image.
- No-cert-manager install path is the recommended one: `config/byo-cert` overlay,
  `dist/install-byo-cert.yaml`, and `hack/webhook-cert.sh` for provisioning and rotation. Chosen
  because a static Secret has nothing to reconcile while the cluster is losing power. Adopting
  `open-policy-agent/cert-controller` was evaluated and declined — reasoning in
  [docs/install.md](install.md). Verified end to end on `kind`: apply with no cert-manager present,
  provision, manager ready, webhook rejects and defaults, rotation with caBundle stable and no
  restart.
- Resolutions with their root causes and verification are recorded per finding in `docs/audits/`.

#### Open Work

- The e2e suite installs via `config/default`, so it only ever exercises the cert-manager path.
  `dist/install-byo-cert.yaml` is now the recommended install and has no CI coverage. Adding a spec
  that applies it, runs `hack/webhook-cert.sh`, waits for readiness, and asserts a webhook rejection
  would cover it — those are the four steps used to verify it by hand, and the bundle needs no
  external dependency in CI.
- Certificate rotation for the no-cert-manager path is a manual `hack/webhook-cert.sh` re-run. The
  1-year serving certificate makes that infrequent, but nothing warns as expiry approaches. Consider
  a metric or a `PowerManagementCluster` condition sourced from the mounted certificate's `NotAfter`.
- Release image signing policy, cosign verification docs, and immutable digest production examples
  (`docs/images.md` describes the target state; keyless Sigstore signing as a release gate isn't
  confirmed wired into CI yet).
- Triaging new unsuppressed medium-or-higher ASH findings is still manual; the scan itself runs on
  every push/PR.
- Decide container-mode vs. locally-installed `grype`/`syft`/`opengrep`/`cfn-nag`/`cdk-nag` for full
  ASH coverage — confirmed still `MISSING` in the scan output, not just undecided in principle.

---

### Telemetry & Triggers

*Sequenced last: every open item here waits on Planning & Execution Logic or Inventory
System landing first, so the section is placed at the end of the tracker deliberately.*

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
