# Adaptive Execution — Tier Pointer Model

Status: design, 2026-08-03, rev 3. Supersedes the earlier mid-flow adaptive-execution proposal and
tier-pointer revisions 1 and 2; the superseded drafts are not retained in the repository.

Provisional `AE-n` prefix; folds into `PL` and `EX` on integration into the requirement docs.

## The model

A **tier pointer** tracks how far down the tier sequence the shutdown has progressed.

- Power degrades → pointer descends → waves execute.
- Power recovers → pointer ascends → **nothing is restored**.
- Power degrades again → pointer descends from wherever it is → already-executed tiers are
  re-attempted and no-op.

The operator records where it is and what it did. It does not reconcile, does not restore, and does
not model the power curve.

## AE-1 · The operator is a recorder, not a reconciler

On the recovery direction the operator stops descending and publishes. It does not:

- Predict where a power dip will bottom out.
- Judge whether recovery is durable or a flicker.
- Restore, scale up, or uncordon anything.
- Reconcile intended state against actual state.

Recovery is a subscriber concern (OD-1, OD-5). The recovery system is the component that has to be
smart.

## AE-2 · Shutdown actions are idempotent — required, not incidental

Re-descent works because every shutdown action is a no-op when already applied: scaling an
already-zero Deployment, cordoning a cordoned node, powering off a node that is already off.

This is a requirement, not a property that happens to hold. A non-idempotent action would break
re-descent silently, surfacing only during a second dip in a real outage.

Consequence: no flapping protection is needed on descent.

## AE-3 · Ascent is bookkeeping only

Moving the pointer up records that power improved. It triggers no actions. No hysteresis required.

## AE-4 · The publisher emits data, events, and actions — never analysis

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

## AE-5 · Publish on cadence and on change

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

## AE-6 · Abort is halt, not undo

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
- Mode transitions are events under AE-4 and AE-5.

Capability gating applies: a device whose firmware reports a static runtime estimate cannot support
timing adaptation. Declared in the profile telemetry section (CR-2), validated at compile time as in
PL-19. Ungated, this is the OD-9 silent-failure class.

The pointer model needs no such gate — it responds to power state, not runtime projections, and
works on any device.

## Executor state

Pointer and timing mode must survive executor restart, or a restarted instance resumes at the wrong
depth or silently reverts to `Nominal`. Bound to OD-17.

## Open decisions

**OD-27 · Timing adaptation parameters.** Hysteresis count, improvement margin, and scope.

**OD-28 · Relationship to OD-12.** OD-12 decides what to do with an infeasible plan before it
starts; timing adaptation re-decides during.

**OD-29 · Tier ascent trigger.** What power condition moves the pointer up.

**OD-30 · Cadence intervals.** Publish interval during idle versus active flow, and whether it is
global or per-flow.
