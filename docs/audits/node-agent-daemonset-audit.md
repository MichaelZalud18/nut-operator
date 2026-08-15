# Node Agent DaemonSet Audit

Status: audit record, 2026-08-03, against commit `00eb3c0`. Static reading only.

Components: Node Agent / DaemonSet.

Supplements `operator-maturity-benchmarks.md`. Findings use the same `F-n` namespace, continuing
from F-7.

Remediation note: F-8 through F-13 are implemented in the node-agent hard-infra pass. F-14 remains
open as the next node-agent-specific audit item.

**Update (2026-08-05): F-8 through F-14 are all closed** — each fix lives at its own site
(`internal/controller/nodepoweragent_render.go`, the webhook defaulter in
`internal/webhook/v1alpha1`, and `internal/kubeactions/runner.go` for self-exclusion). A second, fresh pass against the current code (not just
this file) surfaced four more findings, `F-33`–`F-36`, appended below rather than folded into the
original findings list, which is left as the historical record it was written as.

## Benchmark basis

No formal DaemonSet standard exists. The conventions below are drawn from the established
privileged node-agent implementations — Cilium, node-exporter, CSI node plugins, NVIDIA GPU
Operator, and Kured — and from the constraints this project's own design imposes.

The GPU Operator is the closest structural analogue for the *operator-manages-DaemonSets* pattern:
it renders and reconciles several node-level DaemonSets from CRs, gates them on node labels, and
sequences them against each other. Patterns worth borrowing are listed under "GPU Operator
patterns" below.

## Findings

**F-8 · No `updateStrategy` set on the rendered DaemonSet.** Kubernetes defaults to
`RollingUpdate` with `maxUnavailable: 1`, which is the desired behavior, but it is inherited rather
than declared. Given this DaemonSet is the mechanism that shuts hosts down safely, its rollout
behavior should be explicit and pinned. A future default change or a user-supplied patch would
otherwise go unnoticed.

**F-9 · `priorityClassName` is user-supplied with no default.** The field exists at
`Placement.PriorityClassName` and is plumbed through, but nothing defaults it. Convention for node
agents is `system-node-critical`. This matters more here than for a typical agent: the node agent
is a tier 0 workload under OD-4, and a preempted or evicted power agent means the node it covers
cannot be released cleanly. Recommend defaulting to `system-node-critical` via the webhook, in the
same place the hardening defaults are already applied.

**F-10 · No tolerations defaulted.** `Placement.Tolerations` is passed through verbatim with no
baseline. A node agent that cannot tolerate `NoSchedule` and `NoExecute` taints will be missing
from exactly the nodes most likely to be tainted — including nodes already cordoned by an earlier
wave, or by a Kured reboot per the SB-5 coexistence caveat. Recommend a defaulted baseline
toleration set with user additions merged on top.

**F-11 · No `terminationGracePeriodSeconds` set.** Defaults to 30s. The agent participates in host
shutdown; its own termination semantics during a flow should be a deliberate value, not inherited.
Interacts with EX-14 resume behavior — an agent killed at 30s mid-handoff is a different failure
than one given a defined window.

**F-12 · No probes on either container.** No liveness, readiness, or startup probes found in either
render path. For the `upsmon` container, a readiness probe reflecting NUT server connectivity would
surface RS-17 telemetry loss at the pod level. Caution: a liveness probe on the actuator is
actively dangerous — a restart loop during a shutdown flow could re-trigger or lose signal state.
Recommend readiness on `upsmon`, no liveness on the actuator, and an explicit comment saying why.

**F-13 · The privileged actuator is not implemented, so its privilege scope is unverified.** Both
containers currently render with `restrictedContainerSecurityContext()` — `AllowPrivilegeEscalation:
false`, `ReadOnlyRootFilesystem: true`, all capabilities dropped. No `Privileged`, `HostPID`,
`HostPath`, or `nsenter` anywhere in the render path. This is consistent with the stub-only
actuator policy and is correct for today.

It is also the audit's main forward risk. When real actuation lands, the temptation is
`privileged: true`. The design should pre-commit to the narrowest viable form: `CAP_SYS_BOOT` alone
if the actuator calls `reboot(2)` directly, or `hostPID` plus a targeted `nsenter` if it must
invoke `systemctl`. Full privilege should require a written justification, since it would undermine
the SB-3/SB-4 argument that the privileged half is small and dumb.

**F-14 · No PodDisruptionBudget and no drain-exclusion handling for the agent itself.** DaemonSet
pods are not evicted by standard drain, so this is not an active bug. But EX-13 has the executor
performing evictions during flows, and nothing observed prevents a flow from targeting the power
agent's own namespace. The tier 0 self-exclusion rule from OD-4 exists in the design and does not
appear to exist in code.

## Not findings

- Container security contexts are strong and applied consistently across both render paths:
  non-root at UID 65532, read-only root filesystem, all capabilities dropped, no privilege
  escalation.
- `NodeSelector` and `Affinity` handling is careful — it detects and rejects conflicting label
  values and refuses to silently merge `matchExpressions` with a user-supplied required node
  affinity. Better than typical.
- Webhook-applied hardening defaults (`ReadOnlyRootFilesystem`, `RunAsNonRoot`) mean the safe
  posture holds even when a user omits the fields.
- Owner references are set on the rendered DaemonSet.

## GPU Operator patterns worth borrowing

- **Node-label gating.** The GPU Operator labels nodes by detected hardware and gates DaemonSet
  scheduling on those labels. The analogue here is labeling nodes by resolved power domain
  membership (IN-7) so agent placement follows derived topology rather than hand-maintained
  selectors.
- **Explicit sequencing between node-level components.** GPU Operator orders driver → toolkit →
  device plugin → monitoring, rather than letting them race. If a USB actuation DaemonSet arrives
  under OD-10, it will need the same treatment relative to the existing agent.
- **A per-node status object rather than status aggregated only on the parent CR.** Useful for
  answering "is node X's agent healthy and current" without parsing a cluster-wide status blob.
  Bounded by GP-3 — per-node observed state is legitimate CR status; history is not.
- **Validator/init containers that gate readiness on a real precondition.** The equivalent here is
  refusing readiness until the agent has established a NUT session, so an agent that cannot see its
  UPS is visibly not ready rather than silently idle.

## Kured as a reference model

Still a good reference for node-agent mechanics, not for architecture.

Worth borrowing: lock/lease coordination limiting how many nodes act concurrently; sentinel-file
detection (structurally identical to the signal file, opposite direction); cordon-and-drain before
terminal action; and the general watch → coordinate → hand off to host shape.

Where this project should stay divergent: Kured is a single container holding both cluster
credentials and host actuation authority. The two-container split gives the privileged half no
Kubernetes token and no policy authority, which is a stronger posture (SB-3, SB-4). Kured also has
no ordering, no dependency graph, and no external event source.

SB-5 is unaffected — different trigger, different product.

## Recommended order

1. F-9 priority class default — one webhook default, prevents preemption of a tier 0 workload.
2. F-10 toleration baseline — prevents the agent being absent from tainted nodes.
3. F-13 pre-commit the actuator privilege model — cheap now as a written decision, expensive to
   retrofit after an image ships.
4. F-8 explicit update strategy — one field.
5. F-14 self-exclusion enforcement — implements an existing design rule.
6. F-11, F-12 grace period and probes — deliberate values, with the actuator liveness hazard
   documented.

All items above are closed as of 2026-08-05.

## Findings — second pass, 2026-08-05

Static reading plus live verification against a real kind cluster (previously blocked on tooling —
`kind` is now installed and confirmed working in this environment). Continues the `F-n` namespace
from `F-32`.

**F-33 · `spec.shutdown.requireFreshTelemetry` was defaulted but never enforced.** The field exists,
defaults to `true` in the webhook, and its doc comment says it "blocks actuation when UPS telemetry is
stale" — but grep across the whole codebase found nothing reading it anywhere. `internal/trigger`'s
own staleness gating is a different, ShutdownFlow-trigger-level mechanism that doesn't cover this: a
flow can be triggered by one device's condition while its groups release nodes covered by a different
agent whose own devices are independently stale. A field that looks like a safety gate and silently
does nothing is worse than not having the field: a user reading the CRD schema has every reason to
believe it's load-bearing. Fixed: per-agent freshness is computed once in `nodeReleasesForTarget`
(walking `NUTServerRefs` → `SelectedDevices` → each `UPSDevice.Status.Phase`) and threaded into the
same group-level `AgentShutdown` gate `F-14`'s readiness check already uses. Fails closed — an
unresolvable device set counts as not fresh.

**F-34 · Tier-0 DaemonSet containers had no default resource requests/limits.**
`NodePowerAgentResources.Upsmon`/`.Actuator` are plain `corev1.ResourceRequirements{}` with nothing
defaulting them anywhere in the render or webhook path. A user who doesn't set `spec.resources` runs
BestEffort QoS on the one pod this entire project depends on surviving node pressure — no OOM-score
protection, no scheduler capacity reservation. `priorityClassName: system-node-critical` (`F-9`)
protects against *preemption*; it does nothing for OOM-kill ordering, which is scored primarily off
QoS class and requests. Fixed in the webhook defaulter, where `F-9`'s `priorityClassName` default
already lives, applied per resource key so a user-supplied value is never overwritten.

