# Project Tasks

This file is the public implementation tracker for `nut-operator`.

Architecture, security, API, and design documents describe the system in its finalized form. Current
build state, open implementation work, and validation gates live here so the architecture docs do
not become a progress diary.

Work is organized by component so it can be picked up independently. Each component section lists
what is built and what remains open, with design-doc identifiers (`OD-n`, `PL-n`, etc.) and audit
findings (`F-n`) cited so the reasoning behind an item is one click away rather than re-litigated.
Items that genuinely span two components are listed under their primary owner with a cross-reference.

Last reviewed: 2026-08-03

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/design/inventory-provider-contract.md` (`IN-n`).

#### Built

- `internal/inventory`: a pure compiler (no I/O, no Kubernetes imports) that validates a `Snapshot`
  of entities and edges, then derives power domains by transitive closure over `feeds` edges
  (`IN-7`/`IN-9`), derives communication-carrier ordering from `carries` edges (`IN-3`/`IN-5`), and
  enforces the orphan rule with an explicit exemption escape hatch (`IN-12`). Deterministic and
  hash-stable; a node reachable from two UPS roots lands in both domains with no special-casing
  (`IN-11`).
- `PowerInventoryNode` and `PowerInventoryEdge` CRDs. `PowerInfrastructure` and
  `UPSDevice.spec.powerDomains` round out the four entity kinds/domain label from `IN-1`/`IN-10`.
  `PowerInventoryEdge` is the only place topology (`feeds`/`carries`) is authored.
- Admission webhooks and controller-level validators for all four inventory CRDs: DNS-subdomain node
  name checks, tier-0 rejection, `feeds`-requires-input/`carries`-forbids-input, self-edge rejection,
  entity-kind and relation enum checks.
- `declarative_inventory_resolver.go`/`declarative_inventory_adapter.go` wire the compiler into
  reconciliation. This is load-bearing, not decorative: `ShutdownFlowReconciler` calls it on every
  reconcile, so an invalid inventory graph (an orphaned node, a malformed edge) genuinely blocks or
  degrades `ShutdownFlow` acceptance today.
- The compiled topology's hash folds into the planner's structural plan-identity hash (`IN-14`).
- Numbered shutdown tiers (`OD-4`) land on `PowerInventoryNode.spec.roles.shutdownTier`, with
  `lastDitchRole` aliased to tier 1.
- Test coverage: compiler determinism/domain-derivation/orphan/communication-path tests, end-to-end
  resolver tests against a fake client, and webhook validation tests per CRD.

#### Open Work

- **Wire the derived topology into the planner.** `Topology.Domains` and `Topology.CommunicationOrders`
  are computed and validated but nothing downstream consumes them — trigger domain matching still
  runs off live telemetry snapshots, not this derived closure, and `carries`-based ordering
  (`PL-21`) has no consumer yet either. This is the single highest-value remaining item in this
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
`docs/design/capability-profiles.md`, `capability-profiles-and-upsd-config.md`,
`device-profile-scope-and-provenance.md`.

#### Built

- `UPSCapabilityProfile` CRD with declared telemetry (NUT variables) and actuation
  (behaviors/quirks) sections, matched via a deterministic precedence chain: exact model+firmware →
  exact model → model glob → driver family → universal floor, with CRD-over-bundled and
  highest-semver tiebreaks within a tier.
- Bundled catalog (`config/catalog/upscapabilityprofiles.yaml`): Ubiquiti UniFi UPS Tower and 2U
  profiles, with recorded quirks and actuation intentionally left undeclared pending firmware
  verification.
- Capability matching feeds the resolver's structural bundle and folds into the plan-identity hash.
- Sizing/configuration boundary is settled and documented: profiles influence `upsd` config
  (driver selection, poll behavior) but never pod sizing, replica count, or scheduling.
- Device-class scope is settled: queried devices (UPS, NUT-managed PDU) get profiles; topological
  devices (switches, panels, non-NUT power devices like UniFi RPS) do not.
- Provenance field design (`ProjectVerified`/`Community`/`UserLocal`) and the upgrade-safety
  guarantee (CRD profiles outrank bundled data, upgrades never touch user CRs) are drafted and in
  the FAQ.

#### Open Work

- **`F-25` telemetry variable aliasing** — no mechanism exists. A device reporting `battery.low`
  instead of the standard `battery.charge.low` is silently invisible to the normalizer; the value
  survives in the raw variable map but no derived field or trigger sees it. Highest-priority item in
  this component — it's a recorded, real quirk with no path to act on it. Implementation lands in
  the resolver (see Telemetry & Triggers), but the schema change (`UPSCapabilityTelemetrySpec` alias
  map) is owned here.
- **`F-26` firmware-gated quirks can't expire.** `Quirks` is a flat `[]string` with no firmware
  constraint, so a device on current firmware inherits every historical quirk of its model family
  permanently. Decide between structured quirk objects and firmware-ranged selectors (`OD-22`).
  Concrete case found (2026-08-04, see `docs/design/capability-profiles.md` Field Verification):
  the bundled Ubiquiti quirk `built-in-nut-server` does not hold against real `UPS 2U`/`UPS Tower`
  hardware on firmware `1.6.1` — TCP 3493 is closed, SNMPv3 is the only working telemetry path.
  Needs a decision, not just a code fix.
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
- `OD-23` alias collision/precedence rules (two device names mapping to one canonical name, or a
  name both aliased and natively present).
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
`telemetry-normalization.md`, `trigger-evaluation.md`, `resiliency-and-partitions.md`.

#### Built

- `internal/nut`: a real NUT protocol client — `LIST VAR` with `BEGIN`/`END LIST VAR` framing and
  `ERR` handling — not a wrapper around `upsc`.
- `internal/telemetry`: pure normalization from raw NUT variable maps to stable policy/audit facts —
  phase classification (`Online`/`OnBattery`/`LowBattery`/`Stale`/`Unavailable`/`Unknown`), parsed
  charge/runtime/load, non-fatal diagnostics for unknown status symbols and bad numeric values.
- `internal/polling` composes the transport and normalizer into a per-target poll with an audit
  adapter for `ups_telemetry_snapshots`.
- `internal/trigger`: pure evaluator. Given trigger definitions, normalized UPS state, and prior hold
  state, it returns eligibility, selected devices, next hold state, and diagnostics — no Kubernetes
  reads, no NUT polling, no wall-clock access.
- `UPSDevice` reconciliation resolves its `NUTServer`, polls the in-cluster Service, updates status,
  and records durable snapshots when the audit store is ready.
- `ShutdownFlow` trigger evaluation is wired end-to-end: `internal/shutdownflow` adapts
  `spec.triggers`/`UPSDevice.status` into the evaluator, persists hold state, records
  `shutdownflow_decisions`, and dispatches eligible episodes to the executor, deduplicated by
  `status.lastExecution.deduplicationKey`.
- `dummy-ups` repeater/relay mode for upstream NUT appliances (`UPSDevice.spec.upstreamNUT`) is
  implemented and upstream-loyal — this corrects the original `F-22` finding, which overstated NUT
  feature non-use.

#### Open Work

- `F-25` alias resolution belongs here at runtime — once Capability Profiles adds the alias map to
  the schema, this component applies it when building the telemetry snapshot, and records that a
  value arrived under a non-standard name in the snapshot's diagnostics.
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

- `internal/planner`: pure graph compilation — `requires`/`before`/`after` edges, cycle detection,
  wave compilation, deterministic plan-hash identity, structured diagnostics (no bare "cycle
  detected"), degradation handling (`Unknown` feasibility on stale/missing telemetry, universal-floor
  warnings that don't fail the compile).
- Numbered shutdown tiers (`OD-4`, closed) fully implemented: central
  `PowerManagementCluster.spec.shutdownTiers` policy, per-group tier inputs, label/selector/default
  tier resolution with total deterministic precedence, derived tier-to-tier edges (`internal/planner/tiers.go`),
  tier-0 rejection for orchestrated groups and nodes, and tier status published on steps, waves, and
  the graph.
- Planner artifact publication (`internal/planner/artifacts.go`): normalized dependency graph,
  edge/source explanations with provenance (`Declared`/`Derived`/`Policy`), advisory startup wave
  projection, deterministic Mermaid/Graphviz/D2 exports (consumer side lives in Outputs &
  Publishing).
- `ShutdownFlow` dry-run execution dispatch from eligible trigger decisions into `internal/executor`,
  with `status.lastExecution`, active-trigger deduplication, durable execution evidence, and
  `ExecutionReady` condition updates.
- `internal/executor`: ordered wave execution evidence, dry-run action attempts, node release
  records, signal-file handoff records, resume-state updates — restart-safe by design (`EX-14`).
- `internal/kubeactions`: enforce-mode `ScaleWorkload`, `CordonNodes`, `DrainNodes`, provider-neutral
  `RunWorkflow` hooks (the `argoproj.io/workflows` RBAC noted in `F-5` is this — a real, used
  integration, not scaffolding), and projected-Secret `AgentShutdown` signal handoff.
- Node-agent coverage gating: enforce-mode releases block when a target node lacks a ready agent pod;
  dry-run records the same degraded facts without acting.
- **`ShutdownFlow`'s continuous status-update conflicts are fixed (`F-31`, 2026-08-04).** The
  resourceVersion race documented below is closed operator-wide by switching every controller's status
  write from `Status().Update()` to `Status().Patch()` — see Operator Maturity & Hardening. What
  `F-31` does *not* address: `SetupWithManager` still watches `UPSDevice` with no predicate, so every
  telemetry tick (5–15s per device) still re-enqueues a `ShutdownFlow` reconcile, each doing a Postgres
  audit-store round trip via `recordShutdownFlowAudit` — real reconcile churn, just no longer
  error-producing churn. Scoping that watch predicate to the fields the trigger logic actually reads
  (phase, charge %, runtime seconds) remains open below.

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
- `OD-12` infeasible-plan policy field default and options (reject/warn/truncate), referenced by
  `EX-3`, not yet decided or implemented.
- `OD-18` tier inversion validation (shared with Inventory System — this is the planner
  tier-compilation half).
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

- `NUTServer` operand rendering: Namespace, ConfigMap, operator-managed Secret, Service,
  NetworkPolicy, Deployment, and upstream NUT relay mode via `dummy-ups`.
- `upsd.conf`/`upsd.users` rendering with injection-validated config values.
- Container security context: non-root UID 65532, read-only root filesystem, all capabilities
  dropped, no privilege escalation, consistent across the render path.
- Project-owned `nut-server` Dockerfile with pinned NUT packages and healthcheck instructions.
- Protocol fidelity confirmed by audit: real `LIST VAR` framing (not `upsc` shelling), standard NUT
  variable names throughout, `MODE=netclient`, every agent connects as `secondary`,
  `SHUTDOWNCMD "/bin/true"` as the stub actuator expressed in NUT-native terms.
- **`F-23` privileged-user separation is done**, ahead of the original audit's expectation:
  `renderUPSDUsers` already renders a separate `[admin]` user (`actions = SET`, `instcmds = ALL`,
  its own `admin-password`) distinct from the `[monitor]` secondary user. Node-agent rendering
  (`nodepoweragent_render.go`) only ever reads `monitor-password` — the admin credential structurally
  never reaches a node agent. This closes the credential-separation half of `F-23`; the design
  question it was gating (which instant commands actually get exposed) is still `OD-20`.
- **`F-15` `spec.replicas` is pinned to 1.** CRD schema (`+kubebuilder:validation:Maximum=1`) and
  the admission webhook (`validateNUTServerAdmission`) both reject any value other than 1 — defense
  in depth per the policy-gate pattern in `docs/security.md`.
- **`F-16` Deployment strategy is explicit `Recreate`.** Set in `ensureNUTServerDeployment`, so an
  upgrade never briefly runs two `upsd` instances and splits NUT's client-login accounting.
- **`F-17` readiness probe now proves live driver connectivity, not just structural registration
  (2026-08-04).** `upsc -l` alone was wrong: verified empirically (real container, real
  `snmp-ups` failure) that it lists every device *defined in `ups.conf`* regardless of whether the
  driver ever actually connected — a fully-disconnected driver still passed it. The probe
  (`upsdReadinessProbeScript`) now lists devices via `upsc -l`, then queries a real variable
  (`ups.status`) per device, passing if any one has a genuinely connected driver — same "at least
  one UPS" intent as originally documented, just actually true now. Does not yet prove any single
  device's telemetry is *fresh* — see Open Work below.
- **Driver-startup/container-lifecycle coupling fixed (2026-08-04).** `images/nut-server/entrypoint.sh`
  ran `upsdrvctl start` under `set -eu` with no failure handling — any single driver failing to
  start (bad credentials, unreachable device) killed the whole container before `upsd` ever ran, so
  a broken UPS took down telemetry for every *other* device on that server too, and made it
  impossible to reach the server at all to fix credentials. Now `upsdrvctl start` failures are
  logged and non-fatal; `upsd` always starts, and the (now-correct) `F-17` readiness probe reports
  the real per-driver state instead of the whole pod crash-looping.
- **`F-18` priority class and PodDisruptionBudget are in place.** The webhook defaults
  `spec.placement.priorityClassName` to `system-cluster-critical` when unset (mirrors the
  `system-node-critical` pattern already used for `NodePowerAgent`, at the cluster-singleton tier
  instead of the per-node tier). `ensureNUTServerPodDisruptionBudget` renders a PDB with
  `minAvailable: 1`, which — paired with the `F-15` replica pin — blocks voluntary eviction of the
  sole `upsd` pod entirely.
- **`F-21` `upssched` non-use is a recorded decision**, not an omission — see the resolution note in
  `docs/audits/nut-usage-audit.md`. Follows from `SB-2b` and `GP-4`: `upssched` is a per-node
  sequencer, and sequencing is reserved for the operator's deterministic planner.
- **`F-24` confirmed no credential leak path.** The `upsd.users` Secret has no `Hash` set in
  `ManagedResourceStatus` (only the ConfigMap does — `configHash` is computed from `configData`
  alone, never from Secret contents), no log statement in the render or controller path touches
  `secret.Data` or a password value, and `NUTServerReconciler` has no Event Recorder wired in at all,
  so there is no Events leak path to check.

- **`UPSDevice.spec.credentialSecretRef` is wired (2026-08-04).** Found unwired while pointing a
  real `snmp-ups` device at production hardware requiring SNMPv3 (`secName`/`authPassword`/
  `privPassword`, not just a community string) — fixed same day.
  `resolveUPSDeviceCredentials` (`nutserver_render.go`) fetches the referenced Secret (same
  operand-namespace only, matching the existing `upstreamNUTAuthProjections` convention) and merges
  its keys into the device's driver options, winning over any `driverOptions` collision. `ups.conf`
  itself moved out of the plain `ConfigMap` into a new dedicated Secret
  (`<name>-nut-driver-config`) projected into the same `/etc/nut` volume — the container mount path
  is unchanged, no image changes needed. The Deployment's rollout-trigger hash still covers the full
  rendered config (including credentials) so a rotation still rolls the pod; that hash is a one-way
  SHA-256 digest, doesn't leak the plaintext. Matches the `F-24` precedent of not putting a content
  hash on credential-bearing `Secret`s in `ManagedResourceStatus`.

#### Open Work

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
- **Scripted UPS-state-transition simulation (`dummy-ups` `dummy-loop`/`.seq`/`TIMER` mode) is not
  modeled.** Confirmed 2026-08-04 (zero matches for `dummy-loop`/`.seq`/`TIMER` anywhere in the repo).
  Only static single-state simulation exists today (`renderDummyUPSDefinition`'s `.dev` file, fixed
  `OL`/100%/3600s/10% — real, and already used in tests), which can't drive an actual
  `OnBattery`→`LowBattery` transition to exercise `ShutdownFlow` trigger eligibility end-to-end. NUT's
  driver supports this natively via `mode = dummy-loop` plus a `.seq` file with `TIMER` directives; the
  operator has no mechanism to deliver arbitrary simulation-fixture file content into the container,
  only the one hardcoded `.dev` case. Two paths, in order: (1) prototype via the existing
  `driverOptions` passthrough — `dummyUPSDefinitionFileName` already backs off its own `.dev`
  rendering whenever `driverOptions.port` is set explicitly, so `port`/`mode = dummy-loop` render into
  `ups.conf` correctly today; the missing piece is getting the `.seq` file itself onto the filesystem
  (needs a hand-authored ConfigMap + volume patch outside the CRD today, not a first-class field).
  (2) If that proves useful, promote to a real `simulation` field on `UPSDeviceSpec` (ConfigMap ref +
  mode) — fixture data should be declarative and Git-reviewable, not a manual patch.
- **`snmpsim` as a driver-conformance testing track, separate from `dummy-ups`.** Confirmed zero
  coverage today: every `snmp-ups` reference in the repo is a config-string literal in tests ("does
  the operator write `driver = snmp-ups` correctly"); nothing runs the actual `snmp-ups` NUT driver
  against anything, real or simulated. Production/alpha hardware runs `snmp-ups` over SNMPv3, and this
  session's entrypoint-coupling and readiness-probe bugs were both in that exact path — a real,
  currently-unguarded regression surface. No operator/CRD change needed: `UPSDevice.spec.endpoint` +
  `credentialSecretRef` already point `snmp-ups` at any host:port with SNMPv3 material, so a
  `snmpsim-command-responder` Deployment serving `.snmprec` data (recorded from real hardware or
  synthesized from the UPS MIB) is a drop-in target. Division of labor: `snmpsim` proves the driver
  talks to a vendor's OIDs correctly; `dummy-ups` (once scripted transitions exist, above) proves the
  operator reacts correctly once a device reports it's dying. `snmpsim` serves a static tree by
  default (variation modules needed for dynamic values) — the wrong tool for transition testing, don't
  conflate the two.
- **Define the e2e target state for `dummy-ups`/`NUTServer`-backed coverage.** `test-e2e.yml`
  currently runs `make test-e2e` against `kind` with no NUT simulator involved at all. Worth asserting
  in that suite: CRDs install, all operand kinds render (Deployment/Service/ConfigMap/NetworkPolicy for
  `NUTServer`, DaemonSet for `NodePowerAgent`), the DaemonSet lands on every `kind` node and its
  `upsmon` container actually reaches a `NUTServer` pod backed by a `dummy-ups` fixture, and a sample
  `ShutdownFlow` compiles to the expected waves in `/status`. Achievable today with the existing static
  `.dev` fixture — no dependency on the scripted-transition work above. Real actuation testing
  (`SystemdPoweroff`) is structurally out of scope for `kind` regardless: `kind` nodes are containers
  and can't be powered off, and `NodePowerAgent` defaults to `DryRun`/`Stub` with `SystemdPoweroff`
  additionally gated behind `Actuate` plus an approval annotation — that testing belongs on disposable
  VMs outside GitHub Actions.

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
`internal/nodeagent`. Audit: `docs/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`).

#### Built

- `NodePowerAgent` DaemonSet rendering: Namespace, ServiceAccount, ConfigMap, Secret-backed
  `upsmon.conf`, egress NetworkPolicy, `MonitorOnly`/`DryRun`/`Actuate` modes.
- Explicit rollout strategy, `system-node-critical` priority default, baseline toleration set, and
  readiness probes — closes `F-8`, `F-9`, `F-10`, `F-12` from the DaemonSet audit.
- `power-signal-writer` (`cmd/power-signal-writer`): project-owned `SHUTDOWNCMD` binary, writing to a
  projected signal Secret; `upsmon` gets a writable `/run` mount while the root filesystem stays
  read-only.
- `internal/nodeagent` (`signal.go`): node actuator signal handling — structured shutdown JSON
  validation, signal TTL and node-name matching enforcement, dry-run `SystemdPoweroff` skip, watches
  both the local `upsmon` handoff file and the executor-projected Secret path.
- `cmd/node-actuator`: syscall-backed host poweroff (`poweroff_linux.go`, with
  `poweroff_unsupported.go` for other GOOS) with command override support.
- Approved `SystemdPoweroff` isolation uses `hostPID` + `CAP_SYS_BOOT` only — the narrow-privilege
  model `F-13` called for, not blanket `privileged: true`. Non-root, all other capabilities dropped,
  read-only root filesystem, no Kubernetes service-account token.
- Partition-aware coverage status: per-node agent-pod readiness feeds `AgentShutdown` executor
  release evidence; enforce-mode blocks releases against nodes without a ready agent, dry-run records
  the same degraded facts without acting.
- **`F-14` self-exclusion is enforced in the executor.** `internal/kubeactions.Runner` resolves every
  `NodePowerAgent`'s operand namespace (`protectedNamespaces`) and skips it structurally: `ScaleWorkload`
  targets in a protected namespace are excluded rather than scaled (`selfExcluded` in the action
  outcome), and `DrainNodes` eviction skips pods in a protected namespace regardless of owner kind
  (`selfExcludedNamespaces`) — belt-and-suspenders on top of the pre-existing DaemonSet-ownership
  skip in `evictablePod`, which only covered eviction and only for DaemonSet-owned pods. Not just a
  PDB question: DaemonSet pods were already undrainable; the gap was that nothing was *structurally*
  protected against a future non-DaemonSet resource landing in that namespace, or against a
  `ScaleWorkload` group whose selector happened to sweep it in.
- **`F-24` confirmed no credential leak path, with one caveat.** No log statement in the render or
  controller path touches `secret.Data` or the monitor password, and there's no Event Recorder wired
  into `NodePowerAgentReconciler`. Unlike the NUT Server side, the `upsmon.conf` Secret's
  `ManagedResourceStatus` entry *does* carry a `Hash` (`hashByteMap(secretData)`) — this is a
  one-way SHA-256 hash of a 32-byte random value (`randomPassword()`), used for the standard
  config-hash-triggers-rollout pattern, not a partial leak: recovering the password from the hash is
  computationally infeasible. Confirmed safe, not just assumed.

#### Open Work

- In-cluster smoke test proving projected Secret signal updates reach DaemonSet pods within the
  configured TTL on common runtimes. Blocked on tooling: `kind` isn't installed in this environment
  (docker is); this needs a real kubelet, not envtest, so it can't be faked with the existing suite.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/design/published-planner-artifacts.md` (`GP-6`/`GP-7`).

