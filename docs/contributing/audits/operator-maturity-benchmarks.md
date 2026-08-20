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

**Open, not yet characterised.** The flow settles at `phase: Aborted` with
`lastExecution.reason: AlreadyExecuted` and the message "eligible trigger episode already has
execution evidence". Correct deduplication of a repeated episode reading as a terminal *failure* is
suspicious, but the failing resume-state write above is a plausible confounder, so this is recorded
as a question rather than a finding until `F-100` is fixed and it can be retested cleanly.
