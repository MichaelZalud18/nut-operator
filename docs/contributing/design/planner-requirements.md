# Planner Requirements

Components: Planning & Execution Logic, Capability Profiles.
Audience: contributors.

This document defines the requirements for the planner package, the "decide" stage of
`nut-operator`.

Companion to `scope-boundaries.md`. `PL-n` identifiers are stable and are not reused or renumbered.

## Position in the System

The system splits into **detect → decide → act**. The planner is only "decide."

| Stage | Owns | I/O |
| --- | --- | --- |
| Resolver (detect) | Kubernetes reads, NUT telemetry, capability profile loading, topology provider resolution, inventory merge | Yes |
| **Planner (decide)** | **Graph construction, validation, wave compilation, feasibility** | **No** |
| Executor (act) | Actuation, eviction, node release, audit writes | Yes |

Everything the planner needs arrives as a resolved input bundle. This is what makes it unit-testable
in isolation and what makes audit correlation possible.

---

## Architecture

**PL-1** · Pure function. `Compile(StructuralInputs, TelemetryInputs) → (Plan, Diagnostics, error)`.
No Kubernetes client, no NUT client, no database handle, no filesystem access, no network access.

**PL-2** · Clock is injected. No `time.Now()` in the package. Durations are arithmetic over input
values.

**PL-3** · No ambient state. No environment reads, no hostname resolution, no randomness, no global
mutable state.

**PL-4** · The resolver owns all I/O upstream. The executor owns all actuation downstream. Neither
concern leaks into the planner.

---

## Inputs

Inputs are partitioned into two bundles. The partition is load-bearing — see PL-42.

### Structural bundle

Slow-changing. Hashed, determinism-tested, staleness-checked.

**PL-5** · Flow spec: groups, `requires` / `before` / `after` edges, shutdown tiers, timeouts,
trigger declarations, abort policy.

**PL-6** · Topology bundle: the `feeds` and `carries` edge sets over the inventory entity set, with
input qualifiers on `feeds`. Power domains are **not** supplied — they are derived by transitive
closure over `feeds` from each `UPSDevice` root, computed in the pure package alongside capability
matching. See `inventory-provider-contract.md`.

**PL-7** · Resolved capability profiles, one per device, already matched by the resolver via the
deterministic precedence chain (exact model+firmware → exact model → model glob → driver family →
unidentified-device profile; CRD source over bundled within a tier; highest semver within a source). The planner
consumes matched results and never performs matching. Matching is pure logic and lives in its own
package under the same determinism discipline as the planner; the resolver calls it.

**PL-9** · Node inventory with roles: control-plane membership, quorum requirements, last-ditch role
assignments.

**PL-10** · Workload inventory sufficient for node-clearance reasoning and PodDisruptionBudget
awareness.

### Telemetry bundle

Continuously changing. Never hashed, never part of plan identity.

**PL-8** · Power state snapshot per power domain: runtime remaining, battery charge, on-battery
duration, each carrying an explicit confidence and staleness marker.

### Common

**PL-11** · Every input carries a source identifier and observation timestamp so diagnostics can
attribute a rejection to the input that caused it.

**PL-42** · The structural/telemetry partition is enforced at the type level, not by convention.
Telemetry values must be structurally incapable of reaching the hash computation in PL-14. Without
this, plan identity changes on every telemetry tick, determinism testing becomes impossible, and
the revalidation check in PL-31 can never pass.

---

## Outputs

**PL-12** · Compiled waves: ordered, each carrying its concurrent group set, per-group timeout, wave
duration, and cumulative duration. Extends the existing `status.compiledWaves`.

**PL-13** · Flattened review view, parallel to `status.compiledSteps`.

**PL-14** · Plan identity: a deterministic hash over the **structural** input bundle plus the emitted
plan. Required for audit correlation across restarts and for the revalidation check in PL-31. The
actuator handoff file already carries flow identity; this is the key that makes it resolvable.

