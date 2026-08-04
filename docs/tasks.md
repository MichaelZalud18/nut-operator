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
- **`ShutdownFlow` reconciler hits continuous status-update conflicts.** Confirmed via a 10h log pull
  against the alpha deployment (2026-08-04): 1,516 `"the object has been modified"` errors spread
  evenly across the whole window (~1/48s), not just a post-deploy burst — 744 against `ShutdownFlow`
  specifically. Root cause: `SetupWithManager` watches `UPSDevice` with no predicate, so every
  telemetry tick (5–15s per device) re-enqueues a reconcile; `Reconcile` does a single `r.Get` +
  `r.Status().Update` (not `Patch`), and back-to-back reconciles race the informer cache, so the
  final write frequently uses a stale `resourceVersion`. Self-heals via controller-runtime's built-in
  requeue-on-error, but it's constant reconcile churn — each attempt also does a Postgres audit-store
  round trip via `recordShutdownFlowAudit`. Fix candidates: switch the status write to `Status().Patch`
  (sidesteps the resourceVersion race entirely), and/or scope the `UPSDevice` watch predicate to the
  fields the trigger logic actually reads (phase, charge %, runtime seconds).

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
- **`F-17` readiness probe reflects driver registration**, not just TCP listen: an exec probe runs
  `upsc -l localhost:<port>` against the local `upsd` socket. This proves the driver registered at
  least one UPS with `upsd`; it does not yet prove any single device's telemetry is fresh — see Open
  Work below.
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

- **`UPSDevice.spec.credentialSecretRef` is wired (2026-08-04).** Found unwired while pointing real
  `snmp-ups` `UPSDevice` resources at the homelab's UniFi UPS fleet (needed SNMPv3 `secName`/
  `authPassword`/`privPassword`, not just a community string) — fixed same day.
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

- `OD-20` instant command scope, narrowed and deprioritized (2026-08-03): the operator's actuator
  already owns real shutdown (nodes and workloads). The only remaining use case for NUT instant
  commands is the tail end after the operator has already finished — `shutdown.return` stops the UPS
  discharging into a dead load and auto-restores power when line power returns. Redundant with the
  actuator for anything actually running in the cluster; only matters for non-cluster hardware on
  the same UPS or battery-waste cleanup. Not pursued unless that narrow case becomes a real need.
- Credential rotation and advanced driver-specific config for the NUT operand render path.

#### Deferred / Declined (2026-08-03)

- `OD-19` FSD usage — deferred. Staying on the executor's own signal file; no plan to also wire up
  NUT's native forced-shutdown broadcast.
- `F-17` follow-on (per-device telemetry-freshness readiness) — declined. The built `upsc -l` check
  (structural: did the driver register) is the right stopping point. Tying pod readiness to live
  telemetry values would drop every connected node agent into DEADTIME on a single flaky poll —
  worse than the gap it would close.
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
  leader-election RBAC namespaces, manager pull policy, documented service-account token/digest
  exceptions, Kustomize image placeholder repair, Dockerfile healthchecks.
- Local AWS Labs ASH security scan configuration and `make security-scan` target.
- Project-owned OCI images for all four operands (`nut-server`, `upsmon-agent`, `node-actuator`,
  `operator`), multi-arch, with SBOM/provenance attestation and vulnerability scanning via the
  `Images` GitHub Actions workflow.

#### Open Work

- **`F-2` leader election defaults to `false`.** Confirmed still the case
  (`cmd/main.go`: `flag.BoolVar(&enableLeaderElection, "leader-elect", false, ...)`). One-line fix,
  worst failure mode in the whole audit — two operator instances compiling/executing competing
  shutdown flows. Highest priority item in this section.
- **`F-1` zero finalizers on any controller.** Confirmed — no `Finalizer` references outside tests.
  Operand teardown relies entirely on owner references, which don't cover cross-namespace operands or
  external side effects.
- **`F-4` RBAC breadth.** Confirmed present: `nodepoweragent_controller.go` and
  `nutserver_controller.go` both hold `namespaces` `create`, which is hard to justify for a shutdown
  operator; narrow to what's actually needed or document why cluster-wide namespace creation is
  required.
- `F-5` **re-scoped after checking source**: the `argoproj.io/workflows` RBAC is not leftover
  scaffolding — it's the real `RunWorkflow` hook mechanism in `internal/kubeactions`, already listed
  as built under Planning & Execution Logic. Remaining work here is narrower than the original
  finding suggested: document this integration in `docs/security.md`'s network/RBAC sections so it
  isn't mistaken for scope creep again.
- `F-7` idempotency test: reconcile from a partial-failure state and assert convergence — no such
  test exists across the four operand render paths.
- Release image signing policy, cosign verification docs, and immutable digest production examples
  (`docs/images.md` describes the target state; keyless Sigstore signing as a release gate isn't
  confirmed wired into CI yet).
- Re-run ASH after each hardening pass; triage every unsuppressed medium-or-higher finding.
- Decide container-mode vs. locally-installed `grype`/`syft`/`opengrep`/`cfn-nag`/`cdk-nag` for full
  ASH coverage.

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
