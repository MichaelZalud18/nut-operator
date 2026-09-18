# Completed Tasks

Components: Cross-cutting.
Audience: contributors.

Historical completion records moved from [active engineering tasks](tasks.md) on 2026-09-15.
Completion dates, scope limits, and validation statements below are preserved as recorded, not
fresh test results. Current behavior is owned by code and design contracts. Open work remains in
[tasks.md](tasks.md), [release tasks](tasks-v1-release.md), or [post-v1 tasks](tasks-post-v1.md).
Move completed entries here with their date and evidence; do not renumber task IDs or duplicate
status across trackers. Historical run output and longer investigations remain in the linked audits.

## Superseded Tasks

Superseded means the task definition was replaced, not that its investigation or fix passed.
Original scope and evidence are retained below; only the replacement entries own active status.

### F-97: NUT Startup And Readiness

**Superseded 2026-09-17:** split into [NS-1 readiness correctness and NS-6 startup verification](tasks.md#nut-server--upsd).
NS-6 is part of ENG-1 acceptance of the redesigned supervisor. The old probe-driven restart
mechanism has been replaced; investigating a current startup defect requires reproducing it
under the new supervisor. The demonstrated readiness risk stays active for v1 under NS-1.

Original entry (historical, not an additional open task):

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
  **2026-09-17 research:** real NUT 2.8.5 reports `RESPONSIVE` for a STOP-confirmed driver
  after a timed-out handshake, while upsd initially serves cached `OL`. The current five-second
  readiness deadline masks the observed seven-second false positive; shorter/partial-response
  variants still need qualification. The Go supervisor no longer restarts on probe misses.
  **High follow-up:** qualify/fix readiness classification separately from capturing the original
  spontaneous startup failure. Preserve the existing timeout; increasing it is not a fix.
  [Pinned upstream analysis, diagnostic, and remaining experiment matrix](contributing/audits/nut-readiness-investigation-2026-09-17.md).

### F-146: Controlled Kind Cost Comparisons

**Superseded 2026-09-17:** replaced by [OM-1 targeted Kind efficiency investigation](tasks.md#operator-maturity--hardening).
Retain the shared suite. The exhaustive comparison matrix is no longer a prerequisite for
closing an investigation; controlled measurements apply to a specific optimization proposal.
This is a scope correction, not completion of the old matrix or deferral beyond v1.

Original entry (historical, not an additional open task):

- `F-146` [Medium, investigation] finish controlled Kind setup/cost comparisons before any
  restructuring. Measure focused/full, PR/promotion, cache states, failures/retries/cancellations,
  resource use, and setup/scenario/teardown separately; retain unsuccessful observations.
  The existing single successful trace supports retaining shared setup, not an average or savings
  claim. Compare selective fixtures and component tests against duplicated setup/maintenance costs.
  **Acceptance:** a measured keep/change decision preserving full coverage, exact promoted images,
  network-policy enforcement, cleanup, and required-check semantics. No suite split is approved.
  **2026-09-17:** retained shared setup. Current failed CI observations stop before E2E;
  the local preflight still rejects 128 inotify instances (512 required). Controlled comparisons
  remain blocked on a provisioned runner, not established by historical aggregate timings.
  The audit now specifies paired inputs, cache evidence, phase/resource records, and retention
  of failed/canceled attempts. This is not a completed measurement gate.
  [Evidence, dependency map, and measurement criteria](contributing/audits/kind-modularity-2026-09-13.md).

## Inventory System

- [x] `TEST-4` [Medium] disposable real-NetBox compatibility suite (2026-09-17).
  `make test-netbox` builds the shipped importer and runs it inside an owned, pinned NetBox
  service with PostgreSQL/Redis on an internal Docker network, no published ports or site inputs.
  Synthetic REST fixtures cover UPS/node/infrastructure metadata, tag filtering, downstream
  power-input identity, physical communication cables, real v1/v2 authentication, and pagination
  where required edges cannot be recovered from the first page. Tests compare deterministic
  snapshots and exact typed CR specs with an independently authored inventory contract.
  Malformed metadata, missing authentication, unmapped secondary supplies, and orphaned nodes
  must fail with empty stdout and specific diagnostics; credentials stay out of output/errors.
  This exposed an importer gap: a valid primary feed could conceal an omitted secondary supply.
  The CLI now rejects `PowerEndpointUnmapped` before either output format is written; a focused
  regression checks both formats and the exact sanitized error.
  **Validated:** the final `make test-netbox` run passed against the pinned real services on a
  Linux arm64 Docker daemon, and independent label queries confirmed no owned containers/network
  remained. Ten Docker-free lifecycle tests, six CR-corruption cases, importer race tests, vet,
  and scoped lint passed. Review fixes strengthened pagination/CR/diagnostic assertions and
  bodyless fixture DELETE requests. The path-filtered NetBox workflow is separate from Kind;
  its GitHub amd64 execution remains release-candidate evidence to collect, not a claimed pass.
  [Fixture contract and dependency sources](../test/netbox/README.md).

- [x] `ENG-9` [Medium] extract the remaining controller inventory/resolution integration
  (2026-09-15). `internal/kubeinventory` owns Kubernetes inventory reads, API conversions,
  device-scoped capability lookup, and the existing inventory/profile validation needed by both
  resolution and reconciliation. Controllers retain metrics/status publication and lifecycle;
  `internal/shutdownflow` owns eligible-scope compilation and history lookup by execution-plan hash.
  `internal/resolver` remains pure, and uncached wave/per-release safety gates remain unchanged.
  The bounded validation extraction does not complete ENG-5's separate admission unification.
  **Validated:** adapter tests cover all seven read failures, cancellation/deadline propagation,
  diagnostic attribution, inventory identity, node roles, power domains, communication provenance,
  list-order determinism, profile fallback, scoped history identity, and full-plan validation before
  pruning. `go test ./api/... ./internal/... ./cmd/... -count=1` passed with local envtest assets.
  Race-enabled kubeinventory/shutdownflow/resolver/planner/executor/controller component tests
  passed (the controller envtest suite ran in the broad non-race pass). Existing F-127/F-128/F-129
  and PL-21 regression tests passed; repository lint reported zero issues. Independent source
  review found no actionable regressions.
  See [the adapter boundary](../internal/kubeinventory/README.md).

## Operator Maturity & Hardening

- [x] `ENG-8` [Low] trim production comment history (2026-09-17).
  Removed audit-number and prior-bug narration from planner, controller/rendering, executor,
  kubeactions, and webhook comments. Retained current invariants, authorization boundaries,
  protocol limitations, requirement references, and regression tests. NUT readiness wording
  describes the flag check without claiming it proves live driver health; NS-1 owns that fix.
  Coordinated the planner comments with the separately staged ENG-2 extraction.
  **Validated:** identical Go token and directive streams across all 37 edited runtime files;
  planner, controller (envtest), executor, kubeactions, and webhook tests passed. Repository lint
  reported zero issues; independent review confirmed preserved safety reasoning and no behavior
  changes. Historical findings remain in the existing audits and completed task records.

- [x] `OM-1` [Low] targeted Kind cost investigation (2026-09-17).
  Rechecked the successful published-image trace at `a1e76b7`, ranked scenario work (590.124s),
  pre-suite command work (about 178.247s), BeforeSuite (47.004s), and AfterSuite (13.313s).
  Retained two pre-E2E failures, a canceled 17m47s Kind attempt, and a pre-Kind cancellation
  separately; an in-progress pipeline is not completion evidence. No average, controlled
  speedup, PR/source-build, resource, or full cache-state claim is made.
  Decision: retain the shared cluster, full coverage, immutable-image promotion, enforced
  network policy, cleanup ownership, required checks, and deadlines. Future optimization
  should first measure scenario convergence/signal latency and pre-suite compilation phases;
  shared image loading is not the dominant measured cost. This closes the scoped investigation,
  not TEST-1/TEST-2/TEST-3 or an optimization implementation.
  [Evidence, ranking, and decision](contributing/audits/kind-modularity-2026-09-13.md#om-1-decision-september-17).

- [x] `TEST-2` candidate fixture/component slice (2026-09-17). Added a shared-Manager scenario
  that drives dummy-ups from Online to OnBattery, observes eligible DryRun non-effects, and
  approves Enforce for ordered scale/drain and operator-published Simulate handoff. PostgreSQL
  audit rows must show targeted effects and non-overlapping action order; signal identity must
  match the completed execution. Untouched workloads and a separately injected expired signal
  provide controls. Positive signal injection is not used. Fixture checks cover admission,
  compiled waves, telemetry sequencing, placement, image identity, negative-control isolation,
  and rejection of invalid audit evidence. Original 22/24 registration fingerprints are preserved
  alongside the new scenario. Checked bounded teardown verifies fixture removal before restoring
  shared scheduling; cleanup failures retain the reservation until owned cluster teardown.
  Tagged race tests, cleanup failure/ordering checks, and tagged lint passed.
  **Scope limit:** live feasibility remains TEST-2 in `tasks.md`;
  component/race passes are not a Kind or guest-shutdown pass.

- [x] `TEST-1` extracted-scenario live qualification (2026-09-17). Two owned three-node Kind
  runs passed upgrade/replacement, metrics, admission/certificates, signal handoff, scripted
  Online/OnBattery/LowBattery telemetry, and SNMP conformance. Fixed metrics-token logging and
  verified the projected-token probe live; fixed shared teardown ordering and observed fixture
  restoration before manager removal on a real failed spec. Both runs removed their owned
  clusters and restored the tracked manager manifest. Race/component regressions, unchanged
  scenario registration, and tagged lint passed. Shared setup and CI remain intact.
  **Scope:** both overall runs failed in the separate TEST-2 candidate and skipped NS-6;
  this closes the extracted scenarios, not those tasks or full-suite/promotion acceptance.
  [Dated evidence and findings](contributing/audits/kind-qualification-2026-09-17.md).

- [x] `TEST-3` live cancellation qualification (2026-09-17). `make test-kind-lifecycle`
  passed both partial-startup and post-API SIGTERM against disposable three-node Kind clusters.
  Each case verified failure exit semantics, removal of captured and cluster-labeled node IDs,
  private-state cleanup, preservation of pre-existing containers, and byte-identical external
  kubeconfigs. The existing acceptance cluster and unrelated Kind cluster survived both cases.
  Full-suite and exact-image acceptance remain tracked separately in the active TEST-3 entry.

- [x] `TEST-3` cancellation qualification harness/component slice (2026-09-17).
  `make test-kind-lifecycle` invokes the shipped owned runner and observes partial-startup and
  API-owner milestones before SIGTERM. It checks captured and cluster-labeled container IDs,
  preservation of pre-existing containers, unchanged external kubeconfigs, failure exit semantics,
  and private-state cleanup. Its observer never deletes Docker resources. Failed attempts retain
  private evidence; repeated cancellation is suppressed during bounded child reaping.
  A manual supplemental workflow provisions the existing host limits and Kind pin; the normal
  E2E workflow also runs cluster-free lifecycle checks without replacing its exact-image gate.
  Review caught and fixed late-created survivors escaping the captured-ID check.
  Existing runner tests now tolerate concurrent `/proc` disappearance and bounded descendant exit
  after direct-child reaping, while still failing for a live survivor. Three repeated runs of
  the real repeated-cancellation regression passed. All 22 runner and 23 lifecycle component
  tests passed; Bandit reported no findings after reviewing intentional subprocess usage.
  Live qualification remains open in TEST-3.

- [x] `TEST-1` implementation/component slice (2026-09-17). Extracted upgrade, metrics,
  webhook/certificate, signal-handoff, scripted dummy-ups, and SNMP scenarios from the manager
  suite into focused files, retaining their registration order and shared install/teardown.
  Shared helpers own manifest application, partial-apply cleanup, dummy UPS/server rendering,
  Ready-pod parsing, and driver-state reads. Scenario manifests and assertions stay visible;
  image references, bounds, network policy, and the separate VM harness are unchanged.
  **Validated:** cluster-free tagged fixture tests and both registration fingerprints captured
  from the original suite (22 normal scenarios, 24 with soak enabled); all 19 Kind ownership
  harness tests passed. Tagged cluster-free race tests passed and tagged lint reported zero
  issues. Five repeated registration runs passed. Registration checks preserve full names, order,
  labels, ordered/serial placement, and enabled state without bypassing the real suite's ownership guard.
  Independent scope and correctness/security reviews passed; added-code safety checks were clean.
  Live focused/full qualification remains in `tasks.md`: the existing host preflight rejects
  128 available inotify instances against 512 required. No live Kind pass is claimed.

- [x] `ENG-5` [Medium] unify static admission/reconciliation validation (2026-09-15).
  `internal/resourcevalidation` owns pure NodePowerAgent and ShutdownFlow rules and shared field
  checks. Admission retains API field errors/warnings and controllers adapt errors to conditions;
  defaulting and fresh execution/publication authorization remain separate. Both boundaries now
  reject agent `Always` pull policies and apply the full static rules to bypassed-admission objects.
  Reserved operand namespaces share one owner; map validation produces stable error ordering.
  **Validated:** 64 raw/defaulted create/update/controller parity cases, non-mutation and
  bypassed-admission rejection conditions, deterministic error ordering, full controller/admission
  suites and race runs. Manifest generation retained identical webhook/CRD/RBAC outputs; lint
  reported zero issues. Independent review found no blocking security or correctness findings.

- [x] `TEST-3` implementation/component slice (2026-09-15). `hack/test-kind.py` owns a unique
  Docker-backed Kind cluster and private temporary kubeconfig, refusing existing-cluster reuse.
  Context/cluster-UID checks precede CNI setup and direct Go suite entry; cleanup deletes only
  verified full node container IDs, stops owned command groups, preserves primary failures, and retains private
  state when cleanup cannot be proved. Standalone name-only setup/deletion refuses to run.
  The shared full suite and promoted-image inputs are preserved. Kind/curl helper versions are
  explicit; CONTRIBUTING.md owns the pinning policy with an AGENTS.md pointer.
  **Validated:** nineteen cluster-free harness tests cover external-config preservation, unique
  identities, partial setup/test failure, cancellation, context/UID mismatch, replacement-node
  refusal, cleanup failure, file permissions, and actual child-process timeout/SIGTERM cleanup.
  Tagged E2E compilation/image-table tests and tagged Go lint passed. Direct Go/CNI invocation
  without owned state fails before mutation. Four Low Bandit subprocess advisories were reviewed
  as intentional shell-free local-tool/test execution and annotated at the exact call/import sites.
  Review identified Medium cleanup races in name-based deletion after identity verification and
  repeated cancellation during process teardown; fixes bind deletion to full IDs and suppress
  repeated signals until owned children are killed/reaped. Replacement-after-check, repeated-signal,
  deadline, and real Make-precedence regressions passed; independent re-review approved both fixes.
  Work and cleanup have separate budgets; runner loss or the outer CI deadline can still prevent cleanup.
  Full Kind and live cancellation acceptance remain in TEST-3; host preflight blocked creation.

- [x] `ENG-6` [Low] split operand rendering into focused same-package files (2026-09-15).
  `nodepoweragent_render.go` and `nutserver_render.go` retain reconciliation orchestration;
  adjacent files own discovery, config/credentials/TLS, policies, workloads, and node-agent
  signal authorization, scheduling, Talos, and readiness/status. Existing shared rendering
  helpers live in `operand_render_helpers.go`. No API, service boundary, or rendering behavior
  changed, and the completed ENG-1 supervisor implementation is preserved.
  **Validated:** token-level comparison preserved all 156 declarations and their attached
  comments. Controller tests passed before extraction and with the race detector afterward,
  including envtest. The full API/internal/command suite passed; repository lint reported zero
  issues, and credential/unsafe-execution pattern scanning found no matches in the moved files.
  Existing regression tests continue to cover owner references, workload security/config,
  rollout hashes and holds, signal revocation, TLS, and readiness. Historical audit references
  remain historical; comment cleanup stays scoped to ENG-8. Independent review confirmed
  declaration/comment preservation and found no security or logic regressions.

- [x] `ENG-7` [Medium] retire Event-only NUTServer and NodePowerAgent finalizers (2026-09-15).
  No external teardown obligation was present. New resources receive no cleanup finalizer;
  live and terminating legacy resources retire only the operator's key through an optimistic
  metadata patch. Foreign finalizers, concurrent metadata, and resource specifications are
  preserved. Admission permits exact retirement on invalid legacy objects without allowing
  unrelated changes; defaulting must preserve that exact cleanup request.
  Kubernetes garbage collection owns rendered workloads, generated credentials, and signal
  Secrets. Shared namespaces and external credential/TLS Secrets remain unowned. Deletion is not
  immediate cancellation of an already projected halt signal; existing authorization and expiry
  semantics remain unchanged. Upgrade/uninstall documentation records the migration and limits.
  **Validated:** component migration/error/conflict tests and controller envtest passed. The
  isolated `make test-operand-deletion` Kind run passed all four focused specs, proving real owned
  resource garbage collection without an operator deletion reconcile, migration of terminating
  objects, and preservation of shared namespaces and an external credential Secret. The runner
  uses a private kubeconfig and removes its test cluster. An intermediate run was invalidated by
  editing the active runner; cleanup succeeded and the fixed runner passed on repeat.
  Broad Go tests passed. Final controller/admission suites, including envtest, passed under the
  race detector; the admission regression exercises Default -> ValidateUpdate on invalid legacy
  resources and rejects mixed changes. Repository lint reported zero issues and added-code
  scanning found no credential assignments or unsafe execution patterns.

## Planning & Execution Logic

- [x] `ENG-4` [Medium] remove unsupported durable executor resume machinery (2026-09-15).
  Removed persisted reconstruction, completed-group skipping, and resume-only audit/executor
  interfaces. New executions start from current observations, not published historical adaptive
  status. Episode deduplication, in-process progress/adaptive transitions, fresh authorization,
  targeting, signal expiry/withdrawal, ordinary audit/history, and worker/storage ownership remain.
  Migration 9 deprecates the old checkpoint table without changing migrations 1-8 or deleting
  existing evidence; legacy spool checkpoint entries remain while supported records replay.
  Scope, executor, adaptive, and schema contracts describe this boundary and archival strategy.
  **Validated:** controller/audit/executor suites and race runs, fresh-state and repeat-safe
  workload shutdown with different execution IDs, legacy spool replay, real PostgreSQL upgrade
  and retention tests, and the broad API/internal/command regression suite. Independent review
  found no blocking security or correctness issues; repository lint reported zero issues.

- [x] `F-132` [High] separate long-running execution from reconciliation (2026-09-15).
  Bounded manager-owned workers serialize each flow, publish running counts/adaptive state through
  reconciliation, retain completion until status succeeds, and cancel/join on deletion, replacement,
  spec changes, and manager shutdown. Atomic cross-flow claims defer conflicts without starting
  effects; disjoint node-only work can proceed concurrently. Audit storage and claims outlive
  overlapped actions and their cancellation cleanup. Fixed an early-return overlap cleanup path
  and normalized trigger episode timestamps to Kubernetes persistence precision. Shared spool
  append/cap checks and snapshot replay preserve evidence across concurrent workers.
  Acceptance covers independent flows beside a blocked action, advancing heartbeats, duplicate
  reconciliation, status-write failure, bounded capacity, cancellation/resource release,
  conflicting execution/retry, audit lifetime, concurrent spooling/replay, blocked rehearsal
  cadence, and overlapped observation-failure cleanup.
  The existing policy tests exercise the asynchronous path. `ENG-3`'s audit orchestration cleanup
  and `ENG-4`'s resume-only removal remain separate; SB-1 and fresh authorization are preserved.
  **Validated:** full `go test ./api/... ./internal/... ./cmd/... -count=1` passed with local envtest
  assets. Race-enabled audit, controller (including real manager/watch/heartbeat/shutdown), and
  executor suites passed. Repeated concurrent-flow and shared-spool race tests passed; repository
  lint reported zero issues. Added-line security scans and independent source review cleared the
  F-132 implementation. Hadron changes remained outside this review.
  See [in-process ownership](contributing/design/executor-requirements.md#in-process-ownership).

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

- [x] `F-142` [Medium] reject unsupported failure-policy settings before v1 (2026-09-13).
  Admission, CRD validation, and pure compilation accept only `HaltAndSurface`, abort `notify: false`,
  and `continueOnError: false`. Normal Notify actions and settled advisory-hook behavior remain
  unchanged. Defaults, samples, generated installers, and the owning design contracts agree.
  The upgrade guide covers explicitly clearing previously defaulted abort notification settings.
  **Validated:** rejection matrices through create/update admission and API-to-planner conversion;
  API-server default/update checks; full API/internal/command race suite and repository lint.

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

## NUT Server / upsd

- [x] `ENG-1` [Medium] supervisor Kind acceptance (2026-09-17). Two owned three-node Kind
  runs passed real Online/OnBattery/LowBattery telemetry and driver replacement within one
  30-second budget: 9.099 and 13.389 seconds, retaining pod/container identity. The enabled
  NS-6 spec below passed with the stable shared-PID sidecar and unchanged privilege boundary.
  Operand API-token mounting is explicitly disabled; creation and drift repair are covered.
  The inotify preflight passed at 512 without weakening its guard. Earlier implementation and
  real-binary parity evidence remain below; the migration contract remains in
  [the supervisor design](contributing/design/nut-supervisor-migration.md).
  **Scope:** local working-tree images include the staged planner refactor. Overall commands
  still had unrelated TEST-2 or component-test failures. This closes the supervisor acceptance
  scope, not TEST-2 or TEST-3 full-suite/exact-image promotion.

- [x] `NS-6` [Medium] redesigned-supervisor startup verification (2026-09-17). The enabled
  current-manager Kind spec collected 86 samples across 663.603 seconds with eight authenticated
  monitors and one intentional replacement/reconnect. Zero probe errors, unchanged driver and
  pod/container identities, no spontaneous exits/restarts, unchanged security, and successful
  fixture/cluster cleanup. Runtime image identities and the failed overall command's separate
  cleanup-component assertions are recorded in the [dated evidence](contributing/audits/kind-qualification-2026-09-17.md).
  Independent evidence review approved scoped closure. The earlier ARM64 Docker observation also
  passed 661 seconds, 326 probes, and an eight-client reconnect scenario through
  `make docker-smoke-nut-startup`; Kind supplies the missing manager/kubelet qualification.
  `make test-e2e-nut-startup` and the manual workflow retain explicit opt-in; ordinary image
  promotion skips the soak. No historical watchdog/root-cause claim or physical UPS evidence
  is implied; [the earlier investigation](contributing/audits/nut-readiness-investigation-2026-09-17.md)
  remains historical context.

- [x] `NS-10` [Low] define and verify the operand's runtime-tool boundary (2026-09-17).
  Removed unsupported auxiliary CLIs, including broken `nutconf`, and unused client/scanner
  libraries from the runtime image instead of adding an unused C++ runtime. Retained server,
  driver control, query, authenticated-monitor, and operator helper binaries; driver allowlist,
  OpenSSL, and runtime privileges are unchanged. The image workflow now checks executable/link
  dependencies, excluded tools, and startup as non-root with read-only root and no capabilities.
  **Validated:** old-image packaging regression failed; rebuilt native ARM64 image
  `sha256:09f76a2e8c15957e5dabd7fe633383c25bb943a01847121fe152a21652091e05` passed packaging,
  supervisor lifecycle/reload, real-driver and protocol readiness, TLS/certificate rotation,
  and a 40-second eight-client/reconnect smoke with retained artifacts and confirmed cleanup.
  A negative shell regression also proves NSS linkage is rejected rather than ignored by `set -e`.
  This short smoke is not NS-6's eleven-minute Kind qualification.

- [x] `NS-1` [High] bounded, reply-backed NUT readiness (2026-09-17).
  Patched upstream PING timeout classification and complete-line framing; retained upstream
  configuration parsing and driver communication. Added a four-second parallel CLI checker with
  bounded output, a documented 64-device fanout ceiling, exact device/response/PID checks, and
  cancellation/reaping. Kubernetes and Docker use the same helper; privileges and supervisor
  restart behavior are unchanged. Updated the NS-1/NS-2/NS-3 contract and image CI regressions.
  **Validated:** original-image false-positive regression failed as expected; intermediate
  framing regressions failed before their fix. Final real-NUT protocol and real dummy-driver
  suites passed, including missing/frozen/partial/delayed replies, valid/malformed fragmentation,
  mixed ordering, retired socket exclusion, recovery, and global deadlines. Supervisor lifecycle
  smoke, broad API/internal/command tests, envtest, targeted race tests, and lint passed.
  [Dated evidence and limits](contributing/audits/nut-readiness-investigation-2026-09-17.md#ns-1-implementation-and-validation).
  ENG-1/NS-6 retain their separate manager/Kind acceptance scope.

- [x] `ENG-1` implementation and local parity milestone (2026-09-15); the overall task remains
  open in the active tracker for Kind acceptance. Replaced shell supervision with the Go
  `nut-driver-supervisor`, invoked directly by the stable sidecar. NUT still owns enumeration,
  foreground startup, and reload-or-exit decisions. An owned-leader SIGUSR1 fallback handles a
  changed driver name whose new PID filename cannot reach the old process. No new configuration
  parser, service, CRD, privileges, or supervisor state files. Process groups, unreaped-leader
  protection, shared shutdown grace, canceled-command joining, and uncertain-reload retries cover
  normal exit, removal, rollback, and cancellation. Removed the shell/embed and replaced source
  checks with behavioral tests, including 15 membership-change cycles and descendant cleanup.
  **Validated:** full API/internal/command tests; controller/envtest and repeated supervisor/CLI
  race tests; repository lint; independent review; offline secret scan; native ARM64 operand build
  and AMD64 binary cross-build. Real-NUT tests cover idle startup, partial failure, failed reload
  retries, live reload, port-driven restart, driver-name replacement, PID preservation, repeated
  crashes, and termination of a frozen driver. Image CI uses the shipped binary in the same harness.
  **Readiness evidence:** shell baseline and Go comparison each completed 60 samples with zero
  probe misses, server failures, or disagreements. One expanded run recorded two server failures
  during the fixture's final forced port restart. Waiting for the restored port's actual data fixed
  that sequencing error; the confirming 60-sample run passed. Failed samples were retained.
  This does not close F-97 or prove Kind/hardware behavior; the Kind host-preflight blocker is
  recorded in the active task.

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

## Outputs & Publishing

- [x] [Medium] publish v1 communication-ordering artifacts alongside `PL-21` (2026-09-13): derived dependencies,
  their communication-path and power-supply provenance, resulting ordering/timing constraints,
  and unresolved-path diagnostics. Publishing topology alone is not the completed ordering feature.
  **Testable now:** deterministic planner artifact and controller-status fixtures matching the
  dependencies actually used by planning; no switch or PDU actuation is required.
  **Implemented:** derived node/shared-service ordering edges, path-source provenance,
  explanations, diagrams, and structured runtime-budget inputs with unknown supplies,
  unresolved actions, and explicit modeled/unmodeled/exempt coverage. API round-trip tests
  verify service references and coverage survive storage; runtime and artifact fixtures agree.

## Storage & Audit

- [x] `ENG-3` [Medium] separate execution ownership from audit recording (2026-09-15).
  The manager-owned `runShutdownFlow` worker owns bounded storage, spool replay/reporting, and
  cleanup. `recordShutdownFlowAudit` only records compilation/decision evidence; the worker
  independently calls `executeShutdownFlow` for accepted flows. Trigger/rehearsal eligibility,
  fresh authorization, claims, and target gates remain in execution. Evidence and execution errors
  are joined without turning write failures into eligibility decisions. Deferred cleanup closes
  the store after execution and overlapped action cleanup, including cancellation and writer-setup
  failure. Configured spool fallback and the no-spool storage-open gate retain SB-11/F-131 behavior;
  resume-only machinery remains ENG-4's separate scope.
  **Validated:** evidence-only recording cannot open/close storage or run actions; a twelve-case
  matrix covers successful execution, write/close failures, cancellation, rejected/ineligible
  flows, no-spool unavailability/connection failure, joined failures, and writer-setup cleanup.
  Focused tests passed ten race-enabled
  repetitions. Controller/envtest, audit, and executor race suites passed, including existing
  concurrent-worker lifetime, cancellation, approval, spool-full/failure/replay regressions.
  The full API/internal/command suite and the disposable real-PostgreSQL suite passed, including
  bounded locked-writer fallback. Lint reported zero issues; targeted credential/unsafe-execution
  scanning found no matches, and independent review found no security or logic regressions.
  Database bounds do not claim stalled-filesystem protection.

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

## VM Test Coverage

- [x] `VM-3` [High] shipped Linux actuator qualification, closed (2026-09-17).
  Bare-pod milestones, the real rendered DaemonSet/RBAC (missing/expired/wrong-node signals,
  absent approval, and the approved case's host-side shutdown-cause and process evidence), and
  revoked approval are all live- or envtest-verified. Revoking an already-approved annotation is
  itself rejected by the same admission gate (`ValidateUpdate` re-runs full admission on every
  update), not a distinct runtime actuator behavior — confirmed with a new envtest case, not
  guessed.
  [Bare-pod evidence](contributing/audits/hadron-vm-3-actuator-2026-09-13.md);
  [DaemonSet/RBAC and revoked-approval evidence](contributing/audits/hadron-vm-3-actuator-daemonset-2026-09-17.md).

- [x] `VM-7` [Medium bring-up; High shutdown evidence] Talos qualification, closed (2026-09-17).
  Bring-up (pinned artifacts, provisioning, reaching the maintenance API, bootstrap, kubeconfig,
  host-side Ready, owned teardown with secret-safe logs) and the TalosShutdown actuator (negative
  signals and revoked/missing approval leave the guest running; positive evidence distinguishes
  guest shutdown from crash, host kill, or lost access) both have live guest evidence across
  repeated runs, with two independent real root causes found and fixed (a maintenance-mode API gap
  in the version probe, and Talos's own default control-plane scheduling taint).
  [Bring-up evidence](contributing/audits/talos-vm-7-bootstrap-2026-09-17.md);
  [Actuator evidence](contributing/audits/talos-vm-7-actuator-2026-09-17.md).

## Release Readiness

- [x] `REL-5` two-UPS quickstart live qualification (2026-09-17). The owned Kind install-to-plan
  spec passed in two independent clusters using the shipped installer and three-domain example,
  actual admission, live NUT polling/profile matching, rendered agent coverage/readiness, and
  documented published waves. Fixture cleanup completed. The inotify guard remained enforced.
  These are local working-tree images, including the pre-existing planner refactor, not a
  release/promotion digest or real-actuation qualification. Unrelated TEST-2 failures kept the
  overall suites red and are recorded separately.
  [Dated evidence](contributing/audits/kind-qualification-2026-09-17.md).

- [x] `REL-5` quick-start implementation/component slice (2026-09-15). The configuration guide
  links a copyable two-UPS simulation with three actual Kubernetes Node bindings and a supporting
  switch. UPS/NUT setup, topology, and shutdown flow remain the three domains; bootstrap/storage
  and DryRun/Simulate node agents are prerequisites. One profile and NUTServer serve both UPS
  devices. Secrets remain references or operator-generated, TLS-off is limited to disposable
  evaluation, and real actuation is a separate opt-in. The guide describes expected waves,
  extending each domain, external-hook limitations, and cleanup.
  Component tests consume the shipped manifests and renderer, validating admission, power domains,
  agent coverage, per-wave tiers/durations, and rejection of invalid or missing node bindings.
  An owned Kind spec covers BYO-certificate installation through telemetry/readiness and plan
  publication. CI explicitly installs the pinned renderer dependency and runs on example changes.
  **Validated:** all 116 sample/example schemas, quickstart component tests including race,
  tagged E2E compilation and race-enabled command lifecycle tests, ordinary/tagged lint,
  Python security scanning, and independent review. Review identified a Medium timeout-cleanup
  issue: killing only the certificate-script shell could leave child commands alive. Commands now
  own process groups, and spec-scoped signal cancellation stays latched through cleanup. Tests
  cover inherited/closed pipes, TERM-resistant descendants, parent SIGTERM/Interrupt, refusal
  to start commands after cancellation, and preservation of independent signal subscribers.
  Live install-to-plan qualification remains in [REL-5](tasks-v1-release.md#release-readiness):
  host preflight blocked creation at 128 inotify instances versus the required 512.
