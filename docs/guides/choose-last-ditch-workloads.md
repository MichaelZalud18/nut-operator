# Choosing what is last-ditch

Components: Planning & Execution Logic.
Audience: operators.

**The decision:** what must still be running while everything else stops.

Last-ditch is tier 0. It marks the services that have to survive until the very end of a shutdown —
concretely, under an HA control plane, the minimum viable control plane plus whatever the shutdown
itself depends on.

## Why it is a separate idea from "important"

Everything in a cluster is important to someone. Tier 0 is not a priority ranking; it is an answer to
a narrower question: **what does the shutdown itself need in order to finish?**

The operator is a workload in the cluster it is shutting down. So is the API server it issues
evictions against, the DNS it resolves through, and the audit store it writes evidence to. If those
stop while there are still nodes to release, the remaining nodes are not shut down gracefully — they
run until the battery ends.

That is the test. Not "would I miss this", but "would the shutdown stop working without it".

## The rules that follow from it

**Tier 0 is workload-only.** A node cannot be tier 0. The lowest valid tier for a node is 1, the
final orchestrated stop.

**A flow may not target tier 0.** Any flow that does is rejected at compile time, not warned about.
This is structural rather than advisory: the whole point of the tier is that orchestrated shutdown
does not reach it, and a flow able to target it could stop the machinery running the flow.

**The operator's own namespaces are excluded from targeting regardless.** The node agent's namespace
and the controller manager's namespace cannot be evicted or scaled down by a flow, even by a selector
that happens to match. You do not need to remember to protect them.

## The cost of naming too much

Tier 0 is not free capacity. Anything you put there:

- runs to the end of the outage, drawing power the whole time,
- comes down ungracefully when the last node halts, since nothing orchestrates its stop,
- and is invisible to your plan's duration estimate, because it is never part of a wave.

A tier 0 that has grown to a dozen services means the plan you reviewed describes less and less of
what actually happens. If something belongs there because losing it early would be *inconvenient*
rather than *shutdown-breaking*, it is a tier 1 or 2 workload.

## Nodes are ordered explicitly, not by number alone

Control-plane nodes carry explicit late dependencies rather than relying on a low tier number by
itself. The tier expresses intent; the edge is what makes it enforceable when a flow grows and
someone adds a group that did not exist when the tiers were chosen.

## Then

[Set a tier-overrun policy](set-tier-overrun-policy.md), or go straight to
[dry-run to actuate](enable-actuation.md).

Background: [planner-requirements.md](../contributing/design/planner-requirements.md) `PL-22`–`PL-24`,
[glossary](../reference/glossary.md).
