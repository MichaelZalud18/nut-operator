# From dry-run to actuate

Components: Node Agent / DaemonSet, Planning & Execution Logic.

**The decision:** when to let this operator actually stop a machine.

Nothing installed by default can power off a node. Getting there is four deliberate steps, and they
are separate on purpose — each one is legible in Git and visible in `/status` before it can affect
hardware.

## The gates

**1. `ShutdownFlow.spec.mode: Enforce`** — the flow may take real action on workloads. Without it the
flow compiles, publishes its plan, and touches nothing.

**2. The flow's approval annotation** — enforcement is approved on this specific resource. Mode says
what the author wants; the annotation says someone signed off.

**3. `NodePowerAgent.spec.mode: Actuate`** — this agent may halt its node.

**4. `spec.shutdown.actuatorPolicy: PowerOff`** plus the agent's own approval annotation — the
actuator issues `reboot(2)` rather than recording a simulation.

Both approvals are re-checked **when the flow fires, not when it was deployed**. Revoking one
mid-flow downgrades execution at the next wave boundary; in-flight actions in the current wave
complete or time out. Absence of either approval never silently proceeds — it drops to dry-run.

## What to have in hand before crossing

**A dry-run you have actually read.** Not "it compiled" — the compiled waves, in order, with their
durations. Dry-run executes everything except effects: wave sequencing, instance enumeration,
clearance evaluation, and record writing all run identically, and the waits are honored. So the
duration it reports is the duration the real run will take.

**A feasibility verdict you believe.** `status.planFeasibility` compares the plan's estimate against
the runtime your UPS reports, per tier and in total. It warns and never blocks. A plan that does not
fit will still run — and will still not fit.

**Estimates built from something real.** A fresh install estimates from declared timeouts, which are
guesses. A rehearsal (`power.zalud.io/rehearsal-request`) runs one approved enforce-mode sample
against real targets and feeds those durations back into the estimates. That is how the numbers stop
being fiction before an outage rather than after one.

**Proof the cluster can halt a node at all.** This is the one that cannot be inferred:

```sh
make verify-actuation NODE=<node> AGENT=<agent> APPROVE=yes
```

It powers off a real machine and leaves it off. What it establishes cannot be checked any other way —
in particular, that the actuator is genuinely in the host PID namespace, because from a non-initial
namespace `reboot(2)` returns success and does nothing. A node that goes dark is the only available
proof. Full procedure in [install.md](../guides/install.md).

## Order to do it in

Widen the blast radius one step at a time, and let each step run through a real power event if you
can wait for one.

1. `MonitorOnly` — telemetry only, no actuator container at all.
2. `DryRun` + `Simulate` — the actuator accepts and records signals, touches nothing.
3. `Enforce` on the flow, agents still simulating — workload actions become real; nodes do not stop.
4. `Actuate` + `PowerOff` on **one** agent, verified with `make verify-actuation`.
5. The rest of the fleet.

Step 3 is the one worth lingering on. It is where drains, scale-downs, and hooks become real against
live workloads while the machines stay up — the largest behavior change in the sequence, and the one
you can still undo.

## After you cross

- `nutoperator_halt_last_verified_timestamp_seconds` answers "has this cluster ever proven it can halt
  this node", per node, months later. Absence of a series is itself the finding.
- `nutoperator_halt_attempts_total{outcome}` separates nodes that stopped from nodes that were asked
  and did not.
- The actuator narrates the halt path gate by gate, so a node that stays up names the link that broke.

## Background

[security.md](../reference/security.md) for the privilege boundary,
[node-agent-operand.md](../design/node-agent-operand.md) for what the actuator will and will not do,
[metrics.md](../reference/metrics.md) for what to alert on.
