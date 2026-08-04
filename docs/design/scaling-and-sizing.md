# Scaling and Sizing Guidance

Status: working document, 2026-08-03. Derived from the component audits and the placement model.

Components: NUT Server / upsd, Node Agent / DaemonSet, Planning & Execution Logic.

Covers when to scale each component, when not to, and which constraints actually bind. Node count
is not the trigger for any of them.

## Summary

| Component | Scaling axis | Node count relevance |
| --- | --- | --- |
| `upsd` (NUTServer) | UPS count — one server per UPS or per small group | None |
| Operator | Availability — 2–3 replicas with leader election | None |
| Node agent (DaemonSet) | Automatic, one per node | Inherent |

## `upsd` — never scale replicas

Replica count stays at 1 per `NUTServer`, permanently. Multiple replicas behind one Service split
driver state and client login accounting, which is what NUT uses to determine when all clients have
disconnected. See F-15 in `docs/audits/nutserver-pod-audit.md`; the recommendation there is to pin
replicas at admission rather than leave the field settable.

**What scales instead is UPS count.** A second UPS means a second `NUTServer` CR, not
`replicas: 2`. Growth is horizontal by power domain, matching the derived-domain model in IN-7.

**Binding constraint is power, not client count.** A realistic single-UPS rack holds on the order of
ten to twenty nodes before VA capacity runs out. `upsd` handles that client count without strain —
it will never be the limiting factor. The practical weak link in the chain is the UPS's own SNMP
management card and the poll interval it tolerates, both of which cap out well before `upsd` does.

**Do not use VPA on `upsd`.** Three reasons:

- Load is flat and predictable — a small C daemon polling SNMP on a fixed interval, with negligible
  CPU and stable memory.
- VPA resizing requires a pod restart, and restarting `upsd` drops every client session. Automatic,
  unscheduled session drops are exactly the failure mode this component should not have.
- There is nothing to tune. Static requests and limits, set once.

## Operator — 2 to 3 replicas, for availability only

Leader election means one instance reconciles regardless of replica count; additional replicas are
hot standbys. Scaling buys failover, not throughput.

**Prerequisite: F-2 must be fixed first.** `--leader-elect` currently defaults to `false`. Adding
replicas before enabling leader election produces concurrent instances compiling and executing
competing flows — strictly worse than a single replica. See
`docs/audits/operator-maturity-benchmarks.md`.

**The trigger is availability tolerance, not cluster size.** The failure being protected against is
"the node running the operator loses power first," which is as likely on a five-node cluster as on
fifty. Given the operator is the component that saves the cluster during an outage, 2–3 replicas
with leader election enabled is reasonable at any scale.

**Related placement note.** The current example placement puts `upsd` and the operator on the same
control-plane node, which concentrates the decision-maker and the telemetry source on one host.
With three control-plane nodes available, separating them is the more defensible layout once
placement is actually enforced — see the placement caveat in the example pod placement diagram
(pending a refresh before it lands in `docs/diagrams/`), and F-18.

## Node agent — scales inherently

DaemonSet, one pod per node, no sizing decision. Tier 0 per OD-4. Relevant findings are F-9
(priority class default) and F-10 (toleration baseline) in
`docs/audits/node-agent-daemonset-audit.md` — both affect whether the agent is *present* on nodes,
which matters more than how it is sized.

## What actually scales with node count

Neither pod count nor pod size. Two single-instance concerns:

- Planner compile time — graph construction, cycle detection, transitive closure, wave computation.
- Wave execution fan-out — concurrent action dispatch and clearance polling in the executor.

Both are solved with algorithms, not replicas.

### Planner performance, when it becomes a problem

Compile time is not currently a concern and is unlikely to become one soon. When it does, the
ordering is:

1. **Incremental recompilation.** Recompute only what the changed structural input touches, rather
   than the full graph. The structural hash from PL-14 already provides the change-detection
   mechanism.
2. **Closure caching keyed by structural hash.** Power domain closures (IN-7) are pure functions of
   the `feeds` edge set and change rarely.
3. **Domain-scoped pruning.** Compile only the affected power domain's subgraph. Blocked on OD-14,
   which decides whether domain-scoped plans are legitimate at all.

Expect these to be worth a hundredfold before hardware acceleration is even worth measuring.

### GPU acceleration — not applicable

Recorded because the question is reasonable and the answer is not obvious.

Graph compilation is pointer-chasing over a small irregular graph: topological sort, cycle
detection, transitive closure. It is branch-heavy and memory-latency-bound, which is the inverse of
the workload profile GPUs accelerate. Even a large cluster produces a graph of thousands of
vertices, where host-to-device transfer overhead alone would exceed the entire compile time.

This does not generalize to other homelab workloads — transcoding, OCR, and similar batch pipelines
are genuinely GPU-shaped. The planner is not in that family.
