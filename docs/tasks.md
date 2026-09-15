# Project Tasks

This is the public v1 engineering and component tracker for `nut-operator`.

Open work is grouped by owning component. Keep rationale in the design docs, settled decisions in
[scope-boundaries.md](contributing/design/scope-boundaries.md), and evidence in
`docs/contributing/audits/`. Completed records live in [tasks-completed.md](tasks-completed.md);
active entries keep the remaining work and link to detailed research or acceptance contracts.
Release readiness and publishing live in [tasks-v1-release.md](tasks-v1-release.md).
Work deliberately deferred beyond v1 lives in [tasks-post-v1.md](tasks-post-v1.md).

Last reviewed: 2026-09-15 (completed work and research extracted; task scope and recorded test
results preserved). Proposed designs are not settled decisions or evidence of implementation.

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

Task IDs follow their owning component/section and established requirement namespace; testing
belongs alongside that component's implementation work. Existing `ENG-*` and `TEST-*` IDs are
legacy references retained for continuity, not prefixes to extend. See the
[component/namespace index](contributing/design/decision-index.md). Severity on cleanup/research
tasks denotes priority; an implementation risk is not evidence that current behavior is defective.

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

- [ ] `MOD-1` [Medium] investigate the minimum agents-only install for existing NUT endpoints.
  Compare standalone operands with a selectively enabled manager; measure dependencies and resource
  cost. Evaluate typed external targets and mutually exclusive Operator/LocalNUT authority.
  LocalNUT requires explicit OD-37/SB-3 and multi-supply F-45 decisions, never automatic fallback.
  Preserve approval, dry-run, stale/wrong-node rejection, and credential/privilege separation.
  **Testable now:** isolated render/startup and real-NUT-to-Simulate experiments.
  [Research, alternatives, and acceptance](contributing/design/modular-deployment-proposals.md#mod-1).
- [ ] `MOD-2` [Medium] verify mixed built-in and custom host actuation using authored inventory,
  ShutdownFlow, and ShutdownHook before adding a new actuator API. Demonstrate explicit host/group
  targeting without fabricated Kubernetes Nodes or an assumed automatic RunHook fanout.
  **Testable now:** fake receiver and public example covering auth/allowlisting, rehearsal/dry-run,
  repeat safety, timeouts, and ordering. Preserve advisory delivery, distinct from confirmed halt.
  [Research and detailed acceptance](contributing/design/modular-deployment-proposals.md#mod-2).
- [ ] `MOD-3` [Medium] investigate aggregation with external planning, separating aggregation-only
  from aggregation-plus-agents. Reuse MOD-1's dependency comparison; identify a supported telemetry
  and authorized execution boundary without clients synthesizing plans or writing halt Secrets.
  **Testable now:** startup without planning and request-contract tests for approval, targeting,
  stale requests, and cancellation. Deliver a capability matrix and scoped implementation proposal.
  [Research and detailed acceptance](contributing/design/modular-deployment-proposals.md#mod-3).
- [ ] `MOD-4` [Medium] define and implement the managed NUT-only profile for US-4.
  Reuse the manager binary and NUTServer operand; settle the minimal controller/admission set,
  profile-scoped CRDs/RBAC, image default, TLS bootstrap, and explicit client-ingress policy.
  No planner, agents, inventory, PowerManagementCluster, or PostgreSQL dependency; document the
  profile-specific storage exception without changing the full-install contract implicitly.
  **Testable now; Conditional:** clean-cluster real dummy-ups, auth/TLS and allowed/denied traffic,
  device changes, driver isolation, upgrade/reconcile, and absent unrelated watches/permissions.
  [Research, alternatives, and acceptance](contributing/design/modular-deployment-proposals.md#mod-4).
- [ ] `MOD-5` [Medium] add representative acceptance for each approved modular deployment profile.
  Prove intentionally omitted components are unnecessary; reuse existing component/Kind/VM tests
  instead of a component-subset matrix or repeated physical-halt qualification.
  **Testable now once selected; Conditional:** exercise the specific authorization, targeting,
  ordering, failure, and packaging contract of each profile selected for v1.
  [Per-story acceptance criteria](contributing/design/modular-deployment-proposals.md#mod-5).

---

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/contributing/design/inventory-provider-contract.md` (`IN-n`).

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

Remaining default-calibration evidence (`OD-27`) lives in the
[release qualification checklist](tasks-v1-release.md#qualification).

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

- [ ] `ENG-1` [Medium] replace shell driver supervision with Go while preserving singleton upsd,
  the stable sidecar, upstream NUT semantics, and F-144 lifecycle/privilege contracts.
  Start with stable-NUT compatibility verification; use owned foreground processes, serialized
  reconciliation, NUT reload-or-exit decisions, bounded concurrent cleanup, and no extra privileges.
  **Testable now; Conditional:** behavioral parity, unaffected PIDs, malformed config, reload retry,
  child reaping, real-NUT image/race tests, renderer/binary assertions, and F-97 before/after stress.
  Remove shell/embed code only after parity. F-97 needs its own root-cause evidence.
  [Detailed design, migration order, and test matrix](contributing/design/nut-supervisor-migration.md).

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

---

### Node Agent / DaemonSet

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_render.go`, the `upsmon-agent`
and `node-actuator` operand images, `cmd/node-actuator`, `cmd/power-signal-writer`, and
`internal/nodeagent`, plus the operator-side halt evidence in `internal/haltwatch` and
`internal/controller/nodehalt_controller.go`. Design doc:
`docs/contributing/design/node-agent-operand.md` (`NA-n`). Audits:
`docs/contributing/audits/node-agent-daemonset-audit.md` (`F-8`–`F-14`,
`F-33`–`F-36`, `F-54`–`F-92`, `OD-37`) and `operator-maturity-benchmarks.md` (`F-94`).

Completed execution-side safety work (`F-126`, `F-127`, `F-128`) is recorded in
[tasks-completed.md](tasks-completed.md). Remaining guest qualification is owned by VM-3/VM-4/VM-7
below; it does not introduce a new actuator policy.

---

### Outputs & Publishing

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/contributing/design/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

No open work. Communication-ordering artifact completion is in
[tasks-completed.md](tasks-completed.md).

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/contributing/design/audit-storage-schema.md`.

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
- [ ] `ENG-8` [Low] trim audit-history narration from production planner, controller, executor,
  kubeactions, renderer, webhook, and related runtime code. Remove F-number/review chronology and
  prior-bug storytelling only where it does not explain current behavior. Preserve non-obvious
  invariants, protocol constraints, and safety reasoning. Move useful historical detail to existing
  audit documentation or regression tests; do not delete the regression itself.
  **Testable now; Conditional:** comment-only diff review and affected checks; no behavior change.
  Coordinate with ENG-2/ENG-6 and the completed ENG-9 boundary so file extraction and comment
  cleanup do not compete.

- [ ] `F-146` [Medium, investigation] finish controlled Kind setup/cost comparisons before any
  restructuring. Measure focused/full, PR/promotion, cache states, failures/retries/cancellations,
  resource use, and setup/scenario/teardown separately; retain unsuccessful observations.
  The existing single successful trace supports retaining shared setup, not an average or savings
  claim. Compare selective fixtures and component tests against duplicated setup/maintenance costs.
  **Acceptance:** a measured keep/change decision preserving full coverage, exact promoted images,
  network-policy enforcement, cleanup, and required-check semantics. No suite split is approved.
  [Evidence, dependency map, and measurement criteria](contributing/audits/kind-modularity-2026-09-13.md).

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

Owns: the generic PEG/QEMU harness and Hadron/k3s and Talos guest qualification, separate from
Kind and site deployment. Keep logical matrices in component/Kind tests and real guest shutdown
proof at this boundary. Talos is proposed qualification, not a demonstrated pass.
[VM research, decisions, and dated evidence](contributing/audits/vm-test-research-2026-09-15.md)
own detailed prerequisites and prior milestones; the remaining work is below.

- [ ] `VM-8` [Medium] extract shared guest/cluster fixtures from Hadron actuator, manager, and
  UPS-stack scenarios. Own startup/readiness, clients, image import, process evidence, and cleanup;
  keep scenario assertions visible and SSH/Kairos/k3s or Talos provisioning behind guest adapters.
  **Testable now; Conditional:** component failure/cancellation tests and existing live scenarios
  must retain isolation, artifacts, and shutdown-cause checks. Keep Kind separate; defer a standalone
  library until varied scenarios justify it. This fixture boundary precedes VM-7.
  [Detailed criteria](contributing/audits/vm-test-research-2026-09-15.md#vm-8).
- [ ] `VM-7` [Medium bring-up; High shutdown evidence] qualify Talos on the existing VM harness.
  First pin/verify artifacts, provision with supported Talos tooling, reach its API, bootstrap one
  node, fetch kubeconfig, verify Ready from the host, and prove owned teardown with secret-safe logs.
  Then run shipped TalosShutdown: negative signals and revoked/missing approval must leave the guest
  running; positive evidence must distinguish guest shutdown from crash, host kill, or lost access.
  **Testable now; Conditional:** component tests followed by real guest qualification. Preserve
  targeting, privilege boundaries, cleanup, and VM-3 evidence standards; coordinate VM-5/VM-6.
  [Milestones and constraints](contributing/audits/vm-test-research-2026-09-15.md#vm-7).

- [ ] `VM-2` [High] finish the reproducible two-guest Hadron/k3s harness. Single-guest boot,
  host-side kubeconfig access, and basic inter-guest connectivity have dated evidence; they do not
  prove a joined two-node cluster. Establish reliable cluster-link addressing and server/worker join.
  Pin artifacts and images; use private networking, fresh credentials/storage, and explicit context.
  Keep manager, PostgreSQL, and simulated UPS on the survivor; observe outside stopped guests.
  **Testable now; Conditional:** refuse mismatched cluster/VM/node identity before mutation;
  verify port-collision handling and concurrent-run isolation, partial/never-started VM cleanup,
  process ownership before deleting state, and bounded cancellation. Run live cancellation rehearsal.
  Preserve external workflow deadlines for PEG/seed tooling and reserve cleanup margin; runner loss
  cannot guarantee cleanup. Six GiB combined RAM remains an estimate, not a demonstrated minimum.
  Keep Kind helpers and make test-e2e separate.
  [Evidence and full safety criteria](contributing/audits/vm-test-research-2026-09-15.md#vm-2).
- [ ] `VM-3` [High] finish shipped Linux actuator qualification through the real rendered
  DaemonSet/RBAC, including missing/expired/wrong-node signals and absent/revoked approval.
  Prior bare-pod milestones are not full acceptance. Negative cases leave the guest running;
  the approved case halts only its target with host-side shutdown-cause and process evidence.
  **Testable now; Conditional:** negative controls must reject QEMU crashes, forced termination,
  lost SSH, and timeout as successful shutdown. Capture evidence before bounded cleanup and reuse
  it in VM-4; do not broaden privileges or substitute NotReady for actual guest shutdown.
  [Milestone history and evidence contract](contributing/audits/vm-test-research-2026-09-15.md#vm-3).
- [ ] `VM-4` [Medium] finish production outage-to-halt acceptance: real NUT telemetry, trigger,
  planner/executor, live workload drain, operator-produced signal, and guest-initiated power-off.
  Prove survivor availability, current authorization/release evidence, enforced network policy,
  and audit results in the two-guest topology. Measure capacity before adding another worker.
  **Testable now; Conditional:** retain logs/hypervisor evidence outside the guest; manual signal
  injection or a healthy UPS stack is not this end-to-end proof. Reuse VM-3's false-pass controls.
  Harness reset is not operator recovery; restart/resume continuity remains outside SB-1.
  [Prior milestones and remaining acceptance](contributing/audits/vm-test-research-2026-09-15.md#vm-4).
- [ ] `VM-5` [Medium] integrate the proven harness into bounded, initially manual Actions jobs.
  Consume exact-revision immutable images built before guests run; keep minimal token permissions,
  cleanup/time/concurrency bounds, artifact retention, and zero site-secret dependencies.
  **Conditional:** after repeated reliable runs and resource measurements, add component triggers
  for actuation, planning/execution, NUT, policy, packaging, and harness changes; only then consider
  a required check. Reuse the pinned published OS artifact unless customization needs change.
  [Upstream workflow/image-build decisions](contributing/audits/vm-test-research-2026-09-15.md#vm-5).

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
