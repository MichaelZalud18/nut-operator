# Project Tasks

This is the public v1 implementation tracker for `nut-operator`.

Open work is grouped by owning component. Keep rationale in the design docs, settled decisions in
[scope-boundaries.md](contributing/design/scope-boundaries.md), and evidence in
`docs/contributing/audits/`. Completed work is represented by the implemented docs/code, not repeated
here. Work deliberately deferred beyond v1 lives in [tasks-post-v1.md](tasks-post-v1.md).

Last reviewed: 2026-09-12 (F-133, F-135, F-137, and F-140 fixed and regression-tested; other audit findings retain their recorded review dates).

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

None.

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
  effectful; an already-rendered actuator is not current authorization.
  **Flow-level recheck at every wave: done (2026-09-12).** `Executor.ApprovalChecker`
  (`internal/executor/adaptive.go`), read fresh at each wave boundary and sticky once revoked --
  re-approving mid-flow does not resume enforcement partway through a flow already degraded to
  dry-run, since later waves may depend on ordering or clearance decided while revoked. A failed
  read is treated identically to an explicit revocation (PL-32), never to "still approved." Wired
  in `internal/controller/shutdownflow_execution.go`'s `approvalChecker`, which re-fetches the flow
  through `r.reader()` (the same `APIReader`-bypasses-cache path EX-9 already uses for node
  clearance) rather than trusting the snapshot `Input.Approved` was derived from at execution
  start. Verified two ways: `internal/executor/approval_test.go` (fake-client revocation between
  waves, stickiness across a later "approved again" report, checker-error treated as revocation,
  and the checker skipped entirely once already dry-run) and
  `internal/controller/shutdownflow_approval_test.go` (envtest: the same checker closure re-reads
  a real API server after `spec.mode` changes out from under the flow object it was built from,
  and treats a missing flow as a read failure, not as approved).
  **Still open: the agent-level handoff check.** A second, narrower check than the flow-level one
  above -- confirming the specific agent/node release is still authorized right at the point of
  writing its signal, not just that the whole flow was approved at the start of its wave.
  **Testable now:** agent-only revocation and missing-approval cases, once the
  `internal/controller`-side wiring for per-node agent approval is traced -- not assumed to be the
  same mechanism as flow-level `spec.Mode`. Real-guest cross-check once built: `VM-4` (Hadron VM
  Test Coverage).
- [ ] `F-127` [High] resolve targets at wave start and refresh node clearance, readiness, and telemetry
  before each halt signal. These are currently captured before the whole execution: a newly placed
  Pod can be halted, while a node drained by an earlier wave can remain falsely blocked.
  **Testable now:** placement changes, drain-to-release transitions, and stale-agent simulations.
  Real-guest cross-check once built: `VM-4` (Hadron VM Test Coverage).
- [ ] `F-128` [High] implement the documented control-plane quorum and late-ordering checks
  (`PL-23`, `PL-24`, `EX-18`). A plan currently accepts releasing all three control-plane nodes before
  later API work. **Testable now:** synthetic HA membership, readiness loss between releases,
  unsafe-wave rejection, and an explicitly terminal release after orchestration finishes. Not
  covered by `VM-4` as scoped (one control-plane guest, no quorum to test against); would need a
  three-control-plane Hadron topology beyond `VM-2`'s current one-control-plane/one-worker scope.
- [ ] `F-129` [High] restrict execution to actually eligible power domains. Compilation currently
  scopes against every configured trigger, so one rack's outage also executes a healthy rack's
  groups. Preserve conservative handling of mixed, unknown, and shared membership. **Testable now:**
  two-domain controller-to-executor tests with only one eligible trigger, then both.