Target identity includes selector contents, explicit namespaces, full workload references, and
agent references, not just selector presence or reference counts. Set-like field ordering does
not change identity. Compilation establishes the current structural hash before selecting
execution history; previous status is not a compatible-history key after a target edit. Observed
duration estimates remain outside identity, so applying matching history cannot move that hash.

**PL-15** · Edge provenance. Every edge in the compiled graph is labeled authored or derived, and
derived edges name the rule that produced them. This is what makes "why was this node in wave four"
answerable from stored structure per SB-12 rather than from log archaeology.

**PL-16** · Feasibility verdict, per power domain — **advisory at compile time, authoritative at
trigger time**. Three states: `Feasible`, `Infeasible`, `Unknown`. A compile-time verdict computed
while on wall power reads against a full runtime budget and carries little meaning; it is emitted
for review, not for decisions. The binding verdict is recomputed against live telemetry when a
trigger fires. Both verdicts are recorded.

**PL-17** · Structured diagnostics. Rejections and warnings cite the specific groups, edges, or
devices involved. "Cycle detected" is not acceptable output; "cycle: applications → databases →
applications" is.

**PL-18** · Abort-policy annotation. Groups eligible under `abortPolicy.behavior: ContinueSafeSteps`
are marked in the compiled plan, not resolved at execution time.

**PL-45** · Published plan artifact. The planner returns a single structured artifact containing
the compiled execution plan, dependency graph, shutdown waves, advisory startup wave projection,
diagnostics, feasibility verdicts, plan hash, and duration estimates. Status, audit storage, and
rendered diagram outputs are all views of this artifact.

**PL-46** · Dependency graph artifact. The graph is emitted as normalized vertices and edges, not as
formatted text. Every edge carries relation type, source object references, provenance
(`Declared`, `Derived`, or `Policy`), and a stable explanation string.

**PL-47** · Startup waves are advisory projections. The shutdown plan is authoritative for
execution. Startup wave projections are published so recovery systems can consume the same topology,
but `nut-operator` does not execute recovery or own bring-up orchestration.

**PL-48** · Diagram renderers are deterministic exports. Mermaid, Graphviz/DOT, and D2 renderings
are generated from the structured graph artifact. They are conveniences for visualization and AI-
assisted diagramming, never independent sources of truth.

---

## Compilation

The compilation steps in `docs/contributing/design/shutdown-flow.md` remain. These are additions and amendments.

**PL-19** · Trigger-capability validation. Validate every declared trigger against the resolved
capability profiles of all devices in the referenced power domains. A `RuntimeBelow` trigger aimed at
a device whose driver never reports runtime fails silently, during an outage, at the one moment
nobody is watching.

Resolution of the PL-19/PL-33 interaction:

- A trigger unsatisfiable by **some** devices in a domain degrades to a coarser trigger class and
  emits a warning. The compile succeeds and the plan is marked degraded. Degrade mechanics — which
  coarser class substitutes, and whether substitution is automatic or requires a declared fallback
  trigger on the flow — are specified in the capability schema doc.
- A trigger unsatisfiable by **every** device in a domain is a hard rejection. A plan whose triggers
  can never fire is not a degraded plan; it is a non-functional one.
- Use of a fallback profile (PL-33) does not by itself escalate a warning into a rejection. The
  above two rules apply identically whether the profile was resolved or fell back.

**PL-20** · Derive node-clearance edges. Nodes are terminal vertices and cannot power off until
assigned workloads, storage operations, and cluster responsibilities have cleared. These must be
emitted as actual graph edges, not left as prose the executor is trusted to honor. Subject to
execution-time revalidation per OD-11.

Clearance is derived from group-to-node membership resolved before compilation, since expanding a
selector requires reading the cluster and the planner is pure. Nodes a group *acts on* come from
matching its node selector against real node labels; nodes a group *releases* come from
`NodePowerAgent.status.selectedNodes` through `target.agentRefs`, which is the same resolution the
executor performs at release time — deriving it differently would let the plan disagree with what
execution does. For each node, every group acting on it is ordered before the group releasing it.
The edges enter the same graph wave compilation reads, so they change execution order rather than
describing it. Absent membership, clearance derivation is skipped and declared ordering stands
alone. A group that both acts on and releases a node yields no edge: the ordering is internal to
that group's own action sequence.