#### Built

- Single structured planner artifact (`PL-45`–`PL-48`): compiled plan, dependency graph, waves,
  advisory startup projection, diagnostics, feasibility verdicts, plan hash, duration estimates.
- Dependency graph emitted as normalized vertices/edges, not text — every edge carries relation type,
  source object references, provenance (`Declared`/`Derived`/`Policy`), and a stable explanation
  string, so "why was this node in wave four" is answerable from structure, not log archaeology.
- Deterministic Mermaid, Graphviz/DOT, and D2 diagram exports generated from the structured graph —
  renderers, never independent sources of truth.
- Kubernetes-first interface fully in place: CRDs + `/status` + Events + logs + PostgreSQL audit
  records as the whole v1 interface, no dedicated UI. The operator publishes facts; subscribers
  (dashboards, docs generators, monitoring, recovery orchestration) own what they do with them
  (`GP-7`).
- Advisory startup wave projections published for recovery-system subscribers without the operator
  executing recovery itself (`OD-1`/`OD-5`, closed).

#### Open Work

- Once the planner consumes inventory-derived domains and communication ordering (see Planning &
  Execution Logic), the published graph/domain artifacts should reflect that richer structure — right
  now the published graph is only as complete as the planner's current inputs.
- **`F-3` metrics.** No Prometheus registrations exist at all — this is the entire "publish facts as
  metrics" gap. Highest-value candidates per the maturity audit: compile duration, compile failures
  by diagnostic class, plan hash changes, trigger evaluations, wave execution duration, actuation
  attempts, degraded-dependency conditions. The `ServiceMonitor` scaffolding already exists
  (`SB-10`); there's currently nothing project-specific for it to scrape.
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

