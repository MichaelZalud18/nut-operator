# Operator Maturity Benchmarks

Status: living reference. External standards this project measures itself against, and the current
audit position.

Components: Operator Maturity & Hardening.

Re-run this audit at each architecture stage. Benchmark position is a release gate input, not a
retrospective note.

## Benchmark 1 — Operator Capability Levels

The Operator Framework's five-level maturity model. Widely used as the shorthand for "how mature is
this operator," so the project is graded against it whether or not it opts in.

| Level | Meaning | Position |
| --- | --- | --- |
| L1 Basic Install | Provision operands, configuration via CR | Met |
| L2 Seamless Upgrades | Operator and operand version upgrades handled | Partial — no upgrade path exercised, no conversion webhooks |
| L3 Full Lifecycle | Backup, restore, failure recovery | Partial — recovery execution is external subscriber scope; audit durability OD-6 is closed with local spool fallback |
| L4 Deep Insights | Metrics, alerts, log processing, workload analysis | Mostly met for the current v1 operator scope (2026-08-14) — ShutdownFlow, actuator, audit-spool, certificate-expiry, per-UPSDevice telemetry, capability-match, inventory-compiler, and publisher-heartbeat metrics are registered. Alert packaging and external log processing remain deployment concerns. See `docs/metrics.md`. |
| L5 Auto Pilot | Auto-scaling, auto-config tuning, auto-remediation | Out of scope by design (GP-1: non-power triggers excluded) |

Notes specific to this project:

- **L5 is deliberately not a target.** Auto-remediation of node or service health is excluded by
  GP-1 and SB-6. State this in the README so the repo is not graded against a level it declines.
- **L3 is the real gap and the most consequential.** An operator that shuts a cluster down but has
  no defined recovery story sits at L3-incomplete permanently until OD-1 resolves.
- **L4 is now instrumented for the operator-owned surfaces.** See `docs/metrics.md`; packaged
  alerts and external log processing remain deployment concerns.

Check at: every minor release, and before any `v1beta1` promotion.

## Benchmark 2 — Kubebuilder / controller-runtime conventions

Mechanical, checkable, and where most reviewer friction lands.

| Convention | Position |
| --- | --- |
| Idempotent reconcile | **Met (2026-08-04)** — partial-failure convergence tests added for both operand-rendering controllers (`NUTServer`, `NodePowerAgent`; confirmed via grep these are the only two that render Kubernetes child resources). Each seeds a stale, partial operand state, reconciles once and asserts full convergence, then reconciles again and asserts every touched object's `resourceVersion` is unchanged — idempotency, not just no-error. |
| No in-memory state across reconciles | Appears held; planner purity helps |
| Status subresource for observed state only | Met — GP-3 enforces it by design |
| `observedGeneration` tracking | Met — present in all 9 API types and every controller |
| Standard condition types with machine-readable reasons | Mostly met — see audit |
| Finalizers for cleanup | **Met (2026-08-04)** — `NUTServerReconciler`/`NodePowerAgentReconciler` carry finalizers; owner-reference GC for the other 7 was verified never needing one (no Kubernetes child resources rendered). |
| Status writes use `Patch`, not read-modify-write `Update` | **Met (2026-08-04)** — all 9 controllers switched to `Status().Patch(ctx, obj, client.MergeFrom(base))`. Regression-tested, not just converted: `shutdownflow_controller_test.go`'s `resourceVersionRaceInjectingClient` reproduces the exact production race (a write landing between a reconciler's `Get` and its status write) and confirmed both that the old `Update()` pattern fails with the production-observed 409 Conflict, and that `Patch()` doesn't. |
| `RequeueAfter` over sleeps | **Met — confirmed clean.** Zero `time.Sleep` calls in any controller. |
| Leader election | **Met (2026-08-04)** — code default flipped `false` → `true` (`cmd/main.go`), closing the defense-in-depth gap; `config/manager/manager.yaml` already ran with it active via bare `--leader-elect`. `Makefile`'s `run` target now passes `--leader-elect=false` explicitly, since out-of-cluster leader election has no namespace to create its lease in. |

Check at: every controller added, and before release tagging.

## Benchmark 3 — Kubernetes API conventions

`api-conventions.md`. Cheap to satisfy at `v1alpha1`; expensive to retrofit after `v1beta1`.

| Convention | Position |
| --- | --- |
| spec/status separation | Met |
| Optional fields as pointers | Met in sampled types |
| Enum validation on constrained strings | Met — `PowerStorageMode`, `ActuatorPolicy`, others |
| Single storage version per CRD | Met — all 9 CRDs at one version |
| No required fields added post-GA | N/A at alpha; becomes binding at v1beta1 |

Check at: every API change, and as a hard gate before `v1beta1`.

## Benchmark 4 — Project-specific criteria

Not from any external standard. These matter more here than in a typical operator because the
operator's failure mode is an ungraceful cluster shutdown.

- **RBAC minimality.** This operator can stop every workload in the cluster. Its RBAC is a security
  surface, not scaffolding.
- **Leader election behavior during the managed event.** A lease renewal failure mid-shutdown must
  not permit a second operator instance to start a competing flow.
- **Self-liveness posture.** The operator is a tier 0 workload and must not be evicted, drained, or
  probe-restarted by its own flow. **Confirmed unmet 2026-08-04, fixed same day** — see F-30 below.
  The pattern this requires (self-exclusion from orchestrated actions, priority class, PDB) was
  already built and proven correct for `NodePowerAgent` operands (`F-14`, `F-18`); it now also covers
  the controller-manager's own pod and namespace.
- **Degraded-dependency behavior.** Postgres down, NUT unreachable, provider stale — each must
  degrade explicitly and never block power response (SB-11, IN-15, RS-19).

---

# Audit — 2026-08-03