**F-35 · `upsmon` had no liveness probe at all.** The original audit (`F-12`) correctly flagged that
the actuator must never carry a liveness probe (restart mid-flow risks re-triggering or losing signal
state) but didn't separately weigh in on `upsmon`, which carries no such hazard — it's the read-only
monitoring container. The result: a `upsmon` process that hangs without crashing outright sits
`NotReady` forever with no path back to healthy. Fixed with a process-existence check
(deliberately not tied to NUT reachability, which is what the readiness probe already covers), via
`pgrep -x upsmon`. The actuator's deliberate lack of a liveness probe is asserted as a test
invariant rather than left as a comment.

**F-36 · `node-actuator`'s `command` poweroff method is fully implemented but structurally
unreachable.** `runPoweroffCommand` in `cmd/node-actuator/main.go` works and is tested, but
`nodepoweragent_render.go`'s `nodePowerAgentPoweroffMethod` hardcodes `"reboot-syscall"` regardless of
spec content — there's no CRD field that can ever select the other method. Possibly intentional (the
narrower `CAP_SYS_BOOT`-only privilege model is what `F-13` actually recommended over a
`systemctl`-invoking path), but that's a call the design docs should make explicitly rather than
leaving as an accidentally-dead code path.

*Resolved 2026-08-08 by deletion, not exposure.* `POWER_POWEROFF_METHOD`, `POWER_POWEROFF_COMMAND`,
`POWER_POWEROFF_ARGS`, `runPoweroffCommand`, and the three `actuatorConfig` fields behind them are
gone; `runPoweroff` now takes no argument and calls the syscall unconditionally. Exposing the command
path would have meant a CRD field naming an executable run as root on every node, and it would have
widened the container beyond the `CAP_SYS_BOOT`-only model `F-13` argued for, since shelling out to
`systemctl` needs host PID and dbus access. For the one operation whose blast radius is the entire
machine, no configuration surface is the correct amount.

The render emits no `POWER_POWEROFF_*` variable at all, and both the rendered DaemonSet and
`actuatorConfig` are asserted to carry none, so reintroducing a configurable mechanism fails a test
rather than passing silently. Dry-run is now asserted against the syscall in both directions — a
dry-run signal must not reach it, an otherwise identical live signal must — which proves the `DryRun`
flag is what makes the difference rather than some unrelated guard happening to stop execution.

Naming note for anyone reading the code: `reboot(2)` is the syscall used for every machine state
change on Linux, including powering off. The actuator passes `LINUX_REBOOT_CMD_POWER_OFF`, so it
halts and cuts power — it does not restart.

**Not a finding, but worth recording: the in-cluster signal-handoff smoke test is unblocked.** `kind`
is now installed and works cleanly against the Docker daemon in this environment (WSL2, direct docker
access — no nested-sandbox restrictions encountered). Manually verified the exact claim the original
open-work item asked about — a real dummy-ups-backed `NodePowerAgent` DaemonSet, running on a real
kind node, observed a directly-written projected `Secret` signal update in its actuator container logs
~44 seconds after the write, well inside the 2-minute TTL and consistent with kubelet's projected-volume
sync period — before committing the equivalent as a permanent `test/e2e` spec. Full `make test-e2e`
(6 of 6 specs) passes.

## Findings — third pass, 2026-08-12 (signal authority and privilege model)

Continues the `F-n` namespace from `F-53`. Every entry cites the code it was read from at commit
`8d968da`.

Two evidence classes are kept apart deliberately. Claims about **this repository** were read from
the files cited and are stated as fact. Claims about **upstream behavior** — Linux capability
semantics, Kubernetes volume and Pod Security semantics, NUT's own state machine — are marked
*unverified here* and carry their source, except where the note is followed by an observation from
actually running the operand image in this environment, which is called out as such.

### Signal authority and the two-component boundary

**Exposure note for `F-54`, `F-56`, and `F-57`.** All three describe what happens under
`mode=Actuate` with `policy=SystemdPoweroff`. That combination is what `nodePowerAgentRequiresHostPoweroff`
gates on (`internal/controller/nodepoweragent_render.go:338-341`), and it has never shipped: the
actuator has only ever run under `policyStub` (`cmd/node-actuator/main.go:67-68`), and `F-61` records
that the privileged path may not even reach its syscall. They are therefore **pre-commitment
decisions to lock before actuation ships**, not live exposure to remediate — which is why the
recommended order does not put them first despite `F-54` being the most severe finding in this pass.

**F-54 · The local `upsmon` path self-authorizes an unordered fleet-wide halt.** The two rendered
env blocks — `nodePowerAgentSignalEnv` for the `upsmon` container
(`internal/controller/nodepoweragent_render.go:997-1013`) and the actuator's inline block (`:875-887`)
— carry eight and six variables respectively. `POWER_EXECUTION_ID` is in neither.
`cmd/power-signal-writer/main.go:52` reads it with an empty fallback, and `:109-112` synthesizes
`upsmon-<node>-<unix-nanos>` when it is empty. So every signal the local path writes carries a
manufactured execution identity.

`SHUTDOWNCMD` points at that writer (`nodepoweragent_render.go:526`), and the same rendered
`upsmon.conf` emits `MINSUPPLIES 1` (`:525`) with every `MONITOR` line at power value 1 and role
`secondary` (`:552`). One UPS reaching OB+LB therefore satisfies the shutdown condition on every
node monitoring it, simultaneously.

The actuator applies no distinction. `InspectSignal` (`internal/nodeagent/signal.go:51-89`) returns
`Active` for any well-formed, in-TTL, node-matching payload regardless of origin, and `watchSignals`
(`cmd/node-actuator/main.go:145-166`) actuates on any `Active` status. Under `mode=Actuate` with
`policy=SystemdPoweroff` that is the whole fleet halting at once, unordered, at the moment the
sequencer exists to prevent it. `SB-2b` says NUT's threshold model is an input and never the
sequencer; this path makes it the sequencer, and no spec field disables it.

`requireFreshTelemetry` does not cover it: `F-33`'s enforcement lives at
`internal/controller/shutdownflow_execution.go:510`, on the executor's group-release path, which the
local writer never traverses.

*Correction to the framing this finding arrived with.* The note said the local path always carries
`shutdownFlow: upsmon-local`. It does not. `nodePowerAgentShutdownFlowName`
(`nodepoweragent_render.go:1048-1053`) returns `spec.shutdownFlowRef.Name` when that is set and
`upsmon-local` only when it is unset, and `POWER_SHUTDOWN_FLOW` is what the writer reads. An agent
bound to a real flow therefore writes a local signal stamped with the **real** flow name, which
makes the finding worse rather than better: for those agents the only thing distinguishing a
self-authorized halt from an executor-issued one is the `upsmon-` prefix on a synthesized
`executionID`, and nothing inspects it.

*Unverified here (upstream NUT, per `upsmon.conf(5)` and the transfer note's reading of v2.8.5
`clients/upsmon.c`):* that `upsmon` invokes `SHUTDOWNCMD` within one `POLLFREQALERT` of the
monitored set reaching OB+LB below `MINSUPPLIES`. The rendered directives are as cited; the timing
claim is upstream's.

**`OD-37` · Decide and record what the local path is for.** Either an intentional last-resort
backstop — in which case it needs a name, a spec field, a distinct `reason` (it currently shares
`nodePowerAgentSignalReason = "upsmon-fsd"`, `nodepoweragent_render.go:43`, with nothing to
contrast against), and a written statement of what ordering guarantee is surrendered when it fires
— or it is a bypass and should be bound to prior authorization per `F-57`. Leaving it undeclared is
what makes `F-54` a surprise rather than a policy.

*Closed 2026-08-14, as a bypass — not as a backstop.* This audit was written expecting the backstop
arm, and it went the other way. Recorded here so the reasoning above is read as the case that was
argued and lost, not as the standing position.

The backstop reading fails on its own terms. It is only reached when the operator is unreachable,
and that is precisely when ordering matters most — so the moment it engages is the moment its
inability to order becomes the entire problem. `MINSUPPLIES 1` on every agent is not a detail to be
tuned around it: one UPS at OB+LB releases everything it covers, at once. That is not a safety net
under the sequencer, it is a second and worse sequencer that runs when nobody is watching, which is
the objection `OD-36` already used to decline `clone` and `upssched`.

So the local path is a bypass and is bound as one. The mechanism stays — writer binary, signal
format, local file — because the shape is worth keeping for a later authorization model. Its
authority does not: the actuator watches only the projected Secret, and `power-agent-run` is no
longer mounted into it at all, so `F-57` is the enforcement rather than a mitigation. Read-only was
the obvious shape and is the wrong one — the halt came from the actuator *reading* that path, so
closing the write direction would have addressed a threat nobody posed. The accepted cost is
recorded in `SB-3`: an undeliverable signal leaves nodes running until the UPS dies, which is
observable and bounded where an unordered fleet halt is neither.

`F-55`–`F-57` are the implementation, and they resolve in the opposite direction from what this
section anticipated: nothing needs a name, a spec field, or a distinct `reason`, because nothing
authorizes anything.

**F-55 · `PlanConfigHash`, `ExecutionID`, and `ShutdownFlow` are validated for presence, never for
value.** `internal/nodeagent/signal.go:64` rejects a payload only when one of those fields is
empty. No comparison against an expected value exists anywhere in the actuator.