**PL-21** · Derive communication-path edges. A network device carrying the control-plane or NUT path
for node N cannot precede N in shutdown order. The communication path is modeled as `carries` edges
per IN-5; OD-3 is closed.

This is a v1 requirement regardless of whether the communication device is an actuation target.
Its supplying UPS can expire and remove the path without an operator-issued shutdown. Planning
must account for `carries` dependencies together with supplying `feeds` paths and power domains
when ordering and budgeting dependent shutdown work. Publish the derived constraints and their
provenance, including unresolved coverage, in the planner artifacts. Switch actuation and PDU
outlet control remain separate scope decisions, not prerequisites for this requirement.

Domain scoping follows `carries` transitively from entities in affected power domains. A
consumer can remain in scope even when its own supply is unaffected. Multiple supplying
domains do not establish failover or redundant-path guarantees: membership in any affected
domain is sufficient for conservative retention. A carrier without a resolved supplying
domain also retains its consumers and produces `CommunicationPowerDomainUnknown`. These
dependencies participate in structural identity, and `CommunicationPowerDependency`
explanations identify the authored edge source and supplying domains. This scoping rule does
not itself establish a runtime deadline.

For carriers that are node-release targets, derived `CommunicationPath` group edges place
all resolved dependent work, including dependent release, before carrier release. Traversal
includes non-actuated transit devices without inventing shutdown targets for them. Each
constraint publishes a deterministic shortest path as ordering evidence; contributions from
different dependent nodes to the same group edge are combined. A cycle against declared
ordering or tiers is rejected. A single action targeting both carrier and dependent is rejected
as `CommunicationReleaseConflict`: split carrier release into its own group or step. Linear
flows preserve declared order and reject inversions as `CommunicationReleaseOrderInvalid`.

Domain scoping also retains consumers of carriers released by retained mixed-domain groups.
This expansion repeats until no further consumers are brought into scope, before pruning groups.
The order constrains action completion and signal publication, not physical halt acknowledgement.
Carrier power-loss runtime constraints remain separate from release ordering; a switch does not
need to be an actuation target for its supply to constrain the plan.

`ShutdownFlow.spec.communicationPaths` binds the shared `OperatorAPI` and `NUT` paths to required
inventory entity IDs. Include the endpoints and carriers the flow needs; their transitive
`carries` dependencies are included automatically. These are conjunctive requirements, not
alternative routes or a promise of failover. Inventory remains the owner of `feeds` and `carries`;
the flow only declares which entities provide its shared services. This is authored topology,
not Kubernetes Service discovery, pod-placement protection, or a network reachability probe.

Every compiled action depends on each declared shared path, including actions with no resolved
node targets. A shared-path outage retains all work across domains. A retained action releasing
a shared carrier also retains that work. Shared carrier releases must follow all other actions;
separate, mutually dependent service-release groups are rejected. A terminal release group may
publish signals for its shared carriers, but this does not promise manager availability after
that handoff. Normal carrier/dependent same-action conflicts remain rejected. Under `Overlap`,
carrier-release waves drain previously overlapped work before dispatch and surface its failures;
unrelated waves can still overlap. `Preempt` retains the author's existing cancellation policy.

Runtime budgeting includes the UPS devices supplying upstream communication carriers for the
compiled actions' resolved node targets and declared service entities themselves, alongside the
UPS devices selected by the trigger. Work without resolved node targets conservatively includes
all modeled carriers while either shared service has unmodeled coverage. Once both services are
declared or explicitly exempted, node-less actions use those shared paths instead of unrelated
carriers. An exemption acknowledges intentionally omitted coverage; it is not a safety assertion.
The whole-plan supply set is retained
through execution, and telemetry is reread at every wave boundary. Online devices do not impose
battery-runtime deadlines or reduce trust in other devices' estimates. Active supplies must have
trusted runtime capabilities; stale, unreadable, or unresolved supply leaves the budget unknown.
Partial readings cannot prove recovery while an unknown supply remains. Execution still runs
under the existing unknown-runtime timing rules, and feasibility warnings use the same reduction.
These additional timing inputs change neither trigger provenance nor structural plan identity.

