# Adaptive Execution — Tier Pointer Model

Status: design, 2026-08-03, rev 3. Supersedes the earlier mid-flow adaptive-execution proposal and
tier-pointer revisions 1 and 2; the superseded drafts are not retained in the repository.

Components: Planning & Execution Logic.
Audience: contributors.

The provisional `AE-n` prefix is retired. The six model requirements are `EX-25`–`EX-30` in
[executor-requirements.md](executor-requirements.md); the runtime-estimate capability gate, which had
also been carried as `AE-6`, is `CR-4` in [planner-requirements.md](planner-requirements.md). Those
docs hold the numbered requirements; this one holds the model they came from.

## The model

A **tier pointer** tracks how far down the tier sequence the shutdown has progressed.

- Power degrades → pointer descends → waves execute.
- Power recovers → pointer ascends → **nothing is restored**.
- Power degrades again → pointer descends from wherever it is → already-executed tiers are
  re-attempted and no-op.

The operator records where it is and what it did. It does not reconcile, does not restore, and does
not model the power curve.

## EX-25 · The operator is a recorder, not a reconciler

On the recovery direction the operator stops descending and publishes. It does not:

- Predict where a power dip will bottom out.
- Judge whether recovery is durable or a flicker.
- Restore, scale up, or uncordon anything.
- Reconcile intended state against actual state.

Recovery is a subscriber concern (OD-1, OD-5). The recovery system is the component that has to be
smart.

## EX-26 · Shutdown actions are idempotent — required, not incidental

Re-descent works because every shutdown action is a no-op when already applied: scaling an
already-zero Deployment, cordoning a cordoned node, powering off a node that is already off.

This is a requirement, not a property that happens to hold. A non-idempotent action would break
re-descent silently, surfacing only during a second dip in a real outage.

Consequence: no flapping protection is needed on descent.

## EX-27 · Ascent is bookkeeping only

Moving the pointer up records that power improved. It triggers no actions. No hysteresis required.

## EX-28 · The publisher emits data, events, and actions — never analysis

The line is between **stating what is** and **interpreting what it means**.

Publishable, because each is a fact about current or recorded state:

- Current tier pointer, current wave, current timing mode.
- Progress counts and percentages — groups complete in the current wave, waves complete in the
  flow, tiers complete. These are counts against a known denominator, not judgments.
- Every state transition, with timestamp and the power state observed at that moment.
- Every action attempted, with target, outcome, and duration.
- Raw telemetry values as read.

Not publishable, because each is an interpretation the subscriber should own:

- Characterizing a sequence as a dip, a flicker, a recovery, or a brownout.
- Predicting where power is heading or when it will return.
- Estimated time to completion, or any projection.
- Health, severity, or success judgments about the flow as a whole.

Rule of thumb: if it can be computed from the current state or read directly from a device, publish
it. If it requires a theory about what the history means, do not.

Event log shape:

```text
23:00:00  entered tier 3        power: OB, runtime 1200s
23:15:00  completed tier 3      power: OB, runtime 1450s   (7/7 groups)
23:45:00  entered tier 2        power: OB LB, runtime 400s
00:10:00  entered tier 3        power: OL, runtime 2100s
```

## EX-29 · Publish on cadence and on change

Two independent emission paths, both required:

- **Cadence.** A periodic snapshot of current state at a fixed interval, emitted whether or not
  anything changed. This gives subscribers a heartbeat, makes publisher liveness observable, and
  means a subscriber that starts mid-flow does not wait for the next event to learn the current
  state.
- **Change.** Immediate emission on tier transition, wave start and completion, action outcome,
  timing mode change, and power state change.

Neither substitutes for the other. Cadence alone loses transitions between ticks; events alone leave
a subscriber blind during quiet periods and unable to distinguish "nothing is happening" from "the
publisher died."

Cadence interval should be configurable and is likely to differ between normal operation and an
active flow.

Implemented as the reconcile interval rather than a separate timer, because reconciling is what
republishes status: a second loop emitting snapshots alongside the reconciler could report a state
the reconciler had already moved past. The cadence bounds how long the operator may stay quiet and
never delays how soon it may act — a trigger hold expiring in ten seconds is not deferred
to the next heartbeat. A flow counts as active while a trigger is eligible, while one has matched and
is serving its hold, and while an execution is `Running`.

