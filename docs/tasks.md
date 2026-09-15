# Project Tasks

This is the public v1 engineering and component tracker for `nut-operator`.

Open work is grouped by owning component. Keep rationale in the design docs, settled decisions in
[scope-boundaries.md](contributing/design/scope-boundaries.md), and evidence in
`docs/contributing/audits/`. Completed work is represented by the implemented docs/code, not repeated
here. Release readiness and publishing live in [tasks-v1-release.md](tasks-v1-release.md).
Work deliberately deferred beyond v1 lives in [tasks-post-v1.md](tasks-post-v1.md).

Last reviewed: 2026-09-15 (researched task transfer and scope reconciliation; earlier validation
entries retain their recorded dates and are not fresh test results).

The [2026-09-04 fresh review](contributing/audits/fresh-review-2026-09-04.md) records evidence for
`F-126` through `F-143`, including later scope corrections. Open findings are listed below; withdrawn
findings remain only in the audit. High-severity shutdown-safety findings take priority over release
housekeeping.

Testability labels used below:

- **Testable now** means deterministic unit, component, image, envtest, Kind, or k3s coverage can
  prove the behavior without site hardware.
- **Conditional** means the test should exist, but may run only when the owning component or
  packaging it depends on changes.
- **Real-resource** means some final confidence still needs physical hardware, an external service,
  or a real operating-system boundary that simulation cannot honestly prove.

`ENG-*` identifies scoped engineering work and `TEST-*` identifies test/harness work, rather than
new audit findings or design requirements. Severity on cleanup/research tasks denotes priority;
an implementation risk is not evidence that the current behavior is defective.

---

## Components

### Modular Deployment Profiles

Owns: installation and API boundaries across components, based on
[US-1 through US-4](contributing/design/user-stories.md). `MOD-1` through `MOD-3` remain investigations;
`MOD-4` defines the requested managed NUT-only profile, subject to its explicit dependency decisions.
No profile is advertised as supported until its contract, packaging, and acceptance tests agree.
Record conclusions in the owning design contracts and assign implementation work explicitly.
Medium denotes product priority here, not a demonstrated security vulnerability. Reuse existing components before
introducing new services or APIs.

- [ ] `MOD-1` [Medium] investigate the minimum agents-only installation (`US-1`). Compare reusing
  the shipped operand images with standalone manifests against a selectively enabled, lightweight
  operator. The existing DaemonSet has separate upsmon and actuator containers; copying one image
  does not supply configuration, credential separation, signal delivery, or lifecycle management.
  Trace required controllers, CRDs, admission/certificates, RBAC, projected Secrets, inventory,
  telemetry, and audit dependencies. Determine whether existing NUT endpoints can be consumed
  directly without a managed relay. Define who authorizes shutdown and through what supported API;
  do not promote internal signal Secrets to a public API or enable autonomous fallback implicitly
  (OD-37). **Testable now:** isolated render/startup experiments for both candidates, measured
  resource/dependency comparison, dry-run and unauthorized/stale-request rejection. Recommend the
  smallest maintainable option with explicit security and upgrade tradeoffs. Guest power-off
  qualification reuses the separate actuator test boundary; packaging tests do not prove it.
  **Research transferred 2026-09-15:** the current renderer already uses `MODE=netclient`,
  `MONITOR ... secondary`, and the project-owned `power-signal-writer` as `SHUTDOWNCMD`.
  Reuse that upstream netclient model rather than adding a second trigger service. Compare a typed
  external NUT target (UPS name, host/port, Secret-backed credentials, TLS trust) against current
  NUTServer-only refs; reuse upsmon rendering/readiness without fake NUTServer/UPSDevice objects.
  Investigate mutually exclusive `Operator` and `LocalNUT` authorization modes: Operator keeps
  the executor-issued projected Secret; LocalNUT would explicitly authorize the local upsmon
  signal. No implicit fallback or combined Operator-or-LocalNUT mode. Keep LocalNUT a proposal
  until OD-37/SB-3 and sequencing scope are deliberately revised and its approval/identity contract
  is defined; existing flow-binding safeguards cannot simply be disabled to make it work.
  **Security prerequisites:** upsmon must not acquire `CAP_SYS_BOOT`, host PID access, Talos
  credentials, or host-actuation code. Only the actuator may halt a host. Revisit the deferred
  `F-45` multi-supply assumptions before any LocalNUT implementation: hardcoded `MONITOR` power
  and `MINSUPPLIES` stop being inert when local signals are authorized. Keep the existing `OD-19`
  outbound FSD broadcast decision separate from consuming upstream FSD in this proposed mode.
  Prefer a selectively enabled agents-only manager unless comparison shows disproportionate cost;
  its current inventory coverage and ShutdownFlow rollout-hold dependencies must become optional,
  not merely unused CRDs left installed. **Acceptance after design approval:** real external upsd
  without managed NUTServer; real OB+LB/FSD reaches Simulate only in LocalNUT; Operator ignores
  local signals; no mode fallback; stale/wrong-node/unauthorized requests refused; no planner,
  inventory, PostgreSQL, or ShutdownFlow dependency in the agents-only package.
- [ ] `MOD-2` [Medium] verify orchestration with an existing host-shutdown system (`US-2`) before
  designing another actuator mechanism. Build a public-safe example and component fixture using
  authored inventory, `ShutdownHook`, and `ShutdownFlow` `RunHook`, with a fake HTTP receiver and
  explicit rehearsal invocation. Verify a mixed flow can use built-in agents for some nodes and
  hooks for others without requiring an agent for hook-only work. `PowerInventoryNode.nodeName`
  identifies a Kubernetes Node, not an arbitrary external host; establish how external-host
  identity, UPS scope, and communication dependencies are represented without fabricated Nodes.
  `RunHook` does not enumerate node targets: verify explicit per-host/group hook declarations and
  static request data before claiming automatic per-host dispatch. **Testable now:** request
  targeting, Secret-backed authentication, endpoint allowlisting, dry-run with/without rehearsal,
  repeat-safe receiver behavior, timeout/failure evidence, and ordering against surrounding work.
  Distinguish delivery from completed shutdown: hooks remain advisory under OD-33/OD-34. Document
  which story outcomes already work and only open implementation tasks for demonstrated gaps;
  stronger completion/abort semantics require an explicit design decision.
- [ ] `MOD-3` [Medium] investigate aggregation with external planning (`US-3`), reusing `MOD-1`'s
  dependency comparison. Separate aggregation-only from aggregation-plus-agents requirements.
  Establish what runs today with no `ShutdownFlow`, whether planning controllers can be omitted,
  and which install/runtime dependencies remain mandatory. Identify existing NUT/telemetry
  interfaces and the missing, if any, supported boundary for externally ordered execution.
  Compare existing resource composition, selective operator settings, and standalone operands;
  do not require clients to synthesize compiled plans or write internal halt Secrets.
  **Testable now:** isolated startup and telemetry consumption without planning, followed by
  contract tests for whichever request boundary the investigation recommends, including approval,
  targeting, stale requests, and cancellation. Prove execution safeguards remain enforced when
  planning is external. Produce a supported/proposed capability matrix and a scoped implementation
  recommendation; no new network service is assumed. Future profile tests should run conditionally
  on their owning components, APIs, and packaging, independently of Hadron qualification.
- [ ] `MOD-4` [Medium] define and implement a managed NUT-only profile (`US-4`), distinct from
  aggregation with external planning (`MOD-3`). Support operator-managed `UPSDevice`/`NUTServer`
  rather than making users reconstruct a working operand from a raw nut-server image.
  **Existing basis:** NUTServer can omit `managementClusterRef`, select UPSDevices, create its
  standalone namespace, and render configuration, auth, TLS, Deployment, Service, NetworkPolicy,
  readiness, and PodDisruptionBudget; currently this path needs `spec.image.repository`.
  The operand is one upsd plus a separate driver-supervisor sharing `/etc/nut` and `/run/nut`.
  Direct image use remains a low-level development/diagnostic building block, not the primary
  supported install: otherwise users must reimplement config/credential generation, sidecar
  lifecycle, TLS mounts, exposure, policy, readiness, reload/restart, and upgrade behavior.
  **Profile decisions first:** compare NUTServer plus UPSDevice/NUTServer admission against that
  set plus the UPSDevice reconciler. NUTServer reads and validates selected device specs itself;
  useful device status/telemetry must justify the reconciler's capability/telemetry dependencies.
  Inspect admission dependencies too. Prefer the existing manager binary with explicit controller
  selection and profile-scoped CRDs, admission, RBAC, and manifests over a second operator binary
  unless measurements justify one. Do not start unused controllers or grant their permissions.
  No PowerManagementCluster, NodePowerAgent, ShutdownFlow, planner/executor, actuation, inventory,
  or PostgreSQL dependency. Record the profile-specific exception to full-product SB-11 rather
  than implying PostgreSQL is optional for the existing full installation.
  **Usability/API work:** provide a release-owned operand image default. Retain OperatorManaged
  admin/monitor credentials and ExistingSecret support. Keep TLS Required and provide or document
  certificate bootstrap; do not weaken TLS for convenience. Current generated ingress permits
  same-namespace clients and the manager, not generic clients elsewhere. Add explicit reviewable
  namespace/pod selectors and/or CIDRs for cross-namespace or external clients; NodePort or
  LoadBalancer exposure alone is not permission under an enforcing CNI. Account for actual source
  identity after service routing rather than promising CIDR behavior without testing it.
  **Testable now; Conditional:** install into a clean cluster with only this profile's resources;
  a real dummy-ups fixture must produce a Ready two-container operand and queryable upsd Service.
  Prove approved cross-namespace/external access and denied unapproved access under enforced
  policy; auth/TLS, device add/remove/config changes, isolated driver restart, reconcile and upgrade
  behavior use the same contracts as the full product. Assert both absent controller watches and
  RBAC inability to mutate planner/host-actuation resources. Profile testing follows `MOD-5`.