- [ ] `F-132` [High] keep long-running executions from monopolizing reconciliation. The entire flow
  runs synchronously on the sole ShutdownFlow worker, delaying other flows and active status
  heartbeats. **Testable now:** two simultaneous flows, a blocked action, ongoing status cadence,
  and cancellation. Preserve per-flow serialization and correct in-process progress reporting;
  restart/resume continuity is not part of this task (SB-1).
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
- [ ] `PL-21` [High] implement communication-path dependencies in v1 planning and execution.
  Combine `carries` paths with the supplying UPS/`feeds` topology so shutdown work accounts for
  losing a switch or other required communication device when its power source expires, even
  when that device is not an actuation target. Include the operator/API and NUT paths needed to
  finish dependent work. This is not gated on switch shutdown or PDU outlet control.
  **Testable now:** synthetic shared and separate UPS domains, dependent nodes across domains,
  constrained runtime on a communication device's supply, and unknown/exempt path coverage;
  assert resulting ordering, timing constraints, and diagnostics without physical switches.
  **Implemented milestone (2026-09-13):** resolved `carries` dependencies now reach the planner;
  domain scoping retains transitive consumers of affected communication carriers, including
  consumers on otherwise unaffected UPS domains. Unknown carrier supply conservatively retains
  consumers and produces `CommunicationPowerDomainUnknown`. Published explanations include
  edge-source and supplying-domain provenance. Component tests cover transitive/cyclic paths,
  unknown and unaffected supplies, deterministic identity, and inventory-to-artifact propagation.
  **Ordering milestone (2026-09-13):** grouped plans derive carrier-node release order through
  transitive paths; linear plans reject conflicting declared order. Combined carrier/dependent
  release actions are rejected. Mixed-domain releases retain their consumers before pruning.
  Derived ordering edges and path provenance reach published graphs, explanations, and diagrams.
  Component tests exercise inventory-to-executor ordering, dependent-action failure, dry-run
  evidence, tier/declared cycles, linear adapters, and iterative domain-scope retention.
  **Runtime milestone (2026-09-13):** timing observations now include the supplying UPS devices
  of modeled communication paths used by the compiled plan. Supplies are reread at each wave;
  only non-online supplies constrain runtime/trust. Unknown or unreadable supply makes the
  budget unknown without stopping the flow or assuming recovery. Feasibility warnings share
  this reduction. Trigger selection and plan hashes remain independent of these live readings.
  Tests cover transitive/shared supplies, partial recovery, untrusted and stale readings,
  consecutive boundary updates, and actual executor compression from a shorter switch supply.
  **Validation:** race-enabled inventory, planner, resolver, shutdownflow, executor, controller
  (including envtest), and webhook suites passed. `make lint` reported zero issues. Execution
  ordering tests use simulated actions and do not claim physical halt evidence.
  **Still open:** cover operator/API and NUT service paths end to end (including work without
  resolved node targets), and publish structured timing constraints and unresolved coverage.
  Runtime currently uses a conservative whole-plan supply envelope; node-less work includes all
  modeled carriers rather than claiming precise service-path coverage. Physical halt
  acknowledgement remains outside the action-ordering contract.

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

- [ ] [Medium] publish v1 communication-ordering artifacts alongside `PL-21`: derived dependencies,
  their communication-path and power-supply provenance, resulting ordering/timing constraints,
  and unresolved-path diagnostics. Publishing topology alone is not the completed ordering feature.
  **Testable now:** deterministic planner artifact and controller-status fixtures matching the
  dependencies actually used by planning; no switch or PDU actuation is required.
  **Implemented (2026-09-13):** derived carrier-node ordering edges, path-source provenance,
  explanations, and diagrams. Timing constraints and complete unresolved-path coverage remain.

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

#### Hadron VM Test Coverage

Owns: portable Kairos Hadron + k3s test infrastructure, VM-boundary acceptance tests, and their
GitHub Actions integration in this development repository. Keep this separate from Kind and
site deployment configuration, and keep its own scope narrow: a real guest kernel proves things a
Kind node (a container, not a VM) cannot -- real `reboot(2)`, a capability that survived the image
build and registry round trip, genuine host PID namespace membership, real kubelet Pod Security
Admission -- but it does not need to, and should not try to, replicate multi-node HA, drain
sequencing, quorum ordering, or policy-enforcement logic Kind already covers cheaply and
repeatably against fakes. Start with one control-plane VM and one disposable worker; 6 GiB
combined guest RAM is an initial estimate, not a measured minimum. No physical UPS is
required. `VM-1` found GitHub-hosted runner KVM feasibility usable; see
`docs/contributing/audits/hadron-vm-1-feasibility-2026-09-11.md`. `VM-2` has verified a real single
Hadron guest boots to a Ready k3s node; the two-node harness, kubeconfig wiring, and `VM-3` through
`VM-6` remain unfinished. A single-guest boot is not guest-boot-under-load or shutdown acceptance
coverage.

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
