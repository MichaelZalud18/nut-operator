# Executor Requirements

Components: Planning & Execution Logic.

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
plan; execution-time clearance is the proof. The two are not interchangeable, because OD-11 resolves
concrete workload instances at execution: a pod that rescheduled onto the node after the plan
compiled is invisible to the graph and very visible to whoever loses it.

The question asked is "what would still be running when the power goes", not "did the drain command
succeed". Three classes are excluded because each is expected to be there right up until the node
goes down: pods in the node agent's and manager's own namespaces, `DaemonSet` pods that eviction
deliberately does not remove, and static/mirror pods no controller can reschedule. Everything else
still running blocks the release and is named in the record, because "the node is not clear" is not
actionable and "etcd-backup is still on it" is.

The pod list is read straight from the API server rather than the informer cache. This is the last
check before power is cut, and a cache a few seconds behind is exactly long enough to miss a pod that
just landed. An unreadable list fails closed: it is not evidence the node is empty.

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

The timeout becomes a context deadline on the action-runner call. A runner that returns success
against an expired deadline is still a timeout: it did not finish in the time the flow allowed, and
reporting it as success would let the next wave start on work still in flight. A group with no
declared timeout runs unbounded, since an undeclared limit is not a limit of zero.

"As written" means the declared value is the most the flow will allow. The measured timing budget
may shorten it (see `adaptive-execution-tier-pointer.md`) and never lengthens it, so the flow either
honors what the author declared or takes less. The declared value, the effective value, and the
compression ratio between them are all recorded, so a short timeout the author chose stays
distinguishable from a short one the runtime forced — and the arithmetic is visible either way.

**EX-22 · The executor waits.** `Wait` holds the flow for its declared duration, in dry-run as well as
enforce. It is served in the executor rather than the Kubernetes action runner because waiting is not
a cluster mutation, and it runs in dry-run because EX-5 makes dry-run a faithful rehearsal — a
rehearsal that skips the waits reports a flow duration the real run will not reproduce, which is the
number an operator is rehearsing to find.

**EX-23 · Power recovery suspends the flow; it does not halt it.** When the observation at a wave
boundary shows mains restored and no low-battery assertion, the executor stops starting waves and
records a `Suspended` phase. Nothing already shut down is restored; recovery is a subscriber concern
(EX-25, EX-27).

Suspension is explicitly not the EX-30 halt, which is abort: a deliberate stop that latches and never
resumes. A suspended flow must remain able to descend again, so three things hold together:

1. The tier pointer is **not** latched.
2. The pointer is left at the depth it stopped, published on status and persisted in the resume
   state, so the next descent continues from there rather than starting over.
3. Execution deduplication ignores a suspended run. A completed episode deduplicates; a suspended one
   does not, because it has more to do. Without this, dedupe itself would block re-descent during a
   dip-recover-dip outage.

`Suspended` is also distinct from `Completed` and `Aborted`. A completed flow ran every wave, an
aborted one failed, and a suspended one did exactly as much as the outage called for. Collapsing
suspension into completion would make "the outage ended" indistinguishable from "the cluster finished
shutting down".

**EX-12 · Abort policy executes as compiled** (PL-18). A failed group aborts the flow by default.
Under `ContinueSafeSteps`, only groups pre-marked eligible in the compiled plan may still run. The
executor makes no execution-time eligibility judgments.

**EX-24 · Every action in the enum does something.** An action the API accepts and the runner
ignores is worse than a missing one: a flow author reads it as configured behavior. `Gate` was
removed for this reason — nothing defined who opens it, and a shutdown waiting on a human is a
shutdown that does not finish, which is the wrong failure mode during an outage. `Notify` emits a
Kubernetes Event, chosen because Events need no configuration to work; a transport that must be wired
up first would be inert exactly when it is relied on. With no event recorder configured, `Notify`
reports `Blocked` rather than succeeding, since a notification nobody received was not delivered.

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
group states, enumerated instances, tier pointer, and timing mode — is persisted such that a restarted
executor continues rather than re-running completed actions or abandoning the flow. PostgreSQL stores
compact resume state in `executor_resume_states` and durable progress in the execution, wave, group,
and action-attempt tables.

The pointer and the timing mode are part of that state, not decoration on it. A restarted executor
that started from a fresh pointer would re-report tiers it already descended as new work, and one that
started from a fresh timing mode would silently relax a flow that had escalated — handing back time it
may need and cannot get again.

---

## Node Release and Actuation Handoff