- [ ] `MOD-5` [Medium] add representative acceptance coverage for each supported deployment
  profile, after the owning MOD decision approves it. This is a conditional follow-up, not approval
  of every proposed profile or a combinatorial matrix of component subsets.
  **US-1:** existing NUT with no managed NUTServer or built-in planner; preserve approved-mode
  authorization, dry-run, targeting, stale-request rejection, and privilege separation (`MOD-1`).
  **US-2:** a real ShutdownFlow combining agents with ShutdownHook external actuation, including
  authentication, explicit targeting, timeout/failure evidence, repeat-safe delivery, and ordering.
  Delivery is not evidence that a host stopped; preserve the existing advisory hook contract.
  **US-3:** aggregation/telemetry without the built-in planner/ShutdownFlow path; exercise the
  authorized external execution boundary selected by `MOD-3`, including approval, targeting,
  stale requests, and cancellation. **US-4:** the clean managed-NUT-only install in `MOD-4`.
  **Testable now once selected; Conditional:** reuse component/Kind tests; run on owning API,
  component, and packaging changes. Reuse VM Linux/Talos qualification instead of re-proving host
  power-off in every package test. Only profiles selected for v1 become v1 release gates.

---

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/contributing/design/inventory-provider-contract.md` (`IN-n`).

- [ ] `ENG-9` [Medium] finish the integration/resolution boundary outside `internal/controller`.
  Review `declarative_inventory_adapter.go`, `declarative_inventory_resolver.go`, and
  `planner_adapter.go`; move fact gathering and planner-input normalization into existing
  integration-facing packages or a narrow adapter boundary. Preserve the pure no-I/O contract of
  `internal/resolver`: Kubernetes readers do not belong in it. Controllers should own reconciliation,
  desired-resource lifecycle, status/conditions, and orchestration entrypoints; integrations gather
  and normalize external/cluster facts. Keep the existing runtime -> resolution -> pure planner
  architecture, avoid a new integration monolith, and limit package churn to the responsibilities
  needed to make that boundary legible.
  **Testable now; Conditional:** adapter/resolver/controller tests preserve inventory identity,
  power-domain scoping, communication paths, role propagation, diagnostics, and deterministic
  planner inputs. Re-run affected F-127/F-128/F-129 and PL-21 boundaries after extraction; preserve
  uncached wave/release reads rather than turning compile-time snapshots into live safety evidence.
- [ ] `TEST-4` [Medium] add a disposable real-NetBox integration test for the shipped
  `netbox-inventory-sync` workflow. Fake-HTTP tests remain valuable but do not establish real API
  serialization, pagination, authentication, or DCIM relationship compatibility.
  Start an isolated supported, explicitly pinned NetBox version with bounded owned cleanup.
  Seed a UPS, Kubernetes node, power-infrastructure device, a power connection carrying downstream
  input identity, a communication connection, `nut_operator` custom metadata, and power-managed
  tag filtering. Run the shipped CLI and validate the provider-neutral snapshot and/or CR output.
  Exercise real authentication and pagination; credentials must never appear in output or errors.
  Verify deterministic identity/edge mapping and compilation under the same inventory contract as
  authored resources; malformed/unmappable provider data must fail without a misleading partial
  snapshot. **Testable now; Conditional:** run for importer, dependency, contract, and fixture
  changes. Use a dedicated disposable-service suite, not a live NetBox dependency in normal Kind
  E2E or shutdown runtime. An optional Kind test may consume an already-rendered artifact.
  Actual NetBox is required but site resources are not. If advertised as v1-supported, this
  compatibility test is a v1 requirement; otherwise record release placement explicitly.

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
`settled-questions.md`. Audit: `docs/contributing/audits/planner-code-quality.md`.

- [x] `F-126` [High] independently recheck flow and agent authorization (2026-09-13).
  Flow approval is re-read through the uncached reader at every wave and remains sticky-dry-run
  after revocation. The Kubernetes runner now requires a node release validator immediately
  before each signal Secret create/update. Production wiring re-reads the specific agent's current
  approval, mode, and node coverage; the selected UID, generation, and actuator policy must match.
  Missing approval, read failure, deletion/replacement, and changed policy refuse the handoff.
  A rejected node follows the existing `HaltAndSurface` policy; earlier successful publications
  retain their receipts. Linux PowerOff and TalosShutdown use the same independent approval gate.
  **Validated:** stale-cache/fresh-reader matrix; revocation between writes through both Secret
  create and update paths; cancellation, missing validator, identity/specification changes,
  and Talos revocation. The controller suite and repeated focused tests passed with the race
  detector; other packages passed in the broad sweep, and repository lint passed.
  The agent read and Secret write remain separate API requests, not an atomic transaction.
  Fresh placement/readiness/telemetry and wave target resolution are covered by `F-127` below.
- [x] `F-127` [High] resolve targets at wave start and refresh node clearance, readiness, and telemetry
  before each halt signal (2026-09-14). Wave-local resolution, pre-action guard refresh, and the
  independent per-write validation gate now cover the full execution path.
  **Testable now:** placement changes, drain-to-release transitions, and stale-agent simulations.
  Real-guest cross-check once built: `VM-4` (VM Test Coverage).
  **Wave resolution (2026-09-14):** production defers concrete instance enumeration until each
  wave starts, resolving all groups before any action through the uncached API reader. Grouped
  and linear flows use the same adapter and immutable wave-local target snapshots. New workload
  matches are included and removed matches disappear; empty namespace selection no longer falls
  back to all namespaces. Node/agent membership expansion beyond the compiled group fails with a
  recompile-required error, preserving power-domain and communication-ordering constraints.
  Pruned groups remain excluded. Resolution failure, cancellation, or deadline expiry fails the
  wave with explicit audit evidence and no actions from that wave. See EX-8 for deadline semantics.
  **Publication gate (2026-09-13):** the production runner now re-reads agent generation/status,
  signal destination, the actual Pod's node/readiness and actuator mode/policy, reported UPS phase,
  and current blocking Pods before its final authorization check and Secret mutation. Tests use
  stale cached objects plus an independent fresh reader and assert that no signal is written for
  new workloads, missing/unready Pods, stale policy, stale telemetry phase, read failure, or
  cancellation. Affected controller/executor/runner/manager race suites and repeated gate tests
  passed. **Telemetry age gate (2026-09-14):** initial collection and pre-publication validation
  now reject missing, zero, future, or expired last-poll timestamps, even with a healthy reported
  phase. Honor device `staleAfter`; otherwise expire after three effective polling intervals.
  The explicit freshness opt-out remains supported. Controller, action-runner, and executor race
  suites passed, including envtest; publication regressions and deterministic expiry-boundary tests
  cover stale timestamps and configured/default thresholds. See the node-agent design contract.
  **Guard refresh (2026-09-14):** the production executor refreshes release evidence before each
  AgentShutdown group's guards, inside its timeout. Agent reads use the uncached reader. The
  selected node/agent identity, generation, policy, and signal destination cannot change during
  refresh; input evidence remains immutable and compile-withheld nodes stay withheld. The final
  per-write safety/authorization gate remains independent. Regression coverage drives the production
  adapter through an earlier drain and later release, plus newly arrived workloads, readiness and
  selection loss, identity changes, failed reads, cancellation, and refresh timeout.
  **Validated:** controller/envtest, executor, action-runner, and command race suites; wave snapshot
  and audit assertions; grouped/linear production-adapter fixtures; scope expansion and pruned-group
  rejection; read/cancellation/deadline failures; ten race-enabled repetitions of the focused
  wave/guard/publication regression matrix; planner and adapter race suites; lint.
  Guard refresh preserves selected release membership rather than silently replanning. Hadron
  cross-checks remain independently owned by VM-4, not an unfinished F-127 implementation step.
- [x] `F-128` [High] enforce control-plane quorum and terminal ordering (`PL-23`, `PL-24`, `EX-18`)
  (2026-09-14). Inventory roles reach the pure planner and execution guards across providers;
  membership participates in plan identity. Grouped/linear compilation rejects cumulative quorum
  loss before completion, missing HA quorum declarations, and terminal control-plane groups with
  multiple agent channels. Explicit late dependencies are checked independently of tier ordering.
  Each ordinary control-plane publication rechecks uncached Node readiness and pending signals;
  serialized, cancelable publication prevents concurrent agents consuming the same quorum margin.
  A sole final handoff waits for all overlapped work and publishes all validated node signals in
  one Secret mutation. Authority comes from execution position, not a supplied flag. Atomic
  publication is not a simultaneous-projection or physical-halt guarantee; see EX-18.
  **Validated:** synthetic quorum/order/hash matrices, inventory adapter propagation, unavailable
  peers, pending signals, per-write and concurrent-agent races, terminal overlap failure, batch
  create/update/failure, and canceled lock acquisition; ten race-enabled repetitions of the
  focused matrix, broad planner/resolver/adapter/controller/executor/runner/command race suites,
  and lint passed. Component proof requires no hardware.
  A three-control-plane guest topology would provide additional OS/delivery qualification;
  `VM-4`'s current one-control-plane scope does not claim that coverage.
- [x] `F-129` [High] restrict execution to actually eligible power domains (2026-09-13).
  Validate the configured preflight plan, then compile grouped or linear execution against the
  evaluator's eligible UPS roots. Preserve mixed, unmapped, partially unresolved agent, and
  communication-dependent membership; an empty affected node set cannot admit healthy actions.
  Hold expiry expands the selected scope and plan identity; history, published artifacts, and
  execution use the same scoped hash. Resolve only compiled actions, including agent/hook reads.
  Empty execution selections and fully pruned plans fail explicitly rather than activating a
  broad plan or ignored linear fallback. With no eligible trigger, status retains preflight scope.
  **Validated:** controller-to-executor grouped/linear scenarios, real API status round trips,
  hold expiry, history identity, unknown/partial coverage, pruned agent references, empty-domain
  and fallback regressions; full API/internal/command race sweep and repository lint. Independent
  review findings received regression tests and fixes; the focused matrix passed ten repetitions.
- [ ] `F-132` [High] keep long-running executions from monopolizing reconciliation. The entire flow
  runs synchronously on the sole ShutdownFlow worker, delaying other flows and active status
  heartbeats. **Testable now:** two simultaneous flows, a blocked action, ongoing status cadence,
  and cancellation. Preserve per-flow serialization and correct in-process progress reporting;
  restart/resume continuity is not part of this task (SB-1).
  **Modularity context (2026-09-13):** `recordShutdownFlowExecution` directly calls
  `Executor.Execute` inside reconciliation. Separate bounded in-process execution ownership from
  reconciliation/status publication; keep executor policy independently runnable through its
  existing action, observation, approval, and audit interfaces. Define cancellation and manager
  shutdown cleanup, duplicate-reconcile behavior, per-flow serialization, and handling of two
  flows targeting overlapping resources. Merely increasing worker count does not settle these
  contracts. Acceptance must demonstrate a blocked flow cannot starve another flow or progress
  updates, repeated reconciles cannot start duplicate work, and canceled work releases its owned
  resources. A new network service, durable queue, or crash-resume subsystem is not required.
  **2026-09-15 proposal merged here:** long Wait/drain/hook actions need tracked, bounded ownership,
  not an untracked background goroutine. Coordinate audit ownership (`ENG-3`) and removal of
  resume-only machinery (`ENG-4`) without weakening trigger-episode deduplication or fresh per-write
  authorization. This is the existing finding, not a second concurrency task.
- [x] `F-142` [Medium] reject unsupported failure-policy settings before v1 (2026-09-13).
  Admission, CRD validation, and pure compilation accept only `HaltAndSurface`, abort `notify: false`,
  and `continueOnError: false`. Normal Notify actions and settled advisory-hook behavior remain
  unchanged. Defaults, samples, generated installers, and the owning design contracts agree.
  The upgrade guide covers explicitly clearing previously defaulted abort notification settings.
  **Validated:** rejection matrices through create/update admission and API-to-planner conversion;
  API-server default/update checks; full API/internal/command race suite and repository lint.

Remaining default-calibration evidence (`OD-27`) lives in the
[release qualification checklist](tasks-v1-release.md#qualification).

- [x] `PL-21` [High] implement communication-path dependencies in v1 planning and execution
  (2026-09-13). Node `carries` paths and explicit shared `OperatorAPI`/`NUT` service declarations
  drive outage scope, carrier-release ordering, and UPS runtime budgets, including node-less
  actions. Publication and execution share supply selection; artifacts distinguish modeled,
  unmodeled, exempt, and unknown-supply coverage. Carrier releases wait for overlapped work and
  surface its failures. CRDs and installers include the API; the topology guide covers authoring.
  **Validated:** race-enabled API, internal, and command package sweep, including controller
  API-schema/status envtest and webhook suites; repeated overlap/failure/cancellation
  and inventory-to-executor service-path tests; clean repository lint. These component tests
  require no physical switches. Modeled paths are not reachability or redundancy guarantees;
  physical halt acknowledgement and control-plane quorum remain separate contracts.

- [ ] `ENG-2` [Medium] tighten the pure planner without redesigning it. Move normalization,
  base structural validation, and related setup out of `CompileWithHistory` into focused helpers
  so `compiler.go` reads as the compilation pipeline: scope, validate, graph/waves, estimates,
  artifacts/diagnostics, feasibility, hash. Preserve behavior rather than targeting a line count.
  The strongest duplication target is topology derivation across `communication.go`,
  `communication_services.go`, and `scope.go`. Use a narrow immutable compile-scoped index only
  where shared derivations justify it: upstream/dependent adjacency, carrier paths, power-domain
  membership, group-node sets, carrier consumers, and supply constraints. No public framework,
  global cache, or cached data used only once. Preserve deterministic ordering/hashing and no I/O.
  Do not shrink `types.go` merely because StructuralInputs and Plan carry a broad contract; retain
  graph/provenance, explanations, startup-wave projection, diagrams, feasibility, and duration/history
  outputs. Keep communication semantics intact. Comment-history cleanup belongs to `ENG-8`.
  **Testable now; Conditional:** all planner tests, including determinism/hash, validation,
  provenance, feasibility, communication, and quorum stay green; equivalent inputs retain
  equivalent semantic artifacts. Judge clearer ownership and less repeated derivation/plumbing,
  not lines removed.
- [ ] `ENG-4` [Medium] remove machinery used only for durable executor resume, preserving SB-1
  and EX-26. Audit controller/audit/executor callers before removing persisted resume reconstruction,
  completed-group replay/skip logic, and resume-only interfaces or records. This is deletion of an
  unsupported subsystem, not reinstatement of F-130 or a demand for exactly-once execution.
  Preserve trigger-episode deduplication, in-process progress and adaptive execution state, current
  authorization, signal expiry/withdrawal, audit history, and repeat-safe actions. Repeated effects
  after interruption must be safe; precise checkpoint restoration and proof of skipped work are not
  required. Distinguish unused resume persistence from ordinary historical execution evidence.
  Coordinate with `F-132` and `ENG-3`; do not delete shared state before its in-process owner exists.
  **Testable now; Conditional:** repeated-action and episode-boundary tests, adaptive progression,
  fresh approval/targeting and stale-signal regressions, audit/history tests, and affected race suites.
  Define an explicit schema/upgrade strategy for resume-only tables without deleting unrelated
  audit data or rewriting already-applied migration history. Update scope/schema docs and retire
  resume-only tests only after supported behavior has independent coverage.

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/contributing/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`,
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`, `F-85`, `F-124`); relevant findings from `docs/contributing/audits/nut-usage-audit.md`
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`).

