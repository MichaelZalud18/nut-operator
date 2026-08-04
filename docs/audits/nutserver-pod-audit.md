# NUTServer Pod Audit

Status: audit record, 2026-08-03, against commit `00eb3c0`. Static reading only.

Components: NUT Server / upsd.

Scope: the `NUTServer` CRD and the `upsd` Deployment it renders, from
`internal/controller/nutserver_render.go`. Nothing else.

`upsd` is an independent component. It serves a TCP port; node agents are clients of it. It has no
coupling to the `NodePowerAgent` DaemonSet or to the actuator beyond protocol and credentials.
Agent-side findings live in `node-agent-daemonset-audit.md`; cross-component NUT usage lives in
`nut-usage-audit.md`.

Findings use the shared `F-n` namespace.

## Findings

**F-15 · Deployment with a user-settable replica count.** `spec.replicas` is plumbed through with a
default of 1. `upsd` is not horizontally scalable: multiple replicas behind one Service mean
clients land on arbitrary instances, each maintaining independent driver state and independent
client login tracking. That breaks the login accounting NUT relies on to know when all clients have
disconnected.

Recommendation: pin to 1 and reject other values at admission. With the SNMP polling model, one
instance per UPS is the correct shape — a second replica adds duplicate polling load with no
availability benefit, since both fail together when the UPS is unreachable.

Highest-severity finding in this audit: it is currently possible to configure a silently broken
topology.

**F-16 · No Deployment strategy set — defaults to RollingUpdate.** For a singleton whose clients
hold long-lived TCP sessions and login state, a rolling update briefly runs two `upsd` instances and
splits the accounting described in F-15. `Recreate` is correct for this operand, accepting a short
outage window on upgrade.

**F-17 · No probes on the `upsd` container.** Readiness matters here specifically: it should
reflect whether the driver has successfully polled the UPS, not merely whether the process is
listening. A TCP-only check would mark a pod ready while its driver fails to reach the device —
exactly the silent failure RS-17 exists to surface. A local `upsc` query against the socket is the
natural implementation.

**F-18 · No PodDisruptionBudget and no default priority class.** `PriorityClassName` is plumbed
through but never defaulted. `upsd` is on the observability path for every agent; preempting or
draining it mid-event puts every agent into DEADTIME simultaneously. Recommend a default priority
class plus a PDB with `minAvailable: 1` — at one replica that blocks voluntary eviction entirely,
which is the desired behavior.

**F-19 · No `topologySpreadConstraints` or anti-affinity.** The architecture pins `upsd` to the
control plane. Not urgent at one replica, but if an HA topology is ever designed, multiple servers
must not co-schedule.

**F-23 · `upsd.users` role granularity is not fully exploited.** NUT distinguishes `upsmon primary`
from `upsmon secondary` and supports per-user `actions` and `instcmds` grants. The current user
model serves secondary monitoring only. If instant commands enter scope (see `nut-usage-audit.md`,
F-22), they require a separate and more privileged user whose credential must never be distributed
to node agents. Design this before implementing commands, not after.

## Not findings

- Container security context is strong and consistent: non-root UID 65532, read-only root
  filesystem, all capabilities dropped, no privilege escalation.
- `Resources` is plumbed from the CR rather than hardcoded.
- Service DNS resolution falls back sanely to the in-cluster service name when status endpoints are
  not yet populated.
- Config values are validated against injection before being written to NUT config files.
- Owner references are set on rendered resources.

## Recommended order

1. F-15 pin replicas to 1 at admission.
2. F-16 `Recreate` strategy.
3. F-17 readiness probe reflecting driver poll success.
4. F-18 priority class default and PDB.
5. F-23 privileged user design, ahead of any instant-command work.
6. F-19 revisit only if an HA topology is designed.