Publish node and service coverage as `Modeled`, `Unmodeled`, or `Exempt`, including service entity
references. Missing declarations and node paths produce `CommunicationPathUnmodeled` warnings,
even when there are no authored `carries` edges. Node `communicationPathExempt` suppresses that
warning but never removes an existing modeled path or supply constraint. An explicit unknown
service entity is a structural error (`CommunicationServiceEntityUnknown`), unlike an omitted
declaration. Service declarations, exemptions, and modeled paths participate in plan identity;
live telemetry does not. Physical halt acknowledgement and control-plane quorum enforcement
remain separate contracts.

**PL-20a** · Report and block tier inversion. A group whose tier is lower than the tier of a node it
runs on is scheduled to keep working after that node powers off. Compilation reports this as
`ShutdownTierInversion`, naming the group, the node, and both tiers, and withholds the node from
power-off for the whole flow. The withheld nodes are emitted on the plan and published on
`ShutdownFlow.status.blockedNodeReleases`.

Blocking is the default because its failure mode is powering off less of the cluster than intended,
while the alternative cuts power to work the author declared as still needed. A group sets
`tierInversionPolicy: Allow` to accept going down with its node; the inversion is still reported, as
`ShutdownTierInversionAllowed`, because opting in accepts a risk rather than retiring it. One
dissenting group is enough to hold a node up, since powering it off would cut power to the group
that did not accept it. Migration is declined as a general remedy: node-local storage means there is
not always anywhere to migrate to (OD-18).

**PL-20b** · Report defaulted tiers. A group that declares no tier and inherits the cluster default
is reported informationally as `ShutdownTierDefaulted`. Defaulting is legitimate; silence about it
is not, because a mistyped tier label is otherwise indistinguishable from a deliberate default.

Planner diagnostics reach the `ShutdownFlow`: warnings degrade the flow with the diagnostic's own
reason, and every diagnostic including informational ones is recorded in the compilation audit row
under source `Planner`.

**PL-22** · Resolve last-ditch roles into terminal ordering constraints. "Must stay until the very
end" is a role in input and a set of edges in output. The taxonomy is the numbered-tier scheme
(OD-4, closed): tier N+1 → tier N compiles to derived edges labeled per PL-15; explicit `requires`
still orders within a tier; tier 0 members are excluded from flow targeting entirely and any flow
that targets one is rejected.

**PL-23** · Enforce quorum. With an HA control plane, no wave may drop the control plane below quorum
while later waves still require orchestration. Hard constraint. This is the mechanism behind
"minimum viable control plane."

**PL-24** · Enforce explicit control-plane ordering. Control-plane nodes carry explicit late
dependencies rather than relying on a low tier number alone. Reject or warn when they do not.

**PL-25** · *Retired 2026-08-17, never implemented — the failure it described cannot happen.* It
required detecting "co-wave contention": two groups sharing a wave, both targeting workloads on the
same node, where concurrent draining was said to risk violating a PodDisruptionBudget or overwhelming
the node. It specified a three-setting policy field (`Warn`/`Serialize`/`Reject`) to govern it.

Both halves of the premise were wrong. `DrainNodes` evicts through the Eviction API
(`internal/kubeactions/runner.go`), and that API is where PDBs are enforced — the apiserver refuses
an eviction that would breach a budget. Concurrency cannot produce a violation through it. And a PDB
selects one workload's pods, which one group selects; two groups reaching the same budget requires an
unusual authoring shape, and even then produces contention rather than breach. "Overwhelm the node"
inverts the effect: draining removes load, and during a cluster shutdown the evicted pods have
nowhere to reschedule to because every candidate is already cordoned.

The real relationship runs the other way, and is `OD-38`: a PDB does not endanger the shutdown by
being violated, it endangers the shutdown by holding. Number burned rather than reused.

