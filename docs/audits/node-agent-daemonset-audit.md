# Node Agent DaemonSet Audit

Status: audit record, 2026-08-03, against commit `00eb3c0`. Static reading only.

Supplements `operator-maturity-benchmarks.md`. Findings use the same `F-n` namespace, continuing
from F-7.

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
