# Tier-overrun policy

Components: Planning & Execution Logic.

**The decision:** what happens when a tier runs past its budget and the battery does not wait.

Every group declares a timeout, and a timeout says what happens to *that group*. It says nothing
about the tier queued behind it. When tier 4 is still running at the moment tier 3 was due to start,
something has to give, and the executor deliberately does not choose for you.

Set it on the flow: `spec.tierOverrunPolicy`.

## The three answers

**`Wait`** (default) — tier 3 starts only when tier 4 finishes. Tier 3 absorbs the overrun and runs
against whatever time is left.

The safe answer, and the only one that never cuts a running workload short. Choose it when you do not
yet have measured durations to reason with — which is everyone, initially.

**`Overlap`** — tier 3 starts on schedule while tier 4 continues. Both run; tier 4 keeps its
remaining budget, and the executor waits for all overlapped waves before closing the execution.

Correct when the tiers touch nothing in common. The thing to check before choosing it: whether two
tiers running together will contend for the same nodes, the same storage, or the same API budget. If
tier 4 is draining the machines tier 3 wants to talk to, overlapping them is worse than waiting.

**`Preempt`** — tier 4's groups are stopped so tier 3 starts on schedule with its full declared
budget.

This trades one workload's clean shutdown for another's, and nothing the operator can observe tells
it which trade is right. Choosing `Preempt` is a statement that tier 3's work matters more than tier
4 finishing cleanly. Only you can make that claim, which is exactly why it is a policy and not a
heuristic — deciding it from a timer would be the operator substituting its judgement for yours.

## How to actually pick

Run dry-runs and rehearsals first, then read what they measured. The relevant signals:

- `nutoperator_shutdownflow_tier_overruns_total` — which tier overran, under which policy, and what
  the executor did.
- `nutoperator_shutdownflow_tier_overrun_seconds` — by how much. Same labels, so the count and the
  amount describe one event stream.
- `status.lastExecution.tierOverruns` — the same facts on the object.

A tier that overruns by seconds is a timeout that wants adjusting. A tier that overruns by minutes,
every time, is a plan that does not fit — and `status.planFeasibility` was already telling you that
before the outage.

## What does not change

Whichever policy you choose, the plan is not truncated and the flow is not refused mid-outage. An
infeasible plan warns and runs; dropping tiers to make the numbers work would substitute the
operator's judgement for the author's, and refusing to start once the power is already failing is the
worst outcome on the table.

Timing compression is separate and always on: declared timeouts shorten as measured runtime drains,
never lengthen. The declared value, the effective value, and the ratio between them are all recorded,
so a short timeout you chose stays distinguishable from one the runtime forced.

## Then

[From dry-run to actuate](dry-run-to-actuate.md).

Full treatment: [executor-requirements.md](../design/executor-requirements.md) `EX-31`,
[adaptive-execution-tier-pointer.md](../design/adaptive-execution-tier-pointer.md).
