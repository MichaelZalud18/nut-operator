# nut-operator

Kubernetes-native power management built around Network UPS Tools (NUT), controller-runtime, and declarative APIs.

> Disclosure: this project is mostly AI-assisted/vibe-coded. Treat the implementation as requiring normal independent review, security validation, and production qualification before relying on it for real power events.

`nut-operator` models UPS devices, power topology, and node/workload shutdown ordering as Kubernetes CRDs, so it installs against any cluster's own hardware and inventory rather than assuming a specific site's wiring. It models UPS devices, NUT server instances, node power agents, and shutdown flows as Kubernetes resources. Durable audit and execution state belongs in PostgreSQL, with CloudNativePG as the preferred Kubernetes-native backing store.

## Goals

- Manage multiple NUT server instances from one operator.
- Support many network-reachable UPS devices and physical power domains.
- Keep UPS telemetry, policy compilation, and node actuation as separate concerns.
- Default to dry-run and status-visible compiled plans before allowing any host shutdown.
- Publish reusable planner artifacts: compiled execution plans, dependency graphs, waves, execution progress, and explanations.
- Treat Kubernetes resources, events, logs, and GitOps-managed manifests as the v1 user interface.
- Use Kubernetes-native security controls: least-privilege RBAC, status subresources, conditions, NetworkPolicy-ready operands, read-only roots, seccomp, and explicit host-actuator isolation.
- Keep long-lived audit, telemetry, and flow execution state out of CR status and in PostgreSQL.
- Decline the Operator Framework's "Auto Pilot" maturity level by design: no auto-scaling,
  auto-tuning, or auto-remediation. Power state is the only trigger the operator acts on.

## API Shape

The API group is `power.zalud.io/v1alpha1`.

- `PowerManagementCluster` configures global defaults, operand namespace, images, security posture, observability, and PostgreSQL/CNPG storage.
- `UPSDevice` describes a physical or simulated network-reachable UPS, its NUT driver or upstream NUT relay endpoint, credentials, power domains, thresholds, and telemetry behavior. Local USB and serial UPS modes are intentionally unsupported. The driver allowlist is `snmp-ups`, `netxml-ups`, `powerman-pdu`, `apcupsd-ups`, and `dummy-ups` for tests and upstream relays.
- `PowerInfrastructure` describes non-node, non-UPS power or communication path entities such as PDUs, switches, routers, panels, and transfer equipment.
- `PowerInventoryNode` attaches planner-relevant power metadata to Kubernetes node names without replacing the Kubernetes `Node` as the canonical identity.
- `PowerInventoryEdge` declares provider-neutral `Feeds` and `Carries` topology relations between UPS devices, nodes, and infrastructure entities.
- `UPSCapabilityProfile` declares reusable product/SKU capability records: authoritative NUT telemetry variables, UPS actuation behaviors, quirks, and deterministic match selectors. These are not per-site inventory records.
- `UPSCapabilityProbe` reads what a UPS actually reports and drafts a `UPSCapabilityProfile` from it,
  for hardware the bundled catalog does not cover. Advisory only: it never changes how a device
  resolves and never runs on the failure path.
- `NUTServer` renders and operates one logical `upsd` server instance for selected UPS devices.
- `NodePowerAgent` manages the per-node monitoring and actuation DaemonSet. It separates `MonitorOnly`, `DryRun`, and `Actuate` modes.
- `ShutdownFlow` defines dependency-graph shutdown policy compiled into ordered waves. Enforced flows require an explicit approval annotation.

## Safety Model

Real host shutdown is not the default.

`NodePowerAgent` defaults to dry-run behavior through `mode: DryRun` and `shutdown.actuatorPolicy: Stub`. Rendering `SystemdPoweroff` requires both:

- `spec.mode: Actuate`
- `metadata.annotations[spec.shutdown.approvalAnnotation] == "true"`

`ShutdownFlow` follows the same pattern for `mode: Enforce`. This keeps dangerous behavior reviewable in Git and visible in `/status` before it can affect nodes.

The node-agent pod split is:

- `upsmon` container: unprivileged NUT client, no Kubernetes API credentials required for ordinary monitoring.
- `actuator` container: omitted or stubbed by default; approved real host shutdown watches local and projected signal files, then uses the isolated host-action boundary with `hostPID`, `SYS_BOOT`, no Kubernetes token, and no NUT credentials.

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
    auditSpool:
      enabled: true
      path: /var/lib/nut-operator/audit-spool
      maxSize: 64Mi