**PL-26** · Compilation is atomic. It fully succeeds or fully fails. No partial plan is ever emitted.

---

## Determinism

**PL-27** · Identical **structural** inputs produce byte-identical plans and identical plan hashes.
Sorted iteration everywhere; no map-order leakage into wave membership or edge lists.

**PL-28** · Determinism is a test target, not an aspiration. Compile twice within the test suite and
assert equality.

This buys three things: audit records that can be trusted, plan-to-plan diffing that shows what a
topology change did to ordering, and reproducible fixtures.

---

## Staleness and Revalidation

**PL-29** · Plans carry their structural input hash. Staleness is detectable rather than assumed.

**PL-30** · Recompile on spec generation change, on watch events against referenced structural
inputs, and on a periodic minimum interval.

**PL-31** · Revalidate immediately before execution. If the structural input hash no longer matches
at trigger time, the planner does **not** simply refuse.

The sequence is:

1. Attempt recompilation within a bounded time budget.
2. If recompilation succeeds, execute the fresh plan.
3. If recompilation fails or exceeds its budget, a policy field decides between executing the stale
   plan and refusing.

Refusal is never the silent default. A cluster that dies ungracefully because it declined to run a
slightly stale plan is a worse outcome than executing one. This is the same failure class as OD-12
and takes the same shape of answer.

**PL-44** · Orphan validation. Every Kubernetes node must be reachable from at least one
`UPSDevice` via `feeds` edges, or carry an explicit exemption marker excluding it from power
planning. Neither reachable nor exempted is a hard validation failure. This is the guardrail that
makes derived domain membership safe — without it, one unrecorded edge silently drops a node out of
every domain, no trigger covers it, and it hard-drops during an outage with nothing in any log.

**PL-43** · Node-clearance edges derived under PL-20 are computed from compile-time workload
placement, which execution-time instance enumeration can invalidate. Node clearance is revalidated
at execution alongside instance resolution, per OD-11.

---

## Degradation

**PL-32** · Missing or stale data never yields an optimistic verdict. Absent runtime telemetry
produces `Unknown` feasibility, never `Feasible`.

**PL-33** · A device that matches no specific profile matches the **unidentified-device profile** —
the least-specific selector in the precedence chain, bundled with the operator and guaranteed to
always match — and raises a warning. This is not a special case; it is the terminal tier of the
matching algorithm. It does not by itself fail the compile. Interaction with PL-19 is specified
under PL-19.

**Amended 2026-08-05 (OD-31).** Compiling is not the same as enforcing. Matching this profile means
nothing has been verified about the device: some NUT driver answered, and no product profile claimed
it. Earlier wording treated that as a reduced-capability device, which it is not — UPS behavior
varies too widely for an unverified device to be a safe basis for powering nodes off.

The compile still succeeds, so the plan stays reviewable in dry-run, which is where an operator
discovers the gap. What changes is enforcement: a `ShutdownFlow` in `Enforce` mode whose triggers
depend on an unidentified device is rejected, with the devices named, unless
`spec.safety.allowUnidentifiedDevices` records acceptance. This is a configuration-time refusal
visible in Git and `/status` well before an outage, not the mid-outage refusal PL-31 warns against.
Naming follows: "universal floor" implied a guaranteed capability baseline and is retired.

**PL-34** · Planning succeeds against partial input where structurally possible. The plan is marked
degraded and the degradation reasons are enumerated.

OD-14 applies that conservative rule to partial-domain outages. A trigger naming `powerDomains` or a
UPS device that maps to a resolved domain defines the configured preflight scope. When triggers
are eligible, execution uses the evaluator's eligible UPS roots instead of the union of configured
trigger scopes. Held-but-not-yet-eligible devices do not expand that scope. This also applies to
unscoped triggers: only the roots that actually satisfy them select execution domains.

