# Operator Maturity Benchmarks

Status: living reference. External standards this project measures itself against, and the current
audit position.

Components: Operator Maturity & Hardening.
Audience: contributors.

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
| L4 Deep Insights | Metrics, alerts, log processing, workload analysis | Mostly met for the current v1 operator scope (2026-08-14) — ShutdownFlow, actuator, audit-spool, certificate-expiry, per-UPSDevice telemetry, capability-match, inventory-compiler, and publisher-heartbeat metrics are registered. Alert packaging and external log processing remain deployment concerns. See `docs/reference/metrics.md`. |
| L5 Auto Pilot | Auto-scaling, auto-config tuning, auto-remediation | Out of scope by design (GP-1: non-power triggers excluded) |

Notes specific to this project:

- **L5 is deliberately not a target.** Auto-remediation of node or service health is excluded by
  GP-1 and SB-6. State this in the README so the repo is not graded against a level it declines.
- **L3 is the real gap and the most consequential.** An operator that shuts a cluster down but has
  no defined recovery story sits at L3-incomplete permanently until OD-1 resolves.
- **L4 is now instrumented for the operator-owned surfaces.** See `docs/reference/metrics.md`; packaged
  alerts and external log processing remain deployment concerns.

Check at: every minor release, and before any `v1beta1` promotion.

## Benchmark 2 — Kubebuilder / controller-runtime conventions

Mechanical, checkable, and where most reviewer friction lands.

| Convention | Position |
| --- | --- |
| Idempotent reconcile | **Met (2026-08-04)** — partial-failure convergence tests added for both operand-rendering controllers (`NUTServer`, `NodePowerAgent`; confirmed via grep these are the only two that render Kubernetes child resources). Each seeds a stale, partial operand state, reconciles once and asserts full convergence, then reconciles again and asserts every touched object's `resourceVersion` is unchanged — idempotency, not just no-error. |
| No in-memory state across reconciles | Appears held; planner purity helps |
| Status subresource for observed state only | Met — GP-3 enforces it by design |
| `observedGeneration` tracking | Met for every reconciled type. `ShutdownHook` has no field and correctly so: nothing reconciles it — it is watched by `ShutdownFlowReconciler` and validated by a webhook, and the operator holds only `get;list;watch` on it. An `observedGeneration` with no observer would be a field that never moves. Its **declared-but-unwritten status subresource** is the real finding (`F-96`). |
| Standard condition types with machine-readable reasons | Mostly met — see audit |
| Finalizers for cleanup | **Met (2026-08-04)** — `NUTServerReconciler`/`NodePowerAgentReconciler` carry finalizers; owner-reference GC for the other 7 was verified never needing one (no Kubernetes child resources rendered). |
| Status writes use `Patch`, not read-modify-write `Update` | **Met (2026-08-04)** — every controller switched to `Status().Patch(ctx, obj, client.MergeFrom(base))`. Regression-tested, not just converted: `shutdownflow_controller_test.go`'s `resourceVersionRaceInjectingClient` reproduces the exact production race (a write landing between a reconciler's `Get` and its status write) and confirmed both that the old `Update()` pattern fails with the production-observed 409 Conflict, and that `Patch()` doesn't. |
| `RequeueAfter` over sleeps | **Met — confirmed clean.** Zero `time.Sleep` calls in any controller. |
| Leader election | **Met (2026-08-04)** — code default flipped `false` → `true` (`cmd/main.go`), closing the defense-in-depth gap; `config/manager/manager.yaml` already ran with it active via bare `--leader-elect`. `Makefile`'s `run` target now passes `--leader-elect=false` explicitly, since out-of-cluster leader election has no namespace to create its lease in. |
| Generated RBAC covers the calls the code makes | **Met (2026-08-17)** — markers are hand-written and the calls are elsewhere, so the two drifted silently and shipped a `403` on the `OD-38` drain fall-back (`F-93`). `executor_rbac_test.go` now binds the generated `ClusterRole` in envtest and asks the real authorizer, via `SubjectAccessReview`, about every call the executor makes against workloads it does not own. Verified non-vacuous (an unbound user is refused) and verified to fail on the real regression. |

Check at: every controller added, and before release tagging.

## Benchmark 3 — Kubernetes API conventions

`api-conventions.md`. Cheap to satisfy at `v1alpha1`; expensive to retrofit after `v1beta1`.

| Convention | Position |
| --- | --- |
| spec/status separation | Met |
| Optional fields as pointers | Met in sampled types |
| Enum validation on constrained strings | Met — `PowerStorageMode`, `ActuatorPolicy`, others |
| Single storage version per CRD | Met — re-checked 2026-08-17: every CRD serves `v1alpha1` and only `v1alpha1` |
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
Full contract in `docs/reference/metrics.md`.

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
asserts convergence. Given operand rendering across every image type, this is the most likely place
for silent duplicate-creation bugs.

## Not findings

- `observedGeneration` handling is complete across every type and every controller — better
  than typical for this stage.
- Single storage version across every CRD, with no conversion debt accumulated.
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
`grep -c "Status().Update("` across every `*_controller.go` file returns exactly 1 in each;
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
  `nodepoweragent_render.go`). The remaining controllers own no Kubernetes child objects, so no
  owner-reference gap exists there — nothing to close.
- Cluster-scoped CRD owning namespaced operands (every CRD is `+kubebuilder:resource:scope=Cluster`
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

- **`F-31` fixed** — every controller converted from `Status().Update()` to
  `Status().Patch(ctx, obj, client.MergeFrom(base))`. Not just converted: reproduced the exact
  production race as a new envtest regression test (`resourceVersionRaceInjectingClient`,
  `shutdownflow_controller_test.go`) and confirmed it fails against the old `Update()` pattern with
  the same 409 Conflict from the production log, then passes against the new `Patch()` pattern.
- **`F-2` fixed** — leader-election code default flipped `false` → `true`. Verified the flip doesn't
  regress local development before shipping it: controller-runtime requires an in-cluster-detected
  namespace for leader election, which a `go run` process against kubeconfig doesn't have, so
  `Makefile`'s `run` target now passes `--leader-elect=false` explicitly.
- **`F-5` closed** — added an "RBAC Scope" section to `docs/reference/security.md` documenting the
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
with no RBAC diff, ASH) the same day. Full contract in `docs/reference/metrics.md`; recorded here for the
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
`docs/reference/images.md` claims of the build, which is supply-chain posture and this document's subject,
not NUT-mechanism fidelity or the `upsd` pod's shape.

**F-52 · `docs/reference/images.md` made four claims the build did not meet, and they were not the same
kind of claim.** Two were stale descriptions of a build that changed underneath them; two were
aspirations written in the present tense.

Stale descriptions, both left behind by `F-39`'s move from distribution packages to a source build:

- `docs/reference/images.md:20` — "The operand Dockerfiles package real Network UPS Tools binaries from
  **pinned distribution packages**." Both operand Dockerfiles now build NUT from source in a
  dedicated `nut-builder` stage: `images/nut-server/Dockerfile:17` and
  `images/upsmon-agent/Dockerfile:29`, each fetching `nut-${NUT_VERSION}.tar.gz` and running
  `./configure`.
