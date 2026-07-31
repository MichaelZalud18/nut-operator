# nut-operator

Kubernetes-native power management built around Network UPS Tools (NUT), controller-runtime, and declarative APIs.

This project is intentionally designed as a reusable operator rather than a homelab-only manifest set. It models UPS devices, NUT server instances, node power agents, and shutdown flows as Kubernetes resources. Durable audit and execution state belongs in PostgreSQL, with CloudNativePG as the preferred Kubernetes-native backing store.

## Goals

- Manage multiple NUT server instances from one operator.
- Support many network-reachable UPS devices and physical power domains.
- Keep UPS telemetry, policy compilation, and node actuation as separate concerns.
- Default to dry-run and status-visible compiled plans before allowing any host shutdown.
- Use Kubernetes-native security controls: least-privilege RBAC, status subresources, conditions, NetworkPolicy-ready operands, read-only roots, seccomp, and explicit host-actuator isolation.
- Keep long-lived audit, telemetry, and flow execution state out of CR status and in PostgreSQL.

## API Shape

The API group is `power.zalud.io/v1alpha1`.

- `PowerManagementCluster` configures global defaults, operand namespace, images, security posture, observability, and PostgreSQL/CNPG storage.
- `UPSDevice` describes a physical or simulated network-reachable UPS, its NUT driver, endpoint, credentials, power domains, thresholds, and telemetry behavior. Local USB and serial UPS modes are intentionally unsupported. The initial driver allowlist is `snmp-ups`, `netxml-ups`, `powerman-pdu`, `apcupsd-ups`, and `dummy-ups` for tests.
- `NUTServer` renders and operates one logical `upsd` server instance for selected UPS devices.
- `NodePowerAgent` manages the per-node monitoring and actuation DaemonSet. It separates `MonitorOnly`, `DryRun`, and `Actuate` modes.
- `ShutdownFlow` defines dependency-graph shutdown policy compiled into ordered waves. Enforced flows require an explicit approval annotation.

## Safety Model

Real host shutdown is not the default.

`NodePowerAgent` defaults to dry-run behavior through `mode: DryRun` and `shutdown.actuatorPolicy: Stub`. Rendering `SystemdPoweroff` requires both:

- `spec.mode: Actuate`
- `metadata.annotations[spec.shutdown.approvalAnnotation] == "true"`

`ShutdownFlow` follows the same pattern for `mode: Enforce`. This keeps dangerous behavior reviewable in Git and visible in `/status` before it can affect nodes.

The intended node-agent pod split is:

- `upsmon` container: unprivileged NUT client, no Kubernetes API credentials required for ordinary monitoring.
- `actuator` container: omitted or stubbed by default; when future host shutdown is enabled, it becomes the isolated host-action boundary using only the minimum host access that the target runtime proves necessary.

## Storage

The default durable storage mode is CNPG:

```yaml
spec:
  storage:
    mode: CNPG
    cnpg:
      clusterRef:
        namespace: power-system
        name: power-audit
      database: power
      schema: power
```

External PostgreSQL is also modeled for non-CNPG clusters. `Disabled` exists for local development only.

## Current Implementation

Implemented now:

- Kubebuilder/controller-runtime scaffold.
- Five cluster-scoped CRDs with status subresources.
- Validation/status reconcilers for all five resources.
- Dependency-graph validation and compiled-wave status for `ShutdownFlow`.
- `NUTServer` operand rendering for Namespace, ConfigMap, operator-managed Secret, Service, NetworkPolicy, and Deployment.
- `NodePowerAgent` operand rendering for Namespace, ServiceAccount, ConfigMap, Secret-backed `upsmon.conf`, egress NetworkPolicy, and non-privileged DaemonSet in monitor/dry-run/stub modes.
- Manager image Docker build and basic entrypoint smoke test.
- Production-shaped sample resources under `config/samples/`.

Not implemented yet:

- Advanced NUT config rendering, credential rotation, and production operand image publication.
- PostgreSQL schema migrations and audit writer.
- Admission webhooks.
- Full image smoke tests, SBOMs, signing, scanning, and registry publication.

## Development

Use a writable Go build cache in restricted shells:

```sh
GOCACHE=/tmp/go-build-cache make generate
GOCACHE=/tmp/go-build-cache make manifests
GOCACHE=/tmp/go-build-cache go test ./api/... ./internal/controller -run TestNonExistent
```

Full controller tests use Kubebuilder envtest assets:

```sh
GOCACHE=/tmp/go-build-cache make setup-envtest
GOCACHE=/tmp/go-build-cache make test
```

Build and push the manager image:

```sh
make docker-build docker-push IMG=<registry>/nut-operator:<tag>
```

Install CRDs:

```sh
make install
```

Deploy the controller:

```sh
make deploy IMG=<registry>/nut-operator:<tag>
```

Apply sample resources:

```sh
kubectl apply -k config/samples/
```

## Documentation

- [Architecture](docs/architecture.md)
- [Security](docs/security.md)
- [Image strategy](docs/images.md)
- [Shutdown flow design](docs/shutdown-flow.md)
- [Orion cluster example](docs/examples/orion-cluster/README.md)

## Community and Project Info

- [Contributing](CONTRIBUTING.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Security Policy](SECURITY.md)
- [Support](SUPPORT.md)
- [Governance](GOVERNANCE.md)
- [Maintainers](MAINTAINERS.md)

## License

Copyright 2026 Michael Zalud.

Licensed under the [Apache License, Version 2.0](LICENSE).