The configured plan is validated before compiling the execution subgraph. Grouped and linear
plans omit only actions whose resolved node membership is proven wholly outside the affected
domains; dependencies on omitted groups are removed, and linear steps keep their authored order.
Mixed, missing-target, and unmapped membership stays in scope, including an action referencing
both a resolved agent and one without node coverage. Incomplete agent membership also retains
the conservative communication supply envelope even with explicit shared-service exemptions.
Communication-dependent consumers under PL-21 also remain in scope. An eligible root without a
resolved domain disables pruning and emits
`ExecutionPowerDomainUnknown`. An empty execution-root selection is rejected, never interpreted
as permission to execute everything. A fully pruned plan is rejected as `PlanRequired`; ignored
linear fallback steps never become active because all groups were pruned.

The discrete execution-root set participates in structural identity, while live runtime values,
hold timestamps, and observed durations do not. History is selected using the execution plan's
hash. Published waves, graph, feasibility, and trigger evaluation refer to that same plan. With no
eligible trigger, status shows the configured preflight plan; explicit rehearsal retains that
configured scope. A changed eligible set produces a new plan on reconciliation, not an in-flight
change to a running plan. Action idempotency remains the contract for repeated work. Execution
resolves only compiled actions, so pruned agent, hook, or selector reads cannot block another domain.

---

## Non-requirements

**PL-35** · The planner does not execute anything.

**PL-36** · The planner validates trigger *definitions*. The controller evaluates trigger
*conditions*. Separate concerns, separate code paths.

**PL-37** · `DryRun` versus `Enforce` is not the planner's concern. It always compiles. Gating lives
in the controller per GP-2.

**PL-38** · The planner does not write to PostgreSQL or to Kubernetes. It returns values; callers
persist them.

**PL-49** · The planner does not own a UI. Kubernetes status, Events, logs, PostgreSQL records, and
deterministic graph exports are the v1 interfaces. A future UI consumes these artifacts as an
external subscriber.

---

## Testability

**PL-39** · Table-driven fixtures mapping input bundle to expected plan, with golden-file outputs.

**PL-40** · Property tests: output contains no cycles; every input group appears exactly once; no wave
depends on a later wave; quorum holds at every wave boundary.

**PL-41** · Fuzz the graph builder against malformed and adversarial edge sets.

---

## Capability Resolution Model

*Components: Capability Profiles.*

Settled during capability schema deconfliction. Recorded here because it constrains planner inputs.

**CR-1** · Declaration is authoritative; probing is advisory. The planner and the trigger path see
only declared profiles. Runtime probing of NUT variables runs as a reconciliation-time drift
detector: probe, compare against declaration, raise a condition on mismatch. Probe results never
feed the planner and never automatically demote a profile — auto-demotion would reintroduce a
runtime dependency into the failure path. The correction loop is probe → condition → human profile
fix → structural input change → recompile per PL-30.

**CR-2** · Trigger support is derived, not declared. Profiles declare NUT variables (telemetry
section) and behaviors/quirks (actuation section). Trigger-class support is derived from declared
variables through a mapping table owned and versioned by the operator, not by the profile schema. A
profile cannot claim `RuntimeBelow` support directly; it can only declare `battery.runtime`, and the
operator decides what that implies.

**CR-3** · Profile corrections that reduce declared capability are MAJOR version bumps, even though
they are behaviorally fixes. Plans validated against the prior version may fail PL-19 under the
corrected one.

**CR-4** · A runtime figure is trustworthy only on an affirmative `Dynamic` declaration. Profiles
declare `spec.telemetry.runtimeEstimate: Dynamic | Static`, read through
`capability.MatchResult.SupportsTimingAdaptation()`. `Dynamic` permits a device's `battery.runtime`
to drive graduated decisions; `Static` forbids it; **absence forbids it while asserting nothing about
the device.**

Unset is unverified, and unverified is not `Dynamic`. Both consumers reason about how much time is
left, so both need the number to actually fall as the cluster draws on the battery — and assuming the
good case would drive them from a constant at the one moment nobody is watching.

Two consumers today, and they degrade the same way rather than refusing:

- **Advisory feasibility** (PL-16) answers "is there enough runtime to finish", which a fixed estimate
  cannot support. A domain containing a `Static` device compiles to `Unknown` with reason
  `RuntimeEstimateStatic`, naming the device. This is PL-32 applied literally: a value that cannot
  move is missing data with a plausible number attached.