- `docs/reference/images.md:22-23` — "`nut-server` **installs `nut`**" and "`upsmon-agent` **installs
  `nut`**". Neither does. The runtime stages `apk add` shared libraries only and copy the built
  tree from the builder stage.

Aspirations stated as fact:

- `docs/reference/images.md:32` — "pinned NUT version and **base image digest**". The NUT version is pinned
  (`ARG NUT_VERSION=2.8.5`, plus the assertion at `images/nut-server/Dockerfile:119-121` that the
  shipped `upsd` reports it and links OpenSSL rather than NSS — a real and unusually good control).
  The base image is not: every stage in both operand Dockerfiles is `FROM alpine:${ALPINE_VERSION}`
  with `ALPINE_VERSION=3.22`, a mutable tag.
- `docs/reference/images.md:33` — "checksum **and signature** verification for NUT source inputs". Only
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

One related item found while reading: `docs/reference/images.md:26` stated the driver allowlist including
`powerman-pdu`, which the operand image does not contain. That is `F-50`, recorded in
[nut-usage-audit.md](nut-usage-audit.md) — the allowlist appears in two places and both need the
same correction.

Closed 2026-08-14 by rewriting `docs/reference/images.md` and `docs/reference/security.md` into current controls versus
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
- `test/e2e/e2e_suite_test.go:35-48` builds `example.com/nut-operator:v0.0.1` and the operand
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
the images that compile NUT from source — which is why its timeout is 30 minutes.

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

Between them sits the gate. `test-e2e.yml` gained a `workflow_call` trigger taking the image
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
operand images complete a real NUT TLS session; there was no reason for `main` to float past a
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
without altering it, and the images that are not distroless — `nut-server` and `upsmon-agent` —
already carry shells because NUT tooling needs them.

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
after the vulnerability scan using GitHub OIDC keyless signing. `docs/reference/images.md` records the
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

## Findings — new-controller sweep, 2026-08-17

Against `be9f323`, prompted by this benchmark's own "any new controller or CRD" trigger:
`NodeHaltReconciler` and the `internal/haltwatch` package landed since the last pass. Scope widened
from that controller to the RBAC and evidence surfaces it touches. Every claim below was checked
against current source or a live apiserver; none is carried forward from a prior pass.

**F-93 · The `OD-38` budget-override path had no permission to perform its override.** When a
`PodDisruptionBudget` refuses an eviction on an enforcing flow, `Runner.evictPodsOnNode` falls back to
`Delete` on the pod. The generated `ClusterRole` granted `pods: get;list;watch` and
`pods/eviction: create`. No marker anywhere in the tree granted `delete` on `pods` — confirmed by
`grep` over every `+kubebuilder:rbac` marker and by reading `config/rbac/role.yaml`.

So the fall-back returned `403 Forbidden`, which is not `NotFound`, so it took the error return —
aborting the drain on precisely the budget the change existed to override. `OD-38` shipped as a
different error message and nothing else.

Nothing in the suite could see it. `internal/kubeactions` tests run against a fake client, which has
no authorizer and allows every call; the e2e suite deploys the real role but never drives a drain
against a budget-protected workload. Fixed by adding the marker at the call site and regenerating.

**The gap was a class, not an instance, so the fix includes a guard.** Markers are hand-written and
the calls they describe are somewhere else, so the two drift with nothing comparing them.
`executor_rbac_test.go` loads the generated `ClusterRole`, binds it to a test subject in envtest, and
asks the real authorizer — via `SubjectAccessReview` — whether each call the executor makes against
workloads it does not own is permitted. The grant list is written next to the call site that needs
it, so it reads as the executor's API contract rather than a restatement of `role.yaml`.

Two properties were established before trusting it. Envtest enforces RBAC at all: confirmed by
impersonating an unbound user and watching a pod list return `Forbidden`, since a permissive
authorizer would make every assertion pass vacuously. That check is now a spec in the file rather
than a thing done once. And it fails on the real regression: confirmed by removing `delete` from the
generated role and watching the suite fail with the marker to add and the command to re-run.

**F-94 · Halt attempts are held only in process memory, so a manager restart loses the evidence
silently.** `haltwatch.Observer` keeps unresolved attempts in a mutex-guarded map, seeded empty by
`NewObserver()` in `cmd/main.go`. Nothing reconstructs it. An attempt is opened when the signal write
lands and closed when the Node stops reporting or the deadline passes, and both ends have to be
observed by the same process.

A manager restart or a leader-election handoff between those two moments drops the attempt with no
outcome recorded — not `Halted`, not `TimedOut`. Silence, in the one component whose entire purpose
is to stop a halt from being inferred from a machine being dark. The manager is protected from its
own flow (`F-30`), but that does not cover an OOM kill, a rollout, or the node under it losing power,
and mid-outage is exactly when this measurement is being taken.

Reconstruction is feasible and the data is already there: the signal Secret carries `nodeName`,
`shutdownFlow`, `executionID`, and `timestamp` per key, and an outstanding key *is* an outstanding
attempt (`NA-3` — absence is the record). Re-seeding `pending` from the signal Secrets at startup
would close it.

Left open rather than fixed here, because it is not mechanical. A key that is still present may
belong to a node that already halted and was already counted before the restart, so naive re-seeding
double-counts; distinguishing the two means consulting the node's current `Ready` state at startup
and deciding what an already-`NotReady` node with a live key means. That is a change to the `OD-27`
evidence model and wants a decision, not a patch.

**F-95 · The metrics reference contradicted the procedure it cited.**
`docs/reference/metrics.md` said `make verify-actuation` "is how a node gets its first"
`halt_last_verified_timestamp_seconds` sample. `hack/verify-actuation.sh` prints the opposite —
"This run leaves no operator-side metric" — because it hand-delivers the signal with `kubectl` so
planner correctness cannot fail the test, and that puts it outside the executor where the halt clock
starts.

Same class as `F-52`: a documentation claim the build does not meet, in the page that tells operators
what to alert on. An operator following it would run the procedure, watch no series appear, and
conclude the metric was broken. Corrected to state what the procedure does prove and what it does
not.

### `NodeHaltReconciler` against Benchmark 2

Conformant, with one deliberate exception. It writes no objects, so `observedGeneration`, status
patching, and finalizers are all genuinely N/A rather than skipped. Leader election is declared
(`NeedLeaderElection() true`) — a second replica would double-count every attempt. The Node watch
carries a predicate that admits an event only for a node with an outstanding signal, so a
cluster-wide watch on the most frequently updated object in the cluster costs nothing in steady
state. No `time.Sleep`; the deadline sweep is a `manager.Runnable` ticker, which is the right shape
for work that no event will ever trigger. Duplicate registration is already covered —
`TestMainRegistersEachReconcilerOnce` counts every `SetupWithManager` call generically.

The exception is **"no in-memory state across reconciles."** The observer is exactly that, and
deliberately: the two ends of the measurement arrive on different goroutines minutes apart, and the
alternative is writing halt bookkeeping to the API during a power event. The cost of the exception is
`F-94`, and it should be read as the standing price of this design rather than as an oversight.

### Not findings this pass

