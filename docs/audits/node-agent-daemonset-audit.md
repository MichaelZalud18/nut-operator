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

## Not findings — 2026-08-12 privilege-model reading

Recorded verbatim from the transfer note that proposed `F-54`–`F-75` and `OD-37`. Provenance: static
reading of `main` on 2026-08-12 — `internal/controller/nodepoweragent_render.go`,
`internal/nodeagent/signal.go`, `cmd/node-actuator/main.go`, `cmd/power-signal-writer/main.go`, and
the three agent images — checked against Network UPS Tools v2.8.5 `clients/upsmon.c`. Nothing was
run. The findings themselves are still to be written up here; the task line in `docs/tasks.md`
tracks that.

- Defaults fail safe. Mode defaults to `DryRun`, policy to `Stub`, and `SystemdPoweroff` refuses
  unless mode is `Actuate`, so an env-injection failure yields an inert agent rather than a live one.
- `WriteSignalAtomic` writes via temp file and `rename`, so the actuator never sees partial JSON.
- The projected signal Secret is mounted without `subPath` and marked `Optional`, which is why it
  updates in place at all.
- The toleration baseline (`Exists` on both `NoSchedule` and `NoExecute`) covers every taint that can
  block scheduling.
