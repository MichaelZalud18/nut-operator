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

**F-46 · The readiness probe reimplements `upsdrvctl status` in shell, and the Dockerfile
`HEALTHCHECK` is inert.** `upsdReadinessProbeScript` loops `upsc -l`, queries `ups.status`
on each name, and exits 0 on the first device that answers. That is a bespoke aggregation
of "is any driver connected" — and NUT 2.8.5 ships `upsdrvctl status`, which reports each
driver as RUNNING/STOPPED and RESPONSIVE/NOT_RESPONSIVE directly, by probing the driver
socket rather than inferring state from a failed client query.

The probe mechanism is not the problem: Kubernetes offers `httpGet`, `tcpSocket`, `exec`,
and gRPC, and NUT speaks none of the network protocols, so `exec` is the only built-in that
can prove more than an open port. The problem is the command inside it.

The two are not equivalent, which is why this is a finding rather than a swap. `upsc` proves
the whole serving path — client, `upsd`, driver — while `upsdrvctl status` proves driver
health directly and says nothing about whether `upsd` is accepting connections. The
replacement is therefore `upsdrvctl status` for driver state, and `upsd`'s own listening
socket for the rest, rather than one command for both.

Separately, the image declares `HEALTHCHECK ... CMD upsd -V`. Kubernetes ignores Docker
HEALTHCHECK entirely, and `upsd -V` only proves the binary executes — it would pass on a
container whose driver never connected and whose `upsd` was not running. It reads as a
health check while checking nothing that matters. Remove it or make it match the probe.

Fix: replace the shell loop with `upsdrvctl status`, and drop or correct the `HEALTHCHECK`.
Needs verification against a running operand, not a static edit.

## Findings — upstream fidelity pass, 2026-08-12

Continues the `F-n` namespace from `F-46`. Scope: the operand's process model — how `upsd` is
started, how configuration reaches it, and what supervises the drivers.

