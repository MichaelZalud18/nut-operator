# Project Images

This directory contains project-owned image definitions for NUT Operator.

Current images:

- `nut-operator`: controller-manager image built from the root `Dockerfile`.
- `nut-server`: Network UPS Tools `upsd` plus network-capable drivers from Debian packages.
- `upsmon-agent`: unprivileged Network UPS Tools `upsmon` client from Debian packages.
- `node-actuator`: fail-closed stub actuator. It does not perform real host shutdown.

The NUT operand images are interim project-owned development images. They use distribution packages rather than source-built, signature-verified NUT tarballs. Production release images still need pinned base digests, SBOMs, provenance attestations, signatures, and dependency vulnerability remediation.
