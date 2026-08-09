# Planner Requirements

Components: Planning & Execution Logic, Capability Profiles.

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

**PL-5** · Flow spec: groups, `requires` / `before` / `after` edges, phase hints, timeouts,
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

The nine compilation steps in `docs/shutdown-flow.md` remain. These are additions and amendments.

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

**PL-22** · Resolve last-ditch roles into terminal ordering constraints. "Must stay until phase X"
is a role in input and a set of edges in output. The phase taxonomy is the numbered-tier scheme
(OD-4, closed): tier N+1 → tier N compiles to derived edges labeled per PL-15; explicit `requires`
still orders within a tier; tier 0 members are excluded from flow targeting entirely and any flow
that targets one is rejected.

**PL-23** · Enforce quorum. With an HA control plane, no wave may drop the control plane below quorum
while later waves still require orchestration. Hard constraint. This is the mechanism behind
"minimum viable control plane."

**PL-24** · Enforce explicit control-plane ordering. Control-plane nodes carry explicit late
dependencies rather than relying on a high phase number. Reject or warn when they do not.

**PL-25** · Detect co-wave contention, and act on it. Two groups with no dependency edge between them
share a wave and execute concurrently; if both target workloads on the same node, concurrent draining
can violate a PodDisruptionBudget or overwhelm the node. Absence of an authored edge is not proof of
independence.

Detection alone is insufficient. Behavior is governed by a policy field with three settings:

| Setting | Behavior |
| --- | --- |
| `Warn` (default) | Emit a diagnostic; leave the wave as compiled |
| `Serialize` | Insert a derived ordering edge, labeled per PL-15, and recompile |
| `Reject` | Fail the compile |

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
inputs, and on a periodic floor.

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

## Open Decisions

OD-4 is closed (numbered tiers; see `scope-boundaries.md` change log 2026-08-03).

OD-2 and OD-3 are closed by `inventory-provider-contract.md`. There is one entity set with two edge
relations; the logical shutdown graph is compiled output, not a third input.

OD-1 and OD-5 are closed by the published-artifacts boundary: recovery is external subscriber scope,
and startup waves are advisory projections rather than operator-executed recovery.

**OD-16 · Missing `carries` coverage.** Whether a node with no modeled communication path is a hard
validation failure or requires an explicit exemption marker, mirroring PL-44. Silent-assume is
excluded.

**OD-8r · Provider key validation.** Resolver behavior when the topology provider supplies a
malformed or missing model string: reject the device, floor-match with warning, or configurable.

**OD-15 · Probe history persistence is closed.** Profile drift detection writes "last verified
against firmware X" and mismatch evidence to PostgreSQL capability profile verification records,
not CR status.

**OD-14 · Plan scope under partial-domain outage.** The design targets multiple UPS devices and
multiple power domains, and triggers already reference `powerDomains`. If one domain loses power
while another does not, the plan scope policy defines whether execution is cluster-wide or a
domain-scoped subgraph. Partial-domain outage is a realistic scenario for the multi-UPS design and
is handled explicitly rather than inferred from topology alone. This policy shapes PL-16 semantics
and PL-23 quorum enforcement, since a domain-scoped plan may need to reason about control-plane
members outside its own domain.

No longer blocked on missing structure: derived domains (IN-7) plus input-qualified `feeds` edges
(IN-4) supply the data this decision needed. Only the policy choice remains.
