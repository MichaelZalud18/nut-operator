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

### Supervision Choice

Keep the stable sidecar and upstream named `upsdrvctl -FF` workers for v1. The project-owned layer
reconciles dynamic membership and configuration; it does not implement driver protocols. This
decision preserves the existing operand interface and the PID-preservation contract.

| Alternative | Reuse | Remaining integration cost |
| --- | --- | --- |
| NUT service instances | Upstream enumeration and host service lifecycle | Requires systemd/SMF inside this container model |
| [s6-supervise](https://skarnet.org/software/s6/s6-supervise.html) | Per-service restart, state, and control | Generate/remove service directories and coordinate NUT config/reloads |
| [runit runsv](https://smarden.org/runit/runsv.8) | Per-service restart, state, and control | Same membership/configuration adapter, plus new image dependency |
| One Kubernetes container per UPS | Kubelet process supervision | Membership changes replace the pod and interrupt unchanged drivers |

s6/runit are credible replacements for child lifecycle handling, not replacements for the whole
reconciler. Adding either now would introduce another service configuration representation while
retaining the NUT-specific adapter. Revisit if lifecycle requirements outgrow the current small
wrapper; do not add a host init system or a network service just for enumeration. This is a scoped
design choice, not a claim that custom process management is generally preferable.

Enumeration must succeed before a reload reaches `upsd`. NUT can report "no UPS definitions" for
a malformed header as well as for an empty file. Only the renderer's zero-byte configuration is
accepted as intentional removal of all devices; other failed enumerations retain the working
server configuration and workers. This is conservative validation of the renderer's output
contract, not a second NUT parser. Successful enumeration is not full driver-option validation.

## Tests

`go test -race ./internal/nutsupervisor` runs real process trees with fake NUT commands and needs
no API server, cluster, UPS, or Docker daemon. Controller tests separately verify the rendered
command and security/resource configuration. Operand-image and Kind tests establish actual NUT
binary behavior; process fixtures alone do not establish image compatibility or readiness timing.

`make docker-smoke-nut-supervisor NUT_SERVER_IMG=<image>` runs the same supervisor bytes with
actual NUT binaries from the selected operand image. It verifies idle startup, a failing driver
alongside a healthy one, malformed configuration, failed reload retries and recovery, add/remove
reloads, preservation of both worker and driver PIDs, repeated
driver-crash recovery, and graceful termination without remaining NUT workers. It uses dummy UPS
data, a non-root read-only container, private temporary filesystems, no capabilities, and no
external network. It requires neither Kubernetes nor physical equipment. The outer harness bounds
the run and removes its owned container on failure or cancellation.

The existing image workflow runs this check immediately after building its native NUT server
image; `docker-smoke-nut-server` includes it too. This complements, rather than replaces, the
process-fixture tests for deterministic failure injection and Kind's actual sidecar wiring.
