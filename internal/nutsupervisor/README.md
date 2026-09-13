# NUT Driver Supervision

This package owns the driver supervisor independently of Kubernetes rendering.
`supervisor.sh` is embedded unchanged in the manager; the controller places those bytes in the
existing driver-supervisor container command. The NUT operand image supplies the actual NUT
binaries. There is no second generated copy of the script and no new runtime service.

## Runtime Contract

- Enumerate devices through `upsdrvctl list` and run each through `upsdrvctl -FF start <name>`.
- Keep healthy, unchanged workers running across unrelated additions, removals, and crashes.
- Restart a worker when its own configuration changes; retry failed reloads without claiming
  the new configuration was adopted.
- Reload `upsd` when reloadable configuration changes. Listener and certificate changes remain
  controller-owned pod replacements, not supervisor reloads.
- Handle no configured devices as an idle state. Preserve existing workers when listing fails.
- The supervisor owns its state directory and must never share it with another instance.
  Termination stops owned workers; the operand's container boundary remains the final process
  cleanup boundary.

Runtime configuration is explicit:

| Variable | Default | Owner |
| --- | --- | --- |
| `NUT_CONFPATH` | `/etc/nut` | NUT and supervisor configuration directory |
| `NUT_SUPERVISOR_STATE_DIR` | `/run/nut/driver-supervisor` | Private supervisor bookkeeping |
| `NUT_SUPERVISOR_INTERVAL_SECONDS` | `5` | Positive integer reconciliation interval |

The default interval preserves existing recovery cadence. Production does not tune this through
the CRD. Component tests use isolated paths and a shorter interval through this same contract;
they execute the exact script bytes, not a source-rewritten variant.

## Upstream Boundary

NUT provides named driver startup, foreground operation, and configuration enumeration through
[upsdrvctl](https://networkupstools.org/docs/man/upsdrvctl.html). Keep that hardware-specific
behavior upstream. Its
[service-instance controller](https://github.com/networkupstools/nut/blob/master/docs/man/upsdrvsvcctl.txt)
targets systemd and Solaris SMF, rather than providing a drop-in supervisor for this operand.
Adding a host service manager inside the pod is not implied by adopting its per-device model.

A Kubernetes container per UPS would couple device membership to pod replacement. The existing
stable sidecar avoids that coupling. Generic supervisors such as s6/runit could replace child
process lifecycle handling, but do not alone resolve configuration enumeration, per-device change
detection, or `upsd` reload coordination. Evaluate those costs and production-image behavior before
choosing a replacement; extracting this package does not settle that broader redesign.

## Tests

`go test -race ./internal/nutsupervisor` runs real process trees with fake NUT commands and needs
no API server, cluster, UPS, or Docker daemon. Controller tests separately verify the rendered
command and security/resource configuration. Operand-image and Kind tests establish actual NUT
binary behavior; process fixtures alone do not establish image compatibility or readiness timing.

`make docker-smoke-nut-supervisor NUT_SERVER_IMG=<image>` runs the same supervisor bytes with
actual NUT binaries from the selected operand image. It verifies idle startup, a failing driver
alongside a healthy one, failed reload retries and recovery, add/remove reloads, preservation of both worker and driver PIDs, repeated
driver-crash recovery, and graceful termination without remaining NUT workers. It uses dummy UPS
data, a non-root read-only container, private temporary filesystems, no capabilities, and no
external network. It requires neither Kubernetes nor physical equipment. The outer harness bounds
the run and removes its owned container on failure or cancellation.

The existing image workflow runs this check immediately after building its native NUT server
image; `docker-smoke-nut-server` includes it too. This complements, rather than replaces, the
process-fixture tests for deterministic failure injection and Kind's actual sidecar wiring.
