# Decision Index and Glossary

Status: living index. The map across the design document set. When a namespace gains or retires an
identifier, this file updates in the same change.

## Document Set

| Doc | Stage / concern | Namespaces defined |
| --- | --- | --- |
| `scope-boundaries.md` | What the project is and is not | GP, SB, RB; OD registry of record |
| `planner-requirements.md` | Decide | PL, CR |
| `resolver-requirements.md` | Detect | RS |
| `executor-requirements.md` | Act | EX |
| `inventory-provider-contract.md` | Topology input contract | IN |
| `faq.md` | User-facing answers | — |
| Capability schema *(planned)* | Profile format | TBD |
| `telemetry-normalization.md` | NUT variable normalization boundary | Runtime telemetry facts |
| Plan schema *(planned)* | Compiled plan format | Internal |
| `audit-storage-schema.md` | PostgreSQL durable-state schema and writer boundary | Migration-bound |

## Identifier Namespaces

| Prefix | Meaning | Home | Range in use |
| --- | --- | --- | --- |
| GP | Governing principle | scope-boundaries | GP-1 – GP-5 |
| SB | Scope boundary | scope-boundaries | SB-1 – SB-13 |
| RB | Repository-derived boundary | scope-boundaries | RB-1 – RB-7 |
| OD | Open/closed decision | scope-boundaries (registry) | OD-1 – OD-17, OD-8r |
| PL | Planner requirement | planner-requirements | PL-1 – PL-44 |
| CR | Capability resolution rule | planner-requirements | CR-1 – CR-3 |
| RS | Resolver requirement | resolver-requirements | RS-1 – RS-20 |
| EX | Executor requirement | executor-requirements | EX-1 – EX-21 |
| IN | Inventory contract rule | inventory-provider-contract | IN-1 – IN-16 |

Identifiers are stable: never reused, never renumbered. Superseded items are marked in place.

## Decision Registry

### Open

| ID | Question | Blocks | Likely owner doc |
| --- | --- | --- | --- |
| OD-1 | Recovery/startup: in-project at all, and which version | OD-5 | scope-boundaries |
| OD-4 | Last-ditch phase taxonomy | PL-22 | Planner design |
| OD-5 | `requires` edge inversion under startup semantics | — (contingent on OD-1) | Planner design |
| OD-6 | Audit durability while the audit store is shutting down | Audit writer, EX-14, EX-20 | Audit schema |
| OD-8r | Provider key validation policy (interim: floor-match + warning, RS-6) | Resolver | Resolver |
| OD-9 | Trigger degrade mechanics | — | Capability schema |
| OD-10 | USB/serial support: version target and isolation model | — | v2 scoping |
| OD-12 | Infeasible-plan policy field default and options | EX-3 | Planner design |
| OD-14 | Partial-domain outage: cluster-wide vs domain-scoped plan (structure now available) | PL-16, PL-23, EX-10 | Planner design |
| OD-15 | Probe-history table | — | Audit schema |
| OD-16 | Missing `carries` coverage: error vs explicit exemption | Inventory validation | inventory contract |
| OD-17 | Executor mid-flow state persistence for resume | EX-14 | Executor / resolve with OD-6 |

### Closed

| ID | Resolution | Where |
| --- | --- | --- |
| OD-2 | Collapsed — one entity set, two edge relations; logical graph is compiled output | inventory contract |
| OD-3 | Communication path modeled minimally as `carries` edges | IN-5 |
| OD-7 | Profiles are CRDs + bundled data; NetBox references at most | planner CR section |
| OD-8 | Dissolved — lookup, not merge; residue → OD-8r | planner CR section |
| OD-11 | Hybrid selector resolution: compile graph, enumerate at execution | planner Resolved |
| OD-13 | Load shedding node-granular in v1 | planner Resolved |

## Glossary

**Power domain** — the transitive closure of `feeds` edges from a `UPSDevice` root. Derived, never
declared; named on the UPS. A node can belong to more than one (IN-7, IN-11).

**feeds / carries** — the two edge relations. `feeds(A→B)`: A powers B; loss means B is on battery.
`carries(A→B)`: A transports B's NUT/control path; loss means B is unobservable but powered. They
drive opposite planner behavior and are never conflated (IN-3).

**Group** — the unit of shutdown policy in a `ShutdownFlow`: a selector-targeted set of workloads
or agents with an action, relationships, and a timeout.

**Wave** — a compiled set of groups eligible to execute concurrently. Waves are ordered; execution
is wave-by-wave (PL-12, EX-10).

**Structural vs telemetry input** — the load-bearing partition of planner input. Structural:
slow-changing, hashed, plan-identity-bearing. Telemetry: continuous, never hashed, feeds
feasibility only (PL-42).

**Plan hash** — deterministic hash over structural inputs plus emitted plan; the correlation key
across CR status, the signal file, and audit records (PL-14).

**Capability profile** — versioned artifact declaring a device's NUT variables (telemetry section)
and behaviors/quirks (actuation section). Matched, not merged; declaration authoritative, probing
advisory (SB-9, CR-1, CR-2).

**Universal floor** — the least-specific capability profile, bundled with the operator, guaranteed
to match. The terminal tier of the matching chain, not a special case (PL-33).

**Last-ditch** — the role marking services and nodes that must survive until a given shutdown
phase; under HA, the minimum viable control plane. Taxonomy pending OD-4.

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

## Change Log

**2026-07-31 — initial index.** Nine namespaces, twelve open decisions, six closed, glossary of
fifteen terms.
