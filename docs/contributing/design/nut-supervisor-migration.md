# NUT Supervisor Migration

Components: NUT Server / upsd.
Audience: contributors.

Detailed migration constraints and acceptance criteria transferred from the 2026-09-15 task
review. [ENG-1](../../tasks.md#nut-server--upsd) owns implementation and validation status.
The runtime contract remains in [the operand design](nut-server-operand.md) and
[the supervisor package](../../../internal/nutsupervisor/README.md).

## Decision Context

Preserve the existing singleton upsd plus stable sidecar boundary and NUT-owned driver semantics.
The change replaces shell process bookkeeping, not the operand architecture. Historical
F-144 completion is in [completed tasks](../../tasks-completed.md); F-97 remains a separate readiness
investigation. Adoption must preserve those lifecycle contracts and establish actual NUT parity.

## Compatibility Decisions

The [official stable downloads](https://networkupstools.org/download.html) still listed 2.8.5 on
2026-09-15, matching the pinned image. No source version, checksum, or signature gate was changed.

Real-image verification exposed a driver-identity edge case: `upsdrvctl -c reload-or-exit <name>`
constructs the PID filename using the new `driver=` value, so it cannot signal the old process.
The [tagged controller source](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/upsdrvctl.c)
explains that lookup. Keep the named command first; on failure, an active supervisor can deliver
the same SIGUSR1 to its owned foreground leader. The
[tagged driver source](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/main.c)
defines that signal as reload-or-exit. NUT still decides whether changed options require exit.
Keep the revision unconfirmed after fallback and retry normally. Image tests assert the owned
leader/driver PID relationship, changed-driver retirement, and preservation of unrelated workers.
This replaces neither NUT's parser nor its reload policy.

Use observable driver state to confirm reload, rather than merely checking the control command's
exit status. The image test uses `driver.debug` for live reload and new fixture data for port
replacement. The published `driver.parameter.pollinterval` is initialized at startup and is not a
reliable live-reload acknowledgment in this release.

## Migration Constraints and Acceptance

**Upstream gate:** recheck official NUT releases and source downloads when changing the pinned
version. Select a stable release, never an unreleased snapshot just because it is newer.
If a newer stable exists, update `NUT_VERSION`, tarball checksum, signing-key verification, image
assertions, and version-sensitive tests in a clearly separated change. Preserve both source-tarball
checksum and upstream-signature verification. Prove that release's `upsdrvctl list`,
`upsdrvctl -FF start <ups>`, `upsdrvctl -c reload-or-exit <ups>`, `upsdrvctl status`, and
`upsd -c reload` behavior; record differences rather than emulating an older NUT in project code.

**Pod and binary:** keep replicas at one and a stable container list. Adding/removing UPSDevice
must not restart upsd or change the pod shape just to update driver membership. Preserve
`shareProcessNamespace`: cross-container `upsd -c reload` still uses its PID file. Add a dedicated
binary such as `cmd/nut-driver-supervisor`, reusable logic in `internal/nutsupervisor`, and the same
pinned Go builder/static-binary pattern as other project operand helpers. Ship it in nut-server;
render only its direct invocation and bounded runtime configuration. The server entrypoint should
validate configuration and exec `upsd -FF`. No systemd, s6, runit, network service, queue, or new CRD
for this replacement. NUT owns enumeration, driver semantics, and driver-control commands.

**State and concurrency:** one serialized event/reconciliation loop owns membership, config
reloads, child exits, and shutdown. An in-memory UPS-name map owns each foreground upsdrvctl
process, termination/cancellation state, and last exit result. Use `exec.Cmd.Wait`, not `kill -0`
polling, and do not recreate shell `.pid`, `.exit`, or per-device `.digest` bookkeeping. Unexpected
exit reports UPS name and outcome and retries no faster than the existing fixed interval: no tight
respawn loop or added exponential delay during an outage.

**Projected configuration:** bounded periodic whole-file comparison handles atomic projected
ConfigMap/Secret replacement without requiring fsnotify. Standard-library SHA-256 is change
detection, not a security boundary. Zero-byte rendered `ups.conf` means intentional zero devices;
nonempty files must enumerate through NUT. Failed enumeration retains workers, last good server
reload state, and the unapplied new digest for retry; error-string matching must not turn malformed
input into an empty device set. Obtain a valid desired set before changing membership or reloading.
Remove owned workers; add new ones only after corresponding server configuration is accepted.
Unrelated adds/removes/edits must not restart surviving drivers.

**Reload semantics:** for surviving drivers on a valid ups.conf change, use NUT's
`reload-or-exit` decision rather than parsing individual sections or hashing per-driver config.
If NUT requires exit, observe the foreground worker exit and restart only that UPS. An
`upsd.users`-only change reloads upsd without touching drivers. Failed server reload is retried
without claiming adoption. Preserve validation/ordering that protects a working device set.
The owned-process fallback above handles driver-name changes that the named command cannot reach.
Listener/port, TLS certificate, and client-CA changes remain controller-owned pod replacements;
do not widen reload support beyond what the actual NUT release proves.

**Cleanup and security:** own the full process lifecycle using Linux process groups appropriate
to the operand. TERM, wait the existing bounded grace period, KILL remaining owned processes,
and Wait/reap; no orphaned upsdrvctl children or drivers. Start all shutdown grace periods together,
not one full period per UPS. Bound one-shot NUT control commands independently. Verify whether the
old best-effort named `upsdrvctl stop` after terminating an owned worker provides necessary cleanup;
real-process and image cleanup tests established that owned-group termination suffices, so the
named stop is removed. Keep non-root, read-only root filesystem, zero added
capabilities, RuntimeDefault seccomp, and existing config/credential/runtime mounts. No Kubernetes
token/RBAC, host namespace, hostPath, or network listener. The container remains the final cleanup
boundary; userspace deadlines cannot solve kernel uninterruptible I/O.

**Testable now; Conditional:** preserve or replace every meaningful F-144 regression. Deterministic
Go tests with fake NUT commands must cover isolated crashes and fixed restart cadence; unaffected
worker PIDs across add/remove; live reload with unchanged PID; NUT-requested restart of only the
affected driver; malformed/non-enumerable input retaining working workers/server state; empty
configuration converging to zero; users-only reload; failed reload retry; canceled/stalled commands;
owned-child reaping; TERM-ignoring workers; and shared rather than serial multi-worker grace periods.
Keep real dummy-ups/upsd/authenticated-secondary-upsmon image tests against the exact image/version
to ship: idle startup, mixed healthy/failing drivers, add/remove, crashes, reloads, unaffected
worker and driver PIDs, and no residual workers after termination. Run affected Go tests with the
race detector and retain the image workflow smoke gate. Renderer tests must prove direct execution
of the shipped binary; image assertions prove it exists and is executable. Shell source-text checks
can go only after equivalent behavioral coverage exists.

**Migration order:** verify stable NUT and any separate version update; build Go logic/tests while
shell remains a behavioral reference; compare real-NUT image contracts; switch rendered command
and run controller/image/Kind component/race coverage; then remove `supervisor.sh`, embed wrapper,
shell-only state and injection code. Update operand, image, contributor, and supervisor docs to one
implementation, retaining historical audit evidence as historical. Capture before/after
`docker-stress-nut-readiness` results for F-97; changed reproduction frequency is evidence, not a
root-cause conclusion or permission to weaken readiness.