It could not be written today without a render change: the actuator's env block
(`nodepoweragent_render.go:875-887`) carries `POWER_AGENT_MODE`, `POWER_ACTUATOR_POLICY`,
`POWER_NODE_NAME`, `POWER_SIGNAL_PATH`, `POWER_SIGNAL_PATHS`, and `POWER_SIGNAL_TTL` — and not
`POWER_PLAN_CONFIG_HASH` or `POWER_SHUTDOWN_FLOW`, which go to the `upsmon` container only
(`:1008`, `:1010`). The actuator has no expected value to check against.

`resiliency-and-partitions.md` describes signals as plan-hash-bound and execution-bound. At the
enforcement point they are neither. Same class as `F-25`/`F-33`/`F-37`: a property the documents
assert and the code does not implement.

**F-56 · `DryRun` looks like authorization and carries no independent information.** The writer sets
it from its own mode (`cmd/power-signal-writer/main.go:114`, `DryRun: config.Mode != "Actuate"`,
where `Mode` is `POWER_AGENT_MODE` at `:53`). The actuator gates on the field it receives
(`cmd/node-actuator/main.go:174`). The same render injects `POWER_AGENT_MODE` into both containers
(`nodepoweragent_render.go:876` and `:999`) from one call to `nodePowerAgentMode(agent)`.

The actuator is reading its own configuration back out of a file written by its neighbor. For
operator-issued signals the flag does originate outside the pod, but the actuator cannot tell the
two cases apart — see `F-55`. Whatever replaces this has to be unforgeable by the `upsmon`
container.

**F-57 · The trust boundary between the two containers is a shared writable tmpfs.**
`power-agent-run` is an `emptyDir` (`nodepoweragent_render.go:782-787`) mounted at
`/run/power-agent` into the `upsmon` container (`:821`) and the actuator (`:891`), **neither with
`ReadOnly`**. The projected signal Secret is mounted `ReadOnly: true` (`:892`) — so the API-gated
path is the hardened one and the local path is not.

Ordering compounds it. `POWER_SIGNAL_PATHS` is rendered local-first (`:885`, the local path
concatenated with `,` and then the projected path); `signalPaths` preserves input order
(`cmd/node-actuator/main.go:111-138`); `watchSignals` iterates `config.SignalPaths` in order
(`:151`) and actuates inside the loop (`:156`). The less-trusted source is evaluated first, and
since actuation halts the machine, evaluating it first is deciding with it.

What that means concretely: code execution in the `upsmon` container — the one that speaks a network
protocol to a server and parses its responses — writes one JSON file and halts the host.

Three structurally different fixes, to be picked deliberately rather than blended: sign the payload
with an operator-held key and ship the agent only the public half; accept a local-path signal only
when it names an `executionID` already observed on the projected path; or drop the local path and
route the `upsmon` decision through the operator. Mount the actuator's copy `ReadOnly` regardless —
that one is a two-word change and is not a fix by itself.

**F-58 · A signal can actuate twice.** `seen` is declared inside `watchSignals`
(`cmd/node-actuator/main.go:148`) and lives only as long as that call. The signal file lives in
`power-agent-run`, an `emptyDir`, which is pod-scoped rather than container-scoped, so it survives a
container restart that the map does not. An actuator that restarts while a signal is still inside
its TTL re-reads it, finds `seen` empty, and actuates again.

*Resolved 2026-08-14.* The actuated keys are written to the actuator's own state volume — the one
`F-64` added — and read back when the watch loop starts, so a container restart no longer re-actuates
a signal that is still inside its TTL. Bounded at 64 keys, which also closes `F-75`'s unbounded-`seen`
item: the key carries the signal timestamp, so lexical order is chronological and the oldest are
dropped first.

Nothing persists an actuated-signal record, and nothing in the executor retracts a Secret key after
a node goes down. This is a shutdown-side obligation, not a recovery concern — `OD-1` does not cover
it.

**F-59 · TTL spans two clocks with no stated assumption.** The executor stamps `Timestamp`; the
actuator compares it against `time.Now().UTC()` on the node (`cmd/node-actuator/main.go:152`), and
`internal/nodeagent/signal.go:74-81` applies the bound symmetrically — `SignalStale` past `+ttl`,
`SignalFromFuture` past `-ttl`. Inside that window real skew is invisible; past it every
operator-issued signal is rejected on that node.

The only evidence of rejection is a container log line (`cmd/node-actuator/main.go:160-162`), and
`SignalMissing` is deliberately excluded from even that. There is no condition, event, or metric for
"this node rejects what I send it". Needs a stated NTP assumption plus a reporting channel, which is
`F-60`.

**F-60 · The boundary is one-way — the actuator cannot report anything.** No receipt, metric, or
event is emitted from `cmd/node-actuator`, and no channel exists to emit one on:
`AutomountServiceAccountToken` is `false` at both the ServiceAccount
(`nodepoweragent_render.go:674`) and the pod (`:747`).

The consequence is that a signal delivered to a `Disabled` actuator parked in `block()`'s bare
`select {}` (`cmd/node-actuator/main.go:140-143`) is indistinguishable from one that halted a
machine, because the readiness probe is `--version` (see `F-64`). The executor infers success from
the node disappearing.

`resiliency-and-partitions.md` lists per-node heartbeat records as an implementation hook; choosing
its channel is a design decision, not a TODO. Note while deciding: the agent ServiceAccount is
created with no Role or RoleBinding, so flipping automount on for a heartbeat grants whatever is
bound at that time to **both** containers, including the one holding `CAP_SYS_BOOT`.

*Accepted as designed, 2026-08-14, on the owner's call.* The pod already contains a container that
can report: `upsmon` speaks to the NUT server, is not fail-closed, and — since `F-68` — records its
own events to a file the readiness probe reads. The actuator's silence is the point of the
two-component split (`SB-3`, `SB-4`): it holds `CAP_SYS_BOOT`, no ServiceAccount token, and no
policy authority, and giving it a reporting channel means giving it credentials.

So the boundary stays one-way. What the original finding got right is that success is still inferred
from the node disappearing, and that remains true — but the inference is sound for the only action
the actuator takes, and `F-63` now records the one case where it is not (a `reboot(2)` that returns
success without halting the host, which `hostPID` prevents).

### Privilege model

**F-61 · Verify `CAP_SYS_BOOT` survives the switch to UID 65532 — before anything else on this
list.** The pod security context sets `RunAsNonRoot: true`, `RunAsUser: 65532`, `RunAsGroup: 65532`
(`nodepoweragent_render.go:754-762`); the actuator's container context adds `SYS_BOOT` while
dropping `ALL` and keeping `AllowPrivilegeEscalation: false` (`:931-941`).

*Unverified here (upstream Linux and Kubernetes semantics):* a `setuid`-style UID transition clears
the permitted capability set unless the capability is ambient, and Kubernetes exposes no field to
request ambient capabilities. If that holds, the actuator's `reboot(2)` fails with `EPERM` and the
outcome depends on runtime OCI spec generation rather than on anything in this repository.

What makes this urgent rather than theoretical is that nothing here has ever executed. Actuation has
only ever run under `policyStub` (`cmd/node-actuator/main.go:67-68`), and `runPoweroff` is reached
only from `systemdPoweroffActuator` (`:173-180`), which requires both `mode=Actuate` and
`policy=SystemdPoweroff`. The syscall may never have been issued on any cluster.

If the capability does not survive, the options are a root actuator holding `CAP_SYS_BOOT` only, or
a file capability on the binary. Both are larger changes than `F-62` or `F-63`, which is why this
one is sequenced first.

*Confirmed as a defect and resolved 2026-08-13, measured on `kind`.* The capability did not survive.
A pod rendered with the actuator's exact security context reported:

```console
CapInh: 0000000000000000
CapPrm: 0000000000000000
CapEff: 0000000000000000
CapBnd: 0000000000400000     <- SYS_BOOT allowed, and not held
CapAmb: 0000000000000000
```

Kubernetes honored the request into the **bounding** set and the permitted set was empty, so
`reboot(2)` would have returned `EPERM`. Every rendered `mode=Actuate` deployment has been unable to
power off a node, and nothing would have reported it: actuation has only ever run under
`policyStub`, so the syscall was never reached.

Both proposed remedies were measured rather than argued.

- **Root with `CAP_SYS_BOOT` only** works — `CapPrm`/`CapEff` both `0x400000` — and gives up
  `RunAsNonRoot` on the one container that can halt the machine.
- **A file capability on the binary** also works, and keeps everything: UID 65532, `RunAsNonRoot`,
  `AllowPrivilegeEscalation: false`, `drop: ALL`. Two things that looked likely to break it do not:
  `no_new_privs` (which `AllowPrivilegeEscalation: false` sets) does **not** suppress the file
  capability, and a multi-stage `COPY` into `gcr.io/distroless/static` preserves the extended
  attribute that carries it. Both were checked directly rather than assumed.

The file capability was taken, with one correction that only appeared by running it. Applied as
`cap_sys_boot=ep` the image becomes unrunnable wherever `CAP_SYS_BOOT` is outside the bounding set:

```console
$ docker run --rm node-actuator --version
exec /node-actuator: operation not permitted
```

