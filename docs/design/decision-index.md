# Decision Index and Glossary

Components: Cross-cutting.

This is the map across the design document set. When a namespace gains or retires an identifier,
this file updates in the same change.

Every doc under `docs/`, `docs/design/`, and `docs/audits/` carries a `Components:` (or
`*Components:*`) line naming which `docs/tasks.md` section(s) it belongs to, so the mapping is
visible at the point of reading, not just here. `scope-boundaries.md` and `resolver-requirements.md`
tag per-section instead of per-file, since their `## SB-n` boundaries and detect-stage subsections
each map to a different component — the "Component(s)" column below gives the condensed,
file-level view for docs where a single tag is precise enough, and points at "varies" where it
isn't.

The component names are the ten `docs/tasks.md` sections — Inventory System, Capability Profiles,
Telemetry & Triggers, Planning & Execution Logic, NUT Server / upsd, Node Agent / DaemonSet, Outputs
& Publishing, Storage & Audit, Operator Maturity & Hardening, Foundation & Documentation — plus
`Cross-cutting` for principles and boundaries that don't belong to one. `tasks.md` is the source of
truth for what "belongs" to a component actually means in implementation terms; this index only
tags which design content informs which component.

## Document Set

| Doc | Stage / concern | Namespaces defined | Component(s) |
| --- | --- | --- | --- |
| `scope-boundaries.md` | What the project is and is not | GP, SB, RB; OD registry of record | Varies per `## SB-n` — see file |
| `planner-requirements.md` | Decide | PL, CR | Planning & Execution Logic; Capability Profiles (CR section) |
| `resolver-requirements.md` | Detect | RS | Varies per section — see file |
| `executor-requirements.md` | Act | EX | Planning & Execution Logic |
| `settled-questions.md` | — | — | All (standing reference) |
| `inventory-provider-contract.md` | Topology input contract | IN | Inventory System |
| `faq.md` | User-facing answers | — | Cross-cutting |
| `capability-profiles.md` | Profile catalog, SKU records, aliases, probe helper, `upsd` config influence, scope and provenance | CR | Capability Profiles; NUT Server / upsd |
| `telemetry-and-triggers.md` | NUT normalization boundary and trigger decisions | Runtime telemetry and decision facts | Telemetry & Triggers; Planning & Execution Logic |
| `shutdown-flow.md` | Public shutdown-flow model and the published artifact contract | Compiled plan format; artifact contract | Planning & Execution Logic; Outputs & Publishing |
| `audit-storage-schema.md` | PostgreSQL durable-state schema and writer boundary | Migration-bound | Storage & Audit |
| `scaling-and-sizing.md` | Component scaling guidance and what actually binds | — | NUT Server / upsd; Node Agent / DaemonSet; Planning & Execution Logic |
| `adaptive-execution-tier-pointer.md` | Mid-flow adaptation: tier pointer and timing modes | AE (provisional) | Planning & Execution Logic |
| `resiliency-and-partitions.md` | Partition/degradation contract across every external dependency | — | Cross-cutting |
| `upstream-nut-relay.md` | `dummy-ups` relay mode for appliances with a built-in `upsd` | — | NUT Server / upsd |

## Audit Records

Dated audit and findings records live in `docs/audits/` and share the `F-n` findings namespace.

| Doc | Scope | Findings | Component(s) |
| --- | --- | --- | --- |
| `operator-maturity-benchmarks.md` | External maturity standards and the recurring audit | F-1 – F-7, F-30 – F-32, F-38 | Operator Maturity & Hardening |
| `node-agent-daemonset-audit.md` | Node agent DaemonSet render | F-8 – F-14, F-33 – F-36 | Node Agent / DaemonSet |
| `nutserver-pod-audit.md` | `NUTServer` CRD and the `upsd` Deployment it renders | F-15 – F-19, F-23 | NUT Server / upsd |
| `nut-usage-audit.md` | Cross-component NUT mechanism usage and fidelity | F-20 – F-22, F-24, F-37, F-39 – F-41 | NUT Server / upsd; Node Agent / DaemonSet; Telemetry & Triggers |
| `quirks-aliasing-firmware.md` | Quirk handling, variable aliasing, firmware gating | F-25 – F-27 | Capability Profiles |