- [ ] `ENG-1` [Medium] replace the custom shell driver supervisor with a small Go supervisor,
  preserving the stable singleton upsd plus driver-supervisor pod and every meaningful `F-144`
  lifecycle contract. The motivation is maintainable ownership of processes and reloads, not a
  new defect claim or a replacement for NUT driver semantics. Detailed migration acceptance follows
  the existing findings below. `F-97` remains an independent High-priority root-cause investigation.

- [x] `F-144` [Medium] isolate and harden NUT supervision (2026-09-13).
  Runtime shell, configuration, and process tests now belong to `internal/nutsupervisor`;
  the controller embeds the same bytes without source rewriting. Its README compares upstream
  service management, s6, runit, and per-device containers. Retain named upstream `upsdrvctl`
  workers plus the small configuration adapter for v1, with existing credential/privilege boundaries.
  Malformed enumeration preserves working drivers and server configuration; empty startup,
  per-driver failure isolation, PID preservation, reload retry, and restart cadence are covered.
  Workers receive TERM, then KILL after a five-second grace period and are reaped. Shutdown starts
  all worker grace periods together; polling sleeps are interruptible and NUT control commands
  have five-second bounds. Unrelated workers survive removal of a stuck peer.
  **Validated:** full API/internal/command race suite, repository lint, repeated stuck-worker
  regressions, and a stalled named-stop helper. The real-NUT image harness passed normal lifecycle
  tests and killed/reaped a STOP-frozen driver within its bound using the cached ARM64 operand.
  Image CI runs the harness against its newly built native image. Cancellation cleanup is separately
  regression-tested. The container remains the final boundary for unexpected descendants and
  userspace deadlines cannot resolve kernel-level uninterruptible I/O. This closes the modularity
  and lifecycle task, not `F-97`'s intermittent readiness root cause or Kind/hardware qualification.