- **Metric documentation is complete.** All 30 registered collectors appear in
  `docs/reference/metrics.md`. An earlier check in this pass reported 25 missing; that check was
  wrong — the reference groups metrics under `## nutoperator_<subsystem>_*` headings and names them
  by their bare `Name`, which a full-name search does not match.
- **No other uncovered API call.** `Delete` appears exactly once in non-test code, and it is the
  `F-93` call. The only subresource write is `pods/eviction`, which is granted. Scaling patches the
  workload's own `spec.replicas` rather than the `scale` subresource, so `deployments/scale` is
  correctly absent.
- **`ShutdownHook` read-only RBAC matches its use.** `RunHook` reads hooks and never writes them.

### Re-audit triggers

Unchanged, plus one: **after `F-94` is decided**, since the outcome determines whether halt evidence
survives a manager restart and therefore whether `nutoperator_halt_*` can be alerted on as an
absence.

## Findings — documentation sweep, 2026-08-17

**F-96 · `ShutdownHook` declares a status surface that nothing writes.** The type carries
`+kubebuilder:subresource:status` and a `ShutdownHookStatus` with a `Conditions` array. No controller
reconciles the kind: `ShutdownFlowReconciler` watches it to re-reconcile flows, a webhook validates
it, and generated RBAC grants only `get;list;watch`. Confirmed by grep — no `Status().Patch` or
`Status().Update` call anywhere names it.

So `kubectl get shutdownhook -o yaml` shows `status: {}` permanently, for every hook, forever. That
reads as a resource whose controller is broken or has not caught up, which is the same shape a real
outage produces, and there is nothing in the object to distinguish the two.

Found while correcting a claim this benchmark made in the previous pass. That pass recorded
`ShutdownHook` as missing `observedGeneration` and counted it against the convention. That reading was
wrong in a way worth naming: `observedGeneration` is a promise that something observed the spec, and
nothing observes this one, so adding the field would have manufactured a number that never moves. The
gap is one level up — the status subresource itself is the thing that should not be declared, or the
kind needs an observer.

Not fixed here. Removing a status subresource is an API change, and adding a reconciler for a kind
that is deliberately passive is a design question about whether hook health belongs on the hook or on
the flow that invoked it. Recorded in `docs/tasks.md` under Planning & Execution Logic.

## Findings — first real-cluster deployment, 2026-08-20

Against `7068677`, deployed to a live multi-node cluster (Kubernetes 1.35, Cilium CNI) rather than
kind or envtest. The install itself went clean; everything below is what only a real cluster
surfaces. Each finding was confirmed by direct observation, and the chain between them is the point:
the first one makes the operator inert, and the next two make that inertness unexplainable.

**Confirmed working on the way in.** The documented bring-your-own-certificate path installs exactly
as written — bundle applies, the manager holds in `ContainerCreating` until `hack/webhook-cert.sh`
runs (the non-optional Secret volume, as documented), then goes Ready. Every CRD registers. The
documented admission probe returns precisely the rejection the install guide predicts, naming
`spec.deviceRefs` and `spec.tls.serverCertificateRef`. The documented uninstall order tore down the
previous install with no finalizer hangs. Operand rendering, namespace adoption, CNPG schema
migration, and telemetry polling all work.

**`F-97` · The driver watchdog restarts a healthy `dummy-ups` driver, forever.** `upsdrvctl status`
against a `dummy-ups` device in `dummy-loop` mode reports `RESPONSIVE` briefly and `NOT_RESPONSIVE`
for most of each fixture cycle, while `PF_PID` stays constant — the driver process is alive and
simply is not servicing the status socket while it holds a timed block. The watchdog's double-check
falls inside the same unresponsive window, so it confirms and restarts, and `upsdrvctl start` on a
live driver hits `Duplicate driver instance detected (PID file exists)! Terminating other driver!`
The restart is what kills it. Observed cycling on a 30-second interval indefinitely, with `upsd`
alternating `Connected to UPS` and `Can't connect to UPS`, and `upsc` reporting `Driver not
connected`.

The watchdog script is unit-tested against *captured* `upsdrvctl status` output, which is why this
survived: the fixture encodes the belief that `NOT_RESPONSIVE` means dead. For a continuously
polling network driver that holds. For `dummy-ups` in loop mode it does not, and the simulation
scenarios are the only thing that uses it. No evidence either way yet about real `snmp-ups` devices
under load; that is worth checking before treating this as simulation-only.

**`F-98` · Nothing in the shipped bundle grants egress, so the operator is inert in any
default-deny-egress cluster.** `dist/install-byo-cert.yaml` contains zero `Egress` rules, and the
`NetworkPolicy` objects rendered for operands are `Ingress`-only. On a cluster whose baseline
default-denies egress, the operator therefore cannot reach `upsd` on 3493, PostgreSQL on 5432, or
any `ShutdownHook` endpoint. Telemetry polling fails with `i/o timeout`; the audit store fails with
a dial timeout.

Confirmed by construction: adding a `NetworkPolicy` granting egress to 3493 flipped `UPSDevice` from
`TelemetryPollFailed` to `live NUT telemetry is available` immediately, and adding 5432 brought the
CNPG audit store to `ready` with its schema migration applied. Nothing else changed.

`security.md` states that "generated operands are compatible with default-deny namespaces" and lists
the expected policy edges. That is true in the narrow sense that nothing here requires being
un-isolated, and the list is presumably meant as instructions for a cluster administrator. But
nothing says the administrator *must* author those rules for the operator to function, and the
install guide's network table reads as firewall information rather than as a required configuration
step. An operator following the documentation onto a zero-trust cluster gets a silent, total
failure of the product's core function.

*Closed 2026-08-20, by saying it — and deliberately not by shipping it.* `config/network-policy/egress/`
carries the five manager edges as an opt-in overlay, with the two that cannot be written portably
marked for the administrator to complete. `security.md` now leads with the requirement rather than
listing the edges as background, and the install walkthrough names it as one of the three things
that stop a first run.

Shipping it in the default bundle would have been wrong, not merely optional. NetworkPolicy is
additive per direction: a pod with no `Egress`-typed policy selecting it has unrestricted egress,
and the moment one appears the pod is restricted to exactly what that policy lists. Two of the five
edges are unknowable to us — PostgreSQL and hook endpoints are wherever the user put them, and the
API server's address is cluster-specific — so a default egress policy would take every working
permissive cluster and break it in order to half-fix the default-deny ones.

The agent half was already closed and the finding's third confirmation is stale:
`NodePowerAgent` renders its own `Egress` policy naming the `upsd` pods it monitors plus DNS.
Verified in `resolveAgentMonitorTargets`, which builds one rule per monitored `NUTServer` and
appends `dnsEgressRule()`.

**`F-99` · The shipped simulation scenarios cannot compile, and the reason is unreachable.** The
`homelab` and `multistage` scenarios pair a `RuntimeBelow` trigger with a `dummy-ups` fixture that no
bundled capability profile matches. The device resolves to the unidentified profile, so the operator
cannot confirm it reports `battery.runtime`, so the trigger raises
`TriggerUnsupportedByAllDevices` at `Error` severity and the flow is rejected. The fixture does in
fact publish `battery.runtime`, but a declaration is authoritative and an observation is not
(`GP-5`), so this is the design refusing correctly on inputs that were never made consistent with
it. It fails this way on every cluster, by construction, not because of anything environmental.