## Identifier Namespaces

| Prefix | Meaning | Home | Range in use |
| --- | --- | --- | --- |
| GP | Governing principle | scope-boundaries | GP-1 – GP-7 |
| SB | Scope boundary | scope-boundaries | SB-1 – SB-15 |
| RB | Repository-derived boundary | scope-boundaries | RB-1 – RB-7 |
| OD | Open/closed decision | scope-boundaries (registry) | OD-1 – OD-35, OD-8r |
| PL | Planner requirement | planner-requirements | PL-1 – PL-49 |
| CR | Capability resolution rule | planner-requirements | CR-1 – CR-4 |
| RS | Resolver requirement | resolver-requirements | RS-1 – RS-20 |
| EX | Executor requirement | executor-requirements | EX-1 – EX-32 |
| HK | Shutdown hook requirement | shutdown-hooks | HK-1 – HK-10 |
| IN | Inventory contract rule | inventory-provider-contract | IN-1 – IN-16 |
| F | Audit finding | audit records (`docs/audits/`) | F-1 – F-44 (2026-08-10) |

Identifiers are stable: never reused, never renumbered. Superseded items are marked in place.

The provisional `AE` namespace is retired. `AE-1`–`AE-6` were folded into `EX-25`–`EX-30`
(executor-requirements), and the runtime-estimate capability gate that had also been carried as
`AE-6` became `CR-4` (planner-requirements) — it is capability resolution, not adaptive execution,
and two requirements were sharing one number. `adaptive-execution-tier-pointer.md` remains the
narrative account and now uses the folded numbers.

## Decision Registry

### Open

| ID | Question | Blocks | Likely owner doc |
| --- | --- | --- | --- |
| OD-8r | Provider key validation policy (interim: fall back to the unidentified-device profile, plus a warning, RS-6) | Resolver | Resolver |
| OD-10 | USB/serial support: version target and isolation model | — | v2 scoping |
| OD-14 | Partial-domain outage: cluster-wide vs domain-scoped plan (structure now available) | PL-16, PL-23, EX-10 | Planner design |
| OD-16 | Missing `carries` coverage: error vs explicit exemption | Inventory validation | inventory contract |
| OD-19 | FSD usage: NUT's forced-shutdown broadcast as the final release signal, or deliberately declined in favor of the executor's signal file | F-20 | Executor design |
| OD-20 | Instant command scope and gating, and which capability profile fields declare support. Bounded by OD-1 on power-return | F-22, F-23, F-27 | Capability schema |
| OD-21 | Driver configuration ownership: capability profile vs `UPSDevice` spec; hybrid default-plus-override likely (RS-5 pattern) | — | Capability schema |
| OD-24 | Non-NUT power device actuation: second actuation path or permanently topological. Decided alongside OD-10 | — | v2 scoping |
| OD-25 | PDU profile kind: parallel capability kind schema and factored shared machinery. Scaffolding only in v1 | — | Capability schema |
| OD-26 | Provenance field semantics: advisory metadata or resolution-affecting | — | Capability schema |
| OD-27 | Timing adaptation parameters: hysteresis count, improvement margin, and scope | — | Adaptive execution |
| OD-28 | Relationship to OD-12: infeasible-plan policy before start vs timing re-decisions during | — | Adaptive execution |
| OD-30 | Cadence intervals: publish interval during idle vs active flow; global or per-flow | — | Adaptive execution |
| OD-33 | Hook waiting: whether an opt-in bounded wait on hook completion exists, and what happens when the runtime budget expires first. Default decided (proceed); the mechanism is not | — | Shutdown hooks |
| OD-34 | Hook failure and abort policy: whether a failed hook can mark the flow degraded, or stays purely advisory | — | Shutdown hooks |
| OD-35 | *Retired, never a decision.* Raised as "do redundant `feeds` edges change observation aggregation" while recording `F-45`. The premise was invented: `MINSUPPLIES` governs one host's own supplies and never reaches the planner's aggregation. Number burned rather than reused | — | `F-45` |

