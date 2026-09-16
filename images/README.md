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