What makes it a finding rather than a broken example is that **the cluster cannot tell you any of
that.** `ShutdownFlow` status carries `PlannerFailed` and the message "shutdown flow planner failed
after resolver inputs were attached" — no diagnostic, no subject, no reason. Status publishes no
diagnostics at all on the rejection path. Nothing is logged at any level. `plannerDiagnostics` is
computed and reaches only `recordShutdownFlowAudit`, which returns early and silently when
`spec.managementClusterRef` is unset — and none of the simulation scenarios set it, while every
`orion-cluster` resource does.

So the naming diagnostic existed the whole time and had nowhere to go. Recovering it took setting
`managementClusterRef`, granting egress to PostgreSQL, and querying
`power.shutdownflow_compilations` by hand. On the storage mode the install guide recommends for
evaluation — `Disabled` — that path does not exist at all and the information is destroyed.

This is the silent-failure class the project's own principles exclude, sitting on the compile path,
which is the one surface a dry-run evaluation is entirely made of.

*Closed 2026-08-20, on both halves.* The unreachable-reason half was the finding; the broken
scenarios were the thing that exposed it.

Reason: a rejected compile now reports the planner's own diagnostic — reason, subject, and message —
instead of the generic `PlannerFailed`, and every resolver and planner diagnostic is published on
`status.compileDiagnostics` tagged by the stage that produced it. Status is the right surface
precisely because the audit writer is not: it returns early without `managementClusterRef` and
writes nowhere under `storage: Disabled`. The generic reason survives as the fallback for a planner
that produces neither a plan nor a diagnostic, because flattening that into a specific-sounding
reason would hide a real bug.

Scenarios: `docs/examples/simulation/` gained two shared files. `capability-profile.yaml` matches
the fixture models on a `*-fixture` glob and declares exactly the variables the `.seq` fixtures
emit, and each `UPSDevice` now declares `spec.identity.model` to match — the two inputs the
2026-08-20 retest showed were sufficient. `cluster.yaml` is a `PowerManagementCluster` named
`sim-power` that all eleven referencing resources point at, which also closes the broader half
recorded below: a `NodePowerAgent` inherits its operand images through that reference and could not
render without one. It is a local control plane rather than a borrowed `orion-cluster` reference,
since the scenarios exercise none of that example's CNPG storage, hook allowlist, or actuation
policy, and requiring a PostgreSQL cluster before a fixture UPS can report `OnBattery` is a barrier
with nothing behind it.

### Re-audit triggers

Added: **after any change to what status publishes on a rejected compile**, since `F-99` is a
statement about that surface rather than about the planner.

## Findings — rebuild and retest, 2026-08-20

Full teardown and clean rebuild against the current `main` build
(`nut-operator@sha256:b9889d5e`), pinned by digest rather than tag so there is no ambiguity about
what ran. The audit schema was dropped beforehand so migrations were exercised from nothing rather
than re-confirmed.

**Retested clean.** Install, certificate provisioning, CRD registration, and the documented admission
probe all behaved exactly as the previous pass and as documented. Migrations rebuilt the schema from
empty. `F-93` holds: the operator's live RBAC permits `delete pods` and eviction creates. Warnings
surface correctly on an accepted compile — `Degraded` names `InventoryNodeNotInCluster` with its
subject, which is the counterpart that makes `F-99` precise: the diagnostic surface works, and only
the *rejection* path publishes nothing.

**The pipeline completed for the first time.** Supplying the two things `F-99` identified as missing —
a capability profile matching the fixture, and `spec.identity.model` so the matcher has a declared
model to match against — moved the flow from `PlannerFailed` to `Accepted`, and it compiled six waves
in the order the tiers imply: burst node shed first, then three scale groups **sharing one wave**,
then databases, drain, worker stop, and the control plane last. Independent same-tier groups running
concurrently is the property removing `spec.groups[].phase` was meant to restore, observed here on a
real cluster.

The adaptive tier pointer then ran against live fixture telemetry: escalated `Relaxed -> Urgent`,
descended tier 5 through tier 1, and recomputed compression at each boundary — 0.20, 0.22, 0.25,
0.36, 0.67, 1.00 — as the remaining plan shrank against an assumed 4m runtime. Feasibility warned
without blocking, exactly as designed.

**`F-100` · Every execution audit write fails, on every cluster, permanently.** The executor derives
`ExecutionID` from `shutdownExecutionDeduplicationKey`, which returns
`hex.EncodeToString(sha256.Sum256(...))` — 64 hex characters. `power.shutdownflow_executions.execution_id`
is a `uuid` column. PostgreSQL rejects every insert with `invalid input syntax for type uuid`
(SQLSTATE 22P02).

It is not one table. The same identifier keys `executor_resume_states`, `shutdownflow_execution_waves`,
`shutdownflow_execution_groups`, and `shutdownflow_action_attempts`, and every write to all five
fails. After a complete execution — eight action attempts across six waves, with the full adaptive
descent recorded in CR status — the audit store contained zero rows in all five tables, while
`shutdownflow_compilations`, `shutdownflow_decisions`, and `capability_profile_matches` populated
normally. Those use `uuid.NewString()`; the execution path does not.

Two consequences beyond the missing rows. `PostgreSQL holds the record of what actually happened` is
the project's stated division of labour against Kubernetes status, and for executions it holds
nothing, ever. And `executor_resume_states` is how an interrupted executor knows where it was, so
resume across a manager restart cannot work either — the state is never written.

The failure is logged loudly at `ERROR` on every reconcile, which is the one mercy here, and it is
invisible everywhere else: the flow still publishes `status.lastExecution` as though the record
were durable.

Nothing in the suite covers it. envtest has no PostgreSQL, the audit tests do not run against a real
schema, and no e2e path drives an execution against CNPG. It took a real database to see it.

*Closed 2026-08-20, by deriving the identity rather than widening the columns.* Both options were
on the table and the column widening was rejected. The identity does not stay in PostgreSQL: it is
stamped on Kubernetes objects as the `power.zalud.io/execution` label, where 63 characters is the
ceiling — so a 64-character digest was already being silently truncated there, and the label had
stopped equalling the ID it names. `text` would also have meant rebuilding five foreign keys. A UUID
is 36 characters, survives the label intact, and is the type every other identity column in this
schema already uses.

`execution_id` is now a UUIDv5 derived from the episode digest, which keeps the property the digest
was providing: the same trigger episode always yields the same primary key, so a re-record updates
the row instead of creating a second one. The digest itself moved to
`shutdownflow_executions.deduplication_key` (schema migration 7) so a row can still be traced back
to the episode whose content produced it.

The phase mismarking closed with it, and separately. `Execute` returned the accumulated record error
as its own error on the completion path, so a run that traversed every wave came back non-nil and
the controller mapped it to `Failed`. Record errors now travel on `Result.RecordError`, the returned
error is the execution's alone, and an audit outage is published as a `Degraded` condition with
reason `ExecutionAuditRecordFailed`. An audit outage is not a shutdown outcome, and reporting it as
one pointed every reader at the wrong system.