## EX-30 · Abort is halt, not undo

Abort means the operator stops descending. Nothing already shut down is restored. Safe at any point
in the flow.

## Timing adaptation — independent of the pointer

- `Relaxed`, `Nominal`, `Urgent` compiled together, deterministically, from one structural input.
- The executor selects a mode; it never recomputes timings and never recompiles mid-flow, which
  would invalidate the plan hash (PL-14) and break determinism (PL-27).
- Adaptation modifies timeouts, grace periods, and inter-wave delays only. Never wave order, never
  membership, never edges.
- Escalation on a single observation; relaxation requires sustained improvement.
- Evaluated at wave boundaries only.
- Mode transitions are events under EX-28 and EX-29.

Hooks are a timing-adaptation input, not a pointer input. A pre-shutdown hook declared on a group
carries its own bounded timeout, and that timeout is exactly the class of value adaptation scales:
what a system can be given in `Relaxed` is not what it can be given in `Urgent`. A hook never gates
descent — an unfinished hook does not hold the pointer, and shutdown proceeds regardless.

Capability gating applies: a device whose firmware reports a static runtime estimate cannot support
timing adaptation. Declared in the profile telemetry section (CR-2) as
`spec.telemetry.runtimeEstimate: Dynamic | Static`, and read through
`capability.MatchResult.SupportsTimingAdaptation()`. Ungated, this is the OD-9 silent-failure class.

Unset is unverified, and unverified is not Dynamic. Adaptation lengthens and shortens timeouts during
an outage according to how much runtime is left, so it requires an affirmative claim that the number
responds to load -- assuming the good case would drive adaptation from a constant at the one moment
nobody is watching. The declaration is therefore three-valued in effect: Dynamic permits adaptation,
Static forbids it, and absence forbids it while asserting nothing about the device.

The same declaration already has a consumer ahead of the pointer work. Advisory feasibility answers
"is there enough runtime to finish", which a fixed estimate cannot support, so a domain containing a
Static device compiles to `Unknown` with reason `RuntimeEstimateStatic` naming the device. This
follows PL-32: a value that cannot move is missing data with a plausible number attached, and missing
data never yields an optimistic verdict. Absence of the declaration deliberately changes nothing --
every profile shipped today leaves it unset, and treating that as Static would downgrade every
existing deployment on no evidence.

The gate reuses trigger-capability validation rather than duplicating it. Capability matches carry
their declared telemetry content into the planner, and `validateTriggerCapabilities` rejects or
degrades against the devices a trigger targets using a some/all split — so "do these devices report
`battery.runtime`" is answered at compile time by the same mechanism.

The pointer model needs no such gate — it responds to power state, not runtime projections, and
works on any device. Enforcement is separately gated: under OD-31 a device that matched no product
capability profile blocks `Enforce` unless `spec.safety.allowUnidentifiedDevices` records the
acceptance. The pointer model is device-agnostic; running it against an unverified device is an
explicit decision made before the outage, not during one.

## Executor state

Pointer and timing mode must survive executor restart, or a restarted instance resumes at the wrong
depth or silently reverts to `Nominal`. Bound to OD-17.

Both live in `executor_resume_states`, alongside execution ID, plan config hash, current wave index,
phase, and an open `state` payload, written through `UpsertExecutorResumeState` as an upsert keyed
by execution. They belong in that record rather than a parallel one.

Durability caveat, shared with everything on this path: if PostgreSQL is unavailable, resume state
falls to the audit spool and returns on the first reconcile that can write again. A restart during a
database outage therefore resumes from the last state that actually landed, not necessarily the last
one attempted.

## Implementation

`internal/adaptive` is the pure model: no I/O, no Kubernetes client, no wall-clock reads, matching
`internal/planner` and `internal/trigger`. Every function takes the observations plus current state
and returns the next state and what changed; the caller owns time, persistence, and publication. The
behavior worth being certain about here is state-machine behavior, and a state machine that reads the
clock can only be tested by waiting.