```

External PostgreSQL is also modeled for non-CNPG clusters. `Disabled` exists for local development only.
The audit spool path must be backed by a deployment-supplied durable volume.

## Architecture

```mermaid
flowchart TD
  UPS[Network UPS / upstream NUT appliance] -->|NUT protocol| Server[NUTServer operands]
  Server -->|LIST VAR telemetry| Operator[nut-operator controller]
  Inventory[Inventory CRDs / provider adapters] --> Operator
  Profiles[UPSCapabilityProfile catalog] --> Operator
  Operator -->|status summaries| CRDs[power.zalud.io CRDs]
  Operator -->|audit and telemetry| Postgres[(PostgreSQL / CNPG)]
  Operator -->|compiled waves and decisions| Flow[ShutdownFlow]
  Operator -->|published plan artifacts| Artifacts[Plans / graphs / waves / explanations]
  Flow -->|approved handoff| Agents[NodePowerAgent DaemonSets]
  Agents -->|local signal| Actuator[Host actuator boundary]
  Artifacts -.-> Subscribers[Dashboards / monitoring / docs / recovery consumers]
```

The control plane separates detect, decide, and act:

- Detect: NUT polling, UPS status normalization, declarative inventory resolution, capability-profile matching, and topology assembly.
- Decide: pure trigger evaluation and `ShutdownFlow` graph planning into deterministic ordered waves.
- Act: dry-run execution, Kubernetes workload coordination, node-agent handoff, and explicitly approved local host shutdown.

Durable records are written to PostgreSQL. Kubernetes status remains a current-state review surface, not an event log.

## Interface Model

Kubernetes is the primary interface:

- CRDs declare desired state.
- GitOps manages configuration changes.
- `/status`, Kubernetes Events, logs, and PostgreSQL audit records expose current state and history.
- `kubectl` is sufficient for day-to-day operation.

The operator publishes facts, not external commands. Other systems may consume published planner artifacts for dashboards, documentation, monitoring, recovery orchestration, or future automation. The project boundary remains: `nut-operator` owns power-event planning and shutdown execution; subscribers own what they do with the published plan.

## Installation

Requires cert-manager, and PostgreSQL for production use (CloudNativePG or external). Install the
operator with the bundled manifest:

```sh
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install.yaml
```

Everything defaults to dry-run: a `ShutdownFlow` compiles and publishes its full plan without
touching a node until enforcement is explicitly enabled.

Full prerequisites, the Kustomize path, network and firewall requirements, a configuration
walkthrough, upgrade and uninstall order, and troubleshooting are in
[docs/install.md](docs/install.md).

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

Run the AWS Labs Automated Security Helper scan:

```sh
uv tool install 'git+https://github.com/awslabs/automated-security-helper.git@v3.5.8'
make security-scan
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

- [Installation](docs/install.md)
- [Architecture](docs/architecture.md)
- [System architecture diagram](docs/diagrams/system-architecture.md)
- [Security](docs/security.md)
- [Image strategy](docs/images.md)
- [Shutdown flow design](docs/shutdown-flow.md)
- [Project tasks and current build state](docs/tasks.md)
- [Operator maturity benchmarks](docs/audits/operator-maturity-benchmarks.md)
- [Node agent DaemonSet audit](docs/audits/node-agent-daemonset-audit.md)
- [NUTServer pod audit](docs/audits/nutserver-pod-audit.md)
- [NUT usage and fidelity audit](docs/audits/nut-usage-audit.md)
- [Quirk handling, aliasing, and firmware gating](docs/audits/quirks-aliasing-firmware.md)
- [Capability profiles](docs/design/capability-profiles.md)
- [Upstream NUT relay](docs/design/upstream-nut-relay.md)
- [Audit storage schema](docs/design/audit-storage-schema.md)
- [Telemetry and triggers](docs/design/telemetry-and-triggers.md)
- [Resiliency and partitions](docs/design/resiliency-and-partitions.md)
- [Design decision index](docs/design/decision-index.md)
- [Scope boundaries](docs/design/scope-boundaries.md)
- [Planner requirements](docs/design/planner-requirements.md)
- [Resolver requirements](docs/design/resolver-requirements.md)
- [Executor requirements](docs/design/executor-requirements.md)
- [Adaptive execution tier pointer](docs/design/adaptive-execution-tier-pointer.md)
- [Scaling and sizing](docs/design/scaling-and-sizing.md)
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