Still open, and deliberately: resume-after-restart. `executor_resume_states` now persists, which was
the precondition — nothing reads it back yet, and what an interrupted executor should do with the
state it finds is an `F-94` evidence-model question rather than a write path.

**Open, not yet characterised.** The flow settles at `phase: Aborted` with
`lastExecution.reason: AlreadyExecuted` and the message "eligible trigger episode already has
execution evidence". Correct deduplication of a repeated episode reading as a terminal *failure* is
suspicious, but the failing resume-state write above is a plausible confounder, so this is recorded
as a question rather than a finding until `F-100` is fixed and it can be retested cleanly.

## Findings — component sweep, 2026-08-20

Wave recompilation, the capability probe, the node agent operand, and the admission surface, all
against the live cluster.

**Waves recompile from authored inputs, and only from those.** There is no wave configuration to
edit; a wave is output. Confirmed by three reconfigurations of the same flow: adding `before` edges
among three concurrent tier-4 groups split them into three waves (6 waves total, up from 6 to 8);
removing the one shipped ordering edge merged `drain-workers` and `stop-workers` into one wave (down
to 5); retiering `quiesce-databases` from 3 to 4 merged it into the scale wave (down to 4). Restoring
the shipped inputs returned `configHash` to its exact original value, which is `PL-14` determinism
observed rather than asserted.

The merge case is worth noting: it produces exactly the hazard the `homelab` scenario README
describes — a drain and the node stop it protects sharing a wave — and the planner accepts it with no
diagnostic. That matches the documented state, since the requirement to detect it was retired and
nothing replaced it. It is now confirmed reachable in one edit.

**Privilege boundary verified on a rendered pod.** `automountServiceAccountToken: false`,
`hostPID: false` under `Simulate`, `priorityClassName: system-node-critical`, `RuntimeDefault`
seccomp, `runAsNonRoot: true` at uid 65532, read-only roots, `capabilities.drop: [ALL]` with no adds,
and resource requests and limits on both containers so the tier-0 pods are not BestEffort (`F-34`).
Most importantly the actuator mounts `power-agent-signals` and `power-agent-actuator-state` and
**does not mount `power-agent-run`** — the `OD-37` claim in `security.md` observed directly rather
than taken on trust.

**Admission holds where it should.** Tier-0 targeting is rejected with a group-specific message;
reserved operand namespaces are rejected; `Actuate` without the approval annotation is rejected.

**`F-101` · A declared model that contradicts the device is never flagged.** `spec.identity.model`
was set to `simulation-fixture` while the device reported `homelab-fixture`. Matching used the
declared value and bound the device to a profile keyed on it. No condition, no diagnostic, and the
`capability_profile_matches` audit row carries `diagnostics: []` and no reported model at all.

`GP-5` says derived data "may verify that input and raise conditions on mismatch". Both values are in
hand — the operator polls `ups.model` for telemetry and the probe reads it — and nothing compares
them. The consequence is that a typo in `spec.identity.model` silently binds a device to the wrong
capability profile, and that profile is what decides which telemetry is trusted and which triggers
are supported.

*Closed 2026-08-20.* Every successful telemetry poll now records `ups.model`, `ups.mfr`, and
`ups.firmware` on `UPSDevice.status.identity` beside an echo of the declared model, and the
`IdentityVerified` condition states whether the two model strings agree. `capability_profile_matches`
gained `declared_model` and `reported_model` (schema migration 6), so history can say which device a
profile was bound to and not only which profile was chosen.

The condition is `Unknown`, not `False`, when either half is missing. A device with no declared model
matches by driver family and a device that has not polled has reported nothing; failing verification
on either would leave a permanent red condition on healthy devices, which is how a condition stops
being read. The reported value still feeds nothing — matching remains on the declared value per
`GP-5`, and this reports the disagreement rather than resolving it.

**`F-102` · The probe drafts product profiles containing driver-instance variables.** A profile is a
product/SKU record (`SB-9`), and no bundled profile carries a single `driver.*` variable. A draft
from `UPSCapabilityProbe` carried ten: `driver.debug`, `driver.state`, `driver.version`,
`driver.flag.allow_killpower`, and `driver.parameter.{mode,pollinterval,port,synchronous}` among
them. Those describe the driver instance at one site, not the model.

The drafted `spec` is otherwise correct and the actuation section is properly left empty with its
explanation. Only variable *names* reach `status.issueReport`, not their values, so nothing
site-specific leaks — but `driver.parameter.port` is a per-device setting, and the guide directs
users to send that report upstream for inclusion in the shared catalog.

*Closed 2026-08-20.* `BuildDraft` drops the whole `driver.` namespace rather than an enumerated list,
since the objection is to the namespace: `driver.version` records which NUT build read the device and
`driver.parameter.*` records how this installation reaches it, and neither is a property of the
product. The count of dropped names is reported in the draft notes so a user diffing the draft
against `upsc` output can see the difference was deliberate.

Verification changed with it. `driver.*` had also been counted as an *unexpected* variable, because
no profile declares one — which would have reported drift on every verification of a healthy device
the moment profiles stopped carrying them.

**`F-103` · The driver rejection message lists the unsupported drivers as the supported ones.**
`upsdevice_webhook.go:114` calls `field.NotSupported(path, value, unsupportedLocalUPSDrivers())`. The
third argument of `field.NotSupported` is the set of *valid* values, which Kubernetes renders as
"supported values". So rejecting `usbhid-ups` produces `Unsupported value: "usbhid-ups": supported
values: "usbhid-ups", "nutdrv_qx", ...` — naming the rejected driver first in the list of drivers it
claims are acceptable.

Line 116 does the same call correctly with `supportedNetworkUPSDrivers()`. The reader most likely to
hit this is someone with a USB UPS, who is exactly the reader the network-only design needs to
redirect, and who is instead told to retry with a list of drivers that will all be rejected. A
literal swap of the argument would fix the contradiction; saying plainly that USB and serial
attachment is out of scope would fix the confusion.

*Closed 2026-08-20, by saying it rather than swapping it.* The swap was available and was not taken:
`field.NotSupported` exists to offer the reader an alternative, and there is no alternative to offer
someone with a USB UPS — the network allowlist is not a substitute for a device wired to a host. The
rejection is now `field.Invalid` carrying the scope boundary in words. The webhook test asserts the
message names the boundary and that the phrase "supported values" does not appear in it.

**`F-99` is broader than recorded.** Missing `spec.managementClusterRef` does not only cost the audit
record. `NodePowerAgent` inherits its operand images from `PowerManagementCluster.spec.images`
through that same reference, so the simulation scenarios' agents fail to render at all with
"NodePowerAgent upsmon rendering requires an image repository". Adding the reference rendered all
three DaemonSets immediately. Every simulation resource needs it, not just the flow.

*Closed 2026-08-20 with the rest of `F-99`.* All eleven `NUTServer`, `ShutdownFlow`, and
`NodePowerAgent` resources under `docs/examples/simulation/` reference `sim-power`, the local
`PowerManagementCluster` added in `cluster.yaml`.

**`F-98`, third confirmation.** The agent-to-`upsd` edge on 3493 is also unshipped: agents ran but
stayed NotReady, correctly, because the `upsmon` readiness probe queries the UPS it monitors rather
than the host (`NA-8`). Supplying the edge made all five agents ready.

