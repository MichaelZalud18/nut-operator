# Settled Questions

Status: standing reference. Read before proposing a new decision, opening an `OD` number, or
writing a design paragraph that begins "but what if power…".

Components: all.

These are questions that keep getting re-raised and are already answered. Each entry states the
temptation, the requirement that settles it, and the tell — how to recognize you are doing it before
you have written three paragraphs about it.

The meta-rule that produced this file: **an apparent new decision is usually an existing requirement
you have not re-read.** Check the requirement first. `OD-35` was minted, argued, and retired inside
one session because that check was skipped.

---

## 1. Recovery, restoration, and coming back up

**Settled by:** `OD-1`, `OD-5`, `EX-25`.

The operator shuts things down. It does not bring them back, does not uncordon, does not scale back
up, does not reconcile intended state against actual state, and does not track whether recovery
succeeded. Recovery is a subscriber's job, and the subscriber is the component that has to be smart.

**The tell:** any sentence containing "once power is back", "resume normal operation", "restore", or
"return to service". Also: proposing that the operator needs to know when the network is healthy
again. It has no way to know that and no reason to.

## 2. Flicker hysteresis, debouncing, and "is this recovery durable"

**Settled by:** `EX-26` (actions are idempotent — required, not incidental) and `EX-27` (ascent is
bookkeeping only).

A short flicker moves the tier pointer up. That triggers no actions. When power drops again the
pointer descends and re-crosses tiers whose actions are already applied, and every one of those is a
no-op: scaling an already-zero Deployment, cordoning a cordoned node, powering off a node that is
already off. The cost of a flicker is therefore zero, which is exactly why `EX-27` says no hysteresis
is needed on this path.

So there is nothing to protect against and no threshold to tune. A flicker is logged like any other
NUT observation and shows up in metrics. That is the whole behavior.

**The tell:** proposing a "hold time", "confirmation window", "debounce", or "require the condition
to persist" anywhere near power state. If the answer involves waiting to be more sure before acting,
re-read `EX-26` and ask what the wrong action would actually cost. On the descent path it costs
nothing.

**Known contradiction:** the executor's `SuspendOnRecovery` currently ends the run when power
returns, which is not bookkeeping and does have a cost. See open work in `docs/tasks.md`.

## 3. Enforcing feasibility on the operator's behalf

**Settled by:** `OD-12`, `PL-31`.

When the plan will not fit the battery, the answer is warn and run, loudly and legibly. Not reject,
not silently truncate. The person who wrote the flow holds the risk; this operator holds the
information and owes them a clear statement of it. Refusing mid-outage is the worst available
outcome, and quietly dropping tiers substitutes the operator's judgement for the author's.

**The tell:** words like "policy", "enforce", "block", or "reject" attached to something that is a
prediction rather than a structural error. Structural errors — a cycle, an unknown dependency, an
unreachable node — are rejected at compile time and that is correct. Estimates are not errors.

## 4. Analysis and interpretation in published output

**Settled by:** `EX-28`.

Publish what is: current tier, wave, timing mode, progress counts, every transition with its
timestamp and observed power, every action with target and outcome, raw telemetry. Do not publish
what it means: "flickering", "dip", "brownout", "recovering", estimated time to completion, or any
health judgement about the flow as a whole.

The test is one line: if it can be computed from current state or read from a device, publish it. If
it requires a theory about what the history means, it belongs to the subscriber.

**The tell:** any status field whose value is an adjective.

## 5. Becoming a workflow engine

**Settled by:** `SB-15`, and `HK-7` for the hook case specifically.

Retries, backoff, DAGs, branching, artifact passing, and templating from prior results are engine
concerns. This operator invokes, bounds, and records. Reaching a real engine is fully supported and
is what `HK-3` exists for.

**The tell:** proposing that a hook or action gets a retry budget, or that a failure should pick a
different branch.

## 6. A lifecycle controller

**Settled by:** permanently rejected, repeatedly. Argo Events plus Argo Workflows is the entire
orchestration implementation. There is no additional controller that owns lifecycle.

**The tell:** sketching a new controller whose job is to sequence other components.

## 7. Naming a node in documentation or status

**Settled by:** role-based naming throughout.

Never track or narrate which node a workload landed on, not even as point-in-time status. Describe
roles — control-plane, worker — never hostnames.

---

## When something really is a new decision

It is a new decision when the requirements are silent *and* two defensible answers lead to different
code. Say which requirement you checked and why it does not cover the case, then mint the number.
If you cannot name the requirement you checked, you have not checked.