- **Timing compression** (EX-11) needs a measured runtime to compute against. Without trust the
  graduated middle mode is simply unreachable, leaving only "as declared" on mains and "most urgent"
  on battery — which *is* the prohibition, expressed through the observation rather than a second
  mechanism that could disagree with it.

Absence deliberately changes nothing about existing deployments. Every profile shipped today leaves
it unset, and treating that as `Static` would downgrade all of them on no evidence.

Previously carried as a provisional `AE-6`, which collided with the abort requirement of the same
number; it is capability resolution, not adaptive execution, and is numbered here accordingly.

---

## Resolved Decisions

**OD-11 · Selector resolution timing — hybrid.** The graph and its ordering compile ahead of time.
Concrete workload instances enumerate at execution. Consequence: every duration estimate in a
compiled plan is an approximation and must be labeled as such in the plan rather than implied.
Node-clearance edges revalidate at execution per PL-43.

**OD-12 · Infeasible-plan behavior — policy field.** When plan duration exceeds the runtime budget,
behavior is configured, not hardcoded: reject, emit with warning, or emit a truncated best-effort
plan. Rejecting during an actual outage is the worst available outcome, so the default must not be
rejection.

**OD-13 · Load-shedding granularity — node-level baseline.** "What does shedding X buy me" requires
per-workload power draw. The baseline therefore reasons at whole-node granularity — "power off this
node early, gain N minutes" — and treats per-workload attribution as a separate data-source
extension.

---

**OD-7 · Profile storage — closed.** Profiles are CRDs plus bundled operator data. NetBox is
referenced from, never maintained in; at most it carries a custom field pointing at a profile name.

**OD-8 · Merge precedence — dissolved.** There is no field-level merge. The topology provider
supplies matching keys (model, firmware); profiles supply match selectors and capabilities. It is a
lookup, not a merge. Residue: validation rules for malformed or missing model strings from the
provider — a resolver concern, tracked as OD-8r.

**OD-9 · Trigger-capability mismatch — resolved in structure, folded into capability schema.** The
some/all split in PL-19 answers reject-versus-degrade. Remaining degrade mechanics are capability
schema doc content, not a standalone decision.

## Decisions affecting the planner

Both open and closed, because the closed ones are what keep the open ones from drifting. `OD-8r` is
the only entry below that is still open.

OD-4 is closed (numbered tiers; see `scope-boundaries.md` change log 2026-08-03).

OD-2 and OD-3 are closed by `inventory-provider-contract.md`. There is one entity set with two edge
relations; the logical shutdown graph is compiled output, not a third input.

OD-1 and OD-5 are closed by the published-artifacts boundary: recovery is external subscriber scope,
and startup waves are advisory projections rather than operator-executed recovery.

**OD-16 · Missing `carries` coverage — closed as a warning plus an explicit exemption.** A node with
no modeled communication path raises the `CommunicationPathUnmodeled` warning and can be opted out
with `communicationPathExempt`. It is deliberately not the hard failure that a missing `feeds` path
is (`PowerPlanningOrphan`). Missing coverage limits the communication-safety constraints the
planner can derive under v1's `PL-21`; the warning must expose that limitation rather than imply
the path is safe. Silent-assume stays excluded. Full reasoning in
`inventory-provider-contract.md`.

**OD-8r · Provider key validation.** Resolver behavior when the topology provider supplies a
malformed or missing model string: reject the device, fall back to the unidentified-device profile
with a warning, or configurable.

**OD-15 · Probe history persistence is closed.** Profile drift detection writes "last verified
against firmware X" and mismatch evidence to PostgreSQL capability profile verification records,
not CR status.

**OD-14 · Plan scope under partial-domain outage — closed.** Derived domains (IN-7) plus
input-qualified `feeds` edges (IN-4) supply the membership this policy needs. The planner scopes by
pruning only groups proven wholly outside the affected power domains; it keeps ambiguous, mixed, or
global groups rather than assuming them safe to omit.