## Pass: closing F-96, 2026-08-20

**`F-96` · closed by removing the subresource.** `ShutdownHook` declared a status subresource and a
conditions array, and nothing reconciled the kind at all. The decision was whether to give hook
health an observer or drop the field; dropping it is settled by two existing requirements rather
than by preference. `GP-4` excludes probing declared endpoints on a schedule — that is becoming
monitoring, not consuming it — and `OD-34` already publishes hook outcome on the owning
`ShutdownFlow`, which is the resource a failed hook actually degrades. Recorded as `HK-11`.

**`F-113` · A hook outside the allowlist compiles clean and fails during the outage.**
`shutdownFlowHookDigests` resolves each referenced `ShutdownHook` and hashes its spec, and never
compares its endpoint against `PowerManagementCluster.spec.hooks.allowedEndpoints`. The allowlist is
enforced at delivery time only, in `kubeactions/runner.go`.

So a flow referencing a hook whose endpoint is not permitted is accepted, compiles, publishes a plan
naming the hook, and discovers the problem when it tries to deliver — which is during a power event,
the one moment `HK-9` exists to have settled in Git beforehand. The same applies after the fact: an
allowlist edited to drop a host silently invalidates every hook still pointing at it, with nothing
on any resource saying so.

Found while closing `F-96`. It is the argument a hook status *would* have had, and it does not need
one: the check belongs on the compile path, where the flow already resolves the hook and already has
the cluster in hand, and reports through `status.compileDiagnostics` like every other compile
finding.

*Closed 2026-08-20, as a warning rather than a rejection.* `shutdownFlowHookDigests` now checks both
the `invocation` and `dryRun` URLs against the allowlist and publishes `HookEndpointNotAllowed` on
`status.compileDiagnostics`, degrading the flow.

Not a rejection, and the distinction is load-bearing: `OD-34` makes hooks advisory and `HK-7` says a
hook never holds a wave, so an undeliverable hook must not stop a shutdown plan from existing. It
degrades and says why, which moves the discovery from mid-outage to `kubectl`.

The check calls the same `HookURLAllowed` the delivery path uses rather than reimplementing it.
Two copies of one allowlist is the `F-50` shape — they agree until one is edited, and the
disagreement surfaces at the worst possible moment.

## Pass: re-examining the open list, 2026-08-20

Three items carried on the open list were checked against the code rather than against the notes
that produced them. One was a duplicate of `F-100`, one was described wrongly, and the cluster left
running since the previous pass had meanwhile produced two findings of its own.

**The executor's terminal state is `F-100`, not a separate question.** The open list carried "a
dry-run that ran to completion settles at `phase: Aborted`" as an uncharacterised item. It is fully
explained. `sim-homelab-conservation` reports `waveCount: 2`, `groupCount: 4`,
`actionAttemptCount: 4`, and both `startedAt` and `completedAt` — the run traversed its plan and
finished. Its `lastExecution.phase` is nevertheless `Failed`, because `shutdownExecutionPhase(phase,
err)` returns `Failed` whenever `err != nil` regardless of the executor's own result phase, and the
executor returns `result, recordErr` on the completion path. With every audit write failing on
`F-100`, `recordErr` is non-nil for every execution. `applyLastExecutionPhase` then maps execution
`Failed` to flow `Aborted`, which is the phase the earlier pass reported.

The flow-level and execution-level phases were also being read as one field. They are two, and they
never carry the same value on this path.

**`F-104` · `spec.operandNamespace.create` is inert in both directions.** Nothing reads it.
`powermanagementcluster_webhook.go:106` defaults it to `true` and that is the only reference outside
tests. The namespace is created unconditionally by `ensureOperandNamespace` in
`nutserver_render.go` and `nodepoweragent_render.go`, on the reconcile of a `NUTServer` or
`NodePowerAgent` — so `create: false` does not suppress creation, and `create: true` does not cause
it. A `PowerManagementCluster` alone reaches `Ready` with the namespace still absent, which is what
makes the examples fail on their first namespaced object.

The field's own documentation says "create allows the operator packaging to create the namespace".
The packaging creates `nut-operator-system`, the manager's namespace, and never the operand
namespace. `reference/security.md` compounds this by naming `PowerManagementCluster` among the kinds
whose "operand namespace may not exist yet; the operator creates and labels it on first reconcile" —
the cluster reconciler contains no namespace code and never calls
`rejectReservedOperandNamespace`. Either give the field a reader or remove it; the security note
needs correcting either way.

*Closed 2026-08-20, by giving it a reader in both directions.* `PowerManagementCluster` now creates
and labels the operand namespace on reconcile and publishes it in `status.managedResources`, so a
control plane applied on its own leaves a namespace its operands can be rendered into — which is what
`reference/security.md` and the simulation READMEs already claimed. `create: false` is honoured the
other way: the operator labels a namespace that exists and otherwise reports `OperandNamespaceMissing`
rather than creating one.

`create: false` suppresses creation, not adoption. A user saying they own the namespace's existence
is not saying the operator should leave it unlabelled and then fail to find its own operands in it,
so the labels are still applied — and only this operator's two, leaving any
`pod-security.kubernetes.io/*` the cluster's admission owner set untouched. A standalone `NUTServer`
or `NodePowerAgent` with no `managementClusterRef` still creates: there is no field to read, and
creation is both the webhook default and the only answer under which such a CR can converge.

**`F-105` · A driver exit drives every node agent into a forced-shutdown loop.** The driver goes
away, `upsd` drops it (`Can't connect to UPS [sim-homelab-ups] ... Connection refused`), every
`upsmon` loses comms, and after `DEADTIME` each one concludes "Too few UPS(es) are healthy (0<1),
initiating forced shutdown". It runs `SHUTDOWNCMD`, `power-signal-writer` writes a shutdown signal,
and `upsmon` exits 0 — correct NUT behaviour, and in a DaemonSet it is a container exit, so kubelet
restarts it and the cycle repeats.

The escalation to a *shutdown* rather than a logged comms failure needs the fixture as well as the
exit. `upsmon` treats lost comms as fatal only when the UPS was last seen on battery, and the
homelab sequence holds `OB` or `OB LB` for 180 of every 300 seconds. A driver exit during the `OL`
block would be logged and survived.

Left running for seven and a half hours, the agents accumulated 61 to 67 restarts each, three of
them sitting in `CrashLoopBackOff`. Each cycle wrote a fresh shutdown signal with a new
timestamp-derived execution ID (`upsmon-<node>-<nanos>`), so the actuator's `seen` set cannot suppress the
repeat — `NA-3` revocation governs the operator-written Secret, not this node-local path. Only
`actuatorPolicy: Simulate` kept this from being a repeated real shutdown of every node in the
cluster.

Two things need separating here: the driver flap that starts it (`F-97`), and the absence of any
stand-down state after an agent has signalled (this finding). Fixing the watchdog removes today's
trigger without addressing what happens the next time comms genuinely drop.

