# Completed Tasks

Components: Cross-cutting.
Audience: contributors.

Historical completion records moved from [active engineering tasks](tasks.md) on 2026-09-15.
Completion dates, scope limits, and validation statements below are preserved as recorded, not
fresh test results. Current behavior is owned by code and design contracts. Open work remains in
[tasks.md](tasks.md), [release tasks](tasks-v1-release.md), or [post-v1 tasks](tasks-post-v1.md).
Move completed entries here with their date and evidence; do not renumber task IDs or duplicate
status across trackers. Historical run output and longer investigations remain in the linked audits.

## Inventory System

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

## Planning & Execution Logic

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