**EX-15 · The executor releases nodes; the node agent shuts them down.** Release is the handoff
boundary of SB-3: the executor completes clearance, then signals; all cluster authority ends at the
signal.

**EX-16 · The signal file is the interface.** Structured content — execution ID, node name,
timestamp, reason, UPS identity, flow identity, and the plan hash (PL-14) — per the repository's
handoff design. The executor writes per-node projected Secret keys using this shape, and the
actuator's staleness and node-binding checks are the last safety checks in the chain.

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
execution degrades evidence, raises a condition, and the flow continues when the shutdown-time
audit spool is enabled. The spool appends replayable JSONL records with the same execution IDs,
wave keys, action-attempt IDs, release IDs, handoff IDs, and resume-state keys that the primary
PostgreSQL writer uses. If both PostgreSQL and the configured spool path fail, the audit failure is
returned and the flow records the failed evidence path.

**EX-21 · Dry-run produces full evidence.** A dry-run flow writes the same record shape with
effects marked simulated. Rehearsals that leave no trace are not rehearsals.

---

## Adaptive Execution

Folded in from the provisional `AE-1`–`AE-6` in `adaptive-execution-tier-pointer.md`, which remains
the narrative account of the model. These are the numbered requirements.

**EX-25 · The operator is a recorder, not a reconciler** (was EX-25). On the recovery direction the
executor stops descending and publishes. It does not predict where a dip will bottom out, judge
whether a recovery is durable, restore or scale up or uncordon anything, or reconcile intended state
against actual state. Recovery is a subscriber concern (OD-1, OD-5); the recovery system is the
component that has to be smart.

**EX-26 · Shutdown actions are idempotent — required, not incidental** (was EX-26). Re-descent works
because every shutdown action is a no-op when already applied: scaling an already-zero Deployment,
cordoning a cordoned node, powering off a node that is already off.

This is a requirement on the actions, not a property that happens to hold. A non-idempotent action
would break re-descent silently, surfacing only during a second dip in a real outage. Consequence: no
flapping protection is needed on the descent path.

**EX-27 · Ascent is bookkeeping only** (was EX-27). Moving the pointer up records that power improved.
It triggers no actions, and therefore needs no hysteresis — moving up on a brief flicker costs
nothing, and the next descent simply re-crosses tiers that are already no-ops.

**EX-28 · The publisher emits data, events, and actions — never analysis** (was EX-28). The line is
between stating what is and interpreting what it means.

Publishable, because each is a fact about current or recorded state: the current tier pointer, wave,
and timing mode; progress counts against a known denominator; every state transition with its
timestamp and the power observed at that moment; every action attempted with target, outcome, and
duration; raw telemetry as read.

Not publishable, because each is an interpretation the subscriber should own: characterizing a
sequence as a dip, a flicker, a recovery, or a brownout; predicting where power is heading;
estimated time to completion or any projection; health, severity, or success judgments about the flow
as a whole.

The test: if it can be computed from current state or read from a device, publish it. If it requires
a theory about what the history means, do not.

**EX-29 · Publish on cadence and on change** (was EX-29). Two independent emission paths, both
required. Cadence is a periodic snapshot at a fixed interval emitted whether or not anything changed;
change is immediate emission on tier transition, wave start and completion, action outcome, timing
mode change, and power state change.

Neither substitutes for the other. Cadence alone loses transitions between ticks; change alone leaves
a subscriber unable to distinguish "nothing is happening" from "the publisher died", because both look
like silence. The cadence is a ceiling on how long the operator may stay quiet, never a floor on how
soon it may act.

**EX-30 · Abort is halt, not undo** (was AE-6). Abort means the executor stops descending. Nothing
already shut down is restored, which is what makes it safe at any point in the flow.

Halt is reserved for abort and is a one-way latch. Power recovery is a different event and takes a
different path (EX-23): it suspends, leaving the pointer unlatched so a later degrade resumes from
its depth.

---

## Bound Decisions

**OD-6 · Shutdown-time audit durability — closed.** PostgreSQL remains the production record store;
the explicit local audit spool is the fallback for records generated while PostgreSQL is unavailable
during execution. Replay/drain automation is a recovery-subscriber concern.

**OD-17 · Executor state persistence for resume — closed.** The executor persists wave position and
compact state in PostgreSQL `executor_resume_states`. Detailed progress remains in the execution,
wave, group, action-attempt, node-release, and signal-handoff tables.

Bound from elsewhere: OD-12 (infeasibility policy — consumed at EX-3), OD-14 (partial-domain scope
— determines what EX-10 executes when one domain fires).
