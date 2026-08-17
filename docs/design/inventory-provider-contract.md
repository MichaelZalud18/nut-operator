# Inventory Provider Contract

Components: Inventory System.

This document defines the normalized inventory shape that every topology provider emits, and the
rules the operator applies to it.

Companion to `scope-boundaries.md` and `planner-requirements.md`. `IN-n` identifiers are stable and
are not reused or renumbered.

## Framing

This document defines a **provider contract**, not a NetBox integration.

Per SB-8, NetBox is a heavy design influence and a zero-weight runtime dependency. The contract is
therefore designed against the operator's own requirements, not against NetBox's model. If it were
designed the other way, the CRD provider would become "NetBox's model, hand-typed," every NetBox
modeling quirk would become an operator quirk, and the zero-weight claim would quietly stop being
true. NetBox informs the vocabulary. The contract stays the operator's.

At minimum two providers implement it:

| Provider | Status | Snapshot behavior |
| --- | --- | --- |
| Declarative CRD | Default | Native; always current |
| NetBox | Optional add-on | Rendered snapshot at resolve time (IN-14) |

What the contract must carry is set by the planner's declared inputs: capability matching keys
(PL-7), power domain membership (PL-6, PL-8), physical power dependencies (PL-6), node roles (PL-9),
and the communication graph (PL-21).

---

## Entities

**IN-1 · Four entity kinds, and only four.**

| Kind | Role | Canonical home |
| --- | --- | --- |
| UPS device | Power source, domain root | `UPSDevice` CRD |
| Node | Shutdown target | Kubernetes `Node` |
| Power infrastructure | Anything on the power-delivery or communication path between the two — PDUs, switches, routers, transceivers | Inventory contract |
| Edge | Relation between any two of the above | Inventory contract |

Power infrastructure is first-class, not incidental. A switch carrying the operator-to-UPS routing
path is a hard dependency of the entire event pipeline: lose it and the operator cannot observe or
command the UPS at all. These entities exist in the contract for exactly that reason.

**IN-2 · Attribute admission rule.** An attribute exists in the contract only if a planner rule
consumes it. Rack position, site, tenant, cable type, port counts, and serial numbers stay in the
source system and never cross the provider boundary.

This is what keeps the contract small while NetBox stays exhaustive. It is also the test to apply
when a new field is proposed: name the planner rule, or the field does not land.

---

## Edges

**IN-3 · Two edge relations, distinct types.** They are not one relation with a direction flag or a
kind enum, because they produce opposite planner behavior.

`feeds(A → B)` — A supplies power to B. Loss of A means B is running on battery or dying.

`carries(A → B)` — A transports the NUT or control path for B. Loss of A means B still has power but
is no longer observable or commandable.

Conflating them makes the compiler unable to distinguish "shut this down urgently" from "shut this
down last." `feeds` drives urgency and domain membership. `carries` drives ordering — a carrier is
sequenced *after* its dependents, per PL-21.

**IN-4 · `feeds` carries an input qualifier.** Multi-PSU equipment fed from more than one source must
record which input each edge terminates on. This is the one place NetBox's power-port vocabulary
genuinely earns its keep. A node fed from two domains behaves fundamentally differently from a
singly-fed node, and PL-16 feasibility cannot reason correctly without it.

**IN-5 · The communication graph is modeled, minimally.** `carries` edges over the same entity set.
No interface, VLAN, routing, or link-layer detail — none of it is consumed by a planner rule, so IN-2
excludes it.

This closes OD-3. The alternative — documenting the communication path rather than modeling it —
leaves PL-21 unimplementable and leaves the founding observation that every switch between UPS and
node must stay powered permanently informal.

**IN-6 · Missing `carries` coverage is not silently benign.** A node with no `carries` path must
either be flagged or explicitly exempted. Assuming reachability by default recreates the OD-9
silent-failure class in a new location. See OD-16.

---

## Power Domains

