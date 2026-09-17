# NUT Driver Supervision

This package owns the NUT driver lifecycle independently of Kubernetes rendering.
`cmd/nut-driver-supervisor` builds the binary shipped in the NUT operand image. The controller
invokes it directly in the existing sidecar; there is no embedded shell, network service, or new CRD.

## Runtime Contract

- Enumerate devices through `upsdrvctl list` and run each through `upsdrvctl -FF start <name>`.
- One serialized loop owns membership, configuration changes, observed exits, and shutdown.
  Foreground children are collected with `exec.Cmd.Wait`; no supervisor PID, exit, or digest files.
- Preserve healthy workers across unrelated additions, removals, and failures. Restart failed
  workers at a fixed reconciliation cadence, without a tight loop or exponential backoff.
- Compare complete `ups.conf` and `upsd.users` files using SHA-256. This detects projected-volume
  replacement without introducing another NUT configuration parser or filesystem watcher.
- Only zero-byte `ups.conf` means an intentionally empty device set. Failed enumeration preserves
  workers and leaves the revision pending. NUT error text does not turn malformed input into empty.
- Reload upsd before starting newly configured drivers. Failed or uncertain reloads remain pending,
  including configuration rollback while a command runs. Users-only changes never reload drivers.
- NUT decides which surviving drivers can reload and which must exit. Listener, port, TLS, and
  client-CA changes to upsd still require controller-owned pod replacement.

Runtime configuration:

| Variable | Default | Contract |
| --- | --- | --- |
| `NUT_CONFPATH` | `/etc/nut` | Shared NUT configuration directory |
| `NUT_SUPERVISOR_INTERVAL_SECONDS` | `5` | Positive integer reconciliation interval |

The interval is not a CRD tuning surface. The command rejects invalid or overflowing durations
before starting children. `--version` reports the image build version.

## Upstream Reload Boundary

Use `upsdrvctl -c reload-or-exit <name>` for configuration changes. The driver decides whether a
setting is reloadable; the supervisor never hashes individual sections or translates driver options.

When `driver=` changes, upsdrvctl searches for the PID file associated with the **new** driver name.
That cannot locate the old worker. If the command fails while the run is still active, signal the
owned foreground leader with NUT's `SIGUSR1` reload-or-exit signal. Single-device `-FF` executes
the driver as that leader; image tests assert the relationship. NUT then applies its own driver-name
and option checks. This fallback retains an unconfirmed revision for retry and never signals a PID
read from arbitrary replacement configuration. See the
[migration record](../../docs/contributing/design/nut-supervisor-migration.md) for upstream sources.

Command success means a reload request was accepted, not transactional acknowledgment of all
settings. Readiness and telemetry remain independent observations of actual NUT behavior.

## Cleanup and Isolation

Each worker and one-shot command gets an owned Linux process group. Shutdown sends TERM to all
workers together, waits up to five seconds, then kills survivors and joins them. Cancellation during
device removal includes still-terminating workers and immediately starts survivor shutdown.
Enumeration and reload commands have separate five-second deadlines and are killed and joined on
cancellation. No best-effort named stop is needed after an owned process group has been collected.

An exited leader remains waitable until group cleanup, preventing PID/group reuse before the final
signal. The supervisor then calls `Wait`. Descendants remaining in the group are killed even when
the leader exits first. The container init reaps orphaned descendants; the container remains the
final boundary for processes that escape their group. Userspace cannot impose a completion deadline
on kernel uninterruptible I/O.

The sidecar retains its non-root, read-only, capability-free security context and existing mounts.
It has no Kubernetes API client, token, host namespace, hostPath, or network listener. Shared pod
process visibility remains necessary for the separate upsd container's PID-based reload command.

Upstream `upsdrvsvcctl` targets systemd/SMF. s6 or runit would still need this dynamic NUT membership
and reload adapter. A container per UPS would replace the pod when membership changes. Keep the
stable sidecar and NUT-owned driver semantics rather than adding another service manager.

## Tests

`go test -race ./internal/nutsupervisor ./cmd/nut-driver-supervisor` uses real subprocesses and fake
NUT commands for deterministic faults. It covers repeated membership changes, isolated crashes,
fixed retry bounds, empty/malformed configuration, users-only changes, reload failure and rollback,
NUT-requested restart, cancellation during removal, concurrent termination, child reaping, and
descendant cleanup. These tests require no cluster or physical UPS.

`make docker-smoke-nut-supervisor NUT_SERVER_IMG=<image>` executes the **shipped binary** with real
NUT. It checks idle startup, mixed healthy/failing drivers, failed reload retries, malformed input,
unaffected worker/driver PIDs, live debug-level reload, port-driven restart, driver-name replacement,
repeated crash recovery, and termination of a STOP-frozen driver. The non-root read-only container
has no external network or added capabilities. The owning harness bounds the run and cleans up its
container on failure or cancellation. The image workflow runs this same test after building NUT.

`make docker-stress-nut-readiness NUT_SERVER_IMG=<image>` additionally compares 60 fresh driver probes
with four concurrent `upsc` reads per sample while authenticated secondary upsmon runs. Every miss
and the overall 180-second timeout fail the run. The fixture confirms its final port replacement
has converged before sampling. A pass does not close F-97 or establish hardware/Kind compatibility.

The opt-in `NUT_READINESS_DIAGNOSTICS=1 make docker-smoke-nut-supervisor NUT_SERVER_IMG=<image>`
control pauses only the fixture's dummy driver, demonstrates cached upsd reads, checks a bounded
probe, records upstream classification, and verifies same-PID recovery. It is diagnostic evidence,
not a production-readiness pass; see the [F-97 investigation](../../docs/contributing/audits/nut-readiness-investigation-2026-09-17.md).

Controller tests own the rendered command, mounts, resources, and security context. The existing
Kind recovery and telemetry scenarios own Kubernetes integration coverage. Implementation and
remaining validation are tracked in [tasks](../../docs/tasks.md#nut-server--upsd).
