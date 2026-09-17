# NUT Server Operand

Status: design. Covers the `upsd` operand the `NUTServer` CRD renders — how it reports health, how it
starts, and which of NUT's own mechanisms it delegates to.

Components: NUT Server / upsd.
Audience: contributors.

`NS-n` identifiers are stable and are not reused or renumbered.

## Health reporting

**NS-1 · Readiness requires an actual reply from at least one configured driver.** The
`nut-driver-ready` helper asks NUT to enumerate configured devices, then queries their status
concurrently under one four-second deadline. NUT remains responsible for configuration parsing,
the current driver/socket identity, and its driver protocol. A stalled first device cannot
consume the whole budget before a healthy neighbor is checked.

The operand carries narrow patches to pinned NUT: `upsdrvquery_prepare` rejects a PING timeout,
and PONG recognition preserves complete-line framing across reads. This is necessary even with
an outer deadline; a timeout or malformed reply is not evidence of a valid handshake.
The source patches are explicit in the Docker build and must be reassessed when upgrading NUT.
[Investigation and upstream trace](../audits/nut-readiness-investigation-2026-09-17.md).

Readiness checks driver responsiveness, not whether the UPS itself is reachable or its data is
fresh. Those remain per-device telemetry concerns. Process existence, cached `upsc` values, and
an open socket alone do not satisfy readiness.

**Aggregate ready, per-member visible.** One responsive driver is enough. A server configured with
several devices stays ready while any one of them answers, and each device's individual state is
still readable from NUT and `UPSDevice` status. Marking the pod unready because one of four
devices is unreachable would take telemetry for the other three away from every agent, which is a
worse outcome than reporting a degraded set — and device-level health already surfaces on
`UPSDevice` status, which is where a per-device consumer should read it.

**NS-2 · The responsive check is exact and fail-closed.** Accept only a successful bounded NUT
query with the expected device name, exact responsive column, and positive socket-reported PID,
never a substring elsewhere in the output or a PID file alone. Empty configuration, enumeration
failure, missing sockets, and all-unresponsive devices fail. Command output and work are bounded;
cancellation closes and reaps owned queries.
The helper does not restart or signal drivers.

The helper rejects more than 64 configured devices rather than launching an unbounded process
set or serializing stalled probes ahead of healthy ones. This is a readiness safety ceiling, not
a capacity guarantee: size operand resources for the inventory and split larger inventories among
NUTServer instances. Enumeration and each query also have bounded captured output.

**NS-3 · The Docker `HEALTHCHECK` runs the same command as the probe.** Kubernetes ignores the
`HEALTHCHECK` directive, so under this operator the readiness probe is what actually runs. The image
is still runnable directly, though, and there it should answer the same question the same way rather
than drift into a second definition of healthy. Both execute `nut-driver-ready`, with a five-second
outer timeout surrounding the helper's four-second budget. A parity test guards the two entry points.

Liveness is left to the process model rather than a probe: the entrypoint `exec`s `upsd`, so if
`upsd` exits the container exits and Kubernetes restarts it.

### Why not a bespoke `upsc` loop

`upsc -l` lists configured names even without a connected driver, and `upsc` can return cached
values while a driver is frozen. The helper therefore consumes NUT's driver-side response rather
than reconstructing health from client values. A source fix plus a bounded CLI wrapper keeps
configuration and protocol ownership upstream, without adding a second configuration parser or
a new management service.

## Startup

**NS-4 · Drivers are not part of `upsd` startup.** The entrypoint validates mounted configuration
and `exec`s `upsd`; driver processes are owned by the `driver-supervisor` sidecar. A device with bad
credentials or an unreachable endpoint leaves the server process alive, so credentials can be
corrected through the reload path instead of by restarting `upsd`.

The readiness probe is what makes this safe to do: a server with no responsive driver reports
NotReady, while a mixed server can still publish the devices whose drivers are connected.

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

**NS-6 · A sidecar supervises one foreground worker per driver.** A driver that dies leaves `upsd`
alive, so the server container is not restarted. Readiness reports the fault accurately, but
readiness is a signal, not an actor. The `driver-supervisor` sidecar owns the action.

It runs the operand image, shares `/run/nut` and `/etc/nut` with `upsd`, enumerates configured
devices with `upsdrvctl list`, and starts each one as its own `upsdrvctl -FF start <ups>` worker.
If a worker exits, only that UPS is restarted. A bad driver definition therefore does not tear down
the healthy workers beside it.

The container executes `/usr/local/bin/nut-driver-supervisor` directly. Its Go implementation owns
process groups, bounded concurrent termination, and reload requests; NUT owns driver-option
interpretation and restart decisions. The [package contract](../../../internal/nutsupervisor/README.md)
describes configuration rollback handling and the owned-process reload fallback for driver-name
changes. Supervisor bookkeeping is in memory; NUT's own sockets and PID files remain in `/run/nut`.

### Why a sidecar rather than a container per driver

Upstream supervises one service unit per driver (`upsdrvsvcctl`, and nut-driver-enumerator since
2.8.0), and the direct Kubernetes translation is a container per driver with kubelet as the service
manager. That would give per-driver restart backoff and per-driver logs for free.

It was declined because it makes the container list a function of the device set. Adding or removing
a `UPSDevice` would change the pod's containers, which is a pod recreate — dropping every `upsmon`
session and NUT's login accounting, which is the damage `F-15` and `F-16` exist to prevent and which
the reload path in `F-48` is being built to eliminate. One supervisor for all drivers keeps the
pod's shape independent of how many devices a server serves, while one foreground worker per UPS
keeps failure isolation close to the upstream service-instance design.

A liveness probe was the other candidate and is worse on both counts: it restarts `upsd` along with
the drivers, and it cannot fire at all while any one driver still answers — which is the common
case, since a server with four devices losing one still reports ready.

### What the supervisor relies on, and how it is known

Each of these was established by running the operand image, because each one decides part of the
implementation:

- `upsdrvctl -FF start <ups>` keeps a single driver's worker in the foreground and writes the PID
  file NUT's own tooling expects.
- `upsdrvctl -FF start` with no UPS name is the wrong bundle shape for this operand: if any
  configured driver fails, the foreground controller exits the whole bundle and stops the healthy
  workers too.
- Running one foreground worker per UPS isolates the failure. A missing `dummy-ups` definition exits
  that worker, while a neighboring `dummy-ups` worker stays `RESPONSIVE`.
- `upsdrvctl list` returns exit 1 with "no UPS definitions found in ups.conf" for an empty
  selection. That is an idle state for this operator, not a crash condition.

The container carries no probes. A readiness probe would gate the pod's endpoint membership on the
supervisor rather than on the server, and a liveness probe would let a supervisor restart take the
server down with it. Keeping them apart is the reason it is a separate container.

The supervisor owns driver startup and waits for an accepted upsd reload before adding workers.
If the server has not created its PID file yet, the next reconciliation retries. The loop interval
also controls exited-worker recovery and projected-volume configuration checks.

## Configuration changes

**NS-8 · Adding a device reloads `upsd`; changing where it listens replaces the pod.** The
pod-template annotation carries a digest of only the configuration `upsd` cannot adopt at runtime.
Everything else reaches a running server through `upsd -c reload`, issued by the supervisor when it
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

The supervisor and `upsd` share `/run/nut`, so drivers and server communicate over the Unix sockets
NUT already uses. They also share the pod process namespace because `upsd -c reload` needs to signal
the running server across the container boundary. Driver workers are children of the supervisor
sidecar, which is deliberate: that is the container whose only job is driver process ownership.

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