- `F-97` [High] find out why a driver `upsd` is still talking to fails a fresh `upsdrvctl status`
  connection, and only in the minutes after a pod start. The recovery half is done and measured in
  the 2026-08-30 focused Kind run: a killed driver is back in 4.32s against a 30s budget
  (`test/e2e/driver_recovery_test.go`). The readiness-gate half named here is also done —
  `internal/controller/nutserver_readiness_probe_component_test.go` runs the real readiness probe
  script against fake `upsdrvctl status` output and a real kubelet-counting simulation, covering a
  delayed driver start, isolated probe misses that never flap readiness, and a sustained run that
  correctly does. See the 2026-09-03 pass in `operator-maturity-benchmarks.md`. What remains is the
  root cause itself. Testability: **Testable now; Conditional** — build a stress reproducer using
  the actual `dummy-ups`/`upsd`/`upsmon` binaries in an image or isolated Kind cluster, and run it when
  NUT packaging, supervision, probes, or fixtures change. The 2026-08-24 isolated fixture did not
  reproduce the failure; that leaves the reproducer incomplete, not dependent on physical UPS
  hardware. Real USB/SNMP device behavior remains a separate hardware-compatibility boundary.
  **Stress harness (2026-09-13):** `make docker-stress-nut-readiness` runs actual `dummy-ups`,
  `upsd`, and authenticated secondary `upsmon` in a private non-root container, comparing fresh
  driver probes with four concurrent server reads per sample. It reports probe misses, server
  failures, and disagreements separately; misses and the overall timeout fail the run. The sample
  count is bounded, and the owning harness removes the container on exit. This opt-in component
  test is separate from ordinary image smoke and should accompany NUT/probe/supervision changes.
  **Validated:** final 60-sample local run with authenticated upsmon had zero probe misses, server
  failures, or disagreements, and passed lifecycle cleanup; a three-sample run also passed. Both
  used the cached ARM64 operand image. The original intermittent root cause remains open. One
  image architecture and dummy data do not establish Kind or hardware compatibility.

#### ENG-1 Migration Constraints and Acceptance

**Upstream gate:** recheck official NUT releases and source downloads before implementation. The
transferred research recorded 2.8.5 as stable and 2.8.6 as planned; this is not a current upstream
verification. Select a stable release, never an unreleased snapshot just because it is newer.
If a newer stable exists, update `NUT_VERSION`, tarball checksum, signing-key verification, image
assertions, and version-sensitive tests in a clearly separated change. Preserve both source-tarball
checksum and upstream-signature verification. Prove that release's `upsdrvctl list`,
`upsdrvctl -FF start <ups>`, `upsdrvctl -c reload-or-exit <ups>`, `upsdrvctl status`, and
`upsd -c reload` behavior; record differences rather than emulating an older NUT in project code.

**Pod and binary:** keep replicas at one and a stable container list. Adding/removing UPSDevice
must not restart upsd or change the pod shape just to update driver membership. Preserve
`shareProcessNamespace`: cross-container `upsd -c reload` still uses its PID file. Add a dedicated
binary such as `cmd/nut-driver-supervisor`, reusable logic in `internal/nutsupervisor`, and the same
pinned Go builder/static-binary pattern as other project operand helpers. Ship it in nut-server;
render only its direct invocation and bounded runtime configuration. The server entrypoint should
validate configuration and exec `upsd -FF`. No systemd, s6, runit, network service, queue, or new CRD
for this replacement. NUT owns enumeration, driver semantics, and driver-control commands.

**State and concurrency:** one serialized event/reconciliation loop owns membership, config
reloads, child exits, and shutdown. An in-memory UPS-name map owns each foreground upsdrvctl
process, termination/cancellation state, and last exit result. Use `exec.Cmd.Wait`, not `kill -0`
polling, and do not recreate shell `.pid`, `.exit`, or per-device `.digest` bookkeeping. Unexpected
exit reports UPS name and outcome and retries no faster than the existing fixed interval: no tight
respawn loop or added exponential delay during an outage.

**Projected configuration:** bounded periodic whole-file comparison handles atomic projected
ConfigMap/Secret replacement without requiring fsnotify. Standard-library SHA-256 is change
detection, not a security boundary. Zero-byte rendered `ups.conf` means intentional zero devices;
nonempty files must enumerate through NUT. Failed enumeration retains workers, last good server
reload state, and the unapplied new digest for retry; error-string matching must not turn malformed
input into an empty device set. Obtain a valid desired set before changing membership or reloading.
Remove owned workers; add new ones only after corresponding server configuration is accepted.
Unrelated adds/removes/edits must not restart surviving drivers.

**Reload semantics:** for surviving drivers on a valid ups.conf change, use NUT's
`reload-or-exit` decision rather than parsing individual sections or hashing per-driver config.
If NUT requires exit, observe the foreground worker exit and restart only that UPS. An
`upsd.users`-only change reloads upsd without touching drivers. Failed server reload is retried
without claiming adoption. Preserve validation/ordering that protects a working device set.
Listener/port, TLS certificate, and client-CA changes remain controller-owned pod replacements;
do not widen reload support beyond what the actual NUT release proves.

**Cleanup and security:** own the full process lifecycle using Linux process groups appropriate
to the operand. TERM, wait the existing bounded grace period, KILL remaining owned processes,
and Wait/reap; no orphaned upsdrvctl children or drivers. Start all shutdown grace periods together,
not one full period per UPS. Bound one-shot NUT control commands independently. Verify whether the
old best-effort named `upsdrvctl stop` after terminating an owned worker provides necessary cleanup;
retain it only for a demonstrated purpose. Keep non-root, read-only root filesystem, zero added
capabilities, RuntimeDefault seccomp, and existing config/credential/runtime mounts. No Kubernetes
token/RBAC, host namespace, hostPath, or network listener. The container remains the final cleanup
boundary; userspace deadlines cannot solve kernel uninterruptible I/O.

**Testable now; Conditional:** preserve or replace every meaningful F-144 regression. Deterministic
Go tests with fake NUT commands must cover isolated crashes and fixed restart cadence; unaffected
worker PIDs across add/remove; live reload with unchanged PID; NUT-requested restart of only the
affected driver; malformed/non-enumerable input retaining working workers/server state; empty
configuration converging to zero; users-only reload; failed reload retry; canceled/stalled commands;
owned-child reaping; TERM-ignoring workers; and shared rather than serial multi-worker grace periods.
Keep real dummy-ups/upsd/authenticated-secondary-upsmon image tests against the exact image/version
to ship: idle startup, mixed healthy/failing drivers, add/remove, crashes, reloads, unaffected
worker and driver PIDs, and no residual workers after termination. Run affected Go tests with the
race detector and retain the image workflow smoke gate. Renderer tests must prove direct execution
of the shipped binary; image assertions prove it exists and is executable. Shell source-text checks
can go only after equivalent behavioral coverage exists.

**Migration order:** verify stable NUT and any separate version update; build Go logic/tests while
shell remains a behavioral reference; compare real-NUT image contracts; switch rendered command
and run controller/image/Kind component/race coverage; then remove `supervisor.sh`, embed wrapper,
shell-only state and injection code. Update operand, image, contributor, and supervisor docs to one
implementation, retaining historical audit evidence as historical. Capture before/after
`docker-stress-nut-readiness` results for F-97; changed reproduction frequency is evidence, not a
root-cause conclusion or permission to weaken readiness.

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`, plus the operator-side halt evidence in `internal/haltwatch` and
`internal/controller/nodehalt_controller.go`. Design doc:
`docs/contributing/design/node-agent-operand.md` (`NA-n`). Audits:
`docs/contributing/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-92`, `OD-37`) and `operator-maturity-benchmarks.md` (`F-94`).

Execution-side actuation safety findings are owned by Planning & Execution Logic: `F-126`
(independent agent authorization), `F-127` (fresh release evidence), and `F-128` (quorum ordering).
This review found no separate actuator implementation task. Additional Linux guest actuation
coverage is tracked under `VM-3`/`VM-4` below; it does not introduce a new actuator policy.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/contributing/design/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

- [x] [Medium] publish v1 communication-ordering artifacts alongside `PL-21` (2026-09-13): derived dependencies,
  their communication-path and power-supply provenance, resulting ordering/timing constraints,
  and unresolved-path diagnostics. Publishing topology alone is not the completed ordering feature.
  **Testable now:** deterministic planner artifact and controller-status fixtures matching the
  dependencies actually used by planning; no switch or PDU actuation is required.
  **Implemented:** derived node/shared-service ordering edges, path-source provenance,
  explanations, diagrams, and structured runtime-budget inputs with unknown supplies,
  unresolved actions, and explicit modeled/unmodeled/exempt coverage. API round-trip tests
  verify service references and coverage survive storage; runtime and artifact fixtures agree.

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/contributing/design/audit-storage-schema.md`.