**IN-7 · Domain membership is derived, never declared.** A power domain is the transitive closure of
`feeds` edges from a `UPSDevice`. There is no user-supplied membership list.

Rationale is structural rather than aesthetic. Permitting both declared and derived membership would
force every planner rule to handle two membership provenances indefinitely. One computation path,
one source of truth, no reconciliation between them.

**IN-8 · Derivation is compile math, not observation.** The `feeds` edges are authored, structural,
hashed input (PL-42). The closure over them is a pure deterministic function. Nothing here queries a
live system during the failure path, so the reference-not-runtime principle holds intact.

**IN-9 · Closure computation lives in the pure package** alongside capability matching (PL-7), under
the same determinism discipline. Its inputs are already covered by the PL-14 structural hash, so no
hashing change is required.

**IN-10 · Domains are named, not enumerated.** The domain name is declared on the `UPSDevice`;
membership is computed. `ShutdownFlow` triggers continue to reference domains by name, so the
existing CRD trigger surface is unchanged.

Repository note: the declared power-domain field on `UPSDevice` is the domain **label**, not a
membership list. The inventory graph computes membership from `feeds` edges; capability profiles
remain product/SKU records and never carry membership.

**IN-11 · Dual-domain membership is a natural consequence.** A node reachable from two `UPSDevice`
roots belongs to both domains. No special case, no override syntax. This is the data PL-16 already
needed for multi-feed feasibility.

**IN-12 · Orphan rule.** Every Kubernetes node must be reachable from at least one `UPSDevice` via
`feeds` edges, or carry an explicit exemption marker declaring it not UPS-backed and excluded from
power planning. A node that is neither reachable nor exempted is a hard validation failure.

This is the guardrail that makes IN-7 safe. Derived membership's real risk is not derivation — it is
that one unrecorded cable silently drops a node out of every domain, so no trigger covers it, so it
hard-drops during an outage with nothing in any log. The orphan rule converts that silent gap into a
loud one, and the exemption marker is the honest escape hatch for genuinely unprotected hardware.

---

## Identity

**IN-13 · Canonical identity per entity kind.**

| Entity | Canonical key | Mapping owner |
| --- | --- | --- |
| Node | Kubernetes node name | NetBox provider owns k8s-name ↔ NetBox device ID |
| UPS device | `UPSDevice` CRD name | NetBox reference optional |
| Power infrastructure | Contract-local identifier | Provider |

The Kubernetes node name is canonical because it is what the planner targets and what the executor
acts on. Every other identity is a mapping onto it.

Declared node names are checked against the cluster. A `PowerInventoryNode` naming a node that does
not exist raises `InventoryNodeNotInCluster`, so a typo surfaces at reconcile time rather than as a
power domain covering a node nothing can shut down. It warns rather than rejects, because inventory
is legitimately authored ahead of the hardware it describes; the requirement is that the gap is
stated, not that it blocks. With no nodes visible at all there is nothing to check against, and
nothing is claimed.

An entity the NetBox provider cannot map to a canonical key fails at **resolve** time under the
OD-8r policy — never at planner time. Identity resolution is a provider concern; the planner receives
resolved entities or receives nothing.

Capability matching keys — model and optional firmware — arrive through this contract and feed PL-7.
OD-8r's malformed-key policy therefore applies uniformly across both providers rather than existing
as a NetBox special case.

---

## Snapshot Discipline

**IN-14 · Inventory is structural input.** It is hashed, determinism-tested, and plan-identity-bearing
per PL-42.

Consequence for the NetBox provider: it renders a **versioned snapshot** at resolve time. It is never
queried during the failure path. This is the same reference-not-runtime pattern applied to capability
resolution in CR-1.

**IN-15 · Provider outage degrades, never blocks.** A NetBox outage means planning continues against
the last good snapshot with a staleness condition raised. It never means planning is blocked.

