# Project Tasks

This is the public v1 engineering and component tracker for `nut-operator`.

Open work is grouped by owning component. Keep rationale in the design docs, settled decisions in
[scope-boundaries.md](contributing/design/scope-boundaries.md), and evidence in
`docs/contributing/audits/`. Completed records live in [tasks-completed.md](tasks-completed.md);
active entries keep the remaining work and link to detailed research or acceptance contracts.
Release readiness and publishing live in [tasks-v1-release.md](tasks-v1-release.md).
Work deliberately deferred beyond v1 lives in [tasks-post-v1.md](tasks-post-v1.md).

Closed tasks retain their original scope, dates and evidence. Additional work gets a new task ID
and an explicit follow-up link to the closed task(s); do not reopen or rewrite completed records.
Related requirement IDs are references, not available IDs for new follow-up tasks.

Last reviewed: 2026-09-19 (execution safety, Kind qualification, storage recovery, and
modular/planner publication).

Proposed designs are not settled decisions or evidence of implementation.

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
[US-1 through US-4](contributing/design/user-stories.md).
**Approved v1 scope (2026-09-18):** MOD-2 advisory mixed actuation, MOD-4 managed NUT-only
including telemetry-only consumption, and MOD-5 acceptance for those two contracts.
MOD-1 agents-only and MOD-3 external execution are owned by the
[post-v1 backlog](tasks-post-v1.md#modular-deployment-profiles).
MOD-2, MOD-4 and MOD-5 are recorded in the
[completed tracker](tasks-completed.md#modular-deployment-profiles), including component and
owned Kind acceptance. The NS-11 relay compatibility dependency is also complete for the
supported unauthenticated upstream mode; unsupported auth modes are explicitly rejected.
See the [installation guide](installation/nut-only.md) and
[coverage map](contributing/design/modular-acceptance.md) for the implemented boundaries.

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

Completed planner refactors are recorded in
[completed tasks](tasks-completed.md#planning--execution-logic).

Execution authorization and target-drift integration (`EX-34`) and quorum publication
integration (`EX-35`) are complete; their dated CI evidence is in
[completed tasks](tasks-completed.md#planning--execution-logic).

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
passing component/spec evidence from failed overall commands. Subsequent full-suite and
exact-image promotion qualification is recorded under completed TEST-3.

Completed runtime-tool packaging (`NS-10`) is recorded in
[completed tasks](tasks-completed.md#nut-server--upsd); the supported tool boundary is in
[the image guide](../images/README.md#nut-server-runtime-tools).

Completed upstream relay compatibility (`NS-11`) and generated-config Kind evidence are recorded
in [completed tasks](tasks-completed.md#nut-server--upsd). Authenticated and verified-TLS upstream
relay remain unsupported; see the [relay contract](contributing/design/upstream-nut-relay.md).

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
[tasks-completed.md](tasks-completed.md). Remaining composed outage-to-halt qualification is
owned by VM-4 below; it does not introduce a new actuator policy.
The signal-projection rollout follow-up (`NA-13`) passed component checks and full Kind;
see [its completion record](tasks-completed.md#node-agent--daemonset).

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

Execution/audit ownership separation (`ENG-3`) and database component coverage (`F-145`) are
recorded in [completed tasks](tasks-completed.md#storage--audit).

ExternalPostgres readiness recovery (`SA-1`, follow-up to F-131) is complete; implementation
and passing CI evidence are in [completed tasks](tasks-completed.md#storage--audit).

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
Completed full-suite and exact-image qualification (`TEST-3`) is recorded in
[completed tasks](tasks-completed.md#operator-maturity--hardening), with the
[CI acceptance evidence](contributing/audits/kind-qualification-2026-09-17.md#full-suite-published-image-qualification).
The shared suite, ownership safeguards, and image-promotion gate remain unchanged.

### v1 Release Readiness

**New task prefix:** `REL` (release readiness).

Release tasks and acceptance gates live in [tasks-v1-release.md](tasks-v1-release.md).

### VM Test Coverage

**New task prefix:** `VM` (virtual-machine testing).

Owns: the generic PEG/QEMU harness and Hadron/k3s and Talos guest qualification, separate from
Kind and site deployment. Keep logical matrices in component/Kind tests and real guest shutdown
proof at this boundary.
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
  Includes both Hadron and Talos. Run fast Talos component tests (`-tags=talos`, without
  `talos_smoke`) and `hack/test_talos_cleanup.py` in ordinary CI alongside Hadron's component
  checks; keep real guest runs in the separate bounded VM jobs. Verify the fast suites actually
  execute rather than being omitted by build tags or smoke-test name filters.
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

Setup wizard implementation (`ENG-10`) is deferred to the
[post-v1 backlog](tasks-post-v1.md#foundation--documentation). V1 onboarding uses the standalone
quickstart; remaining first-user validation is `REL-6` in the
[release qualification checklist](tasks-v1-release.md#qualification).

## Implementation Dependencies

The 2026-09-15 proposal's tracker split is complete; the work above remains open unless checked.
Keep High shutdown-safety work ahead of cleanup. VM-2's harness safety supports VM-4's composed
guest acceptance; VM-5 owns Hadron/Talos regression wiring. VM-8 extraction is deferred and does
not block those tasks. EX-34 Kind integration and EX-35 API publication checks are completed;
their evidence does not expand VM-4 into a multi-control-plane topology.
This is dependency guidance, not a requirement to serialize independent component work.
Completed ENG-1 includes NS-6 startup verification; NS-1 readiness is also complete. Completed OM-1
retains shared Kind setup; controlled measurements apply to concrete optimizations. MOD-4 is a
distinct managed-NUT profile, not an implicit expansion of MOD-3. ENG-4 must preserve the execution
ownership established by F-132/ENG-3 when deleting resume state. Completed REL-5 provides the
quickstart baseline; REL-6 walkthrough findings inform post-v1 ENG-10 implementation.

---

## Validation Gates

See the [v1 release validation gates](tasks-v1-release.md#validation-gates).