That is every configuration except actuation — including the `DryRun`/`Stub` default every
deployment starts in, where `restrictedContainerSecurityContext` adds no capability at all. Shipping
`=ep` would have crash-looped the actuator container on every node in the fleet in order to make one
rarely-used path work. It is a sharper version of the same lesson `F-46` and `NS-2` record: the
failure was invisible to every test and appeared only when the thing was run.

`cap_sys_boot=p` masks instead of failing. The process starts with empty sets where the capability
is not granted, and `cmd/node-actuator` raises it from permitted into effective immediately before
`reboot(2)`. Measured in both configurations:

| Rendered config | `CapPrm` at start | `CapEff` at start | After raise |
| --- | --- | --- | --- |
| `mode=Actuate` (`SYS_BOOT` requested) | `0x400000` | `0` | `0x400000`, reboot possible |
| `Stub`/`DryRun` default (no request) | `0` | `0` | refused, with the reason named |

Permitted-but-not-effective is worth more than a workaround here. For the whole life of the process
— watching a directory, parsing JSON written by its neighbor across the `F-57` boundary —
`CAP_SYS_BOOT` is held and inert, so a bug reached through that surface cannot halt the host.

Guarded on both sides: `hack/smoke-image.sh` reads the extended attribute off the shipped binary
(from outside, since the image is distroless), and `TestDockerfileAppliesPermittedOnlyFileCapability`
fails if it is ever changed to `=ep`.

*Reopened and re-verified 2026-08-13, against the objection that the fix was inert in the deployed
pod.* It is not inert, but the objection was well-aimed: the verification that first closed this
finding did not use an operator-rendered pod, and the explanation it recorded was wrong.

**Which harness produced which measurement.** Both original runs used hand-written pod specs on
`kind`, copying the security context out of `nodepoweragent_render.go` by hand.

| Measurement | Harness | Image |
| --- | --- | --- |
| The defect (`CapPrm: 0`) | hand-written pod | NUT server image with a shell, not the actuator |
| The `=p` fix working | `kubectl run --overrides` | a stand-in Go probe, not the actuator |

The actuator image itself was only ever run as `--version` to prove it execs. Its capability sets
were never read inside a pod. `AllowPrivilegeEscalation` was **not** changed as part of the fix; it
is `false` in `actuatorContainerSecurityContext` and was `false` in the probe pods, which reported
`NoNewPrivs: 1` alongside a populated `CapPrm`.

**Re-measured properly.** The operator was deployed to `kind` and a real `NodePowerAgent` created
with `mode: Actuate`, `actuatorPolicy: SystemdPoweroff`, and the approval annotation. Read from the
node's PID namespace, since the image is distroless and has no shell to exec into:

```console
$ grep -E 'Uid|Cap|NoNewPrivs|Seccomp' /proc/<actuator>/status
Uid:        65532  65532  65532  65532
CapPrm:     0000000000400000
CapEff:     0000000000000000
CapBnd:     0000000000400000
NoNewPrivs: 1
Seccomp:    2
```

The capability is held, `no_new_privs` is on, and `CapEff` is empty exactly as intended. (`Seccomp:
2` is `RuntimeDefault` from the `F-62` change, applied and filtering.)

**Why `no_new_privs` does not suppress it — the recorded explanation was wrong.** The objection's
reading of `cap_bprm_creds_from_file` is correct: with `no_new_privs` set, any execve that *gains* a
permitted capability has the new set intersected with the caller's. What the earlier note got wrong
was concluding this never applies. It does. One image, one pod, one security context, three exec
paths:

| Exec path | `CapPrm` |
| --- | --- |
| `sh` — a capability-dumb binary | `0` |
| the runtime execs the setcap binary | `0x400000` |
| `sh -c '<the same setcap binary>'` | `0` |

The third row is the downgrade branch firing. The second is the actuator. The difference is which
process called `execve`: containerd writes `permitted: ['CAP_SYS_BOOT']` into the OCI spec (read
back off the running container with `ctr c info`, alongside `no_new_privileges: True`), so the
runtime's init already holds the capability and the entrypoint exec gains nothing. A process spawned
inside the container has an empty permitted set, so its exec is a real gain and is downgraded.

This is a genuine constraint on the design, and it was not previously recorded: **the actuator only
holds the capability while it is the container's entrypoint.** Wrapping it in a shell, an init shim,
or a debug wrapper would disarm actuation silently.

**The distribution path preserves the extended attribute.** `kind load` bypasses registries
entirely, so it proved nothing about a real cluster. Re-checked by pushing the image to a registry
and pulling it back through containerd on the node, with a rebuild under a different `VERSION` to
change the layer digest and force a genuine unpack rather than a content-addressed cache hit. The
pulled image ran with `CapPrm: 0x400000`, and `getcap` on the binary as unpacked on the node
reported `cap_sys_boot=p`.

**The approach stays as a file capability, and the argument for root does not survive the
measurement.** The case for a root actuator rested on the file capability requiring
`no_new_privs` off. It does not. The comparison is:

| | file capability | root actuator |
| --- | --- | --- |
| UID | 65532 | 0 |
| `no_new_privs` | on | on |
| `CAP_SYS_BOOT` in `CapEff` | only for the instant before `reboot(2)` | the whole process lifetime |
| `runAsNonRoot` | pod-level, uniform | needs a container-level override |
| Pod Security violations when actuating | `hostPID`, `SYS_BOOT` | those plus `runAsNonRoot` |

The file capability is narrower on every row. It matters most on the row about `CapEff`: the
actuator shares a writable `emptyDir` with the network-facing container (`F-57`), and for the whole
life of the process — watching that directory, parsing JSON written across that boundary — the
capability is held and inert, so a bug reached through it cannot halt the host.

**What the objection was actually right about is silence, and that is now fixed.** Both remaining
risks — a registry that strips the attribute, an entrypoint that gets wrapped — fail the same way
the original defect did: invisibly, until a node fails to power off. So the actuator now proves the
capability at startup instead of assuming it. When `policy=SystemdPoweroff` and `mode=Actuate`, it
reads its own permitted set and refuses to start without it, naming all three causes:

```console
node-actuator refusing to arm SystemdPoweroff actuation: CAP_SYS_BOOT is not in this process's
permitted set, so reboot(2) would fail with EPERM. One of three things is true: the container
securityContext does not request the capability, the shipped binary lost its cap_sys_boot file
capability (an image registry or builder that drops the security.capability extended attribute), or
this process was not exec'd as the container entrypoint (a capability-dumb parent inside the
container loses it to no_new_privs)
```

Verified by sabotage: an image identical to the shipped one but with `setcap -r` applied exits `78`
with that message, while the shipped image logs `CAP_SYS_BOOT held in the permitted set; actuation
armed`. Only the actuator container fails, so `upsmon` keeps monitoring beside it — the cluster
loses the ability to actuate loudly rather than keeping it in name only. Exiting is the only channel
available, because the pod mounts no ServiceAccount token (`F-60`).

**This unblocks `F-62` and `F-63`, and changes what they are asking.** Both were written against a
configuration that could not actuate. `F-62`'s seccomp question can now be answered against a
process that actually holds the capability, and `F-63`'s `hostPID` justification can be tested by
observing whether `reboot(2)` reaches the host — neither of which was previously possible.

**F-62 · `SeccompProfile: Unconfined` on the actuator is probably wider than needed.** The pod sets
`RuntimeDefault` (`nodepoweragent_render.go:759-761`); the actuator's container context overrides it
to `Unconfined` (`:938-940`), but only on the `hostPoweroff` branch — `restrictedContainerSecurityContext`
sets no profile at all (`:917-925`), so the pod default applies to `upsmon`.

*Unverified here (upstream containerd/Docker seccomp policy):* the runtime default profile permits
`reboot` conditionally on `CAP_SYS_BOOT` being present, which would make `RuntimeDefault` sufficient.
Test that before narrowing, and prefer a narrow `localhostProfile` over `Unconfined` if it is not.

Verified here, separately: `ensureOperandNamespace` (`:633-647`) labels the namespace with
`app.kubernetes.io/managed-by` and `power.zalud.io/operand-namespace` and nothing else. No
`pod-security.kubernetes.io/*` label is applied anywhere in the render path. *Unverified here (Pod
Security Standards):* `hostPID` plus `Unconfined` puts the pod outside `baseline`, so a cluster
enforcing it would reject the actuating configuration and admit the stub one.

*Seccomp half resolved 2026-08-13; the Pod Security half is open and narrowed.*

The seccomp assumption was wrong, and it could only be tested after `F-61` — until then no process
held the capability, so any result would have been about the wrong failure. Measured on `kind`:

| Configuration | `reboot(2)` result |
| --- | --- |
| `RuntimeDefault` + `CAP_SYS_BOOT` | `EINVAL` (errno 22) |
| `RuntimeDefault`, no capability | `EPERM` (errno 1) |

The two errnos are what separate the explanations. A seccomp denial surfaces as `EPERM` before the
syscall body runs at all; `EINVAL` can only come from inside it, past the capability check. So
`RuntimeDefault` reaches the syscall and the capability is the only gate. `Unconfined` is removed —
it was stripping every other syscall filter from the one container that can halt a machine, in
exchange for nothing.

