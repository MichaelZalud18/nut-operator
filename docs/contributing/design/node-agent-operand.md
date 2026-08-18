# Node Agent Operand

Status: design. Covers the DaemonSet the `NodePowerAgent` CRD renders — what authorizes a node to
halt, how that authorization is delivered and withdrawn, and what the operand refuses to do.

Components: Node Agent / DaemonSet.

`NA-n` identifiers are stable and are not reused or renumbered.

This is the only component that can stop a machine. Everything below is written from that fact: the
question is never "how does the agent shut a node down" — `reboot(2)` is one syscall — but "what
makes this particular halt legitimate, and how does the operand know when it stops being so."

Findings and evidence live in [node-agent-daemonset-audit.md](../audits/node-agent-daemonset-audit.md)
(`F-8` – `F-14`, `F-33` – `F-36`, `F-54` – `F-92`).

## The authorization boundary

**NA-1 · One path authorizes a halt, and it is the operator's (`OD-37`).** The actuator watches the
operator's projected Secret and nothing else. NUT's own `SHUTDOWNCMD` path — the conventional way a
`upsmon` secondary halts its host — keeps its writer (`cmd/power-signal-writer`), its signal format,
and its local file, and holds no authority: the shared `power-agent-run` tmpfs is not mounted into
the actuator, and the actuator's default signal path is derived from the node name rather than
falling back to that tmpfs. No supported configuration wires the local path to a halt.

This was decided against the backstop reading, which is the tempting one. A backstop engages when the
operator is unreachable — which is exactly when ordering matters most. Every agent runs
`MINSUPPLIES 1`, so one UPS reaching OB+LB would release its entire coverage simultaneously, which is
not a degraded shutdown but an uncoordinated one. The accepted cost is stated plainly: an
undeliverable signal leaves nodes running until the UPS dies.

**NA-2 · The actuator holds no API credentials.** It reads a mounted volume and calls a syscall. It
does not watch the API, patch node objects, or hold RBAC of its own. This is what makes the operand's
blast radius bounded by what kubelet already projects into it, and it is why signal state is carried
in the Secret rather than in node annotations — annotations would require giving a node-local
container the ability to write to the API.

## The signal lifecycle

**NA-3 · Absence is the record (`F-87`).** The operator writes a node's key into the signal Secret to
actuate it and **deletes that key** when the actuation completes or the flow's episode ends. The
signal does not outlive the actuation it authorized.

Without revocation, only the TTL stood between a stale file and a re-actuation: the actuator's memory
of what it had already acted on is a per-pod `emptyDir`, so any replacement pod — rollout, kubelet
restart, OOM kill, eviction, crash — comes up with an empty `seen` set, finds a live in-TTL signal,
and halts the node again. Kured's sentinel gets this property free by living on tmpfs that the reboot
clears. Revocation is how this operand earns the same one.

Where the owning flow cannot be read at all, the signal is **kept** until its TTL retires it. A
failed lookup is not evidence that the episode ended, and inventing that evidence would revoke a live
release mid-outage.

**NA-4 · An empty channel is distinguishable from a missing one (`F-86`).** The signal Secret always
carries a `delivery-channel` marker key and mounts **non-optionally**. Both halves matter. A Secret
that mounts optionally and is absent looks exactly like one that is present and quiet, so a channel
that cannot deliver would read as a channel with nothing to say — and the failure would surface only
when a shutdown silently did not happen. Mounting non-optionally makes an undeliverable channel fail
readiness instead.

**NA-5 · Whether a node may halt is the actuator's own mode, and no writer can reach it.** The signal
payload carries no `dryRun` field. `spec.shutdown.actuatorPolicy` is `Disabled` (no watch loop),
`Simulate` (accept and record, touch nothing), or `PowerOff` (`reboot(2)` with
`LINUX_REBOOT_CMD_POWER_OFF`), and it is read from the container's own configuration.

A dry-run flag inside the signal would mean the authority to halt a node and the instruction not to
were travelling together in the same document, over the same path, decided by the same writer. Mode
belongs to the thing being asked, not to the asking.

Defense in depth remains on top of revocation, not instead of it: a scan stops at the first live
signal, so one episode delivered twice actuates once (`F-58`), and an agent declaring a
`shutdownFlowRef` accepts releases only from that flow.

## Actuation

**NA-6 · The capability is proven at startup, not discovered during a power event (`F-61`).**
`CAP_SYS_BOOT` reaches the actuator as a **permitted-only file capability** on the binary, and the
process raises it into the effective set only for the syscall itself.

Permitted-only is deliberate and is what makes the image runnable at all: a file capability marked
effective (`cap_sys_boot=ep`) makes `execve` fail with `EPERM` anywhere `CAP_SYS_BOOT` is outside the
bounding set, which is every cluster that has not opted in. Permitted-only masks instead of failing,
so the same image runs in `MonitorOnly` on a locked-down node and arms on a node configured for
actuation.

