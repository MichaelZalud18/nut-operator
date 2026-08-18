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

**NS-7 · A server with no devices runs idle rather than failing.** A `NUTServer` whose device
selector matches nothing renders an empty `ups.conf`, and that is a legitimate state — a server
created before its devices, or one whose last device was removed. It starts, listens, and reports
NotReady.

Two separate things have to allow it, and each fails differently:

- `upsd` calls `fatalx` on a device-less `ups.conf` — "Fatal error: at least one UPS must be defined
  in `ups.conf`", exit 1 — unless `ALLOW_NO_DEVICE` is set. `upsd.conf` therefore always carries it.
- The entrypoint checks that `ups.conf` **exists**, not that it has content. Checking for content
  reported `missing required /etc/nut/ups.conf` for a file that was present and was exactly what the
  operator meant to write, which sends diagnosis toward a broken mount instead of an empty selector.

`ALLOW_NO_DEVICE` is rendered unconditionally rather than only when the selection is empty. A
conditional directive would appear and disappear as devices come and go, so the transition from one
device to zero would itself require a config change in order to survive — precisely the state the
directive exists to make survivable.

Nothing is concealed by running. `NS-1` reports NotReady when no driver is responsive, so the pod
leaves the Service endpoints and an empty server is visibly idle rather than quietly serving
nothing. `upsd`'s own log line names the intended lifecycle: *please configure the file and reload
the service*.

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

The interval is taken at the top of the loop rather than the bottom, so the first pass happens after
a full interval instead of immediately. Both containers start together, and a check at t=0 races the
entrypoint's own `upsdrvctl start` — the drivers are legitimately not yet responsive, the confirming
re-check agrees with the first, and the watchdog restarts a driver that was seconds from healthy.

## Configuration changes

**NS-8 · Adding a device reloads `upsd`; changing where it listens replaces the pod.** The
pod-template annotation carries a digest of only the configuration `upsd` cannot adopt at runtime.
Everything else reaches a running server through `upsd -c reload`, issued by the watchdog when it
notices the files changed.

The split follows what `upsd` actually does, established by running it:

| Change | Adopted on reload | Path |
| --- | --- | --- |
| Device added to or removed from `ups.conf` | Yes — `upsc -l` reports it immediately after | Reload |
| `upsd.users` contents | Yes — `upsd` re-reads and re-parses the file | Reload |
| `LISTEN` address or port | **No** — reload returns success and `upsd` stays on the old port | Restart |
| Serving certificate or client CA | No — the SSL context is built once at startup | Restart |

The `LISTEN` row is why the split is drawn here rather than trusted to reload generally. The reload
does not merely decline the change; it declines it *silently*, exiting 0 and logging nothing about
the port it did not rebind.

Certificates are on the restart path for the same reason and reach it by a different route: they
are not rendered configuration at all, but referenced Secrets mounted as volumes. A digest of that
material is folded into the restart hash, because otherwise a rotated certificate would land in the
pod's filesystem and go unserved until something unrelated happened to restart the process.

**Why this matters more than it sounds.** The annotation previously digested everything rendered,
so any change replaced the pod — and the strategy is `Recreate` (`F-16`), so onboarding a single
UPS dropped every *other* device's `upsmon` sessions along with NUT's login accounting. That is the
damage `F-15` and `F-16` exist to prevent, arriving through the config path instead.

**NS-9 · The pod shares a process namespace.** `upsd -c reload` signals a running process located
through its PID file, and signalling across a container boundary needs `shareProcessNamespace:
true`.

Without it the reload fails in the worst available way. `upsd` is PID 1 in its own container, so the
PID file reads `1`; `upsd` refuses to signal PID 1 — `Ignoring invalid pid number 1` — and the
command still exits 0. A reload path built without the flag would look like it worked.

The isolation cost is small here in a way it would not be elsewhere: both containers run the same
image as the same non-root UID and are peers. This is not the node agent's split, where `F-57`
records a real trust boundary between a container that parses network responses and one holding
`CAP_SYS_BOOT`. The flag also gives the pod's pause container PID 1, which reaps the orphaned
drivers `upsd` never reaped (`F-76`).

### What the container boundary changes

The watchdog and `upsd` share `/run/nut` but not a PID namespace, and that asymmetry decides two
things. Both were confirmed by running the two containers against a shared volume rather than
reasoned about.

**`RUNNING` is namespace-local; `S_RESPONSIVE` is not.** Asked from the watchdog, `upsdrvctl status`
reports `RUNNING` as `N/A` for a perfectly healthy driver, because the PID from the PID file does
not resolve in the watchdog's namespace. `S_RESPONSIVE` stays accurate, because it comes from
probing the driver's socket and the socket is shared.

`NS-1` already declines to consult `RUNNING`, on the grounds that a driver can be running and not
answering. From the sidecar the argument is stronger and no longer optional: a watchdog keyed on
`RUNNING` would find every driver stopped on every pass and restart all of them, forever. The
correct field is correct for two independent reasons.

**A restarted driver lives in the watchdog's container.** `upsdrvctl start` spawns the driver where
it runs, so a driver the watchdog recovers is a child of the watchdog rather than of the entrypoint.
Telemetry is unaffected — NUT drivers and `upsd` communicate over the Unix socket in `/run/nut`,
which both containers mount, and `upsc` against `upsd` returns the device's status normally
afterwards.

The stale PID file that the recovered driver leaves behind is safe to act on across the boundary.
`upsdrvctl` announces "Terminating other driver!" when it finds one, but it verifies the process
before signalling: a PID file pointing at an unrelated process in the watchdog's namespace leaves
that process alive. This was tested directly, by pointing a driver's PID file at an innocent
`sleep` and confirming it survived.

## The admission surface and the image agree

The driver allowlist is pinned to the operand image from both ends. The container smoke test asserts
that every admitted driver is actually present in the image, and a Go test asserts that the
admission list and the image's driver list are the same set. Either check alone leaves the failure
open in one direction; together they make it impossible for admission to accept a `UPSDevice`
naming a driver the operand cannot run.

`spec.driverOptions` is the `ups.conf` escape hatch for anything the typed fields do not cover
(`OD-21`), and it cannot reach around the allowlist it sits behind. `driver` is a reserved key on
both the direct and `upstreamNUT` paths, so a device cannot pass admission declaring one driver and
then render another.

`verifyClientCertificates` is refused at admission rather than rendered. No released OpenSSL `upsd`
honors `CERTREQUEST`, so accepting the field would render configuration that silently does nothing —
a cluster believing it required client certificates while `upsd` asked for none. Refusing at
admission is the only way that belief cannot form. This is a consequence of the OpenSSL backend
decision (`OD-32`); it would be a different answer on an NSS build, which is exactly why the field is
refused rather than ignored.

## Related

- `F-17`, `F-46` — [nutserver-pod-audit.md](../audits/nutserver-pod-audit.md)
- `MINSUPPLIES` and the `MONITOR` power value on the agent side, which express the same
  "healthy while any member is healthy" shape for a host rather than for a server (`F-45`,
  [nut-usage-audit.md](../audits/nut-usage-audit.md))