Three separable pieces, deliberately not sharing mutable state:

- `parameters.go` — OD-27 through OD-30 as data, with defaults and validation.
- `pointer.go` — EX-25 to EX-27 and EX-30: descent, ascent, halt, and re-execution reporting.
- `timing.go` — mode selection and its asymmetric hysteresis.

The pointer and the timing mode are independent, as this document requires. Splitting them at the
file boundary with no shared state is what stops a later change from quietly coupling them.

`PointerState` is persisted across executor restarts, so it can arrive from an older or partially
written record. `normalize()` repairs a `Deepest` that was never set or has drifted above the current
tier, conservatively, to the current tier: a wrong value there mislabels re-execution as new work,
which is a reporting error at exactly the moment a subscriber is trying to understand a second dip.

Unknown or untrusted runtime selects `Urgent`, never `Relaxed`. PL-32 and CR-4 converge on that: the
honest reading of "I don't know how long I have" while running on battery is that there is not much.

### Where the capability gate lives

There is one mechanism, not two. `PowerObservation.RuntimeTrusted` carries the CR-4 declaration, and
the mode selector does the rest: without a trusted runtime the only reachable modes are `Relaxed` on
mains and `Urgent` on battery. `Nominal` is the graduated middle, and reaching it requires a number
the load actually moves.

That is the gate, stated exactly. What CR-4 forbids is a fixed firmware estimate driving graduated
timing decisions, and an unreachable `Nominal` is precisely that prohibition. What it does not forbid
is responding to the binary power state, which every device reports and no capability declaration is
needed to trust. A separate "adaptation disabled" flag would be a second mechanism for the same rule,
and the two would eventually disagree.

Trust is all-or-nothing across the devices a flow selected. One device reporting a constant is enough
to make the aggregate untrustworthy, because the shortest runtime across the set — the number the
model actually reads — could be that constant.

### Executor integration

`internal/executor` owns everything impure: reading power, persisting state, emitting events. The
pointer and the timing mode are evaluated at wave boundaries, never inside a wave, so adaptation can
change timings without touching wave order or membership — those are hashed into plan identity
(PL-14).

Three inputs cross the boundary:

- **The observation**, read through an injected `PowerObserver` at each boundary. Reading it once at
  trigger time would defeat the point of evaluating at boundaries at all.
- **The tier**, taken from each compiled wave's own `shutdownTier` rather than counted. Counting waves
  drifts the moment one tier spans two waves.
- **The prior state**, loaded from the last published execution status so a restarted executor resumes
  where it was (OD-17, EX-14).

Reading power degrades rather than refuses. A `UPSDevice` that cannot be read contributes an unknown
runtime, exactly like a stale one, and the flow continues — PL-32 keeps the reading pessimistic while
PL-31 keeps the flow running, since a cluster that dies ungracefully because one object went missing
is the worse outcome. Only when nothing at all can be read does the flow carry forward the power state
the trigger fired on, because "every device reports mains is back" and "no device reported" reduce to
the same observation and mean opposite things.

### What adaptation is allowed to change

Declared durations only: group timeouts and `Wait` durations. Never wave order, never membership,
never edges — those are hashed into plan identity (PL-14).

**The amount is measured, not chosen.** Compression is the ratio between the runtime the devices
report and the time the remaining plan's own declared durations add up to:

```text
compression = (remaining runtime × (1 − reserve)) ÷ remaining declared plan
```

A fixed "quarter of declared when urgent" answers a question nobody asked. Whether the shutdown
finishes depends on how much runtime is left *and* how much plan is left, and both are measurable at
the moment the decision is made. Two clusters with the same UPS and different plans need different
compression, and one multiplier cannot express that. The ratio also tightens on its own as the flow
proceeds and the battery drains, with no schedule to maintain.

The three named modes remain, and remain useful — they are what an operator reads and what the
hysteresis stabilizes — but no timeout is derived from a mode label. Mode and budget come from the
same observation and never from each other, which is what stops a label quietly becoming the source
of a deadline.

Three things bound the result:

- **Compression clamps at 1.** The declared duration is what the author said the step needs, and
  plentiful runtime is not a reason to overrule it upward — only to honor it.
