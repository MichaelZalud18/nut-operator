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
| L4 Deep Insights | Metrics, alerts, log processing, workload analysis | Not met — no custom metrics registered |
| L5 Auto Pilot | Auto-scaling, auto-config tuning, auto-remediation | Out of scope by design (GP-1: non-power triggers excluded) |

Notes specific to this project:

- **L5 is deliberately not a target.** Auto-remediation of node or service health is excluded by
  GP-1 and SB-6. State this in the README so the repo is not graded against a level it declines.
- **L3 is the real gap and the most consequential.** An operator that shuts a cluster down but has
  no defined recovery story sits at L3-incomplete permanently until OD-1 resolves.
- **L4 is cheap and currently zero.** See audit below.

Check at: every minor release, and before any `v1beta1` promotion.

## Benchmark 2 — Kubebuilder / controller-runtime conventions

Mechanical, checkable, and where most reviewer friction lands.

| Convention | Position |
| --- | --- |
| Idempotent reconcile | Assumed, unverified — no partial-failure convergence test found |
| No in-memory state across reconciles | Appears held; planner purity helps |
| Status subresource for observed state only | Met — GP-3 enforces it by design |
| `observedGeneration` tracking | Met — present in all 9 API types and every controller |
| Standard condition types with machine-readable reasons | Mostly met — see audit |
| Finalizers for cleanup | **Met (2026-08-04)** — `NUTServerReconciler`/`NodePowerAgentReconciler` carry finalizers; owner-reference GC for the other 7 was verified never needing one (no Kubernetes child resources rendered). |
| Status writes use `Patch`, not read-modify-write `Update` | **Not met anywhere** — all 9 controllers call `Status().Update()` exclusively, zero `Status().Patch()`. Confirmed as a live, observed bug for `ShutdownFlow` specifically (10h production log, 744 conflicts) but the vulnerable pattern is universal; the other 8 just reconcile too infrequently for it to have surfaced yet. See 2026-08-04 audit. |
| `RequeueAfter` over sleeps | **Met — confirmed clean.** Zero `time.Sleep` calls in any controller. |
| Leader election | **Active in every real deployment** — `config/manager/manager.yaml` passes bare `--leader-elect`, which Go's `flag` package treats as `true`. The code default is still `false`, which only matters if a manifest ever drops the arg. |

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
`Status().Patch(` returns 0 everywhere. `docs/tasks.md` already documents the observed consequence
for `ShutdownFlow` (10h production log, 744 `"the object has been modified"` conflicts) and correctly
root-causes it to the combination of an unpredicated high-frequency watch plus `Update` instead of
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

- `F-2`, previously flagged as the top item in "Recommended order" below — corrected 2026-08-04, see
  `docs/tasks.md`. Leader election is active in every real deployment via the manifest's bare arg;
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

- Any new controller or CRD.
- Before `v1beta1` promotion (Benchmark 3 becomes binding).
- After OD-1 resolves (determines whether L3 is reachable).
- Before first release with `ActuatorPolicy: SystemdPoweroff` enabled by anyone.

## Fixes applied — 2026-08-04 (same day)

Items 1, 2, and 4 from the recommended order above were implemented and verified (build, vet, full
test suite including new specs, `make manifests`, ASH) the same day this audit pass ran. Full detail in
`docs/tasks.md`'s Operator Maturity & Hardening Built section; summary here for the audit trail:

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

Still open from this list: `F-31` (`Status().Patch()`), `F-3` (metrics), `F-32` (Pod watch cache
scoping), `F-7` (idempotency tests) — unchanged, see `docs/tasks.md`.

A genuine, unrelated finding surfaced while building a new CI check during this same pass: a real
private IP address (a device from this session's private-repo work) had been committed to this public
repo's test fixtures in an earlier commit (`70bb81f`). Fixed in the working tree; still visible in git
history. See `docs/tasks.md`'s Operator Maturity & Hardening Built section and `security.yml`'s new
`private-ip-scan` job.
