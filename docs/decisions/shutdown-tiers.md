# Assigning shutdown tiers

Components: Planning & Execution Logic.

**The decision:** what stops early, what stops late, and what the numbers mean six months from now.

A tier is a number *you write*. A wave is a set of work the planner *derived*. Tiers are input, waves
are output, and nobody writes a wave — if you find yourself trying to, the thing you actually want is
an ordering edge.

## The scheme

**Higher tiers stop earlier. Lower shuts down later.**

| Tier | Meaning |
| --- | --- |
| 0 | Last-ditch. Workload-only, and a flow may not target it — see [choosing what is last-ditch](last-ditch.md). |
| 1 | The final orchestrated stop. Lowest valid tier for a node. |
| 2+ | Progressively earlier. |

The direction is deliberate: adding an earlier tier never renumbers the critical ones. If you later
discover a class of workload that should shed before everything you have, it becomes a higher number
and nothing below it moves.

Tiers are **not comparable across kinds**. A workload at tier 3 and a node at tier 3 are not saying
the same thing, and the ordering between a workload and the node it runs on comes from clearance
edges the planner derives, not from comparing their numbers.

## Picking numbers you will not regret

Leave gaps. Tiers compile into ordering edges, and the integers themselves carry no meaning beyond
their order, so 2/4/6 costs nothing over 1/2/3 and leaves room to insert.

Start coarse. Three tiers that are obviously right beat seven that need a meeting. You can split a
tier later; unpicking a scheme nobody agrees with is harder.

Say it once. A tier can be written on the group or discovered from a label on the target. Writing
both puts the same number in two places with nothing keeping them in agreement — the group wins when
both are set, but that is not obvious to someone reading the labels.

## When tiers are not enough

Tiers order things in *different* tiers. Two pieces of work in the same tier are concurrent unless
you say otherwise, and "obviously the drain goes first" is not something a tier number conveys.

For ordering inside a tier, or any ordering the numbers do not express, write it:

- `requires` — the named group must stay available while this one shuts down.
- `before` — this group must complete before the named one starts.
- `after` — the named group must complete before this one starts.

That is the entire ordering vocabulary. Tiers plus `requires`/`before`/`after`, and no third knob.
There was one once — `spec.groups[].phase` — and it silently serialized independent groups while
claiming to be a hint; it was removed in `v1alpha1`.

## The trap worth knowing about

A drain and the shutdown of the nodes it drains, both at the same tier and with no edge between them,
compile into the *same wave* and run concurrently. The nodes power off while the eviction meant to
move their workloads is still in flight — and the drain reports success, because it issued the
evictions. Nothing warns about this today. Write the `before` edge.

## Tier inversion

If a workload's tier says it should outlive the node it happens to be running on, that is a tier
inversion. The default remedy withholds that node from power-off for the whole flow, so the cluster
shuts down less than planned rather than pulling power from live work. Per group,
`tierInversionPolicy: Allow` opts out, which is the right answer for genuinely disposable capacity —
say, applications on a burst node that the cluster already elected as sheddable.

Watch `nutoperator_shutdownflow_tier_inversions`; it develops over time as workloads reschedule, so a
compile that was clean last month may not be now.

## Then

[Choose what is last-ditch](last-ditch.md), then decide your
[tier-overrun policy](tier-overrun-policy.md).

Model and compilation: [shutdown-flow.md](../design/shutdown-flow.md).