- Storage backend resolution (`internal/storage`) for `Disabled`/`ExternalPostgres`/`CNPG` modes,
  connection-management concerns kept separate from domain validation and controller status.
- Full PostgreSQL audit schema (`internal/audit`): power events, telemetry snapshots, capability
  profile matches and verification/probe history, accepted/rejected planner compilations, shutdown
  decisions, executor runs, wave/group progress, action attempts, node release records, signal-handoff
  evidence, executor resume state. Executor child tables cascade-delete from their parent execution.
- Planner compilation audit records persist compiled waves, dependency graph, advisory startup waves,
  explanations, and diagram exports as structured JSONB payloads.
- Retention enforcement (`spec.storage.retention`, two families: `events` and `telemetry`) evaluated
  by `PowerManagementCluster` storage readiness; negative retention values rejected before a store
  opens.
- **`OD-6` closed:** shutdown-time audit spool (`internal/audit/spool.go`). When enabled, a
  PostgreSQL write failure during execution falls back to a durable local JSONL journal
  (`audit-spool.jsonl`) keyed by a stable replay key (`executionID`/`executionID/waveIndex`), sets
  `AuditSpoolFallback` on `Degraded`/`ExecutionReady`, and never creates a second execution identity.
  Requires CNPG or `ExternalPostgres` plus an explicit durable volume — not a database replacement.

