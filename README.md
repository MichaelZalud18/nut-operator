# nut-operator

Kubernetes-native power management built around Network UPS Tools (NUT), controller-runtime, and declarative APIs.

> Status: early alpha and mostly AI-assisted/vibe-coded. Treat this repository as a design-heavy working prototype until the published images, RBAC, dependency, and shutdown-actuation paths have passed independent review and production-grade validation.

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
- `UPSDevice` describes a physical or simulated network-reachable UPS, its NUT driver or upstream NUT relay endpoint, credentials, power domains, thresholds, and telemetry behavior. Local USB and serial UPS modes are intentionally unsupported. The initial driver allowlist is `snmp-ups`, `netxml-ups`, `powerman-pdu`, `apcupsd-ups`, and `dummy-ups` for tests and upstream relays.
- `PowerInfrastructure` describes non-node, non-UPS power or communication path entities such as PDUs, switches, routers, panels, and transfer equipment.
- `PowerInventoryNode` attaches planner-relevant power metadata to Kubernetes node names without replacing the Kubernetes `Node` as the canonical identity.
- `PowerInventoryEdge` declares provider-neutral `Feeds` and `Carries` topology relations between UPS devices, nodes, and infrastructure entities.
- `UPSCapabilityProfile` declares reusable product/SKU capability records: authoritative NUT telemetry variables, UPS actuation behaviors, quirks, and deterministic match selectors. These are not per-site inventory records.
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
- Nine cluster-scoped CRDs with status subresources.
- Validation/status reconcilers for all nine resources.
- Pure planner package for `ShutdownFlow` graph validation, compiled-wave status, and plan config hashes.
- Pure inventory, capability matching, bundled profile catalog, and resolver assembly packages for provider-neutral topology, profile precedence, and planner input identity.
- `ShutdownFlow` reconciliation against cluster-scoped declarative inventory and UPS capability profiles, with resolved input hashes in status.
- Admission webhooks for `UPSDevice`, `UPSCapabilityProfile`, `PowerInfrastructure`, `PowerInventoryNode`, and `PowerInventoryEdge`.
- `NUTServer` operand rendering for Namespace, ConfigMap, operator-managed Secret, Service, NetworkPolicy, and Deployment.
- Upstream NUT-server relay rendering through `dummy-ups` repeater mode, including Secret-projected `authconf`, egress policy rules, and TCP reachability status.
- `NodePowerAgent` operand rendering for Namespace, ServiceAccount, ConfigMap, Secret-backed `upsmon.conf`, egress NetworkPolicy, and non-privileged DaemonSet in monitor/dry-run/stub modes.
- PostgreSQL audit schema migration package for power events, telemetry snapshots, capability matches, planner compilations, and shutdown decisions.
- PostgreSQL-shaped audit writer boundary with generic SQL executor interfaces and no CNPG-only coupling.
- Installable project-maintained capability profile catalog under `config/catalog/`, including real first-party project profiles for supported UPS product families.
- Manager and project-owned development operand image builds and GHCR tag conventions.
- Production-shaped example resources under `config/samples/`.

Not implemented yet:

- Advanced NUT config rendering, credential rotation, and production-grade operand image release hardening.
- Live NUT variable polling, telemetry normalization, and status-to-policy evaluation.
- Controller wiring for PostgreSQL connections, migration execution, and audit write calls.
- Admission webhooks for `PowerManagementCluster`, `NUTServer`, `NodePowerAgent`, and `ShutdownFlow`.
- Release-grade image smoke tests, SBOMs, signing, scanning policy, provenance, and immutable digest examples.

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

Apply the project-maintained capability catalog after CRDs and the controller are installed:

```sh
make deploy-catalog
```

Apply example resources:

```sh
kubectl apply -k config/samples/
```

Apply the project-maintained capability catalog as CRDs. These are reusable product profiles, not
customer inventory examples:

```sh
kubectl apply -k config/catalog/
```

For release bundles:

```sh
make build-installer build-catalog IMG=<registry>/nut-operator:<tag>
```

## Documentation

- [Architecture](docs/architecture.md)
- [Security](docs/security.md)
- [Image strategy](docs/images.md)
- [Shutdown flow design](docs/shutdown-flow.md)
- [Capability profiles](docs/design/capability-profiles.md)
- [Upstream NUT relay](docs/design/upstream-nut-relay.md)
- [Audit storage schema](docs/design/audit-storage-schema.md)
- [Design decision index](docs/design/decision-index.md)
- [Scope boundaries](docs/design/scope-boundaries.md)
- [Planner requirements](docs/design/planner-requirements.md)
- [Resolver requirements](docs/design/resolver-requirements.md)
- [Executor requirements](docs/design/executor-requirements.md)
- [Inventory provider contract](docs/design/inventory-provider-contract.md)
- [FAQ](docs/design/faq.md)
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