- [x] `F-145` [Medium] add an isolated real-PostgreSQL component test suite for audit persistence
  (2026-09-13).
  **Evidence (2026-09-13):** inspected audit/storage tests exercise SQL and connection interfaces
  through fakes; the inspected CI/e2e harnesses provide no real PostgreSQL test dependency.
  These tests cannot establish server acceptance of migrations, queries, or database semantics.
  Keep the fast fake-based tests and add a disposable PostgreSQL instance using established
  container tooling, with explicit isolated connection configuration and cleanup ownership.
  **Testable now; Conditional:** verify fresh and repeated migrations, custom schema quoting,
  all record write/read paths, uniqueness/upsert behavior, retention, history queries, and spool
  replay. Exercise connection failure and bounded stalled I/O alongside `F-131`, preserving
  action-outcome versus evidence-failure separation. Assert replay repeat safety, not executor
  crash-resume guarantees. Run for audit/storage/schema/dependency/harness changes; no UPS or
  Kubernetes cluster is needed. PostgreSQL coverage does not claim CNPG failover qualification.
  **First component slice (2026-09-13):** `make test-postgres` runs the tagged audit suite against
  a digest-pinned disposable PostgreSQL image, private Docker network, and ephemeral loopback port;
  its trap removes owned resources. Verified fresh/repeated migrations with quoted schema names,
  execution/group persistence, plan-hash and dry-run history filtering, retention cascades, recent
  JSON payload preservation, and deadline cancellation of a real stalled query with subsequent
  pool usability. The explicit DSN entry point is for disposable databases only; tests create and
  remove their own schema. **Completed follow-up:** every Writer record family is persisted and
  counted, all production reader methods run against PostgreSQL, progress upserts are checked,
  and unavailable-database spool capture, failed replay retention, and repeated journal delivery
  are exercised. A real regression reproduced duplicate-key failures for nine immutable record
  families; their inserts now ignore conflicts on their own identity while unrelated integrity
  errors still surface. The repeated-journal test checks database row counts and journal removal.
  `.github/workflows/test-postgres.yml` adds bounded, path-filtered CI plus manual dispatch using
  the same local harness. Local race-enabled database and audit/storage regressions passed;
  hosted execution is verified by the workflow after publication. This does not close `F-131`
  or establish full storage-failure resilience.

- [x] `F-131` [High] make configured audit fallback available during startup storage outages
  (2026-09-13). Unready storage and failed connections reach the spool-backed execution writer.
  Connection, history, replay, and execution writes have context deadlines; a timed-out writer
  latches failure for the reconcile to avoid repeatedly consuming the shutdown window.
  **Validated:** enforced-flow component tests execute actions with unavailable storage; cancellation
  and stalled-writer tests cover fallback; a real PostgreSQL table-lock test proves bounded write
  failure and successful recovery replay. Full API/internal/command race tests passed. Existing
  approval checks remain in force, and evidence failures stay separate from action outcomes.
  Local filesystem stalls are not covered by database deadlines; durable resume remains outside SB-1.

- [ ] `ENG-3` [Medium] separate execution ownership from audit orchestration, preserving `F-131`.
  `recordShutdownFlowAudit` still sets up the writer and calls `recordShutdownFlowExecution`;
  make the eligible, authorized execution path independently explicit, with audit as an attached
  bounded evidence sink. Coordinate writer/store lifetime with `F-132`'s execution owner so a
  reconcile cannot close storage underneath running work. This is an ownership improvement, not
  a reopened claim that unavailable PostgreSQL always blocks shutdown.
  **Reconciled 2026-09-15:** `openExecutionAuditStore` already returns an unavailable bounded store
  for configured spool fallback; `TestShutdownFlowSpoolsWhenDatabaseCannotOpen` covers unready
  storage and connection failure while Enforce actions complete. Preserve this behavior, storage
  I/O bounds, evidence degradation, spool replay, and separation of action/evidence outcomes.
  SB-11's configured-spool condition remains authoritative; do not silently make a no-spool setup
  fail-open or remove full-product storage requirements during structural cleanup.
  **Testable now; Conditional:** unavailable/stalled database, startup/connection failures,
  cancellation, spool full/write failures and replay, correct execution/store cleanup, and
  concurrent-flow ownership. Keep approval gates independent of audit availability. Reuse real
  PostgreSQL coverage from F-145; distinguish filesystem stalls from bounded database I/O.

---

### Operator Maturity & Hardening

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/contributing/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

- [ ] `ENG-5` [Medium] unify shared static validation for admission and reconciliation,
  especially NodePowerAgent and ShutdownFlow. Keep both enforcement boundaries, but extract pure
  per-resource rules with adapters for admission field errors versus status/conditions. Avoid a
  generic validation framework and do not confuse static validation with fresh runtime authorization.
  **Testable now; Conditional:** shared accept/reject matrices, create/update admission and
  controller-condition tests, defaulting and bypassed-admission cases; preserve independent
  execution/publication gates and useful field-specific errors.
- [ ] `ENG-6` [Low] split `nodepoweragent_render.go` and `nutserver_render.go` into focused
  same-package files for target discovery, credentials/TLS, config rendering, NetworkPolicy,
  workload objects, readiness/status, and related helpers. Preserve the reconciler/operand
  architecture; no renderer DSL, public framework, or service layer just to reduce file size.
  Coordinate NUT supervision changes with ENG-1 rather than maintaining two refactor branches.
  **Testable now; Conditional:** unchanged rendered workload/config/security objects, owner refs,
  hashes/rollout behavior, readiness, and controller tests. File movement must not change behavior.
- [ ] `ENG-7` [Medium] re-evaluate NUTServer and NodePowerAgent finalizers. Current finalizers
  rely on owner-reference garbage collection for rendered children and primarily delay deletion
  to emit a teardown Event. Determine whether any concrete durable cleanup obligation remains.
  If not, remove the finalizer dependency; an Event alone should not require an available manager
  to delete a resource. Retain only finalizers protecting a demonstrated teardown contract.
  **Testable now; Conditional:** deletion and owned-resource cleanup, manager-unavailable behavior,
  and upgrade cleanup for objects already carrying the old finalizer. Simply stopping addition of
  finalizers would strand existing objects; define a safe removal path and test it. Keep signal and
  credential cleanup obligations explicit before deciding garbage collection is sufficient.
- [ ] `ENG-8` [Low] trim audit-history narration from production planner, controller, executor,
  kubeactions, renderer, webhook, and related runtime code. Remove F-number/review chronology and
  prior-bug storytelling only where it does not explain current behavior. Preserve non-obvious
  invariants, protocol constraints, and safety reasoning. Move useful historical detail to existing
  audit documentation or regression tests; do not delete the regression itself.
  **Testable now; Conditional:** comment-only diff review and affected checks; no behavior change.
  Coordinate with ENG-2/ENG-6/ENG-9 so file extraction and comment cleanup do not compete.

- [ ] `F-146` [Medium, investigation] assess Kind suite setup costs and component-test boundaries
  before deciding whether any restructuring is warranted.
  **Confirmed context (2026-09-13):** `test/e2e/e2e_suite_test.go`'s shared `BeforeSuite` resolves
  and loads five images and sets up cert-manager even for a focused spec run. This does not mean
  every CI run rebuilds all production images: `.github/workflows/test-e2e.yml` builds from the
  checkout for PR/local runs, while the image-promotion caller passes four published operand/manager
  digests; the test-only SNMP fixture still builds on both paths. Shared setup can amortize costs
  across the full suite and protects production-artifact wiring. Its existence is not a defect.
  **Investigate:** inventory each scenario's real prerequisites and measure build/pull/load,
  cluster/CNI/certificate setup, scenario execution, and teardown separately. Compare focused versus
  full runs and PR versus promotion paths, including cache state, failures, retries, cancellations,
  and runner resource use. Keep unsuccessful timings visible and distinguish canceled observations
  from completed durations. Use existing timing evidence where available; record sample dates and
  counts rather than inferring savings from source alone.
  **Acceptance:** document a measured keep/change decision. Compare shared setup with selective
  fixtures or narrow component-image tests, including duplicated startup and maintenance costs.
  Preserve full Kind coverage, exact promoted-image testing, network-policy enforcement, cleanup,
  and required-check semantics. No suite split, fewer checks, or CI rewrite is approved by this task.
  Priority reflects developer feedback/test isolation, not a demonstrated shutdown-safety defect.
  **Investigated (2026-09-13):** a timestamped successful promotion job shows shared BeforeSuite
  taking 47.004s of a 14m23s job; four production-image pull/load operations account for 16.188s.
  The roughly 9m50s scenario interval includes deployment, waits and cleanup, not just assertions.
  Current recommendation is to retain shared setup and the exact-image gate. See
  [the evidence and dependency map](contributing/audits/kind-modularity-2026-09-13.md).
  Controlled focused/full, PR/promotion, cache-state, failure/retry and resource-use comparisons
  remain open; this single success does not establish average costs or a justified suite split.