### Closed

| ID | Resolution | Where |
| --- | --- | --- |
| OD-2 | Collapsed — one entity set, two edge relations; logical graph is compiled output | inventory contract |
| OD-3 | Communication path modeled minimally as `carries` edges | IN-5 |
| OD-4 | Numbered shutdown tiers: 0 = last-ditch (workload-only), 1 = final stop / lowest for nodes, 2+ earlier; configurable default; compiled to derived edges. Tier-inversion handling deferred to OD-18 | scope-boundaries change log |
| OD-6 | Closed with explicit shutdown-time audit spool: PostgreSQL remains primary; enabled local JSONL spool preserves replayable records when PostgreSQL writes fail during execution | audit-storage-schema.md, EX-20 |
| OD-15 | Capability profile probe history is persisted in PostgreSQL as `capability_profile_verifications` | Audit schema |
| OD-1 | Recovery/startup execution is out of scope; external systems consume published artifacts | scope-boundaries |
| OD-5 | Startup ordering is an advisory projection for subscribers, not operator-executed recovery | scope-boundaries |
| OD-7 | Profiles are CRDs + bundled data; NetBox references at most | planner CR section |
| OD-8 | Dissolved — lookup, not merge; residue → OD-8r | planner CR section |
| OD-11 | Hybrid selector resolution: compile graph, enumerate at execution | planner Resolved |
| OD-13 | Load shedding node-granular baseline | planner Resolved |
| OD-17 | Executor mid-flow state persists to PostgreSQL execution and resume-state tables | executor EX-14 |
| OD-23 | Alias maps live in the profile telemetry section. Native readings outrank aliases; aliasing is one-directional and total; every applied alias is a diagnostic | capability-profiles.md |
| OD-18 | Tier inversion blocks the node by default: an inverted node is withheld from power-off for the whole flow. `spec.groups[].tierInversionPolicy: Allow` opts a group out per workload. Migration declined as a general remedy — node-local PVCs mean there is not always anywhere to move to | Planner tier compilation | Planner design |
| OD-32 | NUT operand SSL backend is OpenSSL, built from source. NSS is more feature-complete for client certificates today, but has no CERTFILE and needs a cert database instead of the PEM a TLS Secret projects. Alpine's NSS build was not a considered choice: the aport requests both backends and NSS wins by precedence in configure.ac | Operand images | F-39 – F-41 |
| OD-9 | Trigger degrade substitutes toward the coarsest `ups.status` trigger: `RuntimeBelow` and `ChargeBelow` fall back to `LowBattery`, which states the same intent coarsely, rather than to `OnBattery`, which states a different one. Substitution is declared via `spec.triggers[].fallbackType`, never automatic — it changes when nodes begin powering off, so per GP-5 it is authored, not derived. Compilation names the fallback that would close a gap | Trigger validation | capability-profiles.md |
| OD-22 | Structured quirk objects carrying firmware scope as a field: `firmware.matches` globs and a `firmware.below` dotted-numeric fix release. Firmware-ranged selectors rejected — a selector scopes the whole profile, and quirks expire independently of the telemetry a model reports | Capability schema | capability-profiles.md |
| OD-31 | An unidentified device blocks Enforce mode unless explicitly accepted. Dry-run review is unaffected. "Universal floor" retired as a name | PL-33 |
| OD-12 | Infeasible plans warn and run. Not rejected — refusing mid-outage is the worst available outcome (PL-31). Not truncated — dropping tiers substitutes the operator's judgement for the flow author's. The author holds the risk; this operator owes them the numbers, stated plainly and visibly: plan estimate against runtime estimate, per tier and in total | EX-3, settled-questions.md |
| OD-29 | Ascent is the strict inverse of descent: mains back and no low-battery assertion (`ShouldAscend`). No hysteresis, no hold time, no confirmation window — EX-27 makes ascent bookkeeping that triggers nothing, and EX-26 makes the re-descent that follows a sequence of no-ops, so a flicker costs nothing to get wrong | EX-26, EX-27, settled-questions.md |

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
