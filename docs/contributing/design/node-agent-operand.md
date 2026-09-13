# Node Agent Operand

Status: design. Covers the DaemonSet the `NodePowerAgent` CRD renders — what authorizes a node to
halt, how that authorization is delivered and withdrawn, and what the operand refuses to do.

Components: Node Agent / DaemonSet.
Audience: contributors.

`NA-n` identifiers are stable and are not reused or renumbered.

This is the only component that can stop a machine. Everything below is written from that fact: the
question is less "how does the agent shut a node down" than "what makes this particular halt
legitimate, and how does the operand know when it stops being so." Linux hosts use one syscall,
`reboot(2)` with `LINUX_REBOOT_CMD_POWER_OFF`; Talos hosts use one Talos machine API call,
`MachineService.Shutdown`.

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

**NA-2 · The actuator holds no Kubernetes API credentials.** It reads a mounted volume and performs
one fixed actuation mechanism. It does not watch the Kubernetes API, patch node objects, or hold RBAC
of its own. This is what makes the operand's blast radius bounded by what kubelet already projects
into it, and it is why signal state is carried in the Secret rather than in node annotations —
annotations would require giving a node-local container the ability to write to the API.

For `TalosShutdown`, kubelet also projects one talosconfig Secret into the actuator. That credential
is not a Kubernetes credential, and it is not mounted for the Linux `PowerOff` or `Simulate` paths.
The expected Talos role is `os:operator`: enough to call reboot/shutdown methods without handing the
actuator the all-method `os:admin` role.

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
`Simulate` (accept and record, touch nothing), `PowerOff` (`reboot(2)` with
`LINUX_REBOOT_CMD_POWER_OFF`), or `TalosShutdown` (`MachineService.Shutdown` through a mounted
talosconfig), and it is read from the container's own configuration.

A dry-run flag inside the signal would mean the authority to halt a node and the instruction not to
were travelling together in the same document, over the same path, decided by the same writer. Mode
belongs to the thing being asked, not to the asking.

Defense in depth remains on top of revocation, not instead of it: a scan stops at the first live
signal, so one episode delivered twice actuates once (`F-58`), and an agent declaring a
`shutdownFlowRef` accepts releases only from that flow.

Before each signal Secret create or update, the operator re-reads that node's `NodePowerAgent`
through its uncached API reader. Physical policies still require `Actuate` and the configured
approval annotation equal to `true`. The selected agent UID, generation, and policy must match;
deleted agents and nodes removed from coverage are refused. This check is required even if the
flow passed its separate approval check at the wave boundary. A read failure blocks publication,
and partial handoff receipts distinguish already-written signals from the refused node.

The agent read and Secret write are separate Kubernetes requests, not an atomic cross-resource
transaction. This gate checks current authorization; refreshing placement, pod readiness, and
telemetry is the separate live-release contract in the executor requirements.

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

**NA-11 · Talos actuation uses the machine API, not host poweroff privileges.** `TalosShutdown` is a
separate actuator policy, because the Linux and Talos mechanisms need different boundary crossings.
The Talos path keeps `hostPID: false`, adds no Linux capabilities, runs under the restricted actuator
security context, and still receives no Kubernetes service-account token. Its only additional inputs
are the read-only talosconfig Secret and `POWER_TALOS_*` environment values rendered from
`spec.shutdown.talos`.

The controller validates the talosconfig Secret/key before rendering and watches that Secret for
create/delete/data changes, because a user-supplied Secret carries no owner reference back to the
agent. Endpoint addresses must be IP literals, and the controller turns them into `NetworkPolicy`
`ipBlock` peers on TCP 50000. DNS names are deliberately refused for v1: Talos clients support them,
but Kubernetes `NetworkPolicy` cannot express a portable DNS egress boundary, so accepting names
would make a default-deny namespace either broken or wider than the CRD says.

The actuator targets the node explicitly. By default `POWER_TALOS_NODE` comes from the Kubernetes
node's `status.hostIP`, because Talos endpoints proxy to nodes by the address as seen by the endpoint
server. `nodeAddressSource: NodeName` is available for clusters whose Talos node names are resolvable
from the control-plane endpoints.

**NA-12 · First-class operating system policies need a distinct safety boundary.** The public support
model is not tied to any maintainer's homelab. A Kubernetes node operating system earns a named
actuator policy when it has a stable, documented shutdown interface that is materially safer or more
correct than the generic Linux `PowerOff` boundary. Talos qualifies because its machine API lets the
actuator shut down a node without host PID access or Linux shutdown capabilities.

Operating systems that still reduce to a local Linux halt stay on `PowerOff`; giving them separate
policy names would imply support differences the implementation cannot actually provide. Bottlerocket
is the next plausible candidate, but it is not a v1 policy: its host API is local to the node, reached
through a Unix socket with SELinux labeling and host mounts, and the available action is documented as
reboot rather than the same clean shutdown contract this operator promises for UPS events. That design
needs its own review before it becomes a public API enum.

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
`FlowBinding`, `ModeAuthorized`, `Sync`, `CapabilityEffective`, and `SyscallIssued`. The Talos path
uses the same signal and mode gates, then `TalosCredential`, `TalosTarget`, and `TalosAPICall`.

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

**NA-10 · The local `upsmon` signal path stands down after signalling (`F-105`).** The
`SHUTDOWNCMD` writer does not return in the rendered `upsmon` container after it writes or reuses a
live local signal. That local path still has no halt authority (`NA-1`), but returning from
`SHUTDOWNCMD` makes `upsmon` exit 0, and a DaemonSet restart then repeats the same forced-shutdown
path while the UPS remains low-battery. Holding the writer process leaves kubelet with one stable
container to terminate when the pod is replaced instead of a restart loop at the worst possible time.

**The record of whether the node stopped is kept by the operator (`OD-27`).** The actuator times its
own flush and logs the syscall, but that log lives on a machine which halts immediately afterwards,
so whether a collector ships it first is a race — and one that loses precisely in the slow-sync case
the measurement exists to capture. The actuator cannot close that gap and must not try: it holds no
Kubernetes API token by design (`NA-2`) and that stays. Instead the operator reconstructs the halt
from two facts it can see on its own — it wrote the signal, and it watched the `Node` stop reporting
— and publishes them as `nutoperator_halt_*`. Coarser, and it survives the node. See
[metrics.md](../../reference/metrics.md).

After a manager restart or leader handoff, the operator re-seeds this in-memory watch from
still-authorized signal Secret keys only for nodes that still report Ready. A live signal beside a
node that is already `NotReady` is not classified here: it may be the requested halt, a partition, or
pre-existing node health, and recording it as any one of those would overstate the evidence.

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