**`F-106` · `deactivateLastExecution` leaves `reason` and `message` contradicting each other.** It
rewrites `Reason` from `AlreadyExecuted` to `TriggerNotEligible` and does not touch `Message`, so the
live flow currently reports `reason: TriggerNotEligible` beside `message: "eligible trigger episode
already has execution evidence"` — a message asserting exactly the state the reason denies.

*Closed 2026-08-20.* Reason and message move together, and `RehearsalAlreadyExecuted` is cleared the
same way — it reaches deactivation by the same path and would have left the same contradiction. Both
strings are now constants shared with the `ExecutionReady` condition, which is what let them drift in
the first place: the condition and `status.lastExecution` were writing the same statement from two
places.

## Pass: root-causing the agent crash loop, 2026-08-20

`F-97` and `F-105` both had the causality backwards. The correction came from watching the driver
process rather than `upsdrvctl status`.

**`F-97` is wrong about its mechanism, and the wrong half is the part that matters.** It recorded
that "the driver process is alive and simply is not servicing the status socket while it holds a
timed block" and that "the restart is what kills it". Neither is true. The driver **exits on its
own**, and the watchdog is what brings it back.

The evidence is a process count taken alongside the status output. Sampling inside the pod across a
transition:

```
00:04:35 | live=2 | sim-homelab-ups dummy-ups RUNNING 34448 RESPONSIVE 34448 "OB DISCHRG"
00:04:39 | live=1 | sim-homelab-ups dummy-ups N/A     34448 NOT_RESPONSIVE N/A
```

`RUNNING` drops to `N/A` and `S_PID` drops to `N/A` because the process is gone. `PF_PID` still
reads `34448` — and `PF_PID` is read from the **PID file**, which the dead driver left behind. That
stale file is the whole source of the original error: "`PF_PID` stays constant" was taken as
evidence that the process was alive, when it is only evidence that nothing cleaned up after it.

`Duplicate driver instance detected (PID file exists)! Terminating other driver!` reads the same
way and is not what it appears to be either. The new instance is clearing a stale PID file, not
terminating a live driver.

The watchdog's real defect is therefore the opposite of the one recorded. It is not restarting
healthy drivers; it is the only thing that restarts dead ones, and it does so on a 30-second poll
that is not faster than `DEADTIME`. Every driver exit becomes a forced shutdown before recovery can
land. Timestamps from one cycle: the driver exits at `23:59:29`, `upsmon` gives up at `23:59:32`,
and the watchdog restarts it at `23:59:59` — 27 seconds too late to matter.

**Why the driver exits is narrowed, not answered.** A second `dummy-ups` was run in the same pod
under a private `NUT_CONFPATH`, on a copy of the same fixture in the same `dummy-loop` mode, with
debug on and invisible to both the watchdog and the readiness probe. It ran 236 seconds without
exiting, including a clean `OL`→`OB DISCHRG` transition across a `TIMER` boundary, and it stayed
`RESPONSIVE` throughout — including while paused inside a timed block, which is the specific
behaviour `F-97` claimed was absent. It also survived being hammered with `upsdrvctl status` at
several times the combined probe and watchdog rate, logging only `sock_arg`/`sock_read` churn.

In the same window the production driver died five times, at lifetimes of 41, 55, 17, 4, and 21
seconds. Observed lifetimes across the session ranged from 4 to 176 seconds with no relation to
position in the sequence, which rules out the fixture reaching a particular block.

What the isolated instance lacks is `upsd` and the five reconnecting `upsmon` clients behind it.
That is where to look next, and it raises the possibility that the loop is self-sustaining: the
agents' own reconnect churn on each restart would be part of what kills the driver again.

**Container resource limits are asymmetric.** The `driver-watchdog` container carries
`10m`/`32Mi` requests and limits. The `upsd` container — the one running both `upsd` and every
driver — declares none at all.

## Pass: benchmarking CI against comparable operator projects, 2026-08-20

The pipeline was compared against the kubebuilder scaffold it started from and against operator
projects with mature release engineering (cert-manager, Cluster API, CloudNativePG). Roughly half of
it is beyond the scaffold, which ships only `lint.yml`, `test.yml`, and `test-e2e.yml`.

Where it is ahead is not repeated here: SHA-pinned actions, default-empty workflow permissions,
cosign signing, `provenance: mode=max` with SBOM, digest-then-promote, installer-freshness diffing,
and an e2e suite that runs real operand images rather than stopping at envtest. The findings below
are the places it is behind what comparable projects do.

**`F-107` · A version tag publishes images that no gate has tested.** `images.yml` triggers on
`push` to `v*.*.*`, and the metadata step emits `type=ref,event=tag`, so a release tag builds and
pushes all four images. But `digests` carries `if: github.event_name != 'pull_request' &&
github.ref == 'refs/heads/main'`, and `e2e` needs `digests` while `promote` needs both. On a tag
push all three are skipped.

The consequence is inverted from the intent. `:main` is carefully a retag of a digest the e2e suite
has run against, and the comment at the metadata step says so. A version tag — the reference a user
is most likely to pin — gets no e2e run at all. Either the gate jobs need to accept tag refs, or
tagging needs to promote an already-tested digest the way `main` does.

*Closed 2026-08-20, by doing both.* `type=ref,event=tag` is gone from the build metadata, so a tag
push publishes only the immutable `sha-` reference; `digests` now accepts `refs/tags/v*` alongside
`refs/heads/main`, which pulls `e2e` and `nut-tls` in behind it; and `promote` resolves its target
from the triggering ref, applying `vX.Y.Z` on a tag and `main` on a branch push.

Accepting tag refs alone would not have been enough. The images were already on the registry by the
time the gate ran, so a failing gate would have left four published, pinnable, untested references
behind it. Withholding the tag until after the gate is what makes the two paths actually equivalent
— which was the intent the comment at the metadata step already claimed.

**`F-108` · Everything is tested against exactly one Kubernetes version.** `ENVTEST_K8S_VERSION` is
derived from the `k8s.io/api` minor in `go.mod`, so envtest tracks whatever the operator compiles
against and nothing else. The e2e cluster takes whatever `kind` `latest` currently defaults to,
which is unpinned and moves without a commit. Comparable projects run a matrix, usually N-2, and
pin the node image explicitly. A compatibility claim in `installation/` cannot be supported by a
single-version pipeline.

**`F-109` · The e2e cluster cannot exercise the two properties the operator exists for.**
`setup-test-e2e` runs `kind create cluster --name $(KIND_CLUSTER)` with no `--config` and no config
file in the tree, so every run is a stock single-node cluster on kindnet.

Two consequences. kindnet does not implement `NetworkPolicy`, so policies apply cleanly and enforce
nothing — which is why `F-98` passed every CI run and failed immediately on a real Cilium cluster.
And a single node means a `NodePowerAgent` DaemonSet has exactly one pod, so wave ordering, tier
descent across nodes, and self-exclusion are structurally untestable. A multi-node kind config, and
a policy-enforcing CNI for at least one job, are what comparable projects use.

**`F-110` · Nothing in the suite induces a failure.** There is no chaos step, no pod deletion, no
partition, no apiserver stall. The only restart-aware assertions run the other way: `byo_cert_test`
checks that the manager's restart count does *not* change across a certificate rotation. Every spec
asserts convergence on a healthy cluster.