Everything below marked *verified* was run against `example.com/nut-server:v0.0.1`, the source-built
operand image (NUT 2.8.5, OpenSSL, per `F-39`'s remedy). The only Dockerfile change between that
image and `main` is the `HEALTHCHECK` instruction (`ffeca4a`), so its `configure` flags and binaries
are the ones this repository currently ships.

**F-47 · `upsd` runs at debug level permanently, and writes no PID file.**
`images/nut-server/entrypoint.sh:34` ends with `exec upsd -D`.

*Verified — `upsd -h` on the built image:*

```text
  -D    raise debugging level (and stay foreground by default)
  -F    stay foregrounded even if no debugging is enabled
  -FF   stay foregrounded and still save the PID file
```

Foregrounding is a documented **side effect** of the debug flag, not its purpose. `-F` is the flag
that means what the entrypoint wants, and `-FF` is the flag that means it while also saving the PID
file.

*Verified by running both:* with `upsd -D`, `/run/nut` contains the driver socket and the driver's
own PID file and no `upsd.pid`. With `upsd -FF`, `upsd.pid` is present. Running `upsd -F` logs
`Running as foreground process, not saving a PID file` explicitly.

The PID file is what makes `F-48` possible — `upsd -c reload` signals a running process located
through it — so `-FF` is the correct replacement rather than `-F`. It costs nothing here: the file
lands in `/run/nut` under `--with-altpidpath` (`images/nut-server/Dockerfile:52`), which is already
a writable `emptyDir`, so the read-only root filesystem is unaffected.

*Resolved 2026-08-12.* The entrypoint runs `exec upsd -FF`, and `NS-5` in
[nut-server-operand.md](../design/nut-server-operand.md) records the process model.

The guard is in `hack/smoke-image.sh`, and it asserts the **PID file** rather than the entrypoint's
text, because the three foreground flags are indistinguishable in every other observable — same
process, same `ps` output, same served port. It starts the real entrypoint against a device-less
`dummy-ups` so the driver fails, which also proves `NS-4`: `upsd` comes up on a partial start, not
only a clean one.

Sabotage-verified rather than assumed: rebuilt with `-F` in place of `-FF`, watched the smoke test
fail with `Running as foreground process, not saving a PID file` in the captured container log, then
restored. A test that passes under both flags would have been worth nothing here, which is the same
standard `F-46` applied to the readiness probe.

**F-48 · Every configuration change recreates the pod, and NUT can reload instead.**
`internal/controller/nutserver_render.go:1020` stamps `power.zalud.io/config-hash` on the pod
template, and `:1010-1015` sets `Recreate` as the Deployment strategy. Together, any change to any
rendered config file replaces the pod — dropping every `upsmon` session and NUT's login accounting,
which is precisely the damage `F-15` and `F-16` exist to prevent. The strategy is right; the trigger
is too broad.

*Verified — `upsd -h` and `upsdrvctl -h` on the built image.* `upsd -c reload` "reread
configuration files". On the driver side `upsdrvctl -c <command>` supports `reload`, `reload-or-error`,
`reload-or-exit`, `data-dump`, and `exit`.

One correction to the task line this finding arrived on: the third command is **`reload-or-exit`**,
not `reload-or-restart`. Its documented behavior is to exit the old driver instance if needed so an
external supervisor starts another copy — which is the same mechanism `F-49` needs, and worth reading
the two findings together.

*Unverified here (upstream `upsd(8)`):* which directives a reload actually re-reads. The task scope
should state explicitly what still requires a restart — `LISTEN`, port, and certificate changes are
the expected set — rather than assume reload covers everything.

Projected volumes update in place, so the config does reach the container without a restart. Blocked
on `F-47`: with `-D` there is no PID file to signal.

**F-49 · Nothing supervises the drivers.** `images/nut-server/entrypoint.sh:30-32` runs
`upsdrvctl start` once, deliberately tolerating failure, and then `:34` `exec`s `upsd`. Nothing
retries and nothing watches.

The failure mode is specific and silent. A driver that dies *after* startup leaves `upsd` alive, so
the container is never restarted; readiness then fails correctly (`NS-1`), which pulls the pod from
the Service endpoints — and it stays unready indefinitely with every agent in `DEADTIME`. Readiness
reports the fault accurately and nothing acts on it.

The entrypoint comment at `:25-29` justifies continuing past a failed start, and that reasoning is
sound for *startup*. It does not address a driver that dies later, which is the gap.

*Unverified here (upstream packaging):* NUT supervises one service unit per driver via
`upsdrvsvcctl` and nut-driver-enumerator (2.8.0+). The Kubernetes-loyal equivalents are a container
per driver sharing `/run/nut`, with kubelet as the service manager, or a liveness probe that fails
when no driver is responsive.

This one carries a design trade-off that has to be settled in the same pass rather than after: a
container per driver makes adding or removing a device a pod recreate, which pulls directly against
`F-48`. `NS-4` does not read as complete until this is decided.

**F-51 · A `NUTServer` whose selector matches nothing cannot start, and says the wrong thing about
why.** `renderUPSConf` (`internal/controller/nutserver_render.go:376-378`) builds the file by
iterating devices, so zero selected devices renders a zero-byte `ups.conf`. The entrypoint's
non-empty test (`images/nut-server/entrypoint.sh:8-11`) fires first and exits with
`missing required /etc/nut/ups.conf` — for a file that exists and is exactly what the operator
intended to write.

*Verified — running the image with a device-less `ups.conf` and `upsd -F`:*

```text
listening on 127.0.0.1 port 3493
Warning: no UPS definitions in ups.conf
Fatal error: at least one UPS must be defined in ups.conf
```

Exit code 1. So both layers fail, and the one the operator controls fails first with a message that
sends diagnosis toward a missing file rather than an empty selector.

*Verified — the same run with `ALLOW_NO_DEVICE true` added to `upsd.conf`:*

```text
Warning: no UPS definitions in ups.conf
Normally at least one UPS must be defined in ups.conf, currently there are none
(please configure the file and reload the service)
```

Exit code 0, still listening. Upstream's own message names the intended lifecycle — configure the
file, reload the service — which is `F-48`'s reload path, so the two findings resolve together. The
entrypoint's `-s` test needs to become a real emptiness check with an accurate message, or move out
of the way of the directive that already handles this case.

**F-53 · The receipt for `F-46` describes the removal and not the replacement.** The NUT Server
*Built* paragraph in `docs/tasks.md` records that the inert Docker `HEALTHCHECK` "is gone". It is
not: `images/nut-server/Dockerfile:135-136` carries a `HEALTHCHECK` running the readiness probe's
`upsdrvctl status` check verbatim, and `NS-3` in `docs/design/nut-server-operand.md` describes it as
present by design.

The sequence that produced the discrepancy is worth recording because it is the general case, not a
typo. The instruction was first deleted on the reasoning that Kubernetes ignores `HEALTHCHECK`; that
was wrong twice — the image is runnable outside Kubernetes, and the security scan's `CKV_DOCKER_2`
flipped from PASSED to FAILED on its absence — so it was restored pointing at the same command as
the probe. The status line was written for the deletion and never updated for the restoration. A
receipt written mid-change describes an intermediate state.

### Recommended order — upstream fidelity pass

1. `F-47` first. One line in the entrypoint, and it is the prerequisite for everything else here:
   without the PID file there is no reload to build on.
2. `F-53` alongside it — a documentation correction with no dependencies, cheapest while the
   surrounding context is already open.
3. `F-49` next, because it is a design decision rather than a change, and the answer constrains
   `F-48`: a container per driver makes device add/remove a pod recreate, which is the outcome
   `F-48` exists to avoid.
4. `F-48` once `F-47` has landed and `F-49` has been decided, scoping explicitly what still requires
   a restart.
5. `F-51` last, since upstream's own remedy for it ends in "reload the service" and reads as
   incomplete until `F-48` exists.

`F-50` and `OD-36` are recorded in [nut-usage-audit.md](nut-usage-audit.md) as NUT-mechanism
fidelity rather than operand-pod findings. `F-50` is independent of this sequence and can be taken
at any point.