*Correction, 2026-08-13:* this first recorded the `EINVAL` as the reboot handler rejecting the
argument. That is the wrong place. `reboot(2)` checks `CAP_SYS_BOOT`, then the magic arguments, then
calls `reboot_pid_ns`, and it is that function's `default:` branch returning `-EINVAL` for
`LINUX_REBOOT_CMD_CAD_ON` from a non-initial PID namespace. The conclusion above is unaffected —
reaching `reboot_pid_ns` means the capability check already passed and seccomp did not intervene —
but the distinction matters for `F-63`, which turns on what that same function does with the
argument the actuator actually uses.

**The Pod Security half is confirmed and is now the whole of `F-62`.** Both shapes were submitted to
labelled namespaces:

- The **actuating** shape is rejected by `baseline`, on two counts:
  `non-default capabilities (container "p" must not include "SYS_BOOT")` and
  `host namespaces (hostPID=true)`. Removing `Unconfined` does not change this; `hostPID` alone
  would.
- The **stub/dry-run default** shape is admitted by `restricted`, the strictest level.

That asymmetry is the useful part, and it is a property of the design worth keeping: the agent's
default configuration is compatible with the tightest Pod Security level a cluster can enforce, and
only enabling actuation requires an exception. The exception is scoped to the configuration that
actually halts machines rather than to the operator as a whole.

*Pod Security half resolved 2026-08-13 — the operator reports the conflict and does not resolve it.*

Writing `enforce: privileged` onto a namespace was rejected: it makes a CR field the thing that
edits a security boundary, and takes the decision away from whoever is accountable for the cluster's
admission policy. But the failure being left in place was not the rejection — the rejection is
loud — it was that nothing connected it back to this operator's own rendering choices.

`nodePowerAgentPodSecurityConflict` reads the operand namespace's `pod-security.kubernetes.io/enforce`
label during rendering, and only when `mode=Actuate` and `policy=SystemdPoweroff` are both set. If
the level would reject the actuating shape, the agent reports `Degraded` with reason
`PodSecurityRejectsActuation` and a message naming both violations and the way out. Only `enforce`
is consulted; `warn` and `audit` produce messages and admit the pod, so reporting on them would be a
false alarm.

Verified end to end on `kind`. Labelling the operand namespace `enforce=baseline` and deleting the
agent pod produced:

```console
$ kubectl get events --field-selector reason=FailedCreate
Error creating: pods "f61v-agent-node-power-agent-xkgd7" is forbidden: violates PodSecurity
"baseline:latest": non-default capabilities (container "actuator" must not include "SYS_BOOT" in
securityContext.capabilities.add), host namespaces (hostPID=true)

$ kubectl get nodepoweragent f61v-agent -o jsonpath='{.status.conditions[?(@.type=="Degraded")]}'
status=True
reason=PodSecurityRejectsActuation
message=namespace "power-system" enforces pod-security.kubernetes.io/enforce=baseline, which
rejects the actuating agent pod on hostPID=true (host namespaces) and CAP_SYS_BOOT (non-default
capabilities); actuation needs that namespace exempted or labelled
pod-security.kubernetes.io/enforce=privileged. The Stub and DryRun default needs neither and is
admitted under restricted
```

Note that Pod Security's own rejection names the same two violations the message does, which is
where the message's wording comes from rather than from the standard. Before this, the only evidence
was a `FailedCreate` event about a pod that does not exist, on a DaemonSet whose desired count is
non-zero and whose current count is zero.

**F-63 · Record why `hostPID` is required.** `daemonSet.Spec.Template.Spec.HostPID = hostPoweroff`
(`nodepoweragent_render.go:748`), where `hostPoweroff` is true only for `mode=Actuate` plus
`policy=SystemdPoweroff` (`:338-341`).

*Unverified here (Linux `reboot(2)` semantics):* called from a non-initial PID namespace, `reboot(2)`
signals that namespace's init rather than halting the host, which makes `hostPID` load-bearing rather
than defense-in-depth.

The reason this needs writing down is that `F-13` framed the choice as an either/or — `CAP_SYS_BOOT`
alone *or* `hostPID` plus `nsenter` — and the code does both, for a reason `F-13` does not state.
Read against `F-13` alone, `hostPID` looks like an unnecessary privilege, and someone will harden it
away.

*Confirmed 2026-08-13, measured with the shipped actuator rather than argued.* `hostPID` is
load-bearing, and removing it fails in the worst available way: silently, reporting success.

`reboot(2)` calls `reboot_pid_ns` before doing anything. From the initial PID namespace it returns
immediately and the real poweroff proceeds. From any other, it does not return to the caller at all:
for `LINUX_REBOOT_CMD_POWER_OFF` and `HALT` it records the namespace's reboot signal, `SIGKILL`s the
namespace's init, and exits the calling task. Only the `default:` branch returns `-EINVAL`, which is
what the `F-62` probe hit with `CAD_ON`.

Run on `kind` with the real `node-actuator` image, a real shutdown signal, `mode=Actuate`,
`policy=SystemdPoweroff`, and `hostPID` deliberately **omitted** — the shape someone would produce
by hardening it away. The actuator was PID 1 in its own namespace, so the blast radius was its own
container:

```console
node-actuator starting mode=Actuate policy=SystemdPoweroff poweroff=reboot-syscall(POWER_OFF)
node-actuator CAP_SYS_BOOT held in the permitted set; actuation armed
node-actuator systemd actuator executing poweroff executionID=f63-probe node=f63-nohostpid flow=f63-flow
```

That is the entire log. `runPoweroff`'s error check produced nothing, because `rebootPoweroff` never
returned to it. The container terminated with exit code `130` — `128 + SIGINT`, the signal
`reboot_pid_ns` records for `POWER_OFF` — and the node and the host stayed up.

So the failure mode of removing `hostPID` is: the syscall reports no error, the actuator logs
nothing wrong, the container dies and is restarted by the DaemonSet, and the machine that was
supposed to power off keeps running. Combined with `F-60` — no receipt, no metric, no channel to
report on — nothing in the cluster distinguishes that from a successful poweroff, because the
executor infers success from the node disappearing and the node has not disappeared.

This is the strongest argument against hardening `hostPID` away, and it is stronger than the
`F-13`-shaped argument that it is merely redundant with `CAP_SYS_BOOT`. The two are not alternatives:
`CAP_SYS_BOOT` is what makes the syscall permitted, and `hostPID` is what makes it mean what the
caller intended. Losing either one produces `EPERM` and a silent no-op respectively, and only the
first is visible.

### Checks that cannot fail

**F-64 · `F-46`'s rule never crossed to the agent side.** `actuatorReadinessProbe`
(`nodepoweragent_render.go:983-995`) execs `/node-actuator --version`, which
`cmd/node-actuator/main.go:39-41` answers by printing a string and returning — before any of the
config parsing at `:50-60` and without reference to the watch loop. A process parked forever in
`block()` passes it, and so would one that never found its signal directory.

The images repeat it: `images/node-actuator/Dockerfile:38` is `HEALTHCHECK ... CMD ["/node-actuator",
"--version"]` and `images/upsmon-agent/Dockerfile:115` is `HEALTHCHECK ... CMD upsmon -V`. `F-46`
retired exactly this instruction on the server image (`upsd -V`) and the reasoning transfers
unchanged: a check that cannot fail is worse than no check, because it reads as coverage.

Actuator readiness should reflect the watch loop — signal directory readable, TTL clock sane, mode
and policy parsed.

*Resolved 2026-08-14, verified against a running agent.* The probe now execs `/node-actuator
--ready`, which reads a state file the watch loop rewrites after every pass and fails on a missing,
stale, unparseable record or on a signal directory the loop could not stat. The staleness bound is
derived from the loop's own interval (3×, floored at 30s) rather than fixed, so slowing the loop
down does not fail a correctly-running actuator.

Under a `Disabled` policy it passes without consulting the file. A process whose configured job is
to block has no loop to report on, and the exec itself is the evidence the container is alive;
demanding a loop there would leave every `Disabled` agent permanently NotReady.

The state file goes to a volume mounted into the actuator and nowhere else, deliberately not to the
`power-agent-run` emptyDir the signal writer also has open for writing. Health evidence a
neighbouring container can rewrite is not evidence, and `F-57` is already open on that boundary.

Both probes were run side by side on a rendered agent whose state volume was mounted read-only —
a real failure, not a simulated one:

```console
$ kubectl exec ... -- /node-actuator --version
77ab7ad-dirty
exit=0
$ kubectl exec ... -- /node-actuator --ready
not ready: the Stub watch loop has not recorded a pass at /run/actuator/state.json: open
/run/actuator/state.json: no such file or directory
exit=1
```

The loop also logs the write failure once, on transition, so the cause is visible from the container
log and not only from the probe. `images/node-actuator/Dockerfile`'s `HEALTHCHECK` now asks the same
question, following the precedent `F-46` set on the server image.

**F-65 · The `upsmon` readiness probe is blind to authentication and TLS.**
`upsmonReadinessProbe` (`nodepoweragent_render.go:965-981`) parses `MONITOR` lines out of
`upsmon.conf`, takes the host half of each `<ups>@<server>` target, and runs `upsc -l "$server"` —
an anonymous `LIST UPS`. It exercises neither the `MONITOR` credentials nor the TLS posture rendered
into the same file.

