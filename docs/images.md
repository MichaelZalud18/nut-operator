# Image Strategy

Components: NUT Server / upsd, Node Agent / DaemonSet, Operator Maturity & Hardening.

`nut-operator` does not default to a third-party NUT container image.

The release shape is project-owned OCI images built from this repository and explicit upstream
inputs. NUT source releases are version-pinned and sha256-verified. Published non-PR images are
keylessly signed with Sigstore/cosign after vulnerability scanning. Base-image digest pinning and
detached NUT source signature verification remain open release hardening work.

- `nut-server`: `upsd` plus network-capable NUT drivers required by the selected `UPSDevice` resources.
- `upsmon-agent`: unprivileged NUT client plus the project-owned `power-signal-writer` used by the
  `NodePowerAgent` DaemonSet.
- `node-actuator`: small host-action process. Stub mode runs without host privileges; approved
  real host shutdown uses the direct Linux poweroff syscall.
- `operator`: controller-manager image built from this repository.

## Rationale

The NUT project publishes source releases and distribution packages, not a single upstream, security-supported container image. Community images are useful for local experiments, but many are designed around direct USB UPS access and document privileged container usage. This operator intentionally targets network-reachable UPS devices, so production images do not require USB device mounts, host device access, or privileged mode.

The operand Dockerfiles build real Network UPS Tools binaries from source in dedicated builder
stages:

- `nut-server` builds NUT, verifies the source tarball sha256, asserts the shipped `upsd` version,
  and asserts OpenSSL linkage. Its runtime image copies the built NUT tree and includes `upsd`,
  `upsdrvctl`, `upsc`, and the allowed network-capable drivers.
- `upsmon-agent` builds NUT the same way and includes `upsmon`, `upsc`, and the Go
  `power-signal-writer` binary from this repository.

The operator validates `UPSDevice` drivers against a network-driver allowlist before rendering operands. The allowlist is `snmp-ups`, `netxml-ups`, `apcupsd-ups`, and `dummy-ups` for tests. Every entry is asserted present in the `nut-server` image by `make docker-smoke-nut-server`, so admission cannot accept a driver the operand cannot run.

## Current Build Controls

Release images currently include:

- pinned NUT version and sha256 verification for NUT source inputs
- OCI annotations such as `org.opencontainers.image.source`, `revision`, `version`, `licenses`, and `documentation`
- non-root runtime users
- image-level healthcheck instructions for directly runnable images with in-container health checks
  (`nut-server`, `upsmon-agent`, and `node-actuator`)
- Kubernetes liveness/readiness probes for the manager image
- read-only root filesystem compatibility
- `RuntimeDefault` seccomp compatibility
- dropped Linux capabilities by default
- multi-arch builds for `linux/amd64` and `linux/arm64`
- SBOM attestation
- SLSA provenance attestation
- vulnerability scanning before published images are accepted
- keyless Sigstore/cosign signatures for published non-PR images

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

Production deployments should pin immutable digests returned by the image workflow and verify the
image signature before rollout. Base-image digest pinning, detached NUT source signature
verification, and a publish gate that promotes the exact image digest exercised by e2e are tracked as
release-hardening work rather than current guarantees.

Verify a published digest with cosign:

```sh
cosign verify \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/MichaelZalud18/nut-operator/\.github/workflows/images\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+)$' \
  ghcr.io/michaelzalud18/nut-operator@sha256:<digest>
```

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
