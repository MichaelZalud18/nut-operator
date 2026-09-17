# v1 Release Tasks

This tracker owns v1 release readiness, validation gates, publishing mechanics, and release-only
checks. Active engineering work lives in [tasks.md](tasks.md); intentionally deferred work lives in
[tasks-post-v1.md](tasks-post-v1.md). Task IDs and prior evidence are preserved when moved here.
Completed task records live in [tasks-completed.md](tasks-completed.md).

Last reviewed: 2026-09-15 (tracker split; historical validation below was transferred, not rerun).
Refresh dated security and compatibility evidence against the release candidate before signing off.

## Release Readiness

Owns: public naming, branch/PR protections, package lifecycle, and first-release validation.
Complete the [component safety work](tasks.md) and validation gates below before tagging v1. Remote settings,
registry cleanup, and publishing require explicit authorization; this checklist grants none.

- [ ] `REL-1` [Medium] choose and implement a new public project name before the v1 release.
  The product covers topology-aware power orchestration, planning, and execution beyond NUT server
  management; the current name undersells that scope. Agree the name with the maintainer before
  performing repository or registry mutations. Check discoverability, existing project/package
  collisions, and naming suitability; retain accurate attribution and the NUT transport dependency.
  **Release coordination:** inventory repository/module paths, image packages, CLI names, manifests,
  labels/annotations, API identities, docs/examples, badges/links, CI permissions, provenance,
  release automation, and branch/PR protections. Decide explicitly which are branding-only changes
  and which require migration; renaming must not silently replace CRDs, strand resources, invalidate
  approvals, or break upgrades. Coordinate with `F-112` before the first tagged release.
  **Acceptance:** an agreed naming/migration plan, updated generated distribution artifacts and
  public references, compatibility or documented migration for existing installs, and verified
  build/install/upgrade/promotion paths. Check remote redirects, package access, and protections
  after separately authorized remote changes. Name selection does not authorize publishing a rename.

- [ ] `REL-2` [High] enable and verify branch/PR protections before release. Protection was
  deliberately deferred during development; inspect actual remote settings and current CI results
  rather than assuming either is ready. Select required checks, review/bypass rules, and force-push
  and deletion safeguards. Verify required checks report for supported PR paths, including docs-only
  changes, without leaving merges permanently pending. Coordinate repository naming with `REL-1`.

- [ ] `REL-3` [Medium] define and verify GHCR retention for production image packages. Refresh the
  package inventory; the 2026-08-25 review found extensive untagged accumulation. Preview deletions
  and protect release/promotion/rollback digests and referenced attestations and multi-platform
  manifests. Untagged does not automatically mean unused. Verify cleanup against the final names.
- [ ] `REL-4` [Low] investigate and retire the manually published legacy image tag identified on
  2026-07-31. Confirm its current identity and consumers before deletion; remove only the approved
  obsolete reference and preserve shared digests needed by supported tags or installations.
- [ ] `F-112` [High] run and verify the first `v*.*.*` release through the existing tag-promotion workflow.
  Local upgrade coverage now checks CRD/deployment reapply plus manager replacement over an existing
  resource. True previous-release schema compatibility starts after there is a previous released API
  to install.

- [ ] `REL-5` [Medium] qualify the documented two-UPS quick start on a disposable cluster.
  The [copyable example](examples/quickstart/README.md), three-domain guide, schema/component
  coverage, and owned Kind install-to-plan spec are implemented; see the
  [completed implementation slice](tasks-completed.md#release-readiness).
  **Remaining acceptance:** run the Kind quickstart spec through the owned harness on a suitable
  host and verify actual installer/admission, NUT polling/profile match, agent coverage/readiness,
  and the documented published waves. Retain the three-domain UX and explicit separate opt-in to
  real actuation. Local preflight on 2026-09-15 blocked cluster creation at 128 inotify instances
  (minimum 512); component/schema passes do not close this live gate. Do not lower the host guard.

## Qualification

Owns: remaining release-facing evidence and public test documentation. Building the component
suites and VM/CI harnesses remains in [engineering tasks](tasks.md). OD-27's evidence target does
not settle the separate, still-open decision below about making a physical plug-pull a v1 gate.

- [ ] `OD-27` [Medium] confirm the reserve and minimum-compression defaults against a real outage.
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

- [ ] `VM-6` [Low] prepare a public-safe Hadron test guide once the harness contract is established.
  Separate portable test instructions from local deployment material; remove private paths,
  hostnames, addresses, credentials, and operational history. Document measured versus estimated
  resource needs, isolation and shutdown safeguards, Kind/Talos boundaries, and conditional CI
  behavior. Review and scan before deciding whether to migrate the draft into contributor docs;
  this task does not publish the draft or assert that compatibility tests have passed.
  Cover the generic fixture and guest-specific Hadron/Talos adapters as they become qualified;
  keep estimates and proposed support distinct from measured resource use and test evidence.

## Validation Gates

Testability labels are defined in [tasks.md](tasks.md). Dated scan/test results below are historical
evidence, not current release sign-off.

- Pure packages pass deterministic unit tests without Kubernetes, NUT, PostgreSQL, or filesystem
  dependencies.
- Controller and webhook tests pass against envtest.
- Each modular installation profile selected for v1 has representative profile-level acceptance
  (`MOD-5`), including proof that deliberately omitted components and privileges are unnecessary.
  Investigations do not become release gates until the supported profile is selected.
- If NetBox is advertised as a supported v1 integration, the disposable real-service compatibility
  suite (`TEST-4`) passes for the declared supported version; ordinary Kind jobs do not require NetBox.
- Operand image smoke tests prove the packaged NUT binaries, entrypoints, users, root filesystems,
  and network-only defaults.
- Public-readiness scans show no private hostnames, private addresses, credentials, or site-specific
  topology.
  **Follow-up (2026-09-17):** refresh secret-scan triage after the controller/executor extractions
  and hash-compatibility fixtures. The targeted detect-secrets run still flags existing Secret
  object names, diagnostic text, and public hash snapshots outside the TEST changes. Review each
  reported location before adding narrow exceptions; the full repository scan is not signed off.
  The [2026-09-17 hygiene run](https://github.com/MichaelZalud18/nut-operator/actions/runs/35284615638)
  also rejects private-range literals in the Hadron/Talos fixtures and audit. Review these with
  the VM workstream: distinguish required synthetic network constants from private deployment
  data, and keep any justified exceptions narrow. Do not disable the public-data scan.
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
  a disposable Talos VM (`VM-7`) or sacrificial node; Hadron cannot supply it. Distinct from the dry-run gate
  above, not a replacement for it: a dry-run never renders the actuate configuration.
- **Open:** whether a live plug-pull is also a v1 gate. The functional path can be simulated by
  replaying Online/OnBattery/LowBattery and runtime-decay traces through the trigger, planner, and
  executor. A physical plug-pull is **Real-resource** evidence only if the v1 gate is explicitly set
  to require end-to-end hardware confidence.
