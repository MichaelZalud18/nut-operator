# Project Tasks

This is the public v1 implementation tracker for `nut-operator`.

Open work is grouped by owning component. Keep rationale in the design docs, settled decisions in
[scope-boundaries.md](contributing/design/scope-boundaries.md), and evidence in
`docs/contributing/audits/`. Completed work is represented by the implemented docs/code, not repeated
here. Work deliberately deferred beyond v1 lives in [tasks-post-v1.md](tasks-post-v1.md).

Last reviewed: 2026-09-11 (Hadron upstream-tooling checks and priorities; existing audit findings last reviewed 2026-09-05).

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

---

## Components

### Inventory System

Owns: the topology and power-domain data model — `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, `PowerInventoryEdge`, the `internal/inventory` compiler, and the declarative
resolver/adapter that feeds it into reconciliation. Design contract: `docs/contributing/design/inventory-provider-contract.md` (`IN-n`).

- [ ] `F-135` [Medium] enforce NetBox same-origin restrictions on HTTP redirects as well as JSON
  pagination links. The default HTTP client forwards the token on a redirect to HTTP on the same
  hostname at another port. **Testable now:** redirect downgrade, port/subdomain changes, redirect
  loops, and valid same-origin pagination using a fake transport or local HTTP servers.

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

- [ ] `F-126` [High] recheck flow enforcement approval at every wave and independently check agent
  actuation approval immediately before handoff. Revoking approval currently leaves later waves
  effectful; an already-rendered actuator is not current authorization. **Testable now:** fake-client
  revocation between waves, agent-only revocation, missing approval, and read-failure cases.
- [ ] `F-127` [High] resolve targets at wave start and refresh node clearance, readiness, and telemetry
  before each halt signal. These are currently captured before the whole execution: a newly placed
  Pod can be halted, while a node drained by an earlier wave can remain falsely blocked.
  **Testable now:** placement changes, drain-to-release transitions, and stale-agent simulations.
- [ ] `F-128` [High] implement the documented control-plane quorum and late-ordering checks
  (`PL-23`, `PL-24`, `EX-18`). A plan currently accepts releasing all three control-plane nodes before
  later API work. **Testable now:** synthetic HA membership, readiness loss between releases,
  unsafe-wave rejection, and an explicitly terminal release after orchestration finishes.
- [ ] `F-129` [High] restrict execution to actually eligible power domains. Compilation currently
  scopes against every configured trigger, so one rack's outage also executes a healthy rack's
  groups. Preserve conservative handling of mixed, unknown, and shared membership. **Testable now:**
  two-domain controller-to-executor tests with only one eligible trigger, then both.
- [ ] `F-132` [High] keep long-running executions from monopolizing reconciliation. The entire flow
  runs synchronously on the sole ShutdownFlow worker, delaying other flows and active status
  heartbeats. **Testable now:** two simultaneous flows, a blocked action, ongoing status cadence,
  and cancellation. Preserve per-flow serialization and correct in-process progress reporting;
  restart/resume continuity is not part of this task (SB-1).
- [ ] `F-138` [High] enforce Wait deadlines and account for waits in compiled runtime budgets.
  Wait currently sleeps before its group timeout is created, and a one-hour Wait group can compile
  to a zero-second estimate. **Testable now:** injected-clock timeout/compression tests for groups and
  linear steps through the real planner-to-executor adapter, in dry-run and enforce modes.
- [ ] `F-136` [Medium] bind plan identity to actual selectors and reference identities, then query
  history using the newly compiled hash. Changing a workload selector from one application to
  another currently leaves the hash unchanged, and history is initially read using the previous
  status hash. **Testable now:** selector/ref mutation matrices, canonical reorder stability, and
  rejection of history belonging to the previous target set.
- [ ] `F-142` [Medium] honor the accepted abort-policy and `continueOnError` fields, or explicitly
  reject unsupported settings before v1. They currently do not reach execution, so a failure always
  stops the tail, including requested abort notifications. **Testable now:** failure-policy matrices
  through admission, adapter, and executor; preserve settled advisory hook behavior.
- `OD-27` [Medium] confirm the reserve and minimum-compression defaults against a real outage.
  Simulation coverage is done —
  `internal/controller/adaptive_boundary_simulation_test.go` compiles a real plan through
  `planner.CompileWithHistory`, crosses the actual production boundary
  (`shutdownflow.APICompiledWaves` → `executorWavesFromFlow`), and drives it through
  `Executor.Execute` with a genuine multi-reading power curve. It asserts the 20% reserve and 10%
  minimum compression against that real compiled plan's own durations, the "plan does not fit"
  verdict surfacing in both the audit record and the event log, replan behavior as this codebase
  implements it (`PointerState.Ascend` into a second `Execute` that re-descends and is reported as
  re-execution — there is no mid-flow recompilation to test, by design), that execution-history
  samples inform `Plan.GroupEstimates` but never leak into the live wave duration the executor
  compresses against, and a calibration check that the fixed reserve comfortably covers a
  representative synthetic halt-duration sample. See `operator-maturity-benchmarks.md`'s
  2026-09-04 pass for what each test proved. What remains is what always remained: the reserve and
  minimum stand in for a handoff tail and a fitness floor nobody has measured against a real
  outage, and simulation is calibration evidence for that, not a substitute for it.
- `PL-21` communication-path edges stay unwired until a network device can be an actuation target
  (`OD-24` makes switches topological-only). Revisit with PDU outlet control.

---

### NUT Server / upsd

Owns: the `NUTServer` CRD, `internal/controller/nutserver_render.go`/`nutserver_probe.go`, and the
`nut-server` operand image. Audit: `docs/contributing/audits/nutserver-pod-audit.md` (`F-15`–`F-19`, `F-23`,
`F-46`–`F-49`, `F-51`, `F-53`, `F-76`, `F-85`, `F-124`); relevant findings from `docs/contributing/audits/nut-usage-audit.md`
(`F-20`–`F-22`, `F-24`, `F-50`, `OD-36`).

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
- [ ] `F-133` [High] prevent credential Secret keys from overriding reserved driver configuration.
  The renderer merges every Secret key after validating the UPSDevice, allowing a Secret containing
  `driver: usbhid-ups` to bypass the network/simulation driver allowlist. **Testable now:** resolve
  actual Secret fixtures through the renderer, reject reserved keys after merge, and retain valid
  driver-specific credentials without exposing their values in diagnostics.
- [ ] `F-141` [Medium] enqueue NUTServers when externally managed TLS certificate Secrets rotate or
  disappear. The restart digest exists, but the Secret watch maps only device credentials, leaving
  a quiet server on its old certificate until an unrelated reconcile. **Testable now:** reference
  mapping and rotation/deletion envtest cases. **Conditional:** image-level verification that the
  served certificate changes on rollout; no physical UPS required.

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

- Publish communication-ordering artifacts once the planner consumes `carries` ordering (see Planning
  & Execution Logic).

---

### Storage & Audit

Owns: the PostgreSQL audit schema, storage backend resolution, retention, and the shutdown-time
spool. Design doc: `docs/contributing/design/audit-storage-schema.md`.

- [ ] `F-131` [High] make the configured audit spool available when PostgreSQL is already unavailable
  at execution start. Storage-readiness and `OpenAuditStore` failures currently return before the
  executor or spool is reached. Bound history, replay, and audit I/O so stalled storage cannot consume
  the shutdown window. **Testable now:** unavailable/stalled backend at trigger time and mid-execution,
  with spool replay when storage returns; preserve approval gates and report evidence failures
  separately from action outcomes. Durable resume evidence is not an execution requirement (SB-1).
- [ ] `F-137` [Medium] record actual group/action completion timestamps. `completedAt` is initialized
  from `startedAt` and never advanced, so measured action durations are zero and cannot train runtime
  estimates. **Testable now:** injected-clock actions and waits, timeout/failure cases, and an
  audit-history-to-planner round trip with nonzero measured durations.
- [ ] `F-139` [Medium] derive node-release and handoff audit outcomes from actual per-node Secret
  write results. A failed handoff currently records `Accepted=true` and `Released=true` when the
  precomputed checks passed. **Testable now:** failed first write, partial multi-node success, and
  cancellation; distinguish signal publication from independently observed host halt evidence.

---

### Operator Maturity & Hardening

Owns: reconciler correctness, RBAC scope, leader election, metrics infrastructure, and
image/supply-chain hardening. Audit: `docs/contributing/audits/operator-maturity-benchmarks.md` (`F-1`–`F-7`).

- Enable branch protection on `main` at release. Deliberately off during build: every CI check
  exists and passes, and requiring them would only add a merge round-trip to a single-maintainer
  repository that is still changing shape daily. This is a release gate, not a gap — the checks to
  require are already there, so turning it on is a repository-settings change and nothing else.
  Recorded here because this section previously described it as already in place.

- Set a retention policy on the four GHCR packages. About 1,780 of ~2,180 versions carry no tag at
  all as of 2026-08-25 -- superseded digests and attestation layers from 550+ builds -- and nothing
  prunes them.
- Delete the one tag on the `nut-operator` package that is neither `main` nor a digest reference,
  pushed by hand on 2026-07-31. It is the only human-readable tag on a public package that the
  promote job did not put there.
- `F-112` run and verify the first `v*.*.*` release through the existing tag-promotion workflow.
  Local upgrade coverage now checks CRD/deployment reapply plus manager replacement over an existing
  resource. True previous-release schema compatibility starts after there is a previous released API
  to install.
- `F-125` [Low] finish the `stableHash`-panics-on-marshal-failure sweep. Fixed 2026-09-04 for
  `internal/capability` and `internal/resolver` (both already had error-returning callers, so this
  was a direct copy of `F-123`'s fix). Still panics in `internal/inventory` (three call sites,
  two of which need a new error return added to their own function first),
  `internal/shutdownflow/adapter.go` (its caller crosses into the webhook and controller
  packages), and two `internal/controller` helpers (caller graph not yet traced). See
  `operator-maturity-benchmarks.md`'s 2026-09-04 pass for the full breakdown.

#### Hadron VM Test Coverage

Owns: portable Kairos Hadron + k3s test infrastructure, VM-boundary acceptance tests, and their
GitHub Actions integration in this development repository. Keep this separate from Kind and
site deployment configuration. Start with one control-plane VM and one disposable worker;
6 GiB combined guest RAM is an initial estimate, not a measured minimum. No physical UPS is
required. `VM-1` found GitHub-hosted runner KVM feasibility usable; see
`docs/contributing/audits/hadron-vm-1-feasibility-2026-09-11.md`. `VM-2` through `VM-6` below
remain planned tasks, not implemented coverage or new required CI checks.

For these tasks, High denotes isolation or shutdown-evidence risk, Medium denotes feasibility or
integration work, and Low denotes optional tooling or documentation. Evaluating upstream reuse is
also an early implementation priority, not a finding that custom VM code is inherently unsafe.

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
  artifact, kubeconfig wiring, and the teardown/mutation-refusal safety checks below, none of which
  are built yet.
  **High-severity safety checks:** mismatched cluster/VM identity must refuse mutation; partial
  startup and cancellation must clean up only the current run. Bind forwarded management ports to
  loopback (done in `test/hadron`) and prove concurrent runs cannot target or delete each other's
  resources (single-VM uniqueness proven in `test/hadron`'s tests; still needed at the two-node
  harness level).
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
- [ ] `VM-4` [Medium] drive a simulated UPS outage through actual NUT telemetry, trigger evaluation,
  planning, execution, draining, signal delivery, and guest power-off. Assert survivor availability,
  current authorization/release evidence (`F-126`/`F-127`), enforced network policy, and audit results;
  manual signal injection alone is not this end-to-end test. Add a second worker for ordered and
  concurrent release scenarios only after measuring capacity. **Testable now; Conditional:**
  retain logs and hypervisor evidence outside stopped guests; harness-owned reset is not operator
  recovery, and restart/resume continuity remains outside scope (SB-1).
- [ ] `VM-5` [Medium] integrate the proven harness as a separate, initially manually triggered
  Actions job. Consume images built from the exact revision under test, using the existing
  digest-based image workflow where applicable, rather than rebuilding while VMs run or pulling
  an unrelated `main` image. After successful repeated runs, add component-based triggers for
  actuator, planner/executor, NUT integration, policy, packaging, and harness changes. Bound job
  time/concurrency and artifact retention, use minimal token permissions, and require no site
  secrets or access to a persistent private environment. **Conditional:** make it a required
  check only after runner feasibility, resource use, and test reliability are demonstrated.
  **Upstream workflow check [Medium]:** review [Kairos's reusable QEMU workflow](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-qemu-test.yaml)
  for reusable setup and diagnostics, not as a drop-in cluster action. Verify caller checkout/test
  layout, artifact names, runner labels, and required secrets before reuse. Pin adopted actions or
  workflows to reviewed commits; do not execute untrusted PR code with privileged tokens or secrets.
  **Image-build check [Low]:** use pinned published artifacts when they meet the test requirements.
  Only if customization is necessary, evaluate the [current Factory workflow](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-factory.yaml)
  instead of inventing an image pipeline. The [old Factory repository](https://github.com/kairos-io/kairos-factory-action)
  is archived and points to this replacement. Factory builds images, not test clusters; keep image
  creation separate from VM execution and retain the existing checksum/digest requirements.

---

### Telemetry & Triggers

Owns: NUT protocol polling (`internal/nut`), normalization (`internal/telemetry`), poll composition
(`internal/polling`), and trigger evaluation (`internal/trigger`). Design docs:
`telemetry-and-triggers.md`, `resiliency-and-partitions.md`.

- [ ] `F-134` [High] carry the selected NUTServer's TLS policy and trust material into authoritative
  operator telemetry polling. The client currently opens plain TCP and sends `LIST VAR` even when
  the server is configured as TLS Required. **Testable now:** protocol fixtures for STARTTLS,
  configured CA/server-identity verification, required-mode downgrade refusal, explicit disabled
  mode, and bounded handshake failure. **Conditional:** compatibility against the shipped NUT image.
- [ ] `F-140` [Medium] select only UPS devices whose own trigger hold has elapsed. When one device
  satisfies a shared trigger's hold, the evaluator currently selects other matching devices whose
  holds have just started. **Testable now:** staggered device transitions, hold resets, and domain
  selection driven by the resulting eligible-device set.

---

### Foundation & Documentation

Owns: scaffold, docs upkeep, examples, and decision-registry maintenance — glue work not owned by one
component.

- [ ] `VM-6` [Low] prepare a public-safe Hadron test guide once the harness contract is established.
  Separate portable test instructions from local deployment material; remove private paths,
  hostnames, addresses, credentials, and operational history. Document measured versus estimated
  resource needs, isolation and shutdown safeguards, Kind/Talos boundaries, and conditional CI
  behavior. Review and scan before deciding whether to migrate the draft into contributor docs;
  this task does not publish the draft or assert that compatibility tests have passed.

---

## Validation Gates

- Pure packages pass deterministic unit tests without Kubernetes, NUT, PostgreSQL, or filesystem
  dependencies.
- Controller and webhook tests pass against envtest.
- Operand image smoke tests prove the packaged NUT binaries, entrypoints, users, root filesystems,
  and network-only defaults.
- Public-readiness scans show no private hostnames, private addresses, credentials, or site-specific
  topology.
- ASH grype low finding `GO-2026-5932` is tracked and triaged: `golang.org/x/crypto v0.56.0`
  (bumped 2026-09-04; see below) still has no fix version from `go list -m -u`, and `govulncheck`
  confirms it is required but not imported at all -- the OpenPGP package in the current dependency
  graph is `github.com/ProtonMail/go-crypto/openpgp`, not `golang.org/x/crypto/openpgp`. Recheck
  before v1 or when `golang.org/x/crypto` publishes a newer release.
- ASH grype high findings `GHSA-vp52-pcj8-j9qc` (`google.golang.org/grpc`) and `GO-2026-6354`/
  `GO-2026-6355` (`golang.org/x/crypto/ssh`, both DoS-on-deadlocked-channel) were fixed 2026-09-04:
  `go get google.golang.org/grpc@v1.83.2 golang.org/x/crypto@v0.56.0 && go mod tidy`. `govulncheck`
  confirmed neither was ever reachable by this project's own call graph -- both arrive through
  `cmd/node-actuator`'s Talos client -- but ASH scores by version present in the build, not by
  reachability, so a fix version existing was reason enough to take it rather than argue the risk
  down. Full suite (build, vet, lint, `go test ./api/... ./cmd/... ./internal/... ./test/utils`,
  `make security-scan`) reran clean afterward.
- `GO-2026-6094` (`github.com/google/cel-go`, JSON private-field exposure via `NativeTypes`/
  `ParseStructTag`) was found 2026-09-04 by `govulncheck` rather than ASH's grype -- grype's
  database did not carry this advisory as of that pass, which is itself the reason to keep running
  both rather than either alone. Reachable at package level (`cmd` →
  `sigs.k8s.io/controller-runtime/pkg/metrics/filters` → `k8s.io/apiserver/pkg/authorization/cel` →
  `github.com/google/cel-go/cel`, controller-runtime's metrics-endpoint authorization filter) but
  not at the symbol level -- the vulnerable functions were never called. `go get
  github.com/google/cel-go@v0.30.0` was first tried directly and rejected: the module was
  requested at a version still pinned to `github.com/google/cel-go@v0.29.0` by
  `k8s.io/apiserver@v0.36.0`, and MVS would not move it alone. Fixed 2026-09-04 by bumping the
  whole `k8s.io/*` API family together (`k8s.io/api`, `apiextensions-apiserver`, `apimachinery`,
  `apiserver`, `client-go` v0.36.0 → v0.37.0, `sigs.k8s.io/controller-runtime` v0.24.1 → v0.25.0),
  which raised cel-go to v0.29.2, then `go get github.com/google/cel-go@v0.30.0` directly --
  `github.com/google/cel-go` is not renamed to `cel.dev/cel-go` until some version past 0.30.0, so
  no path-rename migration was needed to reach the fixed version, contrary to what a first pass at
  this assumed. Full suite (build, vet, lint, `go test ./api/... ./cmd/... ./internal/...
  ./test/utils`, `make manifests generate` with no diff, `make security-scan`) reran clean on the
  bumped versions, including the envtest-backed `internal/controller` and
  `internal/webhook/v1alpha1` suites against the existing cached kubebuilder-assets binaries
  (1.34–1.36), which the client-library bump did not require reprovisioning.
- Alpha deployments run in dry-run by default and expose compiled plans, telemetry status, audit
  records, and approval-gate state before any host action is possible.
- Day-to-day operation works with CRDs, GitOps, `kubectl`, Events, logs, and audit records; no
  embedded dashboard is required for v1.
- Simulated dry-run coverage replays UPS telemetry traces and synthetic runtime decay through
  trigger evaluation, planner compilation, status publication, and audit recording. Testability:
  **Testable now** with unit/component tests plus Kind or k3s runs using `dummy-ups`, `snmpsim`, and
  recorded NUT variable traces. A real UPS dry-run in a real cluster is **Real-resource** confidence
  evidence for a specific environment, not the primary v1 correctness proof.
- One node halted through a real actuator policy. Component coverage should prove approval gates,
  signal validation, stale-signal rejection, rendered security context, Linux syscall wrapper
  behavior, and Talos client request construction. Testability: **Testable now; Conditional** for
  Linux guest shutdown through a disposable Hadron VM (`VM-3`), with hypervisor-confirmed power-off;
  a physical machine is not required to prove that OS boundary. Physical firmware/power behavior
  remains **Real-resource** qualification. `make verify-actuation` exercises signal-to-halt behavior,
  not the complete trigger/planner path (`VM-4`). Talos needs separate `TalosShutdown` proof against
  a disposable Talos VM or sacrificial node; Hadron cannot supply it. Distinct from the dry-run gate
  above, not a replacement for it: a dry-run never renders the actuate configuration.
- **Open:** whether a live plug-pull is also a v1 gate. The functional path can be simulated by
  replaying Online/OnBattery/LowBattery and runtime-decay traces through the trigger, planner, and
  executor. A physical plug-pull is **Real-resource** evidence only if the v1 gate is explicitly set
  to require end-to-end hardware confidence.
