# Glossary

Components: Cross-cutting.

The words this project uses, and the ones it deliberately does not. Where two terms are easy to
confuse — **tier** and **wave** above all — the entry says which is input and which is output,
because that is the distinction most often gotten wrong.

This is the single definition of each term. [decision-index.md](../contributing/design/decision-index.md) links
here rather than repeating it.

**Power domain** — the transitive closure of `feeds` edges from a `UPSDevice` root. Derived, never
declared; named on the UPS. A node can belong to more than one (IN-7, IN-11).

**feeds / carries** — the two edge relations. `feeds(A→B)`: A powers B; loss means B is on battery.
`carries(A→B)`: A transports B's NUT/control path; loss means B is unobservable but powered. They
drive opposite planner behavior and are never conflated (IN-3).

**Group** — the unit of shutdown policy in a `ShutdownFlow`: a selector-targeted set of workloads
or agents with an action, relationships, and a timeout.

**Wave** — a compiled set of groups eligible to execute concurrently. Waves are ordered; execution
is wave-by-wave (PL-12, EX-10). A wave is *output*: the planner derives it from tiers and edges.
Nobody writes a wave.

**Phase** — overloaded, and the source of more confusion than any other word here. It means two
unrelated things, neither of which is an ordering concept:

1. `UPSDevice.status.phase` — the device's power state: `Online`, `OnBattery`, `LowBattery`.
2. `ShutdownFlow.status.phase` and `status.lastExecution.phase` — lifecycle state: `Pending`,
   `Compiled`, `Running`, `Completed`, `Aborted`, and so on.

There was a third. `spec.groups[].phase` was an integer that arrived with the initial scaffold and
was never designed, and it was removed in `v1alpha1` on 2026-08-17. It described itself as a
tie-breaking hint and behaved as a hard wave partition: groups shared a wave only if their phase
numbers matched, so two independent same-tier groups were serialized silently and the plan was
charged the sum of their timeouts rather than the longest. Nothing replaced it, because tiers plus
`before` / `after` already covered every ordering it could express.

Never use "phase" to mean ordering. Ordering across tiers is a **tier**; a set of concurrent work is
a **wave**.

**Stage** — reserved for the detect / decide / act split (resolver / planner / executor). It is a
pipeline position, never a point in a shutdown sequence. "Stages of power distribution" — a UPS
feeding a PDU feeding a rack — is a topology property, unrelated to both meanings; the simulation
scenario named for it says so in its own README.

**Structural vs telemetry input** — the load-bearing partition of planner input. Structural:
slow-changing, hashed, plan-identity-bearing. Telemetry: continuous, never hashed, feeds
feasibility only (PL-42).

**Plan hash** — deterministic hash over structural inputs plus emitted plan; the correlation key
across CR status, the signal file, and audit records (PL-14).

**Published artifact** — structured planner or executor output exposed for consumers: compiled
plan, dependency graph, waves, trigger decisions, explanations, progress, and renderable graph
views.

**Subscriber** — an external consumer of published artifacts, such as recovery orchestration,
dashboards, documentation generators, monitoring systems, or future automation. Subscribers do not
own shutdown planning or host actuation.

**Capability profile** — versioned artifact declaring a device's NUT variables (telemetry section)
and behaviors/quirks (actuation section). Matched, not merged; declaration authoritative, probing
advisory (SB-9, CR-1, CR-2).

**Unidentified-device profile** — the least-specific capability profile, bundled with the operator,
guaranteed to match. The terminal tier of the matching chain, not a special case (PL-33). Matching it
means nothing is known about the device, not that the device has a known minimum capability, which is
why it blocks enforcement under OD-31. Formerly called the "universal floor"; renamed 2026-08-05
because that name implied a capability guarantee it never provided.

**Last-ditch** — the role marking services and nodes that must survive until a given shutdown
phase; under HA, the minimum viable control plane. Concretely: tier 0 in the numbered-tier
scheme (OD-4, closed).

**Shutdown tier** — integer coarse-ordering label on namespaces, workloads, and nodes. Tier 0 =
last-ditch, workload-only. Tier 1 = final orchestrated stop, lowest valid for nodes. Tier 2+ =
progressively earlier. Lower shuts down later. Not comparable across kinds — workload-to-node
ordering comes from clearance edges (PL-20). Compiled into derived edges; `requires` orders within
a tier.

**Tier pointer** — the executor's record of how far down the tier sequence a shutdown has
progressed. Descends as power degrades and waves execute; ascends as bookkeeping only, restoring
nothing (EX-25 – EX-27).

**Signal file** — the structured in-pod handoff from executor authority to host actuation:
timestamp, reason, UPS identity, flow identity, plan hash. The actuator rejects stale files
(EX-16).

**Dry-run** — full execution minus effects. Sequencing, enumeration, clearance, and evidence all
run; only mutation and actuation are suppressed (EX-5).

**Orphan rule** — every node is `feeds`-reachable from a UPS or explicitly exempted; neither is a
hard failure (PL-44, IN-12).

**Detect / decide / act** — the three-stage split: resolver / planner / executor. All input I/O in
detect, pure computation in decide, all effects and evidence in act.

**Provider contract** — the normalized inventory shape all topology providers emit; owned by the
operator, informed by NetBox, transcribing neither (inventory-provider-contract.md).