#### Open Work

- **Spool replay tooling.** The spool writes records; nothing reads them back into PostgreSQL once
  connectivity returns. This is explicitly called out in `resiliency-and-partitions.md` as a needed
  implementation hook and is the clearest actionable gap in this component.
- Controller/envtest coverage for PostgreSQL degradation (writes failing mid-reconcile), beyond the
  spool's own unit tests.
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

- `observedGeneration` tracking across all nine CRDs and every controller; enum validation on
  constrained fields; single storage version per CRD; spec/status separation.
- Source hardening pass for ASH/Checkov findings: explicit helper admin RBAC verbs, non-default
  leader-election RBAC namespaces, documented service-account token/digest exceptions, Kustomize
  image placeholder repair, Dockerfile healthchecks.
- **Manager `imagePullPolicy` changed `Always` → `IfNotPresent` (2026-08-04), reversing the prior
  hardening pass's choice.** Root-caused the `E2E Tests` workflow failing on every run since before
  this session: the suite builds and `kind load`s the manager image locally (no registry involved),
  but `Always` still forced a pull attempt against the placeholder `example.com` tag, which doesn't
  exist — guaranteed `ImagePullBackOff`. `Always` vs `IfNotPresent` makes no real freshness/safety
  difference in this project specifically because every real deployment overrides the base image
  with an explicit digest via Kustomize (`CKV_K8S_43`, already suppressed on this same file) — a
  pinned digest is content-verified regardless of pull policy. Added the matching `CKV_K8S_15`
  suppression.