**IN-16 · Snapshot age escalates, it does not cut off.** Age raises the severity a snapshot is
reported at, through user-configured thresholds on
`PowerManagementCluster.spec.inventory.snapshotAgeLevels`. `Info` is recorded and published without
changing conditions; `Warning` additionally degrades the flows compiled from that snapshot per PL-34.
An unconfigured cluster gets `Info` at one hour and `Warning` at six.

There is deliberately no rejecting level. An upper limit would contradict IN-15 at the worst possible
moment — the outage is exactly when the provider is unreachable and the shutdown still has to be
planned. What the rule actually protects against is silence, so escalation is the whole remedy: "last
good snapshot" must not be able to quietly mean "eight months ago," but it must still be usable when
it does.

The CRD provider satisfies IN-14 through IN-16 trivially, which is an argument for it as default
beyond the ones in SB-8. It stamps no snapshot time because it is rebuilt from live resources on
every resolve, and age evaluation stays silent when there is nothing to measure rather than inventing
staleness from a missing field.

---

## Governing Principle Extracted

**GP-5 · Declared in the failure path; derived only to confirm or alarm.** Anything consumed while
power is failing is authored, structural input. Runtime-derived or externally-queried data may
verify that input and raise conditions on mismatch, but never feeds decisions directly.

Instances: CR-1 (capability declaration authoritative, probing advisory), IN-8 and IN-14 (inventory
authored and snapshotted, never live-queried), IN-12 (derived closure guarded by an authored
completeness rule).

Recorded as a governing principle because it has now decided three separate design questions and
continues to govern new boundary decisions. It is promoted into `scope-boundaries.md` as GP-5.

---

## Decisions Closed

**OD-3 · Communication path representation — closed, modeled minimally.** `carries` edges over the
shared entity set, no link-layer detail (IN-5).

**OD-2 · Three-graph model — collapsed.** There are not three input graphs. There is one entity set
with two edge relations. The physical power graph is `feeds`; the communication graph is `carries`;
the logical shutdown graph is the planner's **compiled output**, not an input. Merge rules and
conflict arbitration are therefore moot — the relations do not overlap, they compose.

---

## Decisions affecting this contract

Both decisions this contract once carried are settled. They are recorded here rather than removed,
because the questions are the kind that get asked again.

**OD-16 · Missing `carries` coverage — settled as a warning plus an explicit exemption.** A node with
no modeled communication path raises the `CommunicationPathUnmodeled` *warning* diagnostic and
increments `communication_path_unmodeled_nodes`; `communicationPathExempt` opts a node out. Neither
of the two framings originally offered won outright: it is not a hard validation failure, and the
exemption marker exists anyway.

The asymmetry with `feeds` is the point. A node no UPS reaches raises `PowerPlanningOrphan`, an
*error*, because it cannot be planned for at all — the operator has no idea when it loses power. A
node with no modeled `carries` path can be planned normally and loses only communication ordering,
which `PL-21` defers past v1. Same shape of gap, different cost, so different severity. Silent-assume
stays excluded either way (IN-6).

**OD-14 · Partial-domain outage plan scope — settled as domain-scoped, conservatively.**
`internal/planner/scope.go` omits only groups whose resolved node membership is proved wholly outside
the affected domains; ambiguous and mixed-domain groups are retained, and the compiled plan states
how many groups were omitted and why.

Not cluster-wide, because an outage in one power domain should not drain another. Not aggressive
pruning either: a group the planner cannot confidently place is a group it must not drop, since the
cost of shutting something down unnecessarily is recoverable and the cost of skipping it is not.

Carried unchanged: OD-4 (last-ditch tier taxonomy), OD-8r (provider key validation), OD-10 (USB
support).

Closed elsewhere: OD-1 (recovery execution is external subscriber scope), OD-5 (startup waves are
advisory projections, not operator-executed recovery), OD-6 (audit durability is local spool
fallback over PostgreSQL), and OD-15 (probe history persistence is an
audit schema record).