- [ ] `TEST-1` [Low] deduplicate the Kind scenario layer and split materially different scenarios
  out of `test/e2e/e2e_test.go`. Extract recurring setup into focused fixtures/helpers using the
  existing dummy UPS fixture as a model. Preserve readable scenario manifests and important
  assertions instead of hiding them in a large builder DSL. Keep Kind and VM fixtures separate;
  `VM-8` is related work, not a shared all-purpose harness.
  **Testable now; Conditional:** retain scenario assertions and failure cleanup in focused/full
  runs. Keep the shared Kind suite/cluster unless controlled `F-146` evidence supports changing it.
- [ ] `TEST-2` [Medium, investigation] prove a full logical ShutdownFlow scenario is feasible in
  Kind before committing it as a permanent gate. Connect real dummy-ups telemetry transitions to
  an eligible ShutdownFlow, production trigger evaluation, planner/executor, drain/order actions,
  an operator-generated node signal, and the Simulate actuator consuming it. Existing telemetry
  and separately injected-signal tests suggest the pieces fit; they do not prove that full path.
  **Testable now; Conditional:** first build a minimal honest fixture, then promote it with positive
  execution/order evidence and negative authorization/stale-signal controls. Do not substitute a
  hand-written halt Secret for production signal publication. Kind ends at simulated actuation;
  real guest shutdown stays in VM-3/VM-4/VM-7. Retain exact promoted-image coverage and network
  policy enforcement. This investigation does not authorize splitting CI or dropping VM evidence.
- [ ] `TEST-3` [Medium; High for mutation isolation] harden Kind reproducibility and kubeconfig
  handling. Give the suite a private temporary KUBECONFIG and explicit owned cluster identity;
  preserve the user's normal context through success, setup failure, cancellation, and cleanup.
  This suite mutates/deletes resources, so accidental use of an unrelated cluster is the High risk.
  **Pinning policy:** production images being promoted remain immutable/digest-addressed;
  semantics-affecting test infrastructure gets an explicit stable version and digest/checksum
  where artifact identity matters. Small gating helpers normally use versioned releases. `latest`
  is allowed only in explicitly non-gating development/compatibility checks; keep upgrades routine.
  Pin Kind CLI rather than downloading latest and version `curlimages/curl:latest`; a checksum or
  digest is optional for these helpers unless needed for immutable identity. Put the authoritative
  policy in CONTRIBUTING.md, a short pointer in AGENTS.md, and actual pins in their existing
  Makefile/workflow/config owners. Do not duplicate exact versions in guidance.
  **Testable now; Conditional:** validate unchanged external kubeconfig/context, refusal to mutate
  a mismatched cluster, cleanup limited to owned resources, and stable version selection on gating
  paths. Preserve shared-suite behavior and required-check semantics; coordinate with TEST-1/F-146.

### v1 Release Readiness

Release tasks and acceptance gates live in [tasks-v1-release.md](tasks-v1-release.md).

### VM Test Coverage

Owns: the generic PEG/QEMU-backed VM harness, Hadron Linux + k3s and Talos guest qualification,
VM-boundary acceptance tests, and their GitHub Actions integration in this development repository.
Talos is proposed coverage under VM-7, not an already-qualified guest. Keep this separate from Kind and
site deployment configuration, and keep its own scope narrow: a real guest kernel proves things a
Kind node (a container, not a VM) cannot -- real `reboot(2)`, a capability that survived the image
build and registry round trip, genuine host PID namespace membership, real kubelet Pod Security
Admission. Keep logical drain/sequencing/quorum/policy matrices in component and Kind coverage;
do not assume that a full logical ShutdownFlow Kind scenario already exists (TEST-2 investigates
that boundary). VM-4 still connects production orchestration to a real guest halt. Avoid replicating
the whole logical matrix in VMs. Start with one control-plane VM and one disposable worker; 6 GiB
combined guest RAM is an initial estimate, not a measured minimum. No physical UPS is
required. `VM-1` found GitHub-hosted runner KVM feasibility usable; see
`docs/contributing/audits/hadron-vm-1-feasibility-2026-09-11.md`. `VM-2` has verified a real single
Hadron guest boots to a Ready k3s node; the two-node harness, kubeconfig wiring, and `VM-3` through
`VM-6` remain unfinished. A single-guest boot is not guest-boot-under-load or shutdown acceptance
coverage.

For these tasks, High denotes isolation or shutdown-evidence risk, Medium denotes feasibility or
integration work, and Low denotes optional tooling or documentation. Evaluating upstream reuse is
also an early implementation priority, not a finding that custom VM code is inherently unsafe.

- [ ] `VM-8` [Medium] extract reusable guest/cluster fixtures from repeated Hadron actuator,
  manager, and UPS-stack scenario setup. Own boot/start, readiness, client/kubeconfig acquisition,
  image import where applicable, guest process-exit verification, and bounded teardown in the
  fixture; individual scenarios keep their behavior/assertions visible. Preserve cancellation,
  resource ownership, failure artifacts, and independent shutdown-cause evidence.
  Put guest-specific provisioning behind adapters: Hadron can use SSH/Kairos/k3s; Talos uses machine
  config, Talos API/talosctl, and Kubernetes. The generic layer must not assume SSH exists.
  **Testable now; Conditional:** reuse component failure/cancellation tests plus existing live
  Hadron scenarios after extraction. This cleanup has value independently of Talos and is VM-7's
  prerequisite. Keep it separate from TEST-1's Kind helpers. Defer a standalone library until
  Hadron/k3s, Talos, actuator, and manager/UPS scenarios establish a useful shared contract.
- [ ] `VM-7` [Medium bring-up; High shutdown evidence] qualify a Talos guest using the existing
  PEG/QEMU harness, not a parallel Talos framework. Build on VM-8's generic fixture and preserve
  artifact verification, loopback-only management, networking, process ownership, evidence capture,
  and bounded cleanup. **Milestone 1:** pin Talos image/version/checksum; use supported tooling to
  generate/apply machine configuration through a guest adapter; reach the Talos API from the host;
  bootstrap one Kubernetes node; obtain kubeconfig and verify actual Node Ready from the host;
  prove clean owned teardown. Keep Talos credentials isolated and out of logs/artifacts.
  **Milestone 2, only after deterministic bring-up:** run the shipped TalosShutdown path with the
  same missing/expired/wrong-node signal, authorization/revocation, targeting, and privilege-boundary
  standards as the Linux VM tests. Negative cases leave the guest running; positive evidence comes
  from outside the guest and distinguishes guest-requested shutdown from QEMU crash, forced kill,
  connection loss, or timeout. Reuse VM-3's evidence checks and negative controls.
  **Testable now; Conditional:** fake/component provisioning/cancellation tests first, then actual
  disposable Talos guest API and shutdown qualification. No physical UPS or site cluster needed;
  a passing Hadron test cannot close Talos acceptance. Coordinate image/job wiring with VM-5 and
  public instructions with VM-6; only gate on repeated, bounded, reproducible guest success.