For an operator whose entire purpose is acting during failure, this is the largest structural gap,
and it is the category `F-97` and `F-105` fall into: a driver that exits, a client that reconnects,
a watchdog that recovers too slowly. Related, the suite is also bounded in minutes, so nothing
sustained is covered — the agent forced-shutdown loop needed hours of continuous running before it
was visible as anything but a restart count.

**`F-111` · Coverage is collected and thrown away.** `make test` writes `cover.out` and nothing
reads it. No gate, no upload, no trend. The file is produced on every CI run and discarded with the
runner.

*Closed 2026-08-20, with a reader and not a gate.* `make cover` and `make cover-html` read the
profile locally, and the Tests workflow puts the total in the job summary and keeps the profile as an
artifact. No threshold was added: a number nobody chose, enforced as a merge gate, produces tests
written to move the number. The total is now visible on every run, which is what makes a regression
arguable.

**`F-112` · No upgrade path is tested, and no workflow produces a release.** There is no release
workflow and no GitHub release has ever been published, which is already why the install
documentation uses clone-based and `raw.githubusercontent.com` paths. Nothing tests that a cluster
running one version converges after the operator is replaced with the next, and nothing tests CRD
schema compatibility across versions. Both become gates the moment a v1 exists.

## Pass: multi-node e2e cluster with policy enforcement, 2026-08-21

`F-109` is closed. The e2e cluster is now three nodes on Calico instead of one on kindnet, and a
guard spec fails the run if the cluster underneath it does not enforce `NetworkPolicy`.

**The guard is the part that matters, and it was checked against both CNIs.** Asserting that a
policy blocks traffic is worth nothing unless the assertion can fail, and under kindnet it cannot.
The same probe — a pod opening TCP to the API server's ClusterIP, a default-deny egress policy
selecting that pod, then the policy removed — was run against both:

| | baseline | policy applied | policy removed |
|---|---|---|---|
| Calico | connects | **blocked**, 3/3 | connects, 3/3 |
| kindnet | connects | **connects**, 3/3 | — |

kindnet accepts the policy and reports it created. It simply does not implement it. On that cluster
the guard fails, which is the intended behaviour and the thing that would have caught `F-98` before
it shipped.

The restore step is deliberate. A connection that fails after a policy is applied has only been
shown to fail; showing it succeeds again once the policy is gone is what attributes the failure to
the policy rather than to a slow pod.

**A silent-undersizing failure showed up while building this, and is now guarded.** Every kind node
is a container drawing on the host's inotify limits. At the common default of
`fs.inotify.max_user_instances=128` the third node's kubelet dies with `inotify_init: too many open
files` and never joins — and `kind create` still exits 0. The first cluster built here came up with
two nodes and reported success; only counting the containers against the API server's node list
showed the difference.

That failure mode restores exactly the blind spot this finding exists to remove, so
`setup-test-e2e` now refuses to run under the default and prints the `sysctl` to fix it, and the CNI
step compares the node count against `kind-config.yaml` rather than trusting `kubectl wait --all`,
which is satisfied by however many nodes happen to exist. CI raises the limit explicitly.

**A second hazard turned up in the same target and is fixed with it.** `kind create cluster` sets
the current kubectl context when it *creates* a cluster and not when it reuses one. Every kubectl in
the e2e suite is unqualified, and the suite creates and deletes namespaces and patches Secrets, so
on the reuse path it runs against whatever context is current — which on a workstation that also
administers a real cluster is not a nuisance. `setup-test-e2e` now pins the context explicitly
before anything else runs.

**The pod subnet is an RFC1918 literal, and the private-IP scan is narrowed rather than loosened.**
`kind-config.yaml` sets the pod subnet to Calico's own default pool so its manifest applies
unedited. That is a generic container-network value describing no real site, the same category as
the `.devcontainer/` exclusion that already existed, so the scan excludes that one named path. A
directory-wide or pattern-wide carve-out is how that check would stop being one.

**Still open, and now testable for the first time.** Three nodes give a `NodePowerAgent` DaemonSet
three pods, one per node, since it tolerates every `NoSchedule` and `NoExecute` taint. Wave
ordering, tier descent across nodes, and agent self-exclusion have no specs yet. Making them
possible and writing them are different pieces of work, and only the first is done.

Calico is pinned at `v3.32.1`. The kind node image is still unpinned, which is the remaining half of
`F-108`.

## Pass: closing the CI and audit-trail findings, 2026-08-21

Five findings closed and one halved, all verified before commit.

**`F-114` closed.** Migration 8 types `trigger_decision_id` as `text`. Migration 7 had moved
`execution_id` off the SHA-256 digest and onto a derived UUID, keeping the digest in
`deduplication_key` — the right shape, and verified against the tables keyed on `execution_id`. It
did not look one column over. `trigger_decision_id` is also `uuid`, and the executor writes
`trigger-001-runtimebelow` into it: a name for a trigger a person authored, which nothing mints and
nothing should coerce.

So `F-100`'s symptom survived `F-100`'s fix exactly. The insert into `shutdownflow_executions` still
failed 22P02, and because the four child tables carry a foreign key to it, each then failed 23503.
On a live cluster the counts told the story on their own — thousands of rows in every table not
keyed on an execution, zero in all five that are.

**`F-115` closed.** Every reconciler declared `+kubebuilder:rbac:groups=""` for events, the legacy
core group, while the recorder writes through `events.k8s.io`. No grant existed for that group at
all, so every event the operator has ever emitted was refused — not degraded, refused. It surfaced
only because cluster-scoped kinds have no namespace on the regarding object, which lands their
events in `default` and makes the refusal name a namespace nobody configured.
`upsdevice_controller.go` records on nine paths and had no events grant in either group.

**`F-113` closed.** A Repo Hygiene job now applies both installers to a throwaway Kind cluster with
`--dry-run=server`. Server-side and not client-side: `--dry-run=client` runs no API validation and
accepts an empty `resources:` list as happily as anything else, which is precisely the check that
was missing.

**`F-108` closed.** envtest runs 1.34, 1.35 and 1.36 on every commit with `fail-fast` off, and the
e2e node image is pinned by digest to 1.34 — the matrix floor, so a break that only appears on the
version e2e actually runs cannot hide behind a green unit run on a newer control plane. The tested
claim in `installation/README.md` now states versions instead of describing how a version is derived.

**`F-97` halved.** The watchdog interval drops from 30s to 5s and the confirmation now waits between
its two readings rather than taking them back to back, which was two reads of one instant and could
only ever agree with itself. The old interval was justified by not churning healthy drivers; that
rationale came from the misreading this audit already corrected, and once the driver is understood
to be exiting, the binding constraint is `DEADTIME` on the other side. A recovery slower than
`DEADTIME` does not delay telemetry, it converts a driver exit into a cluster-wide shutdown signal.
Why `dummy-ups` exits is still open.

**A gate gap worth naming.** `F-113`, `F-114`, and `F-115` are all the same shape: a defect that no
local gate could see, because envtest has no PostgreSQL, applies no helper `ClusterRole`s, and grants
its client everything. Each was found by running the operator against a real cluster and reading what
it wrote, and each had been passing CI for as long as it existed.
