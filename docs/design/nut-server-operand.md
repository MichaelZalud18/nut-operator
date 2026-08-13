# NUT Server Operand

Status: design. Covers the `upsd` operand the `NUTServer` CRD renders — how it reports health, how it
starts, and which of NUT's own mechanisms it delegates to.

Components: NUT Server / upsd.

`NS-n` identifiers are stable and are not reused or renumbered.

## Health reporting

**NS-1 · Readiness means at least one driver is responsive, and NUT reports that itself.** The
readiness probe runs `upsdrvctl status` — NUT's own driver-state report — and is ready when any row
says the driver is responsive.

`upsdrvctl status` prints one TAB-separated row per device configured in `ups.conf`:

```
UPSNAME              UPSDRV  RUNNING PF_PID  S_RESPONSIVE    S_PID   S_STATUS
dummy             dummy-ups  N/A     -3      NOT_RESPONSIVE  N/A
eco650           usbhid-ups  RUNNING 3559207 RESPONSIVE      3559207 "OL"
```

`S_RESPONSIVE` is decided by probing the driver's own socket, which is the question readiness is
actually asking: not "is a name configured" and not "is a port open", but "is a driver alive and
answering". `RUNNING` is deliberately not the field consulted — a driver process can be running and
not responding, and a pod that reports ready in that state is exactly the silent failure `RS-17`
exists to surface.

**Aggregate ready, per-member visible.** One responsive driver is enough. A server configured with
several devices stays ready while any one of them answers, and each device's individual state is
still readable from the same command and from `upsc`. Marking the pod unready because one of four
devices is unreachable would take telemetry for the other three away from every agent, which is a
worse outcome than reporting a degraded set — and device-level health already surfaces on
`UPSDevice` status, which is where a per-device consumer should read it.

**NS-2 · The responsive check is field-exact.** `NOT_RESPONSIVE` contains `RESPONSIVE` as a
substring, so a `grep` for the token matches a dead driver as readily as a live one. That failure
mode is not a false alarm but its opposite: a readiness probe that can never fail, silently, on
exactly the clusters where a driver has stopped answering. The probe therefore compares whole `awk`
fields, which also skips the header row for free — its token is `S_RESPONSIVE`, not `RESPONSIVE`.

**NS-3 · The Docker `HEALTHCHECK` runs the same command as the probe.** Kubernetes ignores the
`HEALTHCHECK` directive, so under this operator the readiness probe is what actually runs. The image
is still runnable directly, though, and there it should answer the same question the same way rather
than drift into a second definition of healthy — so the instruction is the `upsdrvctl status` check
verbatim.

What it must not be is what it was: `CMD upsd -V`, which proves only that the binary executes and
would pass on a container whose driver never connected and whose `upsd` had died. A check that cannot
fail is worse than no check, because it reads as coverage — and removing it outright is not the fix
either, since that trades a misleading check for an absent one.

Liveness is left to the process model rather than a probe: the entrypoint `exec`s `upsd`, so if
`upsd` exits the container exits and Kubernetes restarts it.

### Why not a bespoke `upsc` loop

The original probe (`F-17`) listed devices with `upsc -l` and queried `ups.status` on each name,
treating a failed query as a disconnected driver. It worked, and it was wrong in the way that
matters here: it reimplemented in shell a state report NUT already publishes, so the operand's
definition of "healthy" lived in this repository instead of in NUT.

`upsc -l` alone genuinely cannot answer the question — it lists every name in `ups.conf` whether or
not the driver ever connected — but the fix for that is to ask the component that knows, not to
infer the answer from a client error string. `F-46` records the correction.

This is the same rule as `GP-4` (consume signals, do not rebuild them) applied to health: where NUT
reports something, the operator reads NUT's report.

## Startup

**NS-4 · A failed driver does not take `upsd` down.** The entrypoint runs `upsdrvctl start` and
continues even when it fails, then `exec`s `upsd`. A device with bad credentials or an unreachable
endpoint leaves the other devices queryable, and credentials can be corrected without a restart.

The readiness probe is what makes this safe to do: a partial start surfaces as an unready pod
carrying working devices, rather than as a crash loop that takes the working ones down too.

**NS-5 · `upsd` runs foregrounded *and* writes a PID file.** The entrypoint ends with
`exec upsd -FF`.

Three of `upsd`'s flags keep it in the foreground and the running process looks identical under all
three, so the choice between them is easy to get wrong and impossible to see afterwards:

```text
  -D    raise debugging level (and stay foreground by default)
  -F    stay foregrounded even if no debugging is enabled
  -FF   stay foregrounded and still save the PID file
```

`-D` foregrounds only as a side effect of raising the debugging level, so an operand started that
way runs at debug level for its whole life. `-F` says what is meant but skips the PID file, logging
`Running as foreground process, not saving a PID file`.

`-FF` is the flag this operand needs, because the PID file is load-bearing rather than
housekeeping. `upsd -c reload` signals a running process located through it, and that is the path
by which configuration is re-read without replacing the pod — which would drop every `upsmon`
session and NUT's own login accounting. Choosing a foreground flag therefore chooses whether the
operand can ever reload.

The file lands in `/run/nut` via `--with-altpidpath`, the same writable `emptyDir` that already
holds the driver sockets, so nothing about the read-only root filesystem changes.

The smoke test asserts the file exists rather than asserting the entrypoint's text, since the file
is the only observable that separates `-FF` from the two flags that behave identically in every
other respect. `F-47` records the correction.

## Driver supervision

**NS-6 · A sidecar restarts drivers that stop answering.** `upsdrvctl start` runs once in the
entrypoint. A driver that dies afterwards leaves `upsd` alive, so the container is never restarted,
readiness fails correctly, and the pod leaves the Service endpoints — and stays out of them
indefinitely, with every agent monitoring it in `DEADTIME`. Readiness reports the fault accurately
and nothing acts on it. The `driver-watchdog` container is what acts on it.

It runs the operand image, shares `/run/nut` and `/etc/nut` with `upsd`, and every 30 seconds asks
`upsdrvctl status` which drivers are not responding, restarting each one with
`upsdrvctl start <ups>`.

### Why a sidecar rather than a container per driver

Upstream supervises one service unit per driver (`upsdrvsvcctl`, and nut-driver-enumerator since
2.8.0), and the direct Kubernetes translation is a container per driver with kubelet as the service
manager. That would give per-driver restart backoff and per-driver logs for free.

It was declined because it makes the container list a function of the device set. Adding or removing
a `UPSDevice` would change the pod's containers, which is a pod recreate — dropping every `upsmon`
session and NUT's login accounting, which is the damage `F-15` and `F-16` exist to prevent and which
the reload path in `F-48` is being built to eliminate. One supervisor for all drivers keeps the
pod's shape independent of how many devices a server serves.

A liveness probe was the other candidate and is worse on both counts: it restarts `upsd` along with
the drivers, and it cannot fire at all while any one driver still answers — which is the common
case, since a server with four devices losing one still reports ready.

### What the watchdog relies on, and how it is known

Each of these was established by running the operand image, because each one decides part of the
implementation:

- A driver killed outright reports `RUNNING` as `N/A` and `S_RESPONSIVE` as `NOT_RESPONSIVE`, and
  leaves its PID file behind.
- `upsdrvctl start <ups>` recovers from exactly that state on its own: it detects the stale PID
  file, terminates the phantom, starts a fresh driver, and rewrites the PID file. No stop-then-start
  pair is needed, and adding one would open a window where the driver is deliberately down.
- `upsdrvctl start <ups>` against a **healthy** driver terminates and replaces it. A restart is
  therefore not free, and a single transient reading must not trigger one — so a device seen as
  non-responsive is re-checked before the watchdog acts.
- `upsdrvctl status` prints a version banner above the header, on stdout. This is why the selector
  matches the `NOT_RESPONSIVE` token rather than selecting rows that fail to say `RESPONSIVE`: the
  first implementation did the latter, read the banner as a device named `Network`, and tried to
  start it on every pass. The driver still recovered, so nothing failed — the watchdog simply did
  useless work forever, which is the failure mode `F-46` and `NS-2` describe from the other
  direction.

The container carries no probes. A readiness probe would gate the pod's endpoint membership on the
supervisor rather than on the server, and a liveness probe would let a supervisor restart take the
server down with it. Keeping them apart is the reason it is a separate container.

## Related

- `F-17`, `F-46` — [nutserver-pod-audit.md](../audits/nutserver-pod-audit.md)
- `MINSUPPLIES` and the `MONITOR` power value on the agent side, which express the same
  "healthy while any member is healthy" shape for a host rather than for a server (`F-45`,
  [nut-usage-audit.md](../audits/nut-usage-audit.md))