- [ ] `VM-2` [High] implement a reproducible two-node VM harness using established virtualization
  tooling and declarative Kairos configuration. Pin OS/k3s artifacts, checksums, and test image
  identities; give each run private networking, fresh credentials/storage, and an explicit
  kubeconfig. Refuse mutations unless cluster identity and target VM/node mapping match; do not
  change the user's default context or mount host block devices. Keep the manager, PostgreSQL,
  and simulated UPS on the surviving node, with observation outside the target guests. Teardown
  must be bounded, run on failure/cancellation, and remove only resources owned by that run.
  **Testable now; Conditional:** leave Kind helpers and `make test-e2e` unchanged; explicitly
  scope the isolated VM entry point in contributor guidance when it is implemented.
  **Upstream reuse check: done.** PEG evaluated and adopted via a thin adapter (`test/hadron`,
  build-tag-gated); see `docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md` for the
  license/maintenance/dependency/KVM/SSH-exposure/cleanup findings and how each is closed at the
  adapter layer. Remaining for `VM-2` itself: the actual two-node topology, pinned Hadron + k3s
  artifact, kubeconfig wiring, and the multi-VM teardown/mutation-refusal safety checks below.
  **Adapter hardening (2026-09-11):** regression tests cover process-exit confirmation independent
  of PEG's deleted PID file [High], strict SHA-256 validation [Medium], and bounded/cancellable
  downloads that return errors and remove partial state [Medium]. Existing unit CI runs the tagged
  component tests without KVM; this is not the `VM-5` guest-test job. See the PEG evaluation for
  the reproduced failures, fixes, and remaining harness boundaries.
  **Single-guest boot: verified live (2026-09-12).** `kairos-hadron-v0.5.1-standard-amd64-generic-v4.3.0-k3sv1.36.4+k3s1.iso`
  (`sha256:1488c390e91128e6d8e1f6c2258c3e32881b3ff44de17a700134e2f671f3575c`, cross-checked against
  both GitHub's asset digest and the release's own `.sha256` sidecar) — "Hadron" names a real
  upstream project (`kairos-io/hadron`, a minimal from-scratch Linux distro Kairos combines with a
  Kubernetes distro to build release artifacts), not a project codename. `test/hadron/cloudinit.go`
  builds a cloud-init NoCloud seed ISO carrying an unattended-install cloud-config with the
  adapter's fresh per-run credentials and `k3s.enabled: true`, attached via PEG's `DataSource`, with
  the install device `/dev/vda` (PEG attaches disks as `virtio-blk-pci`, not the `/dev/sda` generic
  Kairos docs assume). `hadron-vm-boot-smoke.yml` (`workflow_dispatch`-only, following `VM-1`'s
  pattern) booted the pinned artifact, ran the unattended install, and confirmed a genuinely Ready
  k3s node via `kubectl get nodes -o json` in 103.83s
  ([run 34705344528](https://github.com/MichaelZalud18/nut-operator/actions/runs/34705344528)).
  Three earlier live runs each found one real, distinct gap before this passed — see
  `docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md` for the full sequence — most
  notably that the `provider-kairos.bootstrap.after.k3s-ready` cloud-config stage Kairos's own docs
  document never fires on this version; k3s itself was healthy well before that was diagnosed, so
  readiness is polled via `kubectl` directly, not that stage's marker file. This proves one
  disposable guest boots and reaches a working k3s node; the two-node topology, kubeconfig wiring,
  and multi-VM safety checks below remain open.
  **Inter-guest networking, component-tested but not yet boot-verified (2026-09-12):** the single
  verified boot above proves one guest works in isolation, not that two can talk to each other --
  PEG's own networking (already replaced once, for the management NIC) gives each guest an
  isolated user-mode NAT stack with no path to any other guest, which is the actual precondition
  for a k3s agent ever joining a k3s server. `test/hadron/network.go` adds `ClusterLink` /
  `ClusterNIC`: a private, host-only virtio-net segment between exactly two guests over a raw QEMU
  socket netdev on `127.0.0.1` (`listen=`/`connect=`, not a bridge or tap device, so no elevated
  runner privileges are needed). Point-to-point rather than multicast, deliberately: this only
  ever needs to serve the "one control-plane VM and one disposable worker" scope stated above, and
  a point-to-point TCP-backed socket is less likely to hit CI-runner-specific multicast/IGMP
  restrictions than `-netdev socket,mcast=...` would. Each side gets its own fresh, randomly
  generated locally-administered MAC (`RandomClusterMAC`, `52:54:00:` OUI) — the two peers only
  need to differ from each other, since the segment reaches nothing else, but this still never
  reuses a static value, consistent with every other credential in this package. Component tests
  cover role/port/MAC wiring and rejection of invalid MACs or an unconstructed `Link`.
  `hadron-cluster-link-smoke.yml` (`workflow_dispatch`-only) boots two live-installer guests over
  this link and pings between their kernel-assigned IPv6 link-local addresses — no install, no
  k3s, isolating exactly this one new mechanism from everything the single-guest workflow already
  proved. `TestSmokeWorkflowsReserveCleanupBudget` (`workflow_test.go`) now checks both smoke
  workflows, not just the first, and caught this new one's own step-timeout-vs-job-timeout budget
  being wrong before any live run. **Passed 2026-09-13** ([run 34766743049](https://github.com/MichaelZalud18/nut-operator/actions/runs/34766743049),
  42.39s): a real SSH banner read back between two independently booted guests over the raw QEMU
  socket netdev link. Full evidence table, including the seven host-side tooling failures along the
  way (guest login, a missing package, BusyBox's `ping` applet having no IPv6 support at all, a
  curl-version log-wording mismatch) — never the `ClusterLink` mechanism itself — in
  [hadron-vm-2-peg-evaluation-2026-09-11.md](contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md).
  The raw L2 segment still carries no DHCP of its own — a future harness needing static or
  negotiated addressing on it (rather than the IPv6 link-local addresses this test used
  deliberately) still needs its own mechanism.
  Checked, not assumed: Kairos's own reference docs do not show a clear, reliable static-network
  cloud-config mechanism, and this project already has one direct cautionary tale about trusting an
  apparently-documented Kairos cloud-config feature that silently did not fire (the `k3s-ready`
  stage, above) — writing speculative network-config YAML now, with no way to verify it live, risks
  repeating that. Left open rather than guessed at.
  **Host-side kube API access: verified live (2026-09-12).** `Config.ForwardKubeAPI` forwards a
  second loopback-bound port to the guest's k3s API server (`Credentials.KubeAPIPort`), and
  `test/hadron/kubeconfig.go`'s `Kubeconfig` fetches the guest's own k3s-generated kubeconfig over
  SSH and rewrites only its server URL's port to match — not the host, which VM-2's own successful
  live run already proved is `127.0.0.1` by default, so the certificate's Subject Alternative Name
  check (which only inspects the host/IP, never the port) needs no changes. Needed regardless of
  whether the eventual harness ends up single- or two-node: something outside any guest needs API
  access either way.
  [Run 34712598425](https://github.com/MichaelZalud18/nut-operator/actions/runs/34712598425)
  fetched the kubeconfig, built a real `client-go` clientset from it, and listed nodes through the
  forwarded port from outside the guest entirely — passed on the first attempt, no iteration
  needed. This is the actual "kubeconfig wiring" `VM-2`'s own text names as open work, not a proxy
  for it.
  **High-severity safety checks:** mismatched cluster/VM identity must refuse mutation; partial
  startup and cancellation must clean up only the current run. Bind forwarded management ports to
  loopback (done in `test/hadron`) and prove concurrent runs cannot target or delete each other's
  resources. Private state-directory allocation is covered by `test/hadron`; a sampled free SSH
  port is not a reservation or proof of concurrent-run isolation. Missing PID evidence fails closed;
  the future harness must separately track never-started VMs and verify target/process ownership.
  **Timeout/cleanup hardening (2026-09-12): locally component-tested; live cancellation rehearsal
  pending.** [Medium] SSH handshake, session creation, commands, polling, and diagnostics now honor
  cancellation/deadlines. [High] The workflow reserves job-level cleanup margin, bounds compilation
  and execution externally, and replaces broad QEMU killing with run-directory-scoped pidfd cleanup
  before artifact upload. State deletion requires successful process cleanup. In-process failure
  cleanup confirms exit without deleting diagnostic evidence; cleanup errors fail the smoke test.
  [Medium] Readiness checks parse the single node's actual Ready condition rather than accepting
  the substring in NotReady. Regression coverage includes stuck SSH phases, cancellation, workflow
  budget/order, process ownership, repeated cleanup, and negative readiness cases.
  **Remaining limits:** PEG startup/seed tooling is not fully context-aware; external workflow
  deadlines remain necessary. A forcibly lost runner cannot execute cleanup; local component tests
  do not prove hosted cancellation behavior. Full VM/node identity and port-collision guards below
  remain open; the cleanup ownership test does not close the two-node harness acceptance criteria.
  **Open — not yet applicable to the single-guest smoke test, but real, specific work once the
  two-node harness exists (do not close as N/A without building these):**

  1. **Mismatched cluster/VM identity must refuse mutation.** No pre-existing cluster exists for
     the current smoke test to target, so there is nothing to mismatch against yet. The harness
     needs an explicit identity check (expected cluster/kubeconfig identity vs. actual, expected
     VM/node mapping vs. actual) before any mutating action, refusing rather than proceeding on a
     mismatch.
  2. **Concurrent runs must not target or delete each other's resources.** Each `workflow_dispatch`
     run of the current smoke test gets its own dedicated GitHub-hosted runner, so there is nothing
     to collide with yet. Once the two-node harness runs multiple VMs within one job -- and once
     more than one harness run can execute concurrently (e.g., two manually triggered runs, or a
     future automated trigger) -- state directories, forwarded ports, and any shared identifiers
     must be run-scoped and verified not to collide, not merely assumed unique the way a single
     `os.MkdirTemp`/`freeport.GetFreePort()` call already is today.
- [ ] `VM-3` [High] test the shipped Linux actuator on Hadron, including missing/expired/
  wrong-node signals and absent or revoked approval. Negative cases must leave the guest running;
  the approved positive case must stop only the intended disposable VM, confirmed through the
  hypervisor rather than Kubernetes `NotReady` alone. Preserve the existing security context and
  approval gates; do not grant blanket privileges. **Testable now; Conditional:** real guest
  OS-boundary coverage, separate from Talos API and physical-machine qualification.
  **High-severity evidence check:** distinguish guest-initiated shutdown from QEMU crash, forced
  termination, lost SSH, or timeout. Capture the shutdown cause and process outcome outside the
  guest before teardown; process disappearance alone is insufficient. PEG's `Stop()` is host-driven
  termination, not actuator success ([QEMU implementation](https://github.com/spectrocloud/peg/blob/d8627da0983c42bde4d5b21dee650205fd1fb3b7/pkg/machine/qemu.go)).
  Add negative controls proving these failure modes cannot satisfy the shutdown assertion, and
  reuse the same evidence checks in `VM-4`. A false pass would hide a broken shutdown path.
  **First three milestones closed 2026-09-13** ([run 34775970876](https://github.com/MichaelZalud18/nut-operator/actions/runs/34775970876),
  all pass): the real, shipped `node-actuator` image arms correctly (`CAP_SYS_BOOT` survives a
  real kubelet/containerd round trip), correctly rejects wrong-node and stale signals without ever
  halting the guest, and correctly halts the guest on a real accepted signal -- full gate chain
  captured (`SignalAccepted` -> `FlowBinding` -> `ModeAuthorized` -> `Sync` -> `CapabilityEffective`
  -> `SyscallIssued`) and independently corroborated by the guest's own QEMU process exiting on its
  own, without the test ever calling `SafeStop`/`SafeTeardown`. Three real, distinct bugs were
  found and fixed along the way (a wrong assumption about capturing a racy pod log, and a genuine
  k3s default-ServiceAccount startup race) -- full history and evidence table in
  [hadron-vm-3-actuator-2026-09-13.md](contributing/audits/hadron-vm-3-actuator-2026-09-13.md)
  (`test/hadron/actuator_smoke_test.go`, `hadron-actuator-smoke.yml`).
  Revoked approval and the full DaemonSet/RBAC remain open.
- [ ] `VM-4` [Medium] drive a simulated UPS outage through actual NUT telemetry, trigger evaluation,
  planning, execution, draining, signal delivery, and guest power-off. Assert survivor availability,
  current authorization/release evidence (`F-126`/`F-127`), enforced network policy, and audit results;
  manual signal injection alone is not this end-to-end test. Add a second worker for ordered and
  concurrent release scenarios only after measuring capacity. **Testable now; Conditional:**
  retain logs and hypervisor evidence outside stopped guests; harness-owned reset is not operator
  recovery, and restart/resume continuity remains outside scope (SB-1).
  **Checked, not assumed (2026-09-13): `test/e2e` does not already cover this end to end.** It
  drives real telemetry transitions from a scripted `dummy-ups` fixture and separately hand-writes
  a shutdown signal into a projected Secret to prove the actuator accepts it, but nothing there
  creates a `ShutdownFlow`, waits for trigger evaluation, and asserts execution/drain/poweroff --
  this is genuinely new ground, not a Hadron variant of an existing Kind test.
  **First milestone passed 2026-09-13** ([run 34777697857](https://github.com/MichaelZalud18/nut-operator/actions/runs/34777697857),
  262.16s, first attempt): `TestHadronOperatorManagerDeploys` (`test/hadron/operator_smoke_test.go`,
  `hadron-operator-smoke.yml`) gets the real CRDs/RBAC/manager Deployment running inside a Hadron
  guest's k3s -- via `config/byo-cert` and `hack/webhook-cert.sh` (this repo's own no-cert-manager
  deploy path, chosen deliberately: "this operator's job is to run correctly while the cluster is
  losing power," per that overlay's own comment, so installing cert-manager into a throwaway guest
  just to get a serving certificate would be a second, unrelated thing to prove reliable).
  **Second milestone passed 2026-09-13** ([run 34781621589](https://github.com/MichaelZalud18/nut-operator/actions/runs/34781621589),
  547.22s): `TestHadronOperatorRunsRealUPSStack` (`hadron-ups-stack-smoke.yml`) additionally
  builds/imports the real `nut-server` and `upsmon-agent` images and applies a real
  `UPSDevice`/`NUTServer`/`NodePowerAgent` fixture -- the same shape `test/e2e`'s own
  signal-delivery spec already proves against Kind -- confirming the real, operator-rendered
  `NodePowerAgent` DaemonSet reaches Ready on a real guest kernel, `DryRun`/`Simulate` so nothing
  can halt the guest yet. Full rationale and evidence table in
  [hadron-vm-4-operator-2026-09-13.md](contributing/audits/hadron-vm-4-operator-2026-09-13.md).
  A `ShutdownFlow` trigger driving a real, operator-produced signal (not one hand-written, per
  "manual signal injection alone is not this end-to-end test"), the two-guest topology, real
  drain/eviction against a live workload Pod, and the network-policy/audit assertions remain open.
- [ ] `VM-5` [Medium] integrate the proven harness as a separate, initially manually triggered
  Actions job. Consume images built from the exact revision under test, using the existing
  digest-based image workflow where applicable, rather than rebuilding while VMs run or pulling
  an unrelated `main` image. After successful repeated runs, add component-based triggers for
  actuator, planner/executor, NUT integration, policy, packaging, and harness changes. Bound job
  time/concurrency and artifact retention, use minimal token permissions, and require no site
  secrets or access to a persistent private environment. **Conditional:** make it a required
  check only after runner feasibility, resource use, and test reliability are demonstrated.
  **Upstream workflow check: done (2026-09-12), not reusable.** Kairos's own
  [reusable QEMU workflow](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-qemu-test.yaml)
  needs `QUAY_USERNAME`/`QUAY_PASSWORD` registry secrets this project has no reason to hold, and
  runs on self-hosted `fast`-labeled runners for nearly every test -- the opposite of what `VM-1`
  proved usable (standard GitHub-hosted `ubuntu-latest`). Its diagnostic patterns are still worth
  knowing: the same KVM ACL+udev fix this project found independently, and a libvirt-bridge
  (`virbr0`) approach to VM-to-VM networking, an alternative to `network.go`'s own point-to-point
  `ClusterLink` worth revisiting only if that turns out not to work live or a future harness needs
  more than two nodes. See `docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md`.
  **Image-build check: done (2026-09-12), not needed.** The pinned Hadron artifact already meets
  every `VM-2` requirement, and all customization so far (credentials, install target, k3s
  enablement) happens at boot time through the cloud-config `DataSource`, not at image-build time
  -- exactly the "pinned published artifact" case this check's own text says to prefer. Kairos's
  [Factory workflow](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-factory.yaml)
  stays unevaluated further unless that changes.

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

The public VM test guide (`VM-6`) lives in the
[release qualification checklist](tasks-v1-release.md#qualification).

- [ ] `ENG-10` [Low, research] evaluate whether a first-time setup wizard improves usability after
  the quick-start/examples in `REL-5` are available. Compare Kubernetes-native authored CRs with a
  wizard generating the same standard resources, not a second configuration model. Evaluate the
  UPS/NUT, topology, and shutdown-policy inputs, safe Secret collection/output, CLI/TUI or other
  form, and maintenance/test burden versus usability benefit.
  **Testable now after REL-5:** representative first-user walkthroughs and generated-resource
  validation can inform an adopt/reject decision. This is research, not a wizard implementation
  commitment or an embedded-UI scope change (SB-14). The quick-start must stand alone even if the
  wizard is rejected; the research lives here, not in the release-only tracker.

## Implementation Dependencies

The 2026-09-15 proposal's tracker split is complete; the work above remains open unless checked.
Keep High shutdown-safety work ahead of cleanup. The suggested test progression is VM-8 fixtures,
Kind fixture/safety work and TEST-2 feasibility, real NetBox coverage, then acceptance for approved
profiles. Talos provisioning follows VM-8; TalosShutdown follows deterministic Talos bring-up.
This is dependency guidance, not a requirement to serialize independent component work.
ENG-1 begins with its stable-NUT gate and preserves F-97's separate investigation. MOD-4 is a
distinct managed-NUT profile, not an implicit expansion of MOD-3. Coordinate F-132/ENG-3/ENG-4 on
execution ownership before deleting resume state, and complete REL-5 before evaluating ENG-10.

---

## Validation Gates

See the [v1 release validation gates](tasks-v1-release.md#validation-gates).