That is the exact shape of `F-40`, where `upsmon` logged `connect failed: SSL error` against a server
`upsc` reached fine. The probe would have reported Ready throughout.

**Verified in this environment:** running the probe's command verbatim against an otherwise valid
`upsmon.conf` containing zero `MONITOR` lines exits **0**. The `awk` output is empty, the `while`
body never runs, and the pipeline's status is the loop's. A rendering bug that dropped every monitor
target would present as a healthy agent.

With `F-35`'s `pgrep -x upsmon` liveness (`:951-963`), nothing anywhere proves `upsmon` holds a live
authenticated session. `F-68`'s `NOTIFYCMD` state file is the NUT-native source for a check that
would.

*Partly resolved 2026-08-14; the authentication and TLS half stays open and is now stated as such.*

The vacuous-pass defect is fixed and was reproduced first rather than taken on trust. The old script
piped targets into `while read`, so with zero `MONITOR` lines the loop body never ran and the
pipeline's status was the loop's:

```console
$ # old script, upsmon.conf with no MONITOR lines, upsc stubbed to always fail
$ sh -ec '... | while IFS= read -r target; do ... upsc -l "$server"; done'; echo $?
0
$ # rewritten script, same file
1
```

The rewrite counts the targets first, fails on none, and runs the loop in the current shell so
`set -e` applies to its body.

The query also changed from `upsc -l "$server"` — an anonymous `LIST UPS` against the host half of
the target — to `upsc "$target"`, the full `<ups>@<server>`. Measured against a real `dummy-ups`
backed server:

| Query | UPS the server serves | UPS it does not |
| --- | --- | --- |
| `upsc -l <server>` (old) | `0` | `0` |
| `upsc <ups>@<server>` (new) | `0` | `1` |

So a rendering bug that put the wrong UPS name in a `MONITOR` line passed the old probe and fails
the new one.

What is still not covered is the part `F-40` fell into: the `MONITOR` credentials and the TLS
posture rendered into the same file. `upsc` does not read `upsmon.conf`, so it cannot exercise
either from here, and no rearrangement of this probe will change that — `F-68`'s `NOTIFYCMD` state
file remains the NUT-native source for a check that would. `images/upsmon-agent/Dockerfile`'s
`HEALTHCHECK` was `upsmon -V` and now asks the same question this probe does.

### NUT mechanisms inert or unused

**F-66 · `POWERDOWNFLAG` is written and structurally unreadable, and no PID file is written.**
`nodepoweragent_render.go:531` emits `POWERDOWNFLAG` at `powerdownFlagPath` (`:614-620`), which
derives from the signal path and therefore lands inside `/run/power-agent` — the `emptyDir` at
`:782-787`, which dies with the pod. Nothing in the repository ever runs `upsmon -K`; a search for it
across the images, the render path, and `cmd/` returns nothing.

**Verified by running `example.com/upsmon-agent:v0.0.1` (NUT 2.8.5):**

- `upsmon -h` reports `-K  checks POWERDOWNFLAG (***NOT CONFIGURED***), sets exit code to 0 if set`,
  `-c reload`, `-c fsd`, and `-P <pid>` (which exists precisely to *bypass* the PID file). There is
  no `-FF`.
- `/run/nut` — the `--with-altpidpath` target configured at `images/upsmon-agent/Dockerfile:53` —
  **does not exist in the image**. The Dockerfile creates `/run/power-agent` only (`:101-102`).
- After running `upsmon -F` to steady state, `/run/nut` still does not exist. No PID file is written
  anywhere under it.

The DaemonSet then mounts the `upsmon-run` `emptyDir` over `/run` wholesale
(`nodepoweragent_render.go:820`), so even an image that did create the directory would have it
masked at runtime.

The cost is `-c reload`, `-c fsd`, and `-K` together: all three signal a running process located
through its PID file, and there is no PID file.

*Partly withdrawn and partly resolved, 2026-08-14 — the PID-file half of this finding was a harness
artifact.* It was verified by running the image with `docker run`, where `/run` is an ordinary image
directory. In the rendered pod `/run` is the `upsmon-run` `emptyDir`, so it is writable, and upsmon
writes its PID file there:

```console
$ find / -name '*.pid'
/run/upsmon.pid
$ upsmon -c reload; echo "exit=$?"
Exiting upsmon instance after sending a signal to the running daemon successfully
exit=0
# and in the upsmon log:
Signal 1: User requested RELOAD
Reloading configuration
```

So `-c reload`, `-c fsd`, and `-K` were never broken in a deployed agent — the cost this finding
records was never paid. The file is at `/run/upsmon.pid` rather than under the configured
`--with-altpidpath=/run/nut`, which is why looking for it there found nothing. A `/run/nut` mount was
added while chasing this and then removed: it stayed empty, and an unused mount that looks like it
does something is worse than no mount.

What remains is `POWERDOWNFLAG`, and it is inert by design rather than broken. It points into the
pod-scoped `emptyDir`, so it does not survive the reboot it exists to communicate across, and nothing
runs `upsmon -K`. Both are correct here: `-K` belongs to a host init script deciding whether to tell
the UPS to cut power on the next boot, and this operator does not own the host's init. The operand's
equivalent is the signal file and the actuator. It is left rendered because upsmon expects the
directive, and recorded here as vestigial so the next reader does not go looking for its consumer.

**F-67 · `Args: ["-D"]` should be `-F`.** `nodepoweragent_render.go:811`. **Verified from
`upsmon -h` on the built image:** `-D` is "raise debugging level (and stay foreground by default)"
while `-F` is "stay foregrounded even if no debugging is enabled". Foregrounding is a side effect of
the debug flag, so the agent runs at debug level permanently on every node. `upsmon` has no `-FF`
and does not need one — unlike `upsd` it has no PID-file-on-foreground variant to select.

*Resolved 2026-08-14.* `Args: ["-F"]`, verified on the rendered pod.

**F-68 · The whole notification surface is unused.** A search of the render path for `NOTIFYFLAG`,
`NOTIFYCMD`, `NOTIFYMSG`, `RBWARNTIME`, `NOCOMMWARNTIME`, and `SHUTDOWNEXIT` returns nothing;
`renderNodePowerAgentSecret` (`nodepoweragent_render.go:523`) emits `MINSUPPLIES`, `SHUTDOWNCMD`,
`POWERDOWNFLAG`, the timing keywords, and `MONITOR` lines only.

This is the NUT-native way for a node to publish its own events, and it is the missing input to two
other findings: `COMMOK`/`COMMBAD`/`NOCOMM` dispatched via `EXEC` into a state file is what `F-65`
needs for a readiness check that means something, and it is a candidate channel for `F-60`.

*Observed while running the image:* with no `NOTIFYCMD` configured, `upsmon` falls back to `wall`,
which is absent from the operand image — the run logged `Warning: no custom notification command
defined` followed by `sh: wall: not found`. The notifications are not merely unused; they currently
fail.

*Resolved 2026-08-14.* `NOTIFYCMD` dispatches to `power-notify-writer`, a new binary in the agent
image that appends each event to a JSON file and interprets nothing. `NOTIFYFLAG` is `SYSLOG+EXEC`
for the nine events worth recording, and `NOCOMMWARNTIME` / `RBWARNTIME` are rendered from new spec
fields.

This is what closed `F-65`'s remaining half. `power-notify-writer --check` fails when upsmon's most
recent word on a UPS was `COMMBAD` or `NOCOMM`, and the readiness probe runs it as its third step —
so readiness now depends on upsmon's own session, which is the one thing `upsc` cannot reach.
Deliberately excluded: `ONBATT` and `LOWBATT`, because a node on battery has a working session with
a UPS telling it something important, and failing readiness there would pull the agent out of
service at the moment it matters. Silence passes too — an agent that has dispatched no event has
nothing to report, and treating that as unhealthy would fail every rollout.

**F-69 · `subPath` mounts do not receive updates.** `nut-client-config` and `upsmon-config` are both
mounted with `SubPath` (`nodepoweragent_render.go:818-819`), as is the server-CA source on the init
container (`:849-855`).

*Unverified here (documented Kubernetes behavior):* a container using `subPath` does not receive
ConfigMap or Secret updates.

If that holds, the config-hash rolling restart (`:743-745`) is not merely the chosen path but the
only one that works, and adding `upsmon -c reload` requires converting to directory mounts first —
on top of `F-66`'s missing PID file, without which the reload cannot be signaled at all.

*Resolved 2026-08-14.* The two `subPath` mounts are replaced by one projected volume carrying both
files, mounted as a directory at `/etc/nut`. upsmon reads its config from a compiled-in sysconfdir
and has no flag to point it elsewhere — `-c` takes a command, not a path — so the files have to
arrive under their real names at that path, which is what a projected volume with two sources
achieves through a single mount that does propagate updates.

Masking `/etc/nut` costs nothing: the image ships only `*.sample` files there. The TLS CApath moved
from `/etc/nut/tls/server-ca.d` to `/var/lib/nut-tls/server-ca.d` so the projection cannot shadow it,
and the init container reads the CA bundle through its own Secret mount rather than out of the
agent's `/etc/nut`, which now deliberately carries only `upsmon.conf` and `nut.conf`.

With `F-66`'s PID file turning out to have been present all along, `upsmon -c reload` is now
reachable end to end. Nothing uses it yet — the config-hash rolling restart is still what runs — but
it is a choice again rather than the only option.