- **A reserve is held back** from the runtime before the plan may use it. The plan measures the work
  it declares; it does not measure the node-agent handoff, the actuator's checks, or the seconds
  between release and actual power loss. Filling exactly the measured runtime would spend that tail.
- **A minimum compression** below which the plan is reported as not fitting. Past that point the
  shutdown does not finish faster; every action just fails its deadline, engaging abort policy at the
  worst possible moment. The flow still runs — refusing mid-outage is worse (PL-31) — but the verdict
  is published rather than buried.

An undeclared duration stays undeclared. Compressing "no limit" would invent a deadline nobody wrote.

With no trustworthy runtime figure, the budget is *assumed* rather than optimistic: the urgent
threshold stands in for the measurement, and the record says it was assumed, so nobody later reads an
assumption as a reading.

### Recovery mid-flow

**Power returning mid-execution changes nothing about the execution.** It is recorded and nothing
else: the pointer ascends as bookkeeping (`EX-27`), the current execution runs its remaining waves to
completion, and whether another execution begins is decided a level up by trigger eligibility.

The executor states this at the branch itself — `internal/executor/adaptive.go` takes the ascent path
only when `ShouldDescend` is false, and the wave runs either way. There is no phase in which a flow
is stopped by good news; the execution phases are `Running`, `Completed`, `Aborted`, `Failed`, and
`Skipped`.

**Why the execution finishes rather than stopping.** Stopping needs a decision, and a decision needs a
theory about what the power reading means — is this recovery, or the first half of a flicker? `EX-28`
puts that interpretation on the subscriber, not the operator. Running to completion needs no theory:
every remaining action is one the plan already authorized under conditions that had already justified
it, and by `EX-26` each is a no-op if the situation has genuinely improved enough for it not to
matter.

**Why this costs nothing.** The expensive-sounding case is a flow that keeps shutting things down
after mains returns. In practice the flow was authorized by a trigger that has already fired, the
remaining waves are the deepest tiers, and the alternative — halting partway — leaves the cluster in
a state no one declared: some tiers down, some up, and no record of which decision produced it. A
completed execution is a state a recovery subscriber can act on. A partially-run one is a puzzle.

Trigger eligibility governs the next execution, so a genuine recovery simply means no new execution
starts, and a flicker means the next one begins from a pointer that was never latched
(`PointerState.Halted` stays false) and is persisted in the resume state.

## Decisions affecting adaptive execution

**OD-27 · Timing adaptation parameters.** Hysteresis count, improvement margin, and the two bounds on
compression. Defaults are implemented and validated in `internal/adaptive` — escalate on one
observation, relax on three, 10% improvement margin, 20% runtime reserve, 10% minimum compression.
The compression amount itself is no longer a parameter: it is measured. What stays open is the
reserve, which stands in for a handoff tail nobody has timed, and the minimum, which is the point at
which the plan is declared not to fit.

**OD-28 · Relationship to OD-12.** OD-12 decides what to do with an infeasible plan before it
starts; timing adaptation re-decides during.

`OD-27` is the only genuinely open decision on this page. Two others were once listed here and are
now settled — recorded below rather than deleted, because a decided question left phrased as an open
one is how it gets re-argued:

**OD-29 · Tier ascent trigger — settled.** Ascent is the strict inverse of descent: mains present and
no low-battery assertion (`ShouldAscend`). No hysteresis, no hold time, no confirmation window.
`EX-27` makes ascent bookkeeping that triggers nothing and `EX-26` makes the re-descent that follows
a sequence of no-ops, so a flicker costs nothing to get wrong. See "Recovery mid-flow" above, and
`settled-questions.md` §2 — proposing a hold time or confirmation window near power state is the tell
that this one is being reopened.

**OD-30 · Cadence intervals — settled.** Publish cadence is cluster-wide, on
`PowerManagementCluster.spec.observability.publishCadence`, defaulting to 60s idle and 10s active. It
describes the publisher's liveness and there is one publisher; per-flow values would make the
reconcile rate a workload author's decision and give a subscriber several intervals to reason about.
