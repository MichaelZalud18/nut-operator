# Executor Requirements

This document defines the requirements for the executor, the "act" stage of `nut-operator`.

Companion to `scope-boundaries.md`, `planner-requirements.md`, `resolver-requirements.md`, and
`inventory-provider-contract.md`. `EX-n` identifiers are stable and are not reused or renumbered.

## Position in the System

The system splits into **detect → decide → act**. The executor is "act."

Every dangerous behavior in the product lives here. The executor's obligations are therefore
dominated by gating, sequencing, revalidation, and evidence: it may only ever run a plan it can
prove it was allowed to run, in the order the plan specifies, while recording what actually
happened.

The executor consumes compiled plans (PL-12 through PL-18). It never modifies a plan, and it never
compiles one — recompilation requests go to the planner.

---

## Trigger Evaluation

**EX-1 · The executor's controller evaluates trigger conditions.** The planner validates trigger
*definitions* (PL-19, PL-36); the executor evaluates whether a defined trigger is *firing*
against live telemetry from the resolver. This is the previously unassigned half of PL-36 and it is
assigned here.

**EX-2 · Trigger evaluation consumes telemetry with its markers.** Confidence and staleness (PL-8)
propagate into the firing decision. A trigger cannot fire on data the resolver marked untrusted
without that fact being recorded in the execution record.

**EX-3 · Trigger-time feasibility is the authoritative verdict** (PL-16). On firing, the executor
requests feasibility recomputation against live telemetry before the first wave starts, and records
both the compile-time advisory and trigger-time authoritative verdicts.

---

## Gating

**EX-4 · Both approval gates are checked at execution time, not deployment time** (RB-4, GP-2).
`ShutdownFlow` enforcement approval and `NodePowerAgent` actuation approval are independently
verified when the flow fires. Absence of either downgrades execution to dry-run behavior; it never
silently proceeds.

**EX-5 · Dry-run executes everything except effects.** Wave sequencing, instance enumeration,
clearance evaluation, and record writing all run identically in dry-run; only workload mutation and
host actuation are suppressed. This is what makes dry-run output a faithful rehearsal rather than a
guess. (GP-2.)

**EX-6 · Mode is re-read per wave.** Revoking an approval annotation mid-flow stops effectful
execution at the next wave boundary. In-flight actions in the current wave complete or time out;
subsequent waves run dry.

---

## Pre-Execution Revalidation

**EX-7 · Hash check, bounded recompile, then policy** (PL-31). On firing with a mismatched
structural hash: attempt recompilation within a bounded time budget; execute the fresh plan on
success; on failure or budget exhaustion, the configured policy chooses between the stale plan and
refusal. Refusal is never the silent default, and whichever path is taken is written to the
execution record with the hash pair.

**EX-8 · Instance enumeration at execution** (OD-11). Selectors resolve to concrete workload
instances per wave, at the time the wave starts. Enumerated instances are recorded so the audit
trail shows what the selectors actually matched, not what they were expected to match.

**EX-9 · Node-clearance revalidation** (PL-43). Before any `AgentShutdown` action, the executor
re-derives that node's clearance against current placement. Compile-time clearance edges are the
plan; execution-time clearance is the proof.

---

## Wave Execution

**EX-10 · Waves execute strictly in compiled order.** Groups within a wave run concurrently; a wave
completes only when every group has met its completion condition or exhausted its timeout. The
executor never reorders, merges, or skips waves.

One active trigger episode maps to one execution deduplication key. While that episode remains
eligible, repeated reconciliations update status and audit decisions but do not create duplicate
execution runs. After the trigger clears, the same plan may execute again for a later episode.

**EX-11 · Per-group timeouts are enforced as written.** Timeout expiry is a group failure, which
engages abort policy — it is not an implicit success.

**EX-12 · Abort policy executes as compiled** (PL-18). A failed group aborts the flow by default.
Under `ContinueSafeSteps`, only groups pre-marked eligible in the compiled plan may still run. The
executor makes no execution-time eligibility judgments.

**EX-13 · Actions use the stock-Kubernetes mechanisms of SB-10.** Scale, suspend, quiesce; evict
with PDB respect; withdraw traffic via Services; cordon before drain. Direct pod deletion only where
the plan explicitly compiled an exceptional override.

The Kubernetes action runner is the effectful boundary for these stock mechanisms. It scales
`Deployment`, `StatefulSet`, and `ReplicaSet` targets, cordons `Node` targets, drains through the
`pods/eviction` subresource, and creates provider-neutral workflow hook objects from explicit
`RunWorkflow` parameters. The executor supplies concrete targets that were enumerated at execution
time and records every action attempt.

**EX-14 · Idempotent, resumable execution.** The executor may restart mid-flow (it is itself a
workload in a cluster that is shutting down). Execution state sufficient to resume — current wave,
group states, enumerated instances — is persisted such that a restarted executor continues rather
than re-running completed actions or abandoning the flow. PostgreSQL stores compact resume state in
`executor_resume_states` and durable progress in the execution, wave, group, and action-attempt
tables.

---

## Node Release and Actuation Handoff

**EX-15 · The executor releases nodes; the node agent shuts them down.** Release is the handoff
boundary of SB-3: the executor completes clearance, then signals; all cluster authority ends at the
signal.

**EX-16 · The signal file is the interface.** Structured content — timestamp, reason, UPS identity,
flow identity, and the plan hash (PL-14) — per the repository's handoff design. The actuator's
staleness rejection is the last safety check in the chain, and the executor must produce signals
that pass it only when genuinely current.

**EX-17 · The executor holds no host credentials and no NUT credentials.** Its authority is the
Kubernetes API and the signal handoff, nothing else. Host action isolation stays entirely in the
actuator container (SB-3, SB-4).

**EX-18 · Terminal ordering is honored to the last node.** Last-ditch constraints compiled per
PL-22 are hard: the executor never releases a node carrying a must-stay role while any wave that
role protects is incomplete. Quorum constraints (PL-23) are checked at each control-plane release,
not assumed from compile time.

---

## Evidence

**EX-19 · The executor is the audit writer.** Execution records, action attempts with outcomes,
enumerated instances, revalidation results, approval evidence, and telemetry snapshots at decision
points — all to PostgreSQL per GP-3 and SB-12, keyed by plan hash (PL-14).

**EX-20 · Audit failure does not halt power response** (SB-11). PostgreSQL unavailability during
execution degrades evidence, raises a condition, and the flow continues. The durability mechanism
for records generated while the audit store is itself shutting down is OD-6, unresolved; until then
the executor buffers best-effort in memory and flushes on availability.

**EX-21 · Dry-run produces full evidence.** A dry-run flow writes the same record shape with
effects marked simulated. Rehearsals that leave no trace are not rehearsals.

---

## Bound Decisions

**OD-17 · Executor state persistence for resume — closed.** The executor persists wave position and
compact state in PostgreSQL `executor_resume_states`. Detailed progress remains in the execution,
wave, group, action-attempt, node-release, and signal-handoff tables.

Bound from elsewhere: OD-6 (audit durability — EX-14, EX-20), OD-12 (infeasibility policy —
consumed at EX-3), OD-14 (partial-domain scope — determines what EX-10 executes when one domain
fires).