### Event-time coupling

**F-70 · Signal TTL is set beside the delivery bound rather than derived from it.** The default is
the literal `"2m"` in two `durationString` fallbacks (`nodepoweragent_render.go:886` and `:1007`);
`api/v1alpha1/nodepoweragent_types.go:236-238` declares `signalTTL` with no CRD default, and the
webhook validates it as positive (`internal/webhook/v1alpha1/nodepoweragent_webhook.go:257`) without
defaulting it.

The one measurement on record is in this file: `F-36`'s smoke-test note observed projected-Secret
delivery at **~44 seconds**. Nothing connects the two numbers, and kubelet sync period plus cache TTL
can push the worst case toward the bound. TTL and the Urgent tier's budget should both derive from
the delivery bound rather than sit next to it as independent constants.

*Resolved 2026-08-14.* The webhook rejects a `signalTTL` below `minimumSignalTTL` (90s), which is the
measured ~44s delivery plus room for kubelet's sync period. The point is not strictness: a TTL under
the delivery bound does not make the system safer, it makes every correctly delivered signal arrive
already expired, rejected as `SignalStale` on every node at once, evidenced only by a container log
line. The constant carries the measurement it derives from so the next person to change it has to
argue with the number rather than guess.

**F-71 · `MONITOR` targets and the readiness probe both depend on cluster DNS.**
`nodepoweragent_render.go:514` builds every monitor target as
`fmt.Sprintf("%s.%s.svc.cluster.local", server.Name, namespace)`, and the readiness probe
(`:965-981`) resolves the same name because it reads its target back out of the rendered `MONITOR`
lines.

CoreDNS is an ordinary workload inside the flow's own path. When it goes, every agent loses
reconnect capability and flips NotReady together, and the probe cannot distinguish that from the
server being down. Rendering the Service ClusterIP or adding `hostAliases` removes the dependency;
either way DNS needs an explicit tier position.

*Resolved 2026-08-14.* `NUTServer` publishes its Service's `clusterIP` in `status.serviceEndpoints`,
and the agent renders that in preference to the DNS name. Verified on `kind`:

```console
$ awk '/^MONITOR/ {print $2}' /etc/nut/upsmon.conf
ds-ups@<service-cluster-ip>
```

The DNS name stays as the fallback rather than being removed: a server that has not yet published an
endpoint still has to be addressable, and a name that resolves later beats no target at all. A
headless Service reports `None`, which is not an address, so it is filtered out rather than rendered
into a `MONITOR` line that fails every connection for no visible reason.

**F-72 · Rollout shape leaves nodes unmonitored and is not suppressed during a flow.**
`nodepoweragent_render.go:736-741` sets `RollingUpdate` with `MaxUnavailable: 1`, no `MaxSurge`, and
no `MinReadySeconds`. Every rollout therefore leaves one node with no agent for a full
pull-and-start window.

`maxSurge: 1, maxUnavailable: 0` is the better shape for a workload whose absence is the failure, and
nothing blocks it: a search of the render path for `HostPort` and `HostNetwork` returns nothing, so
no port conflict prevents two agent pods coexisting on a node during the swap.

*Resolved 2026-08-14.* `maxSurge: 1, maxUnavailable: 0`, confirmed on the rendered DaemonSet. The
rollout-suppression half is not done and is not a shape change: nothing prevents a rollout starting
mid-flow, and deciding what does — a paused DaemonSet, an admission guard, a flow-active condition —
is a design question rather than a field to set.

Separately, a search for rollout suppression — a paused DaemonSet, a flow-active guard — returns
nothing. Nothing prevents a rollout starting while a `ShutdownFlow` is executing.

**F-73 · Agent image residency is unguarded.** `renderImageReference`
(`nodepoweragent_render.go:364-382`) defaults `PullPolicy` to `IfNotPresent` (`:377-380`), which is
the right default. Nothing rejects a user-supplied `Always`, and `ImageReference`
(`api/v1alpha1/common_types.go:68-84`) makes `digest` optional alongside `tag`.

The hazard is specific to this operand: the images may live in a registry running inside the cluster
being shut down, so `Always` turns the agent into a workload that cannot start at exactly the moment
it is needed.

*Resolved 2026-08-14.* `validateNodePowerAgent` rejects `pullPolicy: Always` on either agent image
with `AgentImagePullPolicyAlways`. Rejected rather than defaulted-over, because silently rewriting a
value the user set is how the setting comes back in a later manifest with nobody noticing. The digest
half of the finding is left as it is: `ImageReference` accepts a digest and does not require one,
which is a supply-chain preference rather than the availability hazard this finding is about.

### Coverage

**F-74 · Coverage is measured against the agent's own selector, not against the inventory.** This
finding arrived overstated and is narrowed here.

What already works: `selectedNodeNames` (`nodepoweragent_render.go:1118-1138`) lists nodes matching
`spec.nodeSelector`, and `nodePowerAgentNodeStatuses` (`:1140-1170`) walks that list against observed
pods, reporting `AgentPodMissing` with `Ready: false` for any selected node without one. A node
carrying an untolerated taint is therefore **already visible** as unavailable — the transfer note's
claim that nothing detects a node with no agent pod is wrong.

The real gap is the frame of reference. Both the per-node statuses and `UnavailableNodeCount` are
computed over nodes the agent's own selector already matched, and `DesiredNumberScheduled` /
`NumberReady` (`:249-250`) come straight from `DaemonSet.Status`, which counts scheduled pods. No
path compares either set against the power-domain inventory. A node that the inventory considers in
scope but `spec.nodeSelector` excludes is not degraded and not unavailable — it is absent from every
count, and the agent reports fully ready.

That is the inverse of readiness and the check that catches placement mistakes: not "is the pod I
scheduled healthy" but "is there a node I was supposed to cover and did not".

*Resolved 2026-08-14.* `status.uncoveredNodes` names every `PowerInventoryNode` whose `nodeName` this
agent's selector does not match, and the agent reports `Degraded` with reason
`InventoryNodesUncovered`. Nodes marked `powerPlanningExempt` are excluded — the inventory has
already said they are outside the planning model, and reporting them would train the reader to ignore
the field.

Ranked below `F-62`'s admission rejection, which stops the agent existing at all, and above a TLS
downgrade, which still monitors. It is the only signal in the agent computed against something other
than its own selector, which is precisely why it can catch a selector that is wrong.

### Naming and hygiene

**F-75 · `policySystemdPoweroff` names a mechanism it stopped using.**
`cmd/node-actuator/main.go:29` declares `policySystemdPoweroff = "SystemdPoweroff"` and
`api/v1alpha1/nodepoweragent_types.go:221` exposes it as the CRD enum value a user sets to enable
real actuation. Since `F-36` the branch it selects calls `runPoweroff` → `rebootPoweroff`
(`cmd/node-actuator/main.go:173-189`) and never touches systemd. The file's own header comment
(`:16-25`) explains that shelling out to `systemctl` was removed precisely because it needed
privileges the syscall does not.

The cost is misdirection at review time: a reviewer asking what privileges the container needs is
told dbus and host PID by the name, when the answer is `CAP_SYS_BOOT`. Rename together with the CRD
enum.

Three smaller items to fold in while there, all in the same two files:

- `signalPaths` (`cmd/node-actuator/main.go:111-138`) splits on `:` as well as `,`, whitespace, and
  newlines. Any path containing a colon fragments into nonexistent paths, each failing as
  `SignalMissing` — the one reason `watchSignals` deliberately does not log (`:160-162`). The
  failure is silent by construction.
- `POWER_SIGNAL_PATH` and `POWER_SIGNAL_PATHS` are both rendered
  (`nodepoweragent_render.go:884-885`) with the former's value contained in the latter. Two
  variables, one of them redundant, and the actuator reads `POWER_SIGNAL_PATH` twice at `:57` to
  build the fallback.
- `seen` (`cmd/node-actuator/main.go:148`) is keyed on `SignalKey`, which includes the timestamp
  (`internal/nodeagent/signal.go:92-94`), so it gains an entry per distinct signal and never loses
  one over the pod's lifetime.

*Partly resolved 2026-08-14.* Two of the three smaller items are fixed. `signalPaths` no longer
splits on `:`, which is a legal path character and fragmented any such path into pieces that failed
as `SignalMissing` — the one reason the watcher deliberately never logs, so the failure was silent
by construction. `seen` is now bounded at 64 keys and persisted, which `F-58` needed anyway.

The rename itself is not done, and neither is the duplicate `POWER_SIGNAL_PATH` /
`POWER_SIGNAL_PATHS` pair. Both change the CRD's published enum or the container contract, so they
belong with a deliberate alpha API revision rather than folded into a fix pass — renaming the enum
value silently breaks every existing CR that sets it.

## Not findings — 2026-08-12 privilege-model reading

Recorded verbatim from the transfer note that proposed `F-54`–`F-75` and `OD-37`. Provenance: static
reading of `main` on 2026-08-12 — `internal/controller/nodepoweragent_render.go`,
`internal/nodeagent/signal.go`, `cmd/node-actuator/main.go`, `cmd/power-signal-writer/main.go`, and
the three agent images — checked against Network UPS Tools v2.8.5 `clients/upsmon.c`. Nothing was
run. The findings are written up above.

