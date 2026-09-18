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

**New task prefix:** `MOD` (modular deployment).

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

**New task prefix:** `IN` (inventory).

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/contributing/design/inventory-provider-contract.md` (`IN-n`).

Completed integration work and real-service evidence are recorded in
[completed tasks](tasks-completed.md#inventory-system). Release-candidate NetBox compatibility
remains part of the [release validation gates](tasks-v1-release.md#validation-gates).

---

### Capability Profiles

**New task prefix:** `CR` (capability resolution).

Owns: the `UPSCapabilityProfile` CRD, `internal/capability` matching, the bundled catalog under
`config/catalog/`, and the device-quirk/aliasing/profile-source design surface. Design docs:
`docs/contributing/design/capability-profiles.md`.

None.

---

### Planning & Execution Logic

**New task prefixes:** `PL` (planning), `EX` (execution), `HK` (shutdown hooks).

Owns: `internal/planner` (pure compile), `internal/executor` (wave execution/evidence),
`internal/kubeactions` (action runner), and `internal/shutdownflow` plus the `ShutdownFlow`
controller wiring that connects them. Design docs: `planner-requirements.md`,
`executor-requirements.md`, `shutdown-flow.md`, `adaptive-execution-tier-pointer.md`,
`settled-questions.md`. Audit: `docs/contributing/audits/planner-code-quality.md`.

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

---

### NUT Server / upsd

**New task prefix:** `NS` (NUT server).

Owns: the `NUTServer` CRD, `internal/controller/nutserver_*.go`, and the
`nut-server` operand image. Audit: `docs/contributing/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`,
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`, `F-85`, `F-124`); relevant findings from `docs/contributing/audits/nut-usage-audit.md`
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`).

Completed supervisor Kind acceptance (`ENG-1`), startup verification (`NS-6`), and readiness
correctness (`NS-1`) are recorded in [completed tasks](tasks-completed.md#nut-server--upsd).
The [dated qualification](contributing/audits/kind-qualification-2026-09-17.md) distinguishes
passing component/spec evidence from failed overall commands. Full-suite and exact-image
promotion remain the separate TEST-3 gate.

Completed runtime-tool packaging (`NS-10`) is recorded in
[completed tasks](tasks-completed.md#nut-server--upsd); the supported tool boundary is in
[the image guide](../images/README.md#nut-server-runtime-tools).

---

### Node Agent / DaemonSet

**New task prefix:** `NA` (node agent).

Owns: the `NodePowerAgent` CRD, `internal/controller/nodepoweragent_*.go`, the `upsmon-agent`
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

**New task prefix:** `OP` (outputs and publishing).

Owns: the published planner artifact contract (compiled plan, dependency graph, waves, explanations,
diagram exports) and the CR-status-as-interface model — the "what gets exported and how" surface.
Design doc: `docs/contributing/design/shutdown-flow.md`, Published Artifacts section (`GP-6`/`GP-7`).

No open work. Communication-ordering artifact completion is in
[tasks-completed.md](tasks-completed.md).

---

### Storage & Audit

**New task prefix:** `SA` (storage and audit).

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/contributing/design/audit-storage-schema.md`.