Against commit `00eb3c0`. Static reading only; no cluster runs, no test execution.

## Findings

**F-1 · No finalizers on any controller.** Zero `Finalizer` references outside tests. The operator
creates real operands — DaemonSets, Deployments, Services, ConfigMaps, Secrets, NetworkPolicies.
Deleting a `NodePowerAgent` or `NUTServer` should reliably tear down its operands; without
finalizers this relies entirely on owner references, which do not cover cross-namespace operands or
any external side effect. Highest-value gap in this audit relative to effort.

**F-2 · Leader election defaults to disabled.** `--leader-elect` exists but defaults `false`. For an
operator that can shut down a cluster, two concurrent instances compiling and executing competing
flows is a severe failure mode. `LeaderElectionReleaseOnCancel` is also present but commented out —
worth a deliberate decision rather than an inherited default, since releasing the lease on shutdown
is exactly the behavior that lets a successor take over mid-event.

**F-3 · No custom metrics.** No metrics package, no Prometheus registrations found. This is the
whole of L4 and it is currently empty. Highest-value candidates: compile duration, compile
failures by diagnostic class, plan hash changes, trigger evaluations, wave execution duration,
actuation attempts, degraded-dependency conditions. The `ServiceMonitor` support already noted in
SB-10 has nothing project-specific to scrape.

**F-3 update · all seven highest-value candidates are now registered (2026-08-04).** New
`internal/metrics` package, registered against controller-runtime's own `metrics.Registry` — no new
endpoint or RBAC. The initial "compile failures by diagnostic class" cut landed coarser than the
original wording implied because `internal/shutdownflow` discarded planner diagnostics before they
reached the reconciler. That follow-on is now closed: planner diagnostics travel to the reconciler,
and `compile_total`'s `result` label names the first planner error or warning reason when one exists.
Full contract in `docs/metrics.md`.

**F-4 · Broad write access to core resources.** The operator holds `create;update;patch` on
`configmaps`, `secrets`, `serviceaccounts`, `services`, `namespaces`, and `networkpolicies`, plus
`update;patch` on `deployments`, `statefulsets`, `replicasets`, and `nodes`, plus `pods/eviction`.
Individually justifiable — that is the shutdown mechanism of SB-10 — but the aggregate is
cluster-wide workload control with namespace creation. Two things worth separating: `namespaces`
`create` is hard to justify for a shutdown operator, and `serviceaccounts` write is a privilege
escalation path worth narrowing to specific names.

**F-5 · Argo Workflows RBAC in the operator.** `argoproj.io/workflows` with `create;get;list;watch`.
Not mentioned in any design doc reviewed. Either an undocumented integration or leftover scaffolding
— worth confirming, since it widens the trust boundary.

**F-6 · Condition vocabulary is sound but partly non-standard.** `Ready`, `Degraded`, `Reconciling`,
`Accepted` are conventional. `ExecutionReady` and `TriggerEligible` are bespoke. Bespoke types are
permitted and these are legible, but they should be documented as part of the public API surface
since users will alert on them.

**F-7 · Idempotency unverified.** No test found that reconciles from a partial-failure state and
asserts convergence. Given operand rendering across four image types, this is the most likely place
for silent duplicate-creation bugs.

## Not findings

- `observedGeneration` handling is complete across all nine types and every controller — better
  than typical for this stage.
- Single storage version per CRD across all nine, with no conversion debt accumulated.
- Enum validation is used consistently on constrained fields.
- Status/spec separation holds, reinforced by GP-3.

## Recommended order

1. F-2 leader election default — one-line change, severe failure mode.
2. F-1 finalizers — real correctness gap, moderate effort.
3. F-4/F-5 RBAC narrowing — security surface, cheap to audit, cheaper now than later.
4. F-3 metrics — unlocks all of L4, no architectural risk.
5. F-7 idempotency tests — highest effort, catches the least visible class of bug.

## Re-audit triggers

- Any new controller or CRD.
- Before `v1beta1` promotion (Benchmark 3 becomes binding).
- After OD-1 resolves (determines whether L3 is reachable).
- Before first release with `ActuatorPolicy: SystemdPoweroff` enabled by anyone.

---

# Audit — 2026-08-04

Against commit `9e65976`. Kubernetes/controller-runtime design focus, prompted by the 2026-08-03
audit's own re-audit trigger (several controller/RBAC/watch changes landed this session: `F-28`
CNPG watch gating, `F-29` metrics NetworkPolicy fix, `credentialSecretRef` wiring, `imagePullPolicy`
and entrypoint/readiness fixes). Static reading plus direct `grep`/`go vet` verification of every
claim below — no claim in this section is inferred from the prior audit without being re-checked
against current source.

## Findings