- Defaults fail safe. Mode defaults to `DryRun`, policy to `Stub`, and `SystemdPoweroff` refuses
  unless mode is `Actuate`, so an env-injection failure yields an inert agent rather than a live one.
- `WriteSignalAtomic` writes via temp file and `rename`, so the actuator never sees partial JSON.
- The projected signal Secret is mounted without `subPath` and marked `Optional`, which is why it
  updates in place at all.
- The toleration baseline (`Exists` on both `NoSchedule` and `NoExecute`) covers every taint that can
  block scheduling.

## Recommended order — third pass, 2026-08-12

Given in conversation on 2026-08-12 and recorded here because it existed nowhere else. It is a
dependency order, not a severity order: `F-54` is the most severe finding in the pass and is not
first, for the reason the exposure note above gives.

1. **`F-61` first, before anything else in the privilege group.** It is the only item whose answer
   can invalidate the others: if `CAP_SYS_BOOT` does not survive the UID transition, the container
   shape changes, and `F-62`'s seccomp question and `F-63`'s `hostPID` justification are both being
   asked about a configuration that does not work. Settle it, then `F-62` and `F-63` in either order.

   *Done 2026-08-13. `F-61`, `F-62`, and `F-63` are all closed.* The privilege group is settled and
   step 2 is next. Worth carrying forward from how it went: `F-61` had to be reopened after being
   closed against a hand-written pod, and every conclusion that changed changed because something was
   run in the shape the operator actually renders rather than in a shape assembled to test it.
2. **`F-54` and `OD-37` next, together.** `OD-37` is the decision and `F-54` is its consequence, so
   they resolve as one. They gate `F-55`, `F-56`, and `F-57`: the shape of the authorization fix
   depends entirely on whether the local `upsmon` signal path survives at all. Designing signal
   binding (`F-55`), replacing the `DryRun` flag (`F-56`), or hardening the tmpfs boundary (`F-57`)
   before that answer risks building all three for a path that is about to be deleted.
3. **`F-66` and `F-69` before any `upsmon -c reload` work.** Both are prerequisites rather than
   alternatives: reload signals a process located through its PID file, which `F-66` shows is never
   written, and it is pointless without config updates reaching the container, which `F-69`'s
   `subPath` mounts prevent. Either one alone leaves reload non-functional.
4. **The self-contained items, in any order, whenever convenient.** `F-64` (actuator readiness),
   `F-65` (`upsmon` readiness — `F-68` first if the `NOTIFYCMD` state file is chosen as its source),
   `F-67` (`-D` → `-F`), `F-72` (rollout shape), `F-73` (image residency), and `F-75` (naming and the
   three hygiene items). None of these depends on an open decision.
5. **`F-58`, `F-59`, `F-60`, `F-70`, `F-71`, and `F-74` last**, as they each need a design answer
   first: where an actuated-signal record persists, what channel carries a receipt, what the delivery
   bound actually is, where DNS sits in the tier order, and what "covered" is measured against.
   `F-59` and `F-70` both resolve into `F-60`'s channel choice, so sequence that one ahead of them.

## Findings — v1 triage pass, 2026-08-14

Continues the `F-n` namespace from `F-85`. Scope: the signal delivery channel and the actuator's
dedupe, read while triaging the section for v1 alongside the `OD-37` decision above.

**F-86 · A dead delivery channel is indistinguishable from a quiet one.** `power-agent-signals` is a
Secret volume marked `Optional: true` (`nodepoweragent_render.go:911`). When the Secret is absent
kubelet mounts an empty directory rather than failing the pod, so `unreadableSignalDirs` finds a
directory it can stat, records nothing, and readiness passes. An operator that never created the
Secret, or stopped updating it, presents exactly as a cluster with no flow running.

This matters more after `OD-37` than it did before it: the projected Secret is now the only channel
with authority, so its silence is the single failure that ends in nodes never being released.

The 2026-08-12 "not findings" list credits `Optional` with the fact that the volume updates in
place. That is wrong and is corrected here — in-place updates come from the absence of `subPath`
(`F-69`), and `Optional` governs only what kubelet does when the Secret does not exist.

*Closed, and `Optional: false` alone would not have done it.* The obvious fix stops kubelet
substituting an empty directory for a Secret that is missing, but the operator creates this Secret
with no data at all until a flow writes a node's signal into it — and an empty Secret projects as an
empty directory too. The state the finding describes was reachable with the Secret present and
correct.

So the Secret always carries a `delivery-channel` key (`internal/nodeagent.DeliveryChannelMarker`),
valued with the agent's namespace, name, and generation. Generation rather than a timestamp on
purpose: the value has to be stable across reconciles, or every pass would rewrite the Secret and
re-trigger kubelet projection on every node for nothing. The actuator records directories missing
the marker as `unprojectedSignalDirs`, kept distinct from `unreadableSignalDirs` because the two
mean different things — one is "the volume never arrived", the other is "something is mounted here
and it is not the operator's Secret". Readiness fails on either. `Optional: false` stays as the
outer guard, which is now coherent since the Secret is never empty.

What this does not detect, stated so nobody assumes otherwise: an operator that stopped reconciling
leaves a valid marker behind. Agent readiness is the wrong place to observe manager liveness, and
the manager has its own health endpoints and leader election for it.

**F-87 · A rollout mid-flow can re-actuate a signal inside its TTL.** `F-58` persists actuated keys
so a restarted actuator does not act twice on one signal, but the state lives on a per-pod emptyDir.
`F-72` set `maxSurge: 1`, so a rollout brings up a second pod whose `seen` set is empty, watching
the same projected signal the old pod already acted on. Within the TTL that is a second actuation of
one episode — the outcome `F-58` was closed to prevent, reachable through the change `F-72` made.

Neither finding is wrong on its own; the interaction between them was never recorded. It resolves
with `F-72`'s remaining half — suppressing rollouts while a flow is live — rather than by moving the
state somewhere node-scoped, which would mean a host mount the actuator does not otherwise need.

**F-88 · Dedupe is per-source, not per-episode.** `watchSignals` iterates every configured path
without breaking after it acts, and `SignalKey` (`internal/nodeagent/signal.go:93`) is built from
the payload — execution ID, node, plan hash, timestamp. Two sources describing one episode produce
two payloads and therefore two keys, so the `seen` map does not deduplicate across them.

*Closed.* `OD-37` removes the second source, and the pass now returns at the first live signal
rather than continuing, so the shape is gone as well as the case. The per-pass scan was split out of
`watchSignals` into `scanSignalPaths` to make the at-most-once property testable — the loop around
it never returns, and a rule nothing can exercise is a rule that quietly stops holding.
`TestScanActuatesOnceWhenTwoPathsCarryTheSameEpisode` was confirmed against the unfixed code and
reports two actuations for one episode.

**F-89 · The signal Secret mounts whole on every node. Declined.** One Secret carries the signals
for the entire DaemonSet, so any agent can read the payload addressed to any other node. The
node-name check in `InspectSignal` keeps this to exposure — a node cannot act on another node's
signal — and the payload carries flow identity, plan hash, timestamp, and reason, but no
credentials.

Declined rather than fixed. Per-node Secrets multiply API objects by node count and widen the
operator's write surface for no confidentiality gain over what a cluster-scoped reader can already
see. The cheap alternative, `subPathExpr` with the node name, cannot be used at all: `subPath`
mounts do not receive updates (`F-69`), and a signal that never updates is a signal that never
arrives. Revisit only if the payload gains something worth protecting.

**F-90 · Signal TTL versus kubelet Secret propagation is an unstated assumption.** The TTL bounds
how long a signal stays actionable; kubelet's sync period plus cache TTL bounds how long it takes to
appear in the pod. Nothing states the relationship, and `F-70`'s 90-second floor was derived from
one measurement (~44s) rather than from a stated bound. Folded into `F-59`, which has the same shape
on the clock axis.

**`F-66` remainder closed.** `POWERDOWNFLAG` is no longer rendered. upsmon writes that file so a
system's own late-boot script can ask "did we power off because of the UPS?" and call
`upsdrvctl shutdown`; this operand has no such script and no init system to run one, so the
directive named a path nothing would ever read. Removed rather than left inert, because a rendered
NUT directive invites the next reader to assume a consumer exists — which is how this audit came to
be written in the first place. The PID-file half of `F-66` was withdrawn earlier as a harness
artifact: `upsmon` writes `/run/upsmon.pid` in the pod and `upsmon -c reload` works.

**`F-57` closed, in a different shape than proposed.** The recommendation was to mount the
actuator's `power-agent-run` copy `ReadOnly`. That is the wrong fix and would have looked like the
right one: the host halts because the actuator *reads* the local signal, so closing the write
direction addresses a threat nobody posed. The mount is removed instead — a volume the actuator does
not mount is a volume it cannot be reached through — and the local path is out of both
`POWER_SIGNAL_PATH` and `POWER_SIGNAL_PATHS`.

The actuator's own compiled-in default moved with it. It was `/run/power-agent/shutdown.json`, so a
failure to inject `POWER_SIGNAL_PATH` did not disarm the actuator — it repointed it at exactly the
path `OD-37` declines to trust. The default is now derived from `POWER_NODE_NAME`, and with no node
name there is no path at all: the actuator watches nothing rather than guessing.
