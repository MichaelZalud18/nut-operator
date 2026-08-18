# API reference

Components: Cross-cutting.
Audience: operators and integrators.

Every kind in `power.zalud.io/v1alpha1`, what it is for, and how it relates to the others. All of
them are **cluster-scoped**. For the exact field-level schema, read the CRDs themselves —
`kubectl explain <kind>` after install, or `config/crd/bases/` in the repository — which are generated
from the Go types and are always current.

Nothing here explains *how* to fill these in. The judgement calls live in [Guides](../guides/README.md),
and the order to apply them in is in [Configuration](../installation/configuration.md).

## Cluster configuration

**`PowerManagementCluster`** — the root configuration object, and the one every other kind inherits
from. It owns the operand namespace, the operand image defaults, global security posture,
observability settings, the `ShutdownHook` endpoint allowlist, shutdown tier definitions, and durable
storage. Setting operand images here once is the recommended shape, since `NUTServer` and
`NodePowerAgent` have no built-in image defaults.

## Power hardware

**`UPSDevice`** — one physical or simulated network-reachable UPS: its NUT driver or upstream relay
endpoint, a credential Secret reference, its power domain name, thresholds, and telemetry behavior.

Connectivity is network-only. Local USB and serial drivers are out of scope for this API so that
generated NUT server pods never need host device mounts or privileged access, and direct drivers come
from a reviewed allowlist — `snmp-ups`, `netxml-ups`, `apcupsd-ups`, and `dummy-ups` for simulation
and upstream relays. Unknown drivers are rejected at admission rather than passed through. For
appliances that already expose their own `upsd`, `spec.upstreamNUT` relays from it instead of driving
the device directly.

**`PowerInfrastructure`** — a non-node, non-UPS entity on the power or communication path: a PDU,
switch, router, panel, or transfer device. It exists so topology can explain how a node is fed and
reached without implying that anything actuates it.

## Topology

**`PowerInventoryNode`** — attaches planner-relevant power metadata to a Kubernetes node name. It does
not displace the Kubernetes `Node` as canonical identity; it annotates it.

**`PowerInventoryEdge`** — one provider-neutral relation between two entities, either `Feeds` (A
supplies power to B) or `Carries` (A transports B's NUT or control path). These drive opposite planner
behavior and are never conflated. Power-domain membership is the transitive closure of `Feeds` edges
from a `UPSDevice`, derived rather than declared.

Which edge to write, and why the distinction matters, is in
[Modeling your topology](../guides/model-your-topology.md); the full contract is in
[the inventory provider contract](../contributing/design/inventory-provider-contract.md).

## Capability catalog

**`UPSCapabilityProfile`** — a reusable product/SKU record: which NUT telemetry variables a model
authoritatively reports, its actuation behaviors, its quirks, and deterministic match selectors.
These are product records, not per-site inventory, and the project ships a bundled catalog of the
models it has verified. CRD-authored profiles always outrank bundled ones.

**`UPSCapabilityProbe`** — reads what a device actually reports and drafts a `UPSCapabilityProfile`
from it, for hardware the bundled catalog does not cover. Advisory only: it never changes how a device
resolves, and never runs on the failure path.

**`PDUCapabilityProfile`** — PDU product records, including outlet count and which outlets are
switchable. **Scaffolding for v1** (`OD-25`): the kind, schema, validation, bundled catalog, and
matcher exist, and no device kind, inventory entity, render path, or actuation path consumes them.
PDU actuation does not work.

## Operands

**`NUTServer`** — one logical `upsd` instance. It selects `UPSDevice` objects by label and renders the
server-side NUT configuration, credentials, TLS material references, and service exposure. For
built-in NUT appliances it renders `dummy-ups` repeater mode rather than a hardware driver, which
keeps appliance support non-privileged and network-only.

**`NodePowerAgent`** — one DaemonSet fleet. It references `NUTServer` objects, selects nodes, and
declares whether the fleet is monitoring, dry-running, or allowed to actuate. Its three modes —
`MonitorOnly`, `DryRun`, `Actuate` — are one half of the two-gate safety model; see
[Enabling actuation](../guides/enable-actuation.md).

## Shutdown policy

**`ShutdownFlow`** — the ordered policy layer, and the object you spend the most time authoring. Its
model is a dependency graph of shutdown **groups** compiled into deterministic execution **waves**.
Linear steps remain available for small test installs, but production flows use graph relationships so
independent groups run concurrently while dependent groups stay protected. Enforced flows require an
explicit approval annotation.

**`ShutdownHook`** — one bounded pre-shutdown call to a system outside the cluster, referenced by a
`RunHook` group. HTTP CloudEvents is the primary transport; a generic Kubernetes object write is the
secondary one. This is the operator's **only** outbound network path, so endpoints are allowlisted on
`PowerManagementCluster`. Hook failures are advisory: they mark the flow degraded without holding a
wave or engaging `abortPolicy`.

## Compiled output

The planner turns authored `groups` into status-visible `compiledWaves`. Ordering comes from
`shutdownTier` plus the `requires`, `before`, and `after` relationships, and nothing else.

- `requires` — the referenced group must stay available while this group shuts down.
- `before` / `after` — direct ordering edges.
- `shutdownTier` — coarse ordering, compiled into the same derived edges.
- Groups ready at the same time share a wave. There is no additional ordering key.
- Cycles and unknown group references are rejected before a plan can be accepted.
- Each wave may run its groups concurrently; later waves wait for earlier waves to finish.

**Tiers are input; waves are output.** You never author a wave. See
[the glossary](glossary.md) for the vocabulary and
[the shutdown-flow design](../contributing/design/shutdown-flow.md) for the compilation model.

## Status and conventions

- `spec` carries desired state; `/status` carries conditions, `observedGeneration`, rendered config
  hashes, and compiled plans.
- Status is a current-state review surface, not an event log. Event history, telemetry streams, and
  execution records go to PostgreSQL — see [Storage](#storage).
- Admission webhooks reject unsafe combinations before persistence, and reconcilers repeat the same
  checks in status for defense in depth and for installs that temporarily disable webhooks.

## Storage

PostgreSQL is the durable state store. `PowerManagementCluster.spec.storage.mode` selects between
`CNPG` (the default, a CloudNativePG `Cluster` in the same Kubernetes cluster), `ExternalPostgres`
(a database outside the cluster, referenced by a DSN in a Secret, TLS required by default), and
`Disabled` (evaluation and development only — no audit trail is kept).

`ExternalPostgres` is the more resilient choice for this workload, because a database outside the
cluster is not in the shutdown path of the event it is recording.

The audit spool is the local fallback for the window where PostgreSQL stops accepting writes
mid-shutdown. Its path must be backed by a deployment-supplied durable volume:

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

Schema and replay semantics are in
[the audit storage schema](../contributing/design/audit-storage-schema.md).