The actuator checks the permitted set at startup and **refuses to arm without it**, because every way
of losing `CAP_SYS_BOOT` is silent: a `securityContext` that does not request it, an image registry
or builder that drops the `security.capability` extended attribute, or a capability-dumb parent
process inside the container losing it to `no_new_privs`. Each produces a pod that looks healthy and
would fail with `EPERM` at the one moment it is needed.

`hostPID` is required rather than incidental. Without it, `reboot(2)` kills the container and
**reports success** — the worst available failure, since the caller records a halt that did not
happen. The pod runs under the `RuntimeDefault` seccomp profile regardless.

**NA-7 · A namespace that would reject the actuating pod is reported, never relabelled.** `hostPID`
and non-default capabilities place the pod outside Pod Security `baseline` on their own. When the
operand namespace's Pod Security level would reject it, that surfaces on the agent's `Degraded`
condition. The operator does not relabel the namespace to make its own pod admissible — an operator
that quietly lowers a cluster's security posture to deploy itself has substituted its judgement for
the cluster admin's.

## Reporting

**NA-8 · Readiness reports the failure modes that matter mid-outage.** Both containers have readiness
probes that can actually fail, which is the property `--version`-style probes lack (`F-64`).

- The actuator's probe runs the same code path as the running process, so a probe cannot report
  ready on a path the watch loop would fail.
- `upsmon`'s probe queries every `<ups>@<server>` it monitors rather than anonymously listing the
  host, so an agent that is alive but cannot reach its configured UPS server stays up and NotReady
  instead of passing on a connection it does not use.
- A node whose clock rejects every signal the operator sends reports NotReady rather than logging it,
  so the failure reaches `status.nodeStatuses` instead of a container log nobody reads during an
  outage.

**NA-9 · The halt path narrates itself, link by link.** A node that stays up after a shutdown signal
is the same observation whether the operator never wrote the Secret, kubelet never projected it, the
actuator rejected the payload, the capability was missing, or `reboot(2)` returned an error — and the
last of those is indistinguishable from success when the container is not really in the host PID
namespace. So each link logs itself as `halt gate=<name> result=pass|fail`:
`CapabilityPermitted` at arm time, `SignalChannel` for the projection, then `SignalAccepted`,
`FlowBinding`, `ModeAuthorized`, `Sync`, `CapabilityEffective`, and `SyscallIssued`.

This is not a log level, and there is nothing to switch on. Global verbosity would bury the trace
under the polling loop, which deliberately says nothing on a normal tick — `SignalMissing` is the
common case and logging it would be a line every five seconds forever. The gates are scoped to the
path that halts the machine, and that path runs at most once in a container's life, so the trace is a
handful of lines, once, ever. A switch would also have to be thrown at the one moment nobody can
reach the node to throw it.

Two gates are written where they can still be read rather than where they are cheapest.
`SignalChannel` reports on transition, including its first evaluation, because it is the only link
that can be broken for months without anything asking it to do something. `Sync` reports when the
flush *starts*, not only when it finishes, because a trace that records only completed flushes cannot
distinguish a sync that hung from a sync that was never reached — and those point at different halves
of the system. `SyscallIssued` is the last line the process writes on a working path; its presence
with nothing after it and the node still up is the host-PID-namespace finding.

**The record of whether the node stopped is kept by the operator (`OD-27`).** The actuator times its
own flush and logs the syscall, but that log lives on a machine which halts immediately afterwards,
so whether a collector ships it first is a race — and one that loses precisely in the slow-sync case
the measurement exists to capture. The actuator cannot close that gap and must not try: it holds no
API token by design (`NA-2`) and that stays. Instead the operator reconstructs the halt from two
facts it can see on its own — it wrote the signal, and it watched the `Node` stop reporting — and
publishes them as `nutoperator_halt_*`. Coarser, and it survives the node. See
[metrics.md](../../reference/metrics.md).

**Monitoring configuration does not change during an episode (`F-92`).** DaemonSet spec writes are
deferred while any owning flow is mid-episode and requeued until it settles, so a configuration edit
cannot roll the fleet's monitoring during an outage. A missing DaemonSet is still created — deferral
protects a running fleet, it does not withhold one that does not exist.

## Related

- [node-agent-daemonset-audit.md](../audits/node-agent-daemonset-audit.md) — findings and evidence.
- [scope-boundaries.md](scope-boundaries.md) `SB-3` — the authorization boundary as a scope statement.
- [example-pod-placement.md](../../concepts/pod-placement.md) — where the DaemonSet lands and
  what the tolerations do.
- [scaling-and-sizing.md](scaling-and-sizing.md) — why the agent has no sizing decision.