- Local AWS Labs ASH security scan configuration and `make security-scan` target.
- Project-owned OCI images for all four operands (`nut-server`, `upsmon-agent`, `node-actuator`,
  `operator`), multi-arch, with SBOM/provenance attestation and vulnerability scanning via the
  `Images` GitHub Actions workflow.
- **`F-28` fixed: manager no longer crash-loops forever when the CNPG CRD isn't installed
  in-cluster.** Root-caused via a real E2E failure log (`test/e2e/e2e_test.go:304`, "metrics
  endpoint" spec) after the `imagePullPolicy` fix above eliminated the prior `ImagePullBackOff` and
  let the suite run far enough to hit this. `powermanagementcluster_controller.go`'s
  `SetupWithManager` unconditionally registered `Watches(newCNPGClusterObject(), ...)` against the
  unstructured `Cluster.postgresql.cnpg.io` GVK; when that CRD isn't installed, the underlying
  `source.Kind` REST-mapping lookup retries forever inside its own poll loop, and
  controller-runtime's default 2-minute `CacheSyncTimeout`
  (`sigs.k8s.io/controller-runtime@v0.24.1/pkg/controller/controller.go:249`) then treats the
  never-syncing watch as a fatal controller-start error — confirmed by reading
  `pkg/internal/controller/controller.go` — which kills the whole manager process (`mgr.Start()`
  returns the error, `cmd/main.go` logs "problem running manager" and calls `os.Exit(1)`). The
  captured pod description matched exactly: container instances ran for ~2m18s before
  `Exit Code: 1`, restarting into the identical failure via `CrashLoopBackOff`. (An earlier
  hypothesis blamed a cert-manager/webhook-Secret startup race for this — ruled out by the same log:
  the `webhook-server-cert` Secret was issued within ~1s of pod scheduling, long before either
  crash.) Since CNPG is one of several optional storage backends
  (`Disabled`/`ExternalPostgres`/`CNPG`, see Storage & Audit above), this meant *any* install of this
  operator into a cluster without CNPG's CRDs present — regardless of whether CNPG storage was
  actually configured — could never start. Fixed by adding `cnpgClusterCRDPresent`, a
  `meta.RESTMapper.RESTMapping` discovery check run once in `SetupWithManager`: the CNPG `Watches()`
  is now only registered when the CRD actually resolves; otherwise a one-line log explains the watch
  was skipped. `PowerManagementCluster` resources using CNPG storage still reconcile via their
  existing periodic 5-minute requeue and per-reconcile `Get`-based degradation (both already
  resilient to a missing CRD); the tradeoff is that live watch-triggered reconciliation on CNPG
  status changes only resumes after the manager restarts post-CRD-install, which is an acceptable
  degradation given the alternative was total unavailability. Covered by
  `TestCnpgClusterCRDPresentReflectsRESTMapperContents`.
- **`F-29` fixed: E2E "metrics endpoint" spec timed out because the test's own namespace never
  satisfied the metrics NetworkPolicy it deploys.** Surfaced immediately after the `F-28` fix above
  eliminated the crash-loop and let the same spec run far enough to hit a different, unrelated
  failure: the `curl-metrics` pod's connection to the metrics Service timed out
  (`curl: (28) ... Operation timed out`, not refused) even though the manager's own log confirmed
  `Serving metrics server {bindAddress: :8443, secure: true}` and the Service selector/port matched
  the pod exactly — ruling out a startup or config-mismatch cause. The remaining explanation:
  `config/network-policy/allow-metrics-traffic.yaml` (enabled via `config/default/kustomization.yaml`
  as part of an earlier hardening pass) only admits ingress to the metrics port from namespaces
  labeled `metrics: enabled`; `test/e2e/e2e_test.go`'s `BeforeAll` labeled the test namespace
  `pod-security.kubernetes.io/enforce=restricted` but never `metrics: enabled`, so the curl-metrics
  pod's own namespace never matched the policy's `namespaceSelector` and its traffic was dropped.
  Fixed by adding the missing `kubectl label ns ... metrics=enabled` step alongside the existing
  pod-security label. `allow-webhook-traffic.yaml` was checked and needs no equivalent fix — it
  restricts by port only, not source namespace, by design (kube-apiserver's source identity is
  CNI-specific).
- **CI pipeline optimized and ASH wired in (2026-08-04).** Verified before this pass: `make
  security-scan` (ASH) was local-only, invoked nowhere under `.github/workflows/`; the only automated
  scanning in CI was Trivy on published images in `Images`, which ran *after* push (so a CRITICAL/HIGH
  finding failed the job without un-publishing the already-pushed image) and only on non-PR events (PRs
  got zero vulnerability signal at all). Across `lint.yml`/`test.yml`/`test-e2e.yml`/`images.yml`, also
  confirmed: no `concurrency` cancellation (demonstrated wasteful this session — 5 rapid-fire pushes
  each ran full CI to completion independently), no `paths-ignore` (a docs-only commit triggered the
  full multi-arch `Images` build+push+scan pipeline, also demonstrated this session), no caching for
  envtest binaries or the custom-plugin `golangci-lint` build, no `timeout-minutes` on any job, and
  `go mod tidy` ran silently before tests with any drift discarded rather than failing the build. Fixed
  all of it: added `concurrency`/`paths-ignore`/`timeout-minutes` to all workflows; added a single
  `actions/cache` on `bin/` (keyed on `Makefile`+`.custom-gcl.yml`) covering the custom-plugin
  `golangci-lint` binary, envtest assets, `controller-gen`, and `kustomize` at once, since all of the
  Makefile's `go-install-tool` targets share that directory; replaced the silent `go mod tidy` with a
  `tidy` + `git diff --exit-code` drift check; split `images.yml`'s build step so PR events get their
  own single-platform (native arch), `load: true` build scanned *before* anything is ever pushed —
  closing the PR-coverage gap and dropping the previously-wasted arm64/QEMU build on PRs that produced
  nothing scannable anyway (a multi-platform manifest list can't be `docker load`ed); non-PR
  build-then-push-then-scan ordering is unchanged and the residual risk window is documented in the
  workflow, not silently accepted — genuinely closing it would mean scanning per-platform images before
  assembling the manifest, meaningfully more machinery for a path only ever exercised by this repo's
  own maintainer commits, not external changes. New `security.yml` installs `uv` (cached, keyed on the
  pinned ASH version), runs `make security-scan` on every push/PR, and uploads the full report
  (markdown/html/sarif/flat-json) as a build artifact. Confirmed locally: zero new `actionlint`
  findings from any of the additions, ASH itself completes in ~12s so the 15-minute job timeout is
  headroom, not a tight fit.
- **`F-30` fixed: the controller-manager now protects itself from its own orchestrated shutdown
  actions (2026-08-04).** `internal/kubeactions.Runner.protectedNamespaces` (the mechanism behind
  `F-14`) previously resolved only `NodePowerAgent` operand namespaces, never the manager's own —
  meaning a `ShutdownFlow` group whose selector happened to match the manager's own node or namespace
  could evict (`DrainNodes`), scale to zero (`ScaleWorkload`), or preempt/disrupt it
  (`config/manager/manager.yaml` had no `priorityClassName` or `PodDisruptionBudget`, unlike every
  operand the manager itself renders). Fixed on both layers: `manager.yaml` now sets
  `priorityClassName: system-cluster-critical` (matching `F-18`'s pattern for `NUTServer`) and
  `config/manager/manager_pdb.yaml` adds a `minAvailable: 1` PDB, which — paired with `replicas: 1` —
  blocks voluntary eviction the same way `F-18` already does for the NUT server pod. A new
  `POD_NAMESPACE` downward-API env var lets `Runner.ManagerNamespace` (new field) resolve the
  manager's own install namespace at runtime, wired through `cmd/main.go` and folded into
  `protectedNamespaces` alongside the existing `NodePowerAgent` namespaces — so `DrainNodes` and
  `ScaleWorkload` reject the manager's own namespace through the exact same code path `F-14` already
  proved correct, not a parallel special case. `CordonNodes` was deliberately left alone: cordoning
  only blocks new scheduling, it doesn't evict a pod already running, so it isn't a liveness threat to
  the manager the way eviction/scale-to-zero are. Covered by
  `TestRunnerScaleWorkloadsExcludesManagerNamespace` and `TestRunnerDrainNodesExcludesManagerNamespace`.
- **`F-1` fixed: `NUTServerReconciler` and `NodePowerAgentReconciler` now carry finalizers
  (2026-08-04).** Verified first, carefully, before implementing: owner-reference garbage collection
  already correctly deletes the rendered child resources (Deployment/DaemonSet, ConfigMap, Secrets,
  Service, NetworkPolicy, PDB) for a cluster-scoped owner with namespaced dependents — that part of
  the original finding's framing didn't hold up under direct verification, and building a finalizer to
  "fix" already-working GC would have been finalizers for their own sake. What deletion genuinely never
  had: any observable record it happened at all. This operator's whole interface model is status,
  Kubernetes Events, and PostgreSQL audit records (`GP-7`), and a deleted `NUTServer`/`NodePowerAgent`
  previously left none of the three. `power.zalud.io/nutserver-cleanup` and
  `power.zalud.io/nodepoweragent-cleanup` finalizers now make deletion an explicit, blocking step: on
  delete, each reconciler emits a Kubernetes `OperandTeardown` Event (new `Recorder record.EventRecorder`
  field on both reconcilers, wired via `mgr.GetEventRecorderFor(...)` in `cmd/main.go`; RBAC for
  `events create;patch` added) before removing the finalizer. RBAC for the finalizer itself needed no
  manifest change — kubebuilder had already scaffolded `*/finalizers` `update` on all 9 CRDs from the
  start. Covered by new envtest specs asserting finalizer presence after create and actual object
  deletion after a second reconcile pass (`should finalize and actually delete on deletion`, both
  controllers) — existing tests that delete-and-clean-up in `AfterEach` were updated to reconcile once
  more after `Delete` so the finalizer's own removal is exercised, not just added.
- **`F-4` fixed: operand-namespace fields now reject reserved Kubernetes namespaces
  (2026-08-04).** The `namespaces` `create`/`update`/`patch` RBAC verb genuinely can't be narrowed by
  name — Kubernetes RBAC only supports `resourceNames` on verbs acting on an object that already
  exists, not `create` — so the original "narrow to what's actually needed" framing wasn't achievable
  at the RBAC layer. Investigating *why* it mattered surfaced a real, more concrete gap it was gesturing
  at: `NUTServerSpec.Namespace`/`NodePowerAgentSpec.Namespace`/`PowerManagementCluster.spec
  .operandNamespace.name` are user-settable on cluster-scoped CRDs with zero validation beyond DNS-label
  syntax — nothing stopped a `NUTServer`/`NodePowerAgent` CR from pointing its operand namespace at
  `kube-system` and having the operator `CreateOrUpdate`-relabel it, or later render Deployments/
  Secrets/ConfigMaps into it. Fixed by rejecting `default`/`kube-system`/`kube-public`/
  `kube-node-lease` at both layers: `validateOptionalNamespace` (webhook, the primary defense — rejects
  the request at admission time, covers all three fields via one shared helper) and
  `rejectReservedOperandNamespace` (`internal/controller`, belt-and-suspenders for objects that predate
  the webhook or reach the controller with it disabled). The two are intentionally duplicated rather
  than shared across packages — same accepted pattern as `isSupportedInventoryEntityKind`. `serviceaccounts`
  RBAC breadth (the other half of the original finding) was already confirmed defused in the same
  2026-08-04 audit pass: no `AutomountServiceAccountToken`, no `rolebindings`/`clusterrolebindings`
  RBAC at all.
- **Real private-IP leak found and fixed in the process of building the new CI check below
  (2026-08-04).** `internal/controller/nutserver_render_test.go` had a literal private IPv4 address as
  a test fixture `Endpoint.Host` value — the real IP of a device from this session's private-repo alpha
  deployment work, committed to this *public* repo in `70bb81f`. Self-introduced, caught while
  designing the automated check below (a manual `grep` against the current tree, run before writing
  the CI job, to see what it would actually need to handle), not by any existing tooling — confirms the
  gap the new check closes was real, not hypothetical. Fixed by replacing it with the project's own
  established convention (`*.example.net`, already used throughout `config/samples/` and
  `docs/examples/`); the test doesn't assert on the host value so behavior is unaffected. Still visible
  in git history at `70bb81f` — scrubbing that would mean a history rewrite and force-push, out of
  scope unless explicitly requested. New `security.yml` job `private-ip-scan` greps all tracked files
  for RFC1918 IPv4 literals (`.devcontainer/` excluded — its one RFC1918 usage is a generic Docker
  network-config value, not site-specific infrastructure) and fails the build on any match; confirmed
  clean against the current tree, including this fix. No RFC1918 secret/pattern is embedded in the
  check itself — deliberately generic, so the check's own config can't become a second leak of exactly
  what it's guarding against.
- **`F-31` fixed: all 9 controllers now write status via `Status().Patch()`, not `Status().Update()`
  (2026-08-04).** Confirmed via grep before fixing: all 9 reconcilers called `Status().Update()`
  exactly once each; zero used `Status().Patch()`. Reproduced the exact production failure mode (10h
  log, 744 `ShutdownFlow` conflicts) as a real regression test rather than assuming the mechanism:
  `resourceVersionRaceInjectingClient` in `shutdownflow_controller_test.go` lets the reconciler's own
  `Get` return normally, then — on a separate fetch that never touches the object the reconciler holds
  — advances that same object's `resourceVersion` on the (real, envtest) API server before the
  reconciler reaches its status write, simulating a concurrent write landing between a cache-backed
  read and the eventual write. Verified both directions: temporarily reverted to
  `Status().Update()` and confirmed the test fails with the exact same `409 Conflict` / "the object has
  been modified" error from the production log; restored `Status().Patch(ctx, obj,
  client.MergeFrom(base))` (with `base` captured via `DeepCopy()` immediately after the initial `Get`,
  before any mutation) and confirmed it passes. A merge patch has no `resourceVersion` precondition, so
  it applies cleanly against whatever the live object actually is instead of racing a stale read. Fixed
  identically across all 9 controllers, including `NUTServer`/`NodePowerAgent` where the finalizer-add
  `r.Update()` (a separate, unrelated metadata write) happens between the base capture and the status
  write — harmless, since the status subresource endpoint only ever persists the `.status` diff
  regardless of what else changed in the patch body.
- **`F-2` fixed: leader-election code default flipped `false` → `true` (2026-08-04).** Every real
  deployment was already effectively running with leader election active (`config/manager/manager.yaml`
  passes bare `--leader-elect`, which Go's `flag` package treats as `true`) — this closes the
  defense-in-depth gap where a future manifest change dropping that arg would have silently regressed
  to no leader election. Verified the flip doesn't break local iteration: controller-runtime's leader
  election requires an in-cluster-detected namespace when enabled, which a `go run` process against a
  kubeconfig doesn't have (`unable to find leader election namespace: not running in-cluster` —
  confirmed by reading `sigs.k8s.io/controller-runtime/pkg/leaderelection`, not guessed). Fixed by
  adding `--leader-elect=false` to the `Makefile`'s `run` target, so `make run` keeps working
  out-of-cluster exactly as before; the in-cluster path (`config/manager/manager.yaml`, and thus every
  real and `test-e2e.yml` deployment) is unaffected since it already passed the flag explicitly.
- **`F-5` re-scoped and closed: documented the `argoproj.io/workflows` RBAC/`RunWorkflow` integration
  (2026-08-04).** Added an "RBAC Scope" section to `docs/security.md` covering the two ClusterRole
  grants broader than they look in isolation: `argoproj.io/workflows` (backs the real, used
  `RunWorkflow` executor action in `internal/kubeactions` — creates an Argo `Workflow` referencing an
  existing `WorkflowTemplate` by name, never inline spec; no `workflowtemplates` RBAC at all) and
  `namespaces` `create`/`update`/`patch` (can't be narrowed by name at the RBAC layer since `create`
  doesn't support `resourceNames`; the gap is closed at the input-validation layer by `F-4` instead,
  referenced directly from the new section).

#### Open Work

- **`F-32` `NodePowerAgent`'s Pod watch has no predicate and no cache scoping.** `Watches(&corev1.Pod{}, ...)`
  filters by label inside the map function, but the watch itself and the manager's cache
  (`cmd/main.go` configures no `cache.Options.ByObject`) both cover every Pod cluster-wide. Not
  incorrect — the label filter prevents wrong reconciles — but real memory/CPU overhead at cluster
  scale for watching pods this controller never acts on. Fix: scope via `cache.Options.ByObject` or an
  equivalent label selector on the watch.
- `F-7` idempotency test: reconcile from a partial-failure state and assert convergence — no such
  test exists across the four operand render paths.
- Release image signing policy, cosign verification docs, and immutable digest production examples
  (`docs/images.md` describes the target state; keyless Sigstore signing as a release gate isn't
  confirmed wired into CI yet).
- **ASH now runs automatically on every push/PR** (`security.yml`, 2026-08-04) — "re-run ASH after
  each hardening pass" is no longer a manual step to remember. What remains manual: triaging any new
  unsuppressed medium-or-higher finding it surfaces.
- Decide container-mode vs. locally-installed `grype`/`syft`/`opengrep`/`cfn-nag`/`cdk-nag` for full
  ASH coverage — confirmed still `MISSING` in the scan output, not just undecided in principle.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

#### Built

- Kubebuilder/controller-runtime scaffold, Apache-2.0 licensing, public project metadata.
- Resiliency contract documented for API/PostgreSQL/NUT/telemetry/node-agent partitions: lost
  connectivity degrades certainty, never grants optimistic action (`docs/design/resiliency-and-partitions.md`).
- Public-safe sample manifests and the Orion example topology.
- 2026-08-03 documentation migration: audit records, adaptive-execution design, capability/
  device-profile docs, decision-index update, `OD-4` tier closure applied to `scope-boundaries.md`/
  `planner-requirements.md`.

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
