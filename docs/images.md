# Image Strategy

Components: NUT Server / upsd, Node Agent / DaemonSet, Operator Maturity & Hardening.

`nut-operator` does not default to a third-party NUT container image.

The release shape is project-owned OCI images built from pinned, verified inputs:

- `nut-server`: `upsd` plus network-capable NUT drivers required by the selected `UPSDevice` resources.
- `upsmon-agent`: unprivileged NUT client plus the project-owned `power-signal-writer` used by the
  `NodePowerAgent` DaemonSet.
- `node-actuator`: small host-action process. Stub mode runs without host privileges; approved
  real host shutdown uses the direct Linux poweroff syscall.
- `operator`: controller-manager image built from this repository.

## Rationale

The NUT project publishes source releases and distribution packages, not a single upstream, security-supported container image. Community images are useful for local experiments, but many are designed around direct USB UPS access and document privileged container usage. This operator intentionally targets network-reachable UPS devices, so production images do not require USB device mounts, host device access, or privileged mode.

The operand Dockerfiles package real Network UPS Tools binaries from pinned distribution packages:

- `nut-server` installs `nut`, including `upsd`, `upsdrvctl`, `upsc`, and network-capable drivers such as `dummy-ups`.
- `upsmon-agent` installs `nut`, including `upsmon` and `upsc`, and includes the Go
  `power-signal-writer` binary from this repository.

The operator validates `UPSDevice` drivers against a network-driver allowlist before rendering operands. The allowlist is `snmp-ups`, `netxml-ups`, `apcupsd-ups`, and `dummy-ups` for tests. Every entry is asserted present in the `nut-server` image by `make docker-smoke-nut-server`, so admission cannot accept a driver the operand cannot run.

## Build Requirements

Release images include:

- pinned NUT version and base image digest
- checksum and signature verification for NUT source inputs, or pinned distro package provenance
- OCI annotations such as `org.opencontainers.image.source`, `revision`, `version`, `licenses`, and `documentation`
- non-root runtime users
- image-level healthcheck instructions for container runtimes that honor OCI healthchecks
- read-only root filesystem compatibility
- `RuntimeDefault` seccomp compatibility
- dropped Linux capabilities by default
- multi-arch builds for `linux/amd64` and `linux/arm64`
- SBOM attestation
- SLSA provenance attestation
- Sigstore/cosign signatures
- immutable digest references in deployment examples

## Published Images

The `Images` GitHub Actions workflow builds the manager and operand images for
`linux/amd64` and `linux/arm64`, publishes GHCR manifest lists, emits
BuildKit SBOM/provenance attestations, and scans each pushed image for
high/critical vulnerabilities.

Project-owned images are:

- `ghcr.io/michaelzalud18/nut-operator:main`
- `ghcr.io/michaelzalud18/nut-operator:sha-<git-sha>`
- `ghcr.io/michaelzalud18/nut-server:main`
- `ghcr.io/michaelzalud18/nut-server:sha-<git-sha>`
- `ghcr.io/michaelzalud18/upsmon-agent:main`
- `ghcr.io/michaelzalud18/upsmon-agent:sha-<git-sha>`
- `ghcr.io/michaelzalud18/node-actuator:main`
- `ghcr.io/michaelzalud18/node-actuator:sha-<git-sha>`

Images include OCI source, documentation, license, revision, version, creation time, and vendor labels so GHCR can associate package metadata with this repository.

## Deployment Guidance

Local testing may use community NUT images as development scaffolding. They are not the recommended production baseline for this project.

Production deployments use immutable digests returned by the image workflow and keyless Sigstore signing as a release gate.

Local image smoke tests verify the expected NUT tooling is present:

```sh
make docker-smoke-operands
```

## References

- [NUT download information](https://networkupstools.org/historic/v2.8.5/download.html)
- [OCI image annotations](https://specs.opencontainers.org/image-spec/annotations/)
- [Docker SBOM attestations](https://docs.docker.com/build/metadata/attestations/sbom/)
- [Docker provenance attestations](https://docs.docker.com/build/metadata/attestations/slsa-provenance/)
- [Sigstore container signing](https://docs.sigstore.dev/cosign/signing/signing_with_containers/)