No open work. Execution/audit ownership separation (`ENG-3`) and database component coverage
(`F-145`) are recorded in [completed tasks](tasks-completed.md#storage--audit).

---

### Operator Maturity & Hardening

**New task prefix:** `OM` (operator maturity).

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/contributing/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

Completed comment cleanup (`ENG-8`) and Kind-cost analysis (`OM-1`) are recorded in
[completed tasks](tasks-completed.md#operator-maturity--hardening). The Kind decision retains
the shared suite and existing safety gates.

Completed extracted-scenario qualification (`TEST-1`) and logical-flow feasibility (`TEST-2`)
are recorded in [completed tasks](tasks-completed.md#operator-maturity--hardening).
TEST-2's focused live pass proves feasibility, not full-suite coexistence or image promotion;
that outstanding acceptance is consolidated under TEST-3 below rather than duplicated.

- [ ] `TEST-3` [Medium; High for mutation isolation] harden Kind reproducibility and kubeconfig
  handling. **Implementation complete; live Kind qualification remains.** The runner now owns a
  private kubeconfig and unique cluster, guards context/cluster UID before mutation, and checks
  original container IDs before deletion. Kind/curl helpers are versioned; pinning policy lives in
  CONTRIBUTING.md. [Component evidence](tasks-completed.md#operator-maturity--hardening).
  **Remaining; Testable now; Conditional:** run the existing full Kind suite on a provisioned
  runner, including the now-proven TEST-2 scenario alongside all existing scenarios. Confirm
  policy-enforcing CNI setup, exact promoted-image coverage, unchanged external
  kubeconfig/context, and successful owned teardown. **Live cancellation passed (2026-09-17):**
  `make test-kind-lifecycle` verified partial-startup and post-API SIGTERM, owned-node removal,
  private-state cleanup, preservation of pre-existing containers, and unchanged external kubeconfigs.
  This supplements, rather than replaces, the remaining full-suite and exact-image gate.
  **Host prerequisite resolved (2026-09-17):** the existing inotify preflight passes at 512
  instances; the guardrail is unchanged. Live qualification remains open. Preserve shared-suite and
  required-check semantics; coordinate with TEST-1 and retain OM-1's shared-suite decision.
  Hadron remains a separate harness.

### v1 Release Readiness

**New task prefix:** `REL` (release readiness).

Release tasks and acceptance gates live in [tasks-v1-release.md](tasks-v1-release.md).

### VM Test Coverage

**New task prefix:** `VM` (virtual-machine testing).

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
  library until varied scenarios justify it. Deliberately deferred until a third guest adapter makes
  the overlap self-evident (`golangci-lint`'s own `dupl` check does not flag the current small
  duplication across `test/hadron` and `test/talos`).
  [Detailed criteria](contributing/audits/vm-test-research-2026-09-15.md#vm-8).

VM-3 and VM-7 are closed; see [completed tasks](tasks-completed.md#vm-test-coverage).

- [ ] `VM-2` [High] finish the two-guest Hadron/k3s harness's identity and isolation safety.
  The real two-node join itself is closed: a genuine k3s server/agent pair over the `ClusterLink`
  segment, confirmed by listing two distinct Ready nodes from outside both guests. Remaining:
  refuse mismatched cluster/VM/node identity before mutation; verify port-collision handling and
  concurrent-run isolation, partial/never-started VM cleanup, process ownership before deleting
  state, and bounded cancellation. Run live cancellation rehearsal.
  **Testable now; Conditional:** preserve external workflow deadlines for PEG/seed tooling and
  reserve cleanup margin; runner loss cannot guarantee cleanup. Six GiB combined RAM remains an
  estimate, not a demonstrated minimum. Keep Kind helpers and make test-e2e separate.
  [Evidence and full safety criteria](contributing/audits/vm-test-research-2026-09-15.md#vm-2).
- [ ] `VM-4` [Medium] finish production outage-to-halt acceptance: live workload drain and
  guest-initiated power-off in the two-guest topology. Prove survivor availability, current
  authorization/release evidence, enforced network policy, and audit results. Measure capacity
  before adding another worker.
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

**New task prefix:** `TT` (telemetry and triggers).

Owns: NUT protocol polling (`internal/nut`), normalization (`internal/telemetry`), poll composition
(`internal/polling`), and trigger evaluation (`internal/trigger`). Design docs:
`telemetry-and-triggers.md`, `resiliency-and-partitions.md`.

None.

---

### Foundation & Documentation

**New task prefix:** `FD` (foundation and documentation).

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
Kind fixture/safety work and full-suite acceptance of the proven TEST-2 scenario, then acceptance
for approved profiles. Talos provisioning follows VM-8; TalosShutdown follows deterministic Talos bring-up.
This is dependency guidance, not a requirement to serialize independent component work.
Completed ENG-1 includes NS-6 startup verification; NS-1 readiness is also complete. Completed OM-1
retains shared Kind setup; controlled measurements apply to concrete optimizations. MOD-4 is a
distinct managed-NUT profile, not an implicit expansion of MOD-3. ENG-4 must preserve the execution
ownership established by F-132/ENG-3 when deleting resume state. Complete REL-5 before evaluating ENG-10.

---

## Validation Gates

See the [v1 release validation gates](tasks-v1-release.md#validation-gates).