**F-30 · The controller-manager has no protection against its own orchestrated shutdown actions.**
`internal/kubeactions/runner.go`'s `protectedNamespaces` — the mechanism that makes `F-14`
self-exclusion real — resolves only the operand namespaces of every `NodePowerAgent` in the cluster.
It does not include the controller-manager's own install namespace. `evictablePod` (used by
`drainNodes`) skips `Succeeded`/`Failed` pods, mirror pods, and DaemonSet-owned pods — nothing checks
for the manager's own `control-plane: controller-manager` label or its namespace. `scaleWorkloads`
has the identical gap for `ScaleWorkload`. Concretely: a user-authored `ShutdownFlow` group whose
`nodeSelector` matches the node the manager pod happens to be scheduled on (`DrainNodes`), or whose
`namespaceSelector`/`workloadSelector` happens to match the manager's own namespace or Deployment
(`ScaleWorkload`), evicts or scales down the very process executing the flow, mid-execution. This is
exactly the failure mode Benchmark 4 names ("must not be evicted, drained, or probe-restarted by its
own flow") and exactly the pattern already built correctly for operands (`F-14`, `F-18`) — it just
was never turned back on the manager itself. Compounding factors, all confirmed in
`config/manager/manager.yaml`: no `priorityClassName` (every operand the manager renders gets one —
`system-cluster-critical` or `system-node-critical` — the manager itself gets none), no
`PodDisruptionBudget`, and `replicas: 1` with no static PDB manifest anywhere under `config/`
protecting it from voluntary eviction. Highest-value finding in this pass — it is the one gap that
can make the operator take itself out mid-shutdown, which is the single failure mode this project
exists to prevent elsewhere.

**F-31 · `Status().Update()` (read-modify-write) is universal, not a `ShutdownFlow`-specific bug.**
`grep -c "Status().Update("` across all 9 `*_controller.go` files returns exactly 1 in every file;
`Status().Patch(` returns 0 everywhere. The observed consequence for `ShutdownFlow` — a 10h
production log carrying 744 `"the object has been modified"` conflicts — root-causes to the combination of an unpredicated high-frequency watch plus `Update` instead of
`Patch`. What wasn't previously stated: the `Update`-instead-of-`Patch` half of that root cause is
not particular to `ShutdownFlow` — every controller in the codebase has the identical
resourceVersion race latent in it. The other 8 haven't manifested it in logs only because nothing
currently drives their reconcile frequency as high as `UPSDevice` telemetry ticks (5–15s) do for
`ShutdownFlow`. Fixing `Status().Patch()` as a shared pattern (not a one-off `ShutdownFlow` fix)
closes the whole class, not just the one instance that happened to get loud enough to show up in
logs first.

**F-32 · `NodePowerAgent`'s Pod watch is unscoped — no predicate, no cache scoping.**
`nodepoweragent_controller.go`'s `SetupWithManager` calls `Watches(&corev1.Pod{}, ...)` with a map
function that filters by the `power.zalud.io/nodepoweragent` label, but the watch itself has no
`WithPredicates` and `cmd/main.go` configures no `cache.Options.ByObject` scoping for `corev1.Pod{}`
(confirmed: zero matches for `Cache`/`ByObject` in `cmd/main.go`). controller-runtime's default cache
behavior means this caches and receives watch events for *every* Pod in the cluster, not just
DaemonSet-owned agent pods — the label filter runs after the fact, per event, for every pod in every
namespace. Self-limiting (no incorrect behavior, just wasted cache memory and CPU on the map
function) but real overhead at cluster scale, and inconsistent with how deliberately this project
scopes RBAC and predicates everywhere else. Standard fix: `cache.Options{ByObject: map[client.Object]cache.ByObject{&corev1.Pod{}: {Label: labels.SelectorFromSet(...)}}}` scoped to pods carrying the
agent label, or an equivalent field/label selector on the watch itself.

**F-32 update · the "standard fix" above is unsafe in this codebase; fixed with a dedicated cache
instead (2026-08-04).** `cache.Options.ByObject` scopes the manager's one *shared* cache — the same
cache backing `mgr.GetClient()`, which `internal/kubeactions.Runner.evictPodsOnNode` (the real
`DrainNodes` eviction path) reads via `r.Client.List(ctx, &pods)` with no selector at all, filtering
client-side by `pod.Spec.NodeName`. Scoping that shared cache to `NodePowerAgent`-labeled pods would
have made `DrainNodes` silently stop seeing every other pod in the cluster — breaking real node
eviction during an actual shutdown, not a hypothetical. Fixed with a second, independent `cache.Cache`
(`cache.New`, its own label selector, registered via `mgr.Add`, wired in with
`WatchesRawSource(source.Kind(...))`) that never touches the shared cache or client at all. Verified
end-to-end, not just by reading: a new envtest spec starts a real `ctrl.Manager` (`SetupWithManager`
isn't exercised by the existing reconcile-direct tests), lets it reconcile the pre-existing resource on
its own, then creates a labeled Pod with no manual `Reconcile` call and confirms only the dedicated
watch drives `Status.ReadyNodeCount` to 1.

**F-4 update · `serviceaccounts` RBAC breadth is less dangerous than originally scored.** Verified
directly: `corev1.ServiceAccount{}` creation exists only in `nodepoweragent_render.go`, sets
`AutomountServiceAccountToken = false` on both the created ServiceAccount *and* the DaemonSet pod
template it's mounted into (belt and suspenders), and the operator holds no RBAC on
`rolebindings`/`clusterrolebindings` at all — grepped the full `+kubebuilder:rbac` marker set,
neither resource appears. The operator cannot bind any permission to a ServiceAccount it creates,
structurally, not just by current code behavior. The `namespaces` `create`/`update`/`patch` grant
remains the one live, unresolved piece of the original `F-4` finding; the `serviceaccounts` half is
substantially defused.

## Not findings (re-confirmed this pass)

- `F-2`, previously flagged as the top item in "Recommended order" below — corrected 2026-08-04.
  Leader election is active in every real deployment via the manifest's bare arg;
  the code default only matters as defense-in-depth against a future manifest regression.
- `time.Sleep` usage: zero, confirmed by direct grep across `internal/controller`.
- Owner-reference usage (`SetControllerReference`/`SetOwnerReference`): present and scoped correctly
  in the only two controllers that render child Kubernetes resources (`nutserver_render.go`,
  `nodepoweragent_render.go`). The remaining 7 controllers own no Kubernetes child objects, so no
  owner-reference gap exists there — nothing to close.
- Cluster-scoped CRD owning namespaced operands (all 9 CRDs are `+kubebuilder:resource:scope=Cluster`
  while `NUTServer`/`NodePowerAgent` render namespaced Deployments/DaemonSets/Secrets/etc): this is a
  supported Kubernetes garbage-collection pattern, not a gap — a namespaced object may carry an owner
  reference to a cluster-scoped owner. Checked because it looked suspicious at a glance; it isn't.

## Recommended order (supersedes the 2026-08-03 list)

1. **`F-30` manager self-protection** — the operator taking itself down mid-shutdown is a worse
   failure mode than anything else on this list; the pattern to copy (`F-14`/`F-18`) already exists
   in the codebase.
2. `F-1` finalizers — unchanged in priority; RBAC friction confirmed to be zero.
3. `F-31` `Status().Patch()` — moderate effort, closes the whole class instead of patching the one
   loud instance.
4. `F-4` `namespaces create` narrowing — the `serviceaccounts` half no longer needs it; this is the
   remaining scope.
5. `F-3` metrics — unchanged, still unlocks all of L4.
6. `F-32` Pod watch cache scoping — cheap, but lowest blast radius of this list.
7. `F-7` idempotency tests — unchanged, highest effort.

## Re-audit triggers (unchanged from 2026-08-03, still current)

- Any new controller or CRD. **Fired 2026-08-05**: `UPSCapabilityProbe` adds a tenth CRD and a tenth
  controller. Built to the conventions this benchmark already establishes — `observedGeneration`
  tracking, `Status().Patch()` rather than read-modify-write, enum validation on the phase field,
  cluster scope, status-only observed state — but the audit itself has not been re-run against it.
- Before `v1beta1` promotion (Benchmark 3 becomes binding).
- After OD-1 resolves (determines whether L3 is reachable).
- Before first release with `ActuatorPolicy: SystemdPoweroff` enabled by anyone.

## Fixes applied — 2026-08-04 (same day)

Items 1, 2, and 4 from the recommended order above were implemented and verified (build, vet, full
test suite including new specs, `make manifests`, ASH) the same day this audit pass ran. Recorded
here for the audit trail:

- **`F-30` fixed** — `priorityClassName`/PDB added for the manager; `POD_NAMESPACE` downward API wires
  the manager's own namespace into `kubeactions.Runner.protectedNamespaces` alongside the existing
  `F-14` `NodePowerAgent` protection.
- **`F-1` fixed** — finalizers added to `NUTServerReconciler`/`NodePowerAgentReconciler`. Scope
  correction from the original finding: owner-reference GC was verified already correct for the
  rendered child resources (not a gap); the finalizer's real job is making deletion observable via a
  Kubernetes Event, which previously didn't exist at all for either resource.
- **`F-4` fixed** — reserved-namespace rejection (`kube-system`, `kube-public`, `kube-node-lease`,
  `default`) added at both the webhook and controller layers. The `namespaces create` RBAC verb itself
  is unchanged (Kubernetes RBAC can't scope `create` by resource name), which is why this shows as
  "fixed" via input validation rather than "narrowed" via RBAC.

A genuine, unrelated finding surfaced while building a new CI check during this same pass: a real
private IP address (a device from this session's private-repo work) had been committed to this public
repo's test fixtures in an earlier commit (`70bb81f`). Fixed in the working tree; still visible in git
history. The automated check that now prevents a recurrence is `security.yml`'s `private-ip-scan`
job, which greps all tracked files for RFC1918 literals and fails the build on any match. The check
embeds no RFC1918 pattern of its own, so its config cannot become a second leak of what it guards.

## Fixes applied — 2026-08-04 (later same day)

`F-31`, `F-2`, and `F-5` — the three remaining items from the "Recommended order" list above other
than `F-32`/`F-3`/`F-7` — implemented and verified (build, `make lint`, full test suite including a
new regression spec) the same day. Recorded here for the audit trail:

- **`F-31` fixed** — all 9 controllers converted from `Status().Update()` to
  `Status().Patch(ctx, obj, client.MergeFrom(base))`. Not just converted: reproduced the exact
  production race as a new envtest regression test (`resourceVersionRaceInjectingClient`,
  `shutdownflow_controller_test.go`) and confirmed it fails against the old `Update()` pattern with
  the same 409 Conflict from the production log, then passes against the new `Patch()` pattern.
- **`F-2` fixed** — leader-election code default flipped `false` → `true`. Verified the flip doesn't
  regress local development before shipping it: controller-runtime requires an in-cluster-detected
  namespace for leader election, which a `go run` process against kubeconfig doesn't have, so
  `Makefile`'s `run` target now passes `--leader-elect=false` explicitly.
- **`F-5` closed** — added an "RBAC Scope" section to `docs/security.md` documenting the
  `argoproj.io/workflows` grant (`RunWorkflow` executor action, references `WorkflowTemplate`s by
  name, no `workflowtemplates` RBAC) and the `namespaces create` grant (can't be narrowed by name at
  the RBAC layer; closed at the input layer by `F-4` instead) so neither reads as scope creep again.
  Superseded by the `ShutdownHook` rebuild: generated RBAC no longer grants `argoproj.io/workflows`.

Still open: `F-3` (metrics), image signing, and the container-scanner tooling decision — unchanged,
see `docs/tasks.md`.

## Fixes applied — 2026-08-04 (third pass, same day)

`F-32` and `F-7` — the last two items from the original "Recommended order" list — implemented and
verified (build, vet, `make lint`, full test suite across 8 random seeds, `make manifests` with no RBAC
diff, ASH) the same day. Recorded here for the audit trail:

- **`F-32` fixed** — see the "F-32 update" note above: the finding's own suggested fix
  (`cache.Options.ByObject` on the shared manager cache) turned out to be unsafe, since
  `internal/kubeactions.Runner`'s `DrainNodes` eviction path shares that exact cache/client and lists
  Pods with no selector at all. Fixed with a fully independent `cache.Cache` + `WatchesRawSource`
  instead, verified end-to-end against a real, started `ctrl.Manager` in a new envtest spec — the only
  path that exercises `SetupWithManager` at all, since the existing suite calls `Reconcile` directly.
- **`F-7` fixed** — added partial-failure convergence + idempotency tests for both operand-rendering
  controllers (`NUTServer`, `NodePowerAgent` — confirmed via grep these are the only two that render
  Kubernetes child resources, and that every render helper already used `controllerutil.CreateOrUpdate`
  with no raw `Create()` calls, so there was no live duplicate-creation bug, only missing coverage).
  Found and fixed a real envtest gotcha along the way: envtest runs no namespace controller, so a
  `Delete()` in one spec's `AfterEach` never actually finishes, and a later spec creating the
  same-named namespace can fail depending on timing — both new specs use a dedicated, uniquely-named
  namespace instead of relying on that cleanup ever completing.

## Fixes applied — 2026-08-04 (fourth pass, same day)

`F-3` — the last item from the original "Recommended order" list — implemented and verified (build,
vet, `make lint`, full test suite including new collector and reconcile-path tests, `make manifests`
with no RBAC diff, ASH) the same day. Full contract in `docs/metrics.md`; recorded here for the
audit trail:

- **`F-3` fixed** — see the "F-3 update" note above. All seven highest-value candidates the audit
  named are registered: `compile_total`/`compile_duration_seconds`, `plan_hash_changes_total`,
  `trigger_evaluations_total`, `degraded`, `execution_duration_seconds`, and
  `actuator_action_attempts_total`/`actuator_action_duration_seconds`. Instrumented at the impure
  boundary (`internal/controller`, `internal/kubeactions`), not inside the pure `internal/planner` or
  `internal/trigger` packages. Tested at two levels: the `internal/metrics` package's own unit tests
  exercise every collector directly, and delta-based assertions added to existing `runner_test.go`
  and `shutdownflow_controller_test.go` specs prove the real reconcile/action-runner paths actually
  record them — verified order-independent against the rest of each suite across 8 random
  `ginkgo.seed` values, since other specs in both files reuse the same labels against the same global
  collectors.

All items from the original "Recommended order" list in this audit are now closed: `F-2`/`F-4`/`F-5`
(2026-08-03/04), `F-30`/`F-1` (2026-08-04), `F-31`/`F-32`/`F-7`/`F-3` (2026-08-04). Open work remaining
anywhere in Operator Maturity & Hardening is tracked in `docs/tasks.md`: base-image digest pinning,
detached NUT source signature verification, and ASH finding triage. `F-77`'s tested-digest
publication gate closed 2026-08-15.

## Findings — fifth pass, 2026-08-08

Found while verifying the no-cert-manager install path against a live `kind` cluster, not by reading
code. Continues the `F-n` namespace from `F-37`.

**F-38 · The manager crash-looped on every startup: a reconciler was registered twice.**
`cmd/main.go` called `SetupWithManager` on `UPSCapabilityProbeReconciler` in two places — once with
the other controllers, once again after the webhook block, immediately before the
`+kubebuilder:scaffold:builder` marker. controller-runtime rejects the second registration
("controller with name upscapabilityprobe already exists. Controller names must be unique to avoid
multiple controllers reporting the same metric"), `main` calls `os.Exit(1)`, and the pod enters
`CrashLoopBackOff`. The operator could not start in any cluster.

What makes this worth recording is not the mistake but how it reached `main`. Every envtest suite
wires reconcilers individually against its own manager, so the suite proves each controller works
while never executing `main.go`'s wiring. `go build` and `golangci-lint` both pass — duplicate
registration is valid Go. So Tests, Lint, Security Scan, and Images were all green against an
operator that had never successfully started.

The e2e suite did catch it. It runs `make deploy` and waits three minutes for the controller pod to
report Ready (`test/e2e/e2e_test.go`), which is exactly the check this failure trips, and the E2E
Tests workflow was consequently red on `main` for at least two consecutive commits before the fix.
The gap is therefore not a missing test — it is that a red required-signal workflow did not stop
anything. E2E Tests is not configured as a required status check, so `main` accepted commits over a
failing install gate, and the four green badges made the red one easy to read as flaky.

Fixed by removing the duplicate. Guarded by `TestMainRegistersEachReconcilerOnce` in
`cmd/main_wiring_test.go`, which parses `main.go` and fails when any reconciler type appears in more
than one `SetupWithManager` call. It is a source-shape test rather than a behavioral one because the
wiring is inlined in `main()` and needs a real API server to execute; the same approach the inventory
entity-kind drift tests already use. Verified by reintroducing the duplicate and watching the test
fail, then removing it again.

The durable lesson is broader than the guard: an operator whose install has never been exercised
end-to-end in CI has an untested startup path regardless of unit coverage. Tracked in `docs/tasks.md`
as an e2e gap.

## Findings — sixth pass, 2026-08-12

Recorded here rather than in the two NUT audits it was transferred alongside. `F-52` is about what
`docs/images.md` claims of the build, which is supply-chain posture and this document's subject,
not NUT-mechanism fidelity or the `upsd` pod's shape.

**F-52 · `docs/images.md` made four claims the build did not meet, and they were not the same
kind of claim.** Two were stale descriptions of a build that changed underneath them; two were
aspirations written in the present tense.

Stale descriptions, both left behind by `F-39`'s move from distribution packages to a source build:

- `docs/images.md:20` — "The operand Dockerfiles package real Network UPS Tools binaries from
  **pinned distribution packages**." Both operand Dockerfiles now build NUT from source in a
  dedicated `nut-builder` stage: `images/nut-server/Dockerfile:17` and
  `images/upsmon-agent/Dockerfile:29`, each fetching `nut-${NUT_VERSION}.tar.gz` and running
  `./configure`.
- `docs/images.md:22-23` — "`nut-server` **installs `nut`**" and "`upsmon-agent` **installs
  `nut`**". Neither does. The runtime stages `apk add` shared libraries only and copy the built
  tree from the builder stage.

Aspirations stated as fact:

- `docs/images.md:32` — "pinned NUT version and **base image digest**". The NUT version is pinned
  (`ARG NUT_VERSION=2.8.5`, plus the assertion at `images/nut-server/Dockerfile:119-121` that the
  shipped `upsd` reports it and links OpenSSL rather than NSS — a real and unusually good control).
  The base image is not: every stage in both operand Dockerfiles is `FROM alpine:${ALPINE_VERSION}`
  with `ALPINE_VERSION=3.22`, a mutable tag.
- `docs/images.md:33` — "checksum **and signature** verification for NUT source inputs". Only
  checksum. `images/nut-server/Dockerfile:35` and `images/upsmon-agent/Dockerfile:38` both run
  `sha256sum -c` against a pinned `ARG NUT_SHA256`; a search of both files for `gpg`, `gpgv`,
  `.asc`, and `.sig` returns nothing. NUT does publish detached signatures, so this is unimplemented
  rather than unavailable.

The distinction matters for how it gets closed. The first two are corrections — the document should
describe the source build, and doing so is strictly an improvement, since a pinned source build with
a verified checksum and an asserted TLS backend is a **stronger** supply-chain position than the
distribution packages the text still advertises. The last two are real work, and the choice is to do
them or move them into the "Roadmap" framing the same file already uses at `:43-44` for Sigstore
signatures and digest references. Either is defensible; leaving them in the Build Requirements list
is not, because a reader takes that list as describing released images.

One related item found while reading: `docs/images.md:26` stated the driver allowlist including
`powerman-pdu`, which the operand image does not contain. That is `F-50`, recorded in
[nut-usage-audit.md](nut-usage-audit.md) — the allowlist appears in two places and both need the
same correction.

Closed 2026-08-14 by rewriting `docs/images.md` and `docs/security.md` into current controls versus
open release-hardening targets. The docs now describe the NUT source build, sha256 verification, and
OpenSSL assertions as current controls. Base-image digest pinning and detached NUT source signature
verification remain open work in `docs/tasks.md`.

## Findings — seventh pass, 2026-08-14 (CI stage structure)

Raised while asking whether the pipeline has stages that would justify a shell-bearing "dev" image
tier. It does not, and the interesting part is what that costs — which is nothing to do with what is
inside the images.

**F-77 · The published image is never the image `test-e2e` tested.** `.github/workflows/images.yml`
and `.github/workflows/test-e2e.yml` both trigger on `push` to `main` and on `pull_request`, with no
`needs:` edge between them and no shared artifact. They run concurrently.

Two consequences, both verified by reading the workflows and the suite:

- Nothing gates publication on e2e passing. `images.yml` pushes `:main`, `:sha-<short>`, and the
  branch tag on every push to `main` regardless of what `test-e2e` concludes, because it never
  learns the result.
- `test/e2e/e2e_suite_test.go:35-48` builds `example.com/nut-operator:v0.0.1` and the three operand
  images in `BeforeSuite` and loads them into Kind. The suite therefore exercises a different build
  of the same source than the one published — different tags, different digests, and on `main` a
  different platform set, since `images.yml` builds multi-platform there and the PR path builds
  `linux/amd64` only.

This is the same shape as `F-61`: the artifact that was verified is not the artifact that ships. It
is milder, because both are built from one commit by one Dockerfile, so they should be equivalent —
but "should be equivalent" is exactly the assumption `F-61` was resting on, and the `=ep`
crash-loop was found only by running the real image.

The fix does not need stages in the general sense, only one edge: build once, have `test-e2e` pull
that digest instead of building its own, and gate the `:main` tag on the result. The `sha-<short>`
tag already exists to hang it on. It would also make `test-e2e` faster, since it currently rebuilds
two images that compile NUT from source — which is why its timeout is 30 minutes.

The cost is real and should be weighed rather than assumed away: it serializes the e2e run onto the
path to a published `:main`, on a repository where `main` receives only its own maintainer's
commits.

*Closed 2026-08-15.* Built as the paragraph above describes: build once, test that build, then
float the tag.

`images.yml` no longer publishes `main` at build time. The build job emits only immutable references
— `sha-<short>`, plus version tags on `v*` pushes — and a `promote` job applies `main` afterwards
with `docker buildx imagetools create`, which adds a tag to an existing manifest rather than
rebuilding. `main` and `sha-<short>` therefore resolve to one digest by construction, rather than to
two builds that ought to match.

Between them sits the gate. `test-e2e.yml` gained a `workflow_call` trigger taking four image
references, and `images.yml` invokes it with digests resolved from the registry. Its `push` trigger
is gone: a run racing the build workflow cannot gate it, and two independent e2e runs on one commit
is the cost without the benefit. `pull_request` still builds from the checkout, because a PR
publishes nothing there is anything to pull.

Digests are read back with `imagetools inspect` against the `sha-` tag rather than shuttled between
jobs as artifacts. Matrix legs cannot each contribute a distinct job output — they write the same
keys and the last leg wins — and the registry read has a property the artifact hop does not: what
gets promoted is provably what is present under that tag, not a value copied between jobs. The cost
is a reconstructed tag name (`sha-` plus seven characters, matching `docker/metadata-action`'s short
form). If that convention ever changes, the inspect fails on a tag that does not exist, nothing is
promoted, and `main` keeps pointing at the last image that passed. Fail-closed was the requirement.

The suite side is `suiteImages()`/`resolveSuiteImage` in `test/e2e/e2e_suite_test.go`. Each image
carries an environment variable that, when set, supplies a pre-built reference to pull instead of
building. The pulled image is re-tagged to the same local name the suite has always used, so no spec
can tell which path it came in on — a digest honoured by only some specs would test a mixture of two
builds and report success. With nothing set it builds from the checkout, which is what anyone
running `make test-e2e` on a branch wants.

`nut-tls` was added to the promotion's `needs` while the edge was being drawn. It already proved the
two operand images complete a real NUT TLS session; there was no reason for `main` to float past a
failure it had already caught.

Two things this deliberately does not do. Tag pushes (`v*.*.*`) still publish ungated — the finding
is about `main`, e2e only runs on the main branch, and gating releases is a separate question about
release process rather than about the tested-artifact gap. And the snmpsim fixture is not published,
so it builds from the checkout on both paths; it is a simulated SNMP UPS for driver-conformance
specs, not an operand.

The serialization cost the finding named is now paid: a published `:main` waits on the e2e run.
Accepted for the reason given there — this repository's `main` receives only its own maintainer's
commits — and the immutable `sha-` tag is available immediately for anyone who wants the ungated
artifact.

`TestSuiteImageTableIsWellFormed` guards the five-row image table against the copy-paste slip it
invites: two rows sharing a source variable would silently test one image twice and never test the
other, while CI pulled the wrong artifact and reported success. Confirmed by pointing two rows at
one variable and watching it fail.

*Not adopted, and recorded so it is not re-proposed:* a "dev" image tier carrying shells for
testing, with promotion to the locked-down images. For the actuator specifically it would invert the
result it was meant to check — `F-61` measured `CapPrm 0x400000` when the runtime execs the binary
and `0` when a shell in the container does, so a shell-bearing variant of that image answers the
capability question wrongly. More generally it institutionalizes exactly the tested-artifact /
shipped-artifact gap above. `kubectl debug --target` supplies a shell beside a distroless container
without altering it, and only two of the four images are distroless — `nut-server` and
`upsmon-agent` already carry shells because NUT tooling needs them.

**F-78 · The manager image's `HEALTHCHECK` could not fail.** `Dockerfile:59` was
`HEALTHCHECK ... CMD ["/manager", "--version"]`, the same defect `F-64` fixed on `node-actuator` and
`F-46` fixed on `nut-server`: it proves the binary executes and nothing else.

Unlike those two it has no in-image remedy — the base is `gcr.io/distroless/static:nonroot`, so
there is no shell and no HTTP client to reach the manager's own `/healthz`, and the binary exposes
no readiness subcommand. The options are to add one, mirroring `--ready`, or to drop the instruction
outright on the grounds that Kubernetes ignores `HEALTHCHECK` and `config/manager` already renders
real liveness and readiness probes against `/healthz` and `/readyz`. Dropping it is defensible here
in a way it was not for the operands, which are also run directly; the manager is not.

Closed 2026-08-14 by dropping the manager image `HEALTHCHECK` and documenting the
`CKV_DOCKER_2` skip in the Dockerfile. `config/manager` Kubernetes probes remain the manager
readiness contract; directly runnable images keep their meaningful in-container healthchecks.

Non-F-number release-hardening target closed 2026-08-14: `.github/workflows/images.yml` now installs
cosign from a pinned `sigstore/cosign-installer` action and signs non-PR published image digests
after the vulnerability scan using GitHub OIDC keyless signing. `docs/images.md` records the
verification command.

## Findings — supply chain, 2026-08-15

**Base images pinned by digest.** All five Dockerfiles pinned their bases by tag only, so `alpine:3.22`
or `golang:1.26` could change underneath a rebuild. Now pinned to the multi-arch **index** digest, not
a per-platform one — a per-platform digest would break `--platform` builds, which is the mistake this
pin invites. The tag stays beside the digest (`alpine:3.22@sha256:…`) because the digest is what
Docker honours and the tag is what tells a reader which release it is.

The cost is stated rather than assumed away: a pinned digest does not pick up upstream security fixes
on its own. Nothing in this repository bumps them yet, so the published-image Trivy scan is what makes
a stale base fail loudly. Renovate or Dependabot is the obvious follow-up and is deliberately not
added here — it would start opening PRs on the repository, which is the owner's call to make.

**Detached NUT source signature verification.** The build downloaded `nut-2.8.5.tar.gz` and checked a
sha256 pinned in the Dockerfile. That answers "did the bytes arrive intact" and cannot answer "did
upstream publish these bytes", because the sha256 is a value this repository chose by looking at the
same download. Upstream publishes `nut-<version>.tar.gz.sig`, a detached signature; it is now
verified, and both checks stay because they fail on different things.

The signature is only worth anything if the key is pinned — fetching the key alongside the tarball
would let whoever served a bad tarball serve a matching key. So the key is committed at
`images/nut-signing-key.asc`, its primary fingerprint (`B83459F776B90224988F36C0DE0184DA7043DCF7`,
Jim Klimov, the upstream maintainer) is asserted in both Dockerfiles, and the build makes no keyserver
call at all.

Two details that decide whether this is real or decorative:

The check asserts on gpg's **status output**, not its exit code. A bare `gpg --verify` succeeds for
any key in the keyring, so with the key file itself substituted it would pass. This was verified
rather than assumed: signing the genuine tarball with a throwaway key and swapping in that key,
`sha256sum -c` passed, bare `gpg --verify` reported "Good signature from Impostor", and the pinned
`VALIDSIG` assertion rejected it and failed the build. A wrong pinned fingerprint also fails.

The assertion matches `VALIDSIG`'s **last** field, which is the primary key fingerprint. Releases are
signed by a subkey, so pinning the signing subkey would prove less and break on every routine
rotation.

The ARG is named `NUT_SIGNING_FINGERPRINT` rather than `..._KEY_FPR` because BuildKit's
`SecretsUsedInArgOrEnv` check warns on ARG names containing `KEY`. A public key fingerprint is the
opposite of a secret, so the name avoids a warning that would be wrong rather than suppressing one
that is right.

Verified by building all five images and running `docker-smoke-nut-server`, `docker-smoke-upsmon-agent`,
`docker-smoke-node-actuator`, and the `nut-tls` handshake smoke test against the pinned builds.

**The byo-cert install left the manager unable to start.** Found by the `F-77` gate failing, not by
reading code. `hack/webhook-cert.sh` ended with "The manager reloads the serving certificate without a
restart", which is true for rotation and false for first provisioning. The documented order applies the
manager before issuing the certificate — the namespace and Service have to exist for the certificate to
be issued for them — so kubelet cannot mount `webhook-certs` at all and the pod never starts. A
certwatcher cannot watch a file that was never mounted, and kubelet's mount retry backs off to minutes,
which is what the e2e spec was timing out against.

The script now restarts the manager Deployment when it created the serving Secret rather than rotated
it, and only when the Deployment already exists. Rotation is untouched, so no pointless churn: verified
by running the script twice against a live cluster and confirming the restart fires on the first run and
not the second. The closing message was corrected to say which case it describes.

`byo_cert_test.go`'s readiness wait was a bare `Eventually` around `kubectl wait --timeout=2m`, which
gets exactly one attempt — Gomega's default budget is spent by the time the first poll returns. Given
an explicit 5m budget with a 1m inner wait it can actually retry. The signal-handoff spec had a
related gap: it applied a fixture whose kinds all have mutating webhooks without waiting for the
webhook server, and only ever passed because earlier specs happened to give the manager time. Both
specs now pass when run in isolation, which is the property that was missing.

## Findings — finding triage, 2026-08-15

**Actionable ASH findings were counted but never named.** ASH decides pass/fail and prints a
per-scanner count table. It does not print which findings failed it, so a red build said
`grype: 2 critical` and answering "which two" meant downloading the report artifact and reading
SARIF by hand. That is exactly what happened chasing the `golang.org/x/mod` advisories: the count
was visible immediately, the identifiers took an artifact download and a parse.

`hack/ash-triage.py` reads the aggregated results and names every actionable finding — severity,
scanner, rule, location, and the detail line, which for dependency findings carries the package and
version. It writes the same content as a table to `$GITHUB_STEP_SUMMARY`, so the answer is on the
run page rather than inside an artifact. `make security-scan` now runs it after the scan and
preserves the scan's exit status, so the gate is still ASH's verdict and this only makes it legible;
`make security-triage` runs it standalone.

Two properties decide whether a tool like this is worth having.

It must not disagree with the scan it summarizes. "Actionable" here means unsuppressed and at or
above the threshold, which is ASH's own definition, so the script reconciles its extraction against
the per-scanner `actionable_finding_count` ASH reported and exits 2 on a mismatch rather than
picking a side. A triage tool that quietly reports clean while the scan failed is worse than no
tool, because it is believed. Verified by marking every SARIF result suppressed while leaving ASH's
count at two: the script refused to report clean and said why.

It must work on the day it is needed. On a clean repository every branch of the extraction is dead
code, so a mistake would sit undetected until a real finding appeared and then report nothing.
`--selftest` exercises extraction, suppression handling, threshold filtering, the drift check, and
rendering against inline fixtures, and runs in CI before the scan. Confirmed by deleting the
suppression check and watching the selftest fail. The extraction was also run against the real
failing scan from the `F-77` gate and named both `x/mod` advisories that had failed it.

The tool found a real problem in itself on its first run against a live scan: bandit `B108`, a
hardcoded `/tmp/nut-operator-ash-output` default. The path is read and then believed, so a
predictable location under a world-writable directory is somewhere a local process could plant a
report saying everything is fine. Fixed rather than suppressed — the default is now `$ASH_OUTPUT_DIR`
and the flag is required when that is unset, which affects only a bare manual invocation and serves
that better with an error than a guess.

## Findings — repo hygiene gates, 2026-08-15

**Shipped manifests were never checked against the schemas they claim to satisfy.** CRDs are
generated from the Go types; `config/samples/` and `docs/examples/` are hand-written. Nothing
connected the two, so a field could be renamed, retyped, or removed and every shipped example
describing the old shape stayed green. Found by building the check and running it: the PDU sample had
been invalid against its own CRD since the `OD-22`/`F-26` quirk restructure, declaring integer outlet
ids where the schema requires strings.

`make validate-samples` (`hack/validate-samples.py`) now validates every manifest under
`config/samples/` and `docs/examples/` against the generated schemas, via the make target rather than
the script directly so the CRDs are regenerated from the Go types first — validating against
committed CRDs would check samples against a previous commit's schema, which is the failure this
exists to catch.

**The RFC1918 scan sat behind a path filter that excluded what it guards.** It ran in a workflow with
`paths-ignore: docs/**` and `**.md`. The likeliest place for site topology to leak into a public
repository is documentation and examples, so the one commit shape it existed to check was the one
shape that never ran it. `paths-ignore` is workflow-level and not job-level, so the fix was a
workflow: both checks moved into `Repo Hygiene`, which deliberately carries no path filter at all.

**The published installer was stale and nothing compared it to its source.** `dist/install.yaml` is
the documented one-command install and a build artifact that happens to be committed, so it can fall
behind the manifests it is generated from silently. It had: the `ShutdownHook` CRD and its webhooks
were added and the bundle was never rebuilt, so `kubectl apply -f dist/install.yaml` produced a
cluster where `RunHook` had no CRD to read. Build, test, and lint all passed over it because none of
them compares committed bytes to generator output. The `installer-freshness` job in `Repo Hygiene`
now rebuilds and diffs, pinning the image tag to the committed value so it checks manifest staleness
rather than tag drift.
