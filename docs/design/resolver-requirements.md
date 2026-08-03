# Resolver Requirements

This document defines the requirements for the resolver, the "detect" stage of `nut-operator`.

Companion to `scope-boundaries.md`, `planner-requirements.md`, and
`inventory-provider-contract.md`. `RS-n` identifiers are stable and are not reused or renumbered.

## Position in the System

The system splits into **detect → decide → act**. The resolver is "detect."

The resolver is the only component that touches every external system: the Kubernetes API, NUT
servers, topology providers, and the capability profile sources. It is therefore where most
failure modes live, and its central obligation is converting unreliable external state into the
reliable, hashed, structural input bundles the planner requires — or into loud, attributable
failures.

The planner's purity (PL-1 through PL-4) is purchased entirely by the resolver doing all I/O here.

---

## Responsibilities

**RS-1 · The resolver owns all input-side I/O.** Kubernetes reads, NUT telemetry collection,
topology provider queries, capability profile loading, and probe operations. Nothing downstream
performs input I/O. (Counterpart of PL-4.)

**RS-2 · The resolver produces the two planner input bundles** defined in `planner-requirements.md`:
the structural bundle (flow specs, topology edges, matched capability profiles, node and workload
inventory) and the telemetry bundle (power state snapshots). The structural/telemetry partition of
PL-42 is enforced here first — the resolver's output types are what makes telemetry structurally
incapable of reaching the plan hash.

**RS-3 · Every emitted input carries source identity and observation timestamp** (PL-11). The
resolver is where attribution originates; nothing downstream can reconstruct it.

---

## Capability Resolution

**RS-4 · The resolver invokes matching; it does not implement it.** Profile matching is pure logic
in its own package under planner-grade determinism discipline (PL-7). The resolver supplies
matching keys from the topology contract and receives matched profiles.

**RS-5 · Precedence chain, restated as the resolver consumes it:** exact model+firmware → exact
model → model glob → driver family → universal floor. CRD source over bundled within a tier.
Highest semver within a source. Lexicographic final tiebreak. The universal floor always matches,
so resolution never fails for want of a profile — it degrades (PL-33).

**RS-6 · Provider key validation (OD-8r).** Behavior on a malformed or missing model string from
the topology provider is a policy decision, uniform across providers — not a NetBox special case.
Until OD-8r resolves, the resolver treats it as floor-match with a warning condition, which is the
conservative reading consistent with PL-33.

---

## Probing and Drift Detection

**RS-7 · Probing is a reconciliation-time activity, never a failure-path one** (CR-1, GP-5). The
resolver may enumerate NUT variables on a device during normal reconciliation, compare against the
matched profile's declarations, and raise a condition on mismatch.

**RS-8 · Probe results never modify resolution.** No auto-demotion, no auto-promotion, no silent
substitution. The correction loop runs through a human profile edit, which arrives as a structural
input change and triggers recompilation (PL-30).

**RS-9 · Probe outcomes are audit records.** "Last verified against firmware X" and probe mismatch
history persist to PostgreSQL per GP-3 in capability profile verification records, not to CR status.
CR status carries at most the current drift condition.

**RS-10 · Probe failure is not device failure.** An unreachable device at probe time raises a
staleness condition on the drift check. It does not alter the device's profile, domain membership,
or plan participation.

---

## Topology and Inventory

**RS-11 · The resolver consumes the provider contract**, never a provider's native model. NetBox
specifics stop at the provider boundary (IN-1, IN-2).

**RS-12 · Snapshot rendering.** The NetBox provider renders a versioned snapshot at resolve time
(IN-14). The resolver records snapshot version and age, enforces the configurable age ceiling
(IN-16), and marks derived plans degraded past it (PL-34).

**RS-13 · Provider outage degrades, never blocks** (IN-15). Resolution continues on the last good
snapshot with a staleness condition. A resolver that has never obtained a snapshot from an optional
provider simply proceeds without that provider's entities — the CRD provider is always present.

**RS-14 · Identity resolution happens here and fails here.** Canonical keys per IN-13; entities the
provider cannot map fail at resolve time with an attributable diagnostic. The planner receives
resolved entities or receives nothing (IN-13).

**RS-15 · Domain closure and orphan validation are resolve-time computations.** The resolver
invokes the pure closure computation (IN-9) and the orphan rule (PL-44) as part of assembling the
structural bundle, so that an orphaned node is rejected before any plan is compiled against the
inventory, not during compilation.

---

## Telemetry Collection

**RS-16 · Telemetry is collected continuously and marked honestly.** Each power state snapshot
carries confidence and staleness markers (PL-8). The resolver never fabricates freshness — a stale
reading is emitted as stale, and PL-32 turns that into `Unknown` feasibility downstream.

**RS-17 · Telemetry loss is a first-class condition.** Loss of contact with a NUT server is exactly
the scenario the `carries` graph models. The resolver raises the condition and continues; deciding
what a plan does about unobservable domains is planner and policy territory (OD-14), not resolver
improvisation.

**RS-18 · Telemetry collection must not be able to starve structural resolution.** Separate
control loops or work queues. A flapping UPS emitting status changes at high frequency cannot delay
recompilation of structural changes.

---

## Failure Posture

**RS-19 · Fail loud, degrade explicit, never silent.** Every degradation the resolver introduces —
floor-matched profile, stale snapshot, missing telemetry, unmappable entity — surfaces as a
condition with the source attribution from RS-3. The resolver's outputs are trusted downstream
precisely because their untrustworthiness is always labeled.

**RS-20 · The resolver holds no authority.** It gates nothing, approves nothing, and actuates
nothing. Its entire power is deciding what the planner sees, which is why its honesty requirements
are strict.

---

## Open Decisions

Owned or co-owned here: OD-8r (provider key policy — RS-6 carries the interim behavior) and OD-16
(missing `carries` coverage — validated at resolve time alongside PL-44 once decided).
