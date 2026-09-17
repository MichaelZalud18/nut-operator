# Project Images

This directory contains project-owned image definitions for NUT Operator.

Image definitions:

- `nut-operator`: controller-manager image built from the root `Dockerfile`.
- `nut-server`: source-built Network UPS Tools `upsd` and network-capable drivers, using OpenSSL,
  plus the Go `nut-driver-supervisor` sidecar command.
- `upsmon-agent`: source-built NUT `upsmon` client and project-owned signal/notification writers.
- `node-actuator`: simulation, approved Linux power-off, and Talos shutdown implementations.

The Dockerfiles own source pins, signature checks, runtime dependencies, and build controls.
See [Image Strategy](../docs/reference/images.md) for the publication and verification contract,
and the [actuation guide](../docs/guides/enable-actuation.md) for the approval and privilege
boundaries. The actuator is capable of real shutdown; simulation is the default, not its only mode.

## NUT Server Runtime Tools

The server image retains `upsd`, `upsdrvctl`, and the read-only query client `upsc`.
It also retains `upsmon` for the isolated authenticated-client startup and stress checks.
The operator uses `nut-driver-supervisor` for worker ownership and `nut-driver-ready` for readiness.

The upstream install tree is filtered before it enters the runtime image. `nutconf`, `nut-scanner`,
`upslog`, `upssched`, `upssched-cmd`, `upscmd`, and `upsrw` are excluded: configuration, inventory,
telemetry, sequencing, and actuation belong to the operator and its declared APIs. The unused
C++ client and scanner libraries are excluded too; no C++ runtime is added for these tools.
The compiled driver set, admission allowlist, and OpenSSL backend are unchanged by this filtering.

`hack/smoke-image.sh docker nut-server <image>` verifies the retained tools execute, excluded tools
and libraries are absent, and all shipped drivers resolve their runtime libraries. The packaging
check runs with the image's non-root user, a read-only filesystem, no network, and no capabilities.
