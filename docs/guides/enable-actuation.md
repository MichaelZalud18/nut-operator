# Enabling actuation

Components: Node Agent / DaemonSet, Planning & Execution Logic.
Audience: operators.

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

**Proof the cluster can halt a node at all.** The one thing on this list that cannot be inferred from
any amount of dry-run output. The procedure is below.

## Proving the cluster can halt a node

Everything above verifies that the operator *plans* correctly. It does not verify that this cluster
can carry the plan out, and those are different questions: the configuration able to halt a node —
`mode: Actuate` with `actuatorPolicy: PowerOff` — renders `hostPID`, a `CAP_SYS_BOOT` file
capability, and a Pod Security posture that a dry-run never exercises.

Five things only a real run can establish, and the fourth cannot be checked any other way: **from a
non-initial PID namespace, `reboot(2)` returns success and does nothing.** A node that actually goes
dark is the only available proof.

```sh
make verify-actuation NODE=<node> AGENT=<agent> APPROVE=yes
```

**This powers off a real machine and leaves it off.** It needs physical or IPMI/BMC access to
return, and it comes back cordoned. The target must be cordoned and drained first; the procedure
refuses otherwise. `NODE` has no default.

Restart was considered and rejected rather than overlooked: a restarted node returns with a fresh
actuator holding an empty dedupe set, and a signal still inside its TTL halts it again. A node that
is off stays off, so a leftover signal is inert.

The signal is hand-delivered into the projected Secret — the same path `OD-37` authorizes, not a
second channel — which isolates kubelet admission, file-capability survival, and the host PID
namespace from planner correctness. See [Security](../reference/security.md) for the boundary this
proves.

### Reading the gate trace

The run prints the actuator's gate trace on both outcomes, streamed live because the container is
about to power off with its own log. Each link on the halt path logs itself — `SignalChannel`,
`SignalAccepted`, `FlowBinding`, `ModeAuthorized`, `Sync`, `CapabilityEffective`, `SyscallIssued` —
so a node that stays up names the link that broke instead of leaving you with a machine that is
either dark or not. `SyscallIssued` is written immediately before `reboot(2)` and cannot be written
after it, which is what makes the host-PID-namespace case detectable at all: that line, nothing after
it, and a node still running.

Real executions are also recorded on the operator, which is the side that survives them — see
`nutoperator_halt_*` in [Metrics](../reference/metrics.md), where
`nutoperator_halt_last_verified_timestamp_seconds` answers "has this cluster ever proven it can halt
this node" months later. This procedure deliberately does not produce those: it bypasses the executor
so that planner correctness cannot fail it, and the executor is where the halt clock starts.

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

[Security](../reference/security.md) for the privilege boundary,
[the node-agent design](../contributing/design/node-agent-operand.md) for what the actuator will and
will not do, [Metrics](../reference/metrics.md) for what to alert on.
