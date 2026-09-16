# Quick Start: Configuration

Components: Cross-cutting.
Audience: operators.

Configure three domains, in order: **UPS devices and NUT**, **topology**, then **shutdown flow**.
Each domain contains several related resources; it is not one CRD or necessarily one YAML file.
Keep flows in `DryRun` and agents in `DryRun` / `Simulate` while reviewing the plan. Real actuation
is [a separate decision](../guides/enable-actuation.md).

| Domain | Question it answers | Sub-components |
| --- | --- | --- |
| [1. UPS devices and NUT](#1-ups-devices-and-nut) | What supplies power, and how do we read it? | `UPSDevice`, `NUTServer`, credential and TLS Secrets, capability/behavior profiles, polling and thresholds |
| [2. Topology](#2-topology) | What depends on that power and on which communication paths? | `PowerInventoryNode`, `PowerInfrastructure`, `PowerInventoryEdge`, supply inputs and node roles |
| [3. Shutdown flow](#3-shutdown-flow) | When should we act, on what, and in which order? | `ShutdownFlow` triggers, groups, tiers, dependencies and actions; optional `ShutdownHook` configuration |

## Before the three domains

[Install the operator and its webhook certificate](README.md), then configure the shared
`PowerManagementCluster`: operand namespace, storage, image references, and shutdown-tier defaults.
Reference it from the resources that inherit those settings. `NUTServer` and `NodePowerAgent` have
no built-in operand image defaults. Start from the
[cluster sample](../../config/samples/power_v1alpha1_powermanagementcluster.yaml).
PostgreSQL is required for production; `storage.mode: Disabled` is for evaluation without an audit
trail. Shared bootstrap is a prerequisite, not a fourth configuration domain.

On a default-deny-egress cluster, author manager egress policy before troubleshooting UPS or
database connectivity. The install bundle supplies ingress rules, not those outbound permissions.
Use `config/network-policy/egress/` and the
[network controls reference](../reference/security.md#network-controls).

## 1. UPS devices and NUT

Set up the whole UPS integration here, not just the device object.

### Devices and NUT servers

- **`UPSDevice`:** one per UPS, with its declared identity, network driver/endpoint (or upstream
  NUT connection), named `powerDomains`, and credential references. USB and serial attachment are
  out of scope. Polling and device thresholds also belong here.
- **`NUTServer`:** selects devices by `deviceRefs` or `deviceSelector`. One server renders one
  managed pod containing `upsd` and a supervisor sidecar that owns the selected devices' drivers.
  Multiple UPS devices can share that server; a UPS does not require its own NUTServer.

Start with the [UPSDevice](../../config/samples/power_v1alpha1_upsdevice.yaml) and
[NUTServer](../../config/samples/power_v1alpha1_nutserver.yaml) samples. Adapt referenced endpoints,
names, and images before applying them; samples are not site configuration.

### Secrets and profiles

- **Device credentials:** put SNMP community strings, SNMPv3 material, or upstream credentials
  in the referenced Secrets, never inline in the device spec or committed examples.
- **NUT client authentication and TLS:** keep these distinct from device credentials. Supply
  configured Secret references and the server's certificate/CA material as described below.
- **Capability and behavior profiles:** `UPSCapabilityProfile` describes a device family's
  supported telemetry and behavior. Reuse the bundled catalog or a reviewed custom profile;
  several devices can match the same profile. It is not a per-device object or a shutdown plan.

**`NUTServer` requires TLS by default.** `spec.tls.mode` defaults to `Required`, so a `NUTServer`
without a certificate reference is rejected at admission. Either supply one — see
[NUT protocol TLS](#nut-protocol-tls) — or set `spec.tls.mode: Disabled` while you are evaluating on
a trusted network. This is NUT's own protocol TLS, unrelated to the
[webhook certificate](webhook-certificate.md).

**Runtime-based triggers need a capability profile.** A `RuntimeBelow` or `ChargeBelow` trigger has
to know the device actually reports that reading. A device matching no profile falls back to a
floor that declares `ups.status` and nothing else, and the flow is rejected with
`TriggerUnsupportedByAllDevices`. Apply the bundled catalog first:

```sh
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/config/catalog/upscapabilityprofiles.yaml
```

From a clone, `make deploy-catalog` does the same thing. If your UPS is not in the catalog, that is
expected and handled — see
[Profiling a UPS the catalog does not cover](../guides/profile-an-unknown-ups.md). An `OnBattery`
trigger needs none of this; `ups.status` is always available.

### NUT protocol TLS

Skip this subsection if you explicitly set `spec.tls.mode: Disabled` for a trusted evaluation.

| Field | Renders |
| --- | --- |
| `serverCertificateRef` | `CERTFILE` in `upsd.conf`. Point it at a `kubernetes.io/tls` Secret in the operand namespace; an init container concatenates `tls.crt` and `tls.key` into the single chain-then-key PEM NUT expects. |
| `serverCARef` | `CERTPATH` in every monitoring agent's `upsmon.conf`, plus `CERTVERIFY 1` and `FORCESSL 1`. Without it agents encrypt but cannot authenticate `upsd`, and report a `NUTTLSDowngraded` condition. |
| `verifyClientCertificates` | Leave false. Setting it true is currently rejected at admission. |
| `disableWeakProtocols` | `DISABLE_WEAK_SSL true`. On by default. |

Client-certificate verification is not a supported configuration in the current operator. See the
[NUT operand contract](../contributing/design/nut-server-operand.md#the-admission-surface-and-the-image-agree).
Because `CERTVERIFY` and `FORCESSL` are process-global in `upsmon`, an agent monitoring several
servers uses their weakest common posture and reports a downgrade. Keep TLS modes consistent.

### Check UPS setup

```sh
kubectl get upsdevice,nutserver
kubectl describe upsdevice
kubectl describe nutserver
```

Check device identity/profile matching, current telemetry, and server conditions before relying on
the readings in a flow. For scripted UPS devices instead of hardware, use the
[simulation fixtures](../examples/simulation/README.md).

## 2. Topology

Model relationships separately from how NUT connects to a UPS and from how a flow shuts things down.

| Sub-component | Resource or field | Purpose |
| --- | --- | --- |
| Kubernetes hosts | `PowerInventoryNode.spec.nodeName` | Binds an inventory node to a real Kubernetes Node; includes planning roles and supply requirements |
| Supporting infrastructure | `PowerInfrastructure` | Models switches, routers, PDUs, and other supported infrastructure |
| Power connections | `PowerInventoryEdge`, `feeds` with `input` | Describes the supply path and the downstream input it reaches |
| Communication connections | `PowerInventoryEdge`, `carries` | Describes the NUT/control paths that dependent work needs to keep available |

`PowerInventoryNode.spec.nodeName` is not a place to invent external hosts. Modeling infrastructure
also does not make it an actuation target. UPS resources are the graph's supply roots; downstream
power-domain membership is derived from connections, not a second hand-maintained membership list.

### When an explicit graph is needed

`PowerInventoryNode` and `PowerInventoryEdge` describe how power reaches each machine. The planner
uses that graph to work out which nodes fall together when a given UPS runs out.

**You need them when the wiring is not obvious from the resources themselves:**

- a machine with two supplies, where the `feeds` edge's `input` qualifier says whether they are
  redundant or separate
- anything between the UPS and the host — a PDU, a transfer switch
- nodes that must be marked exempt from power planning
- node-level roles the planner should respect: control plane, quorum member, last-ditch

**You can skip both when one UPS feeds everything.** The flow still finds your nodes — through
`NodePowerAgent.status.selectedNodes` for shutdown groups, and node labels for the rest — the power
domain comes from `UPSDevice.spec.powerDomains`, and tiers come from each group's `shutdownTier`.

> **Apply both or neither.** A `PowerInventoryNode` with no `feeds` edge reaching it is a hard
> `PowerPlanningOrphan` error, not a warning: a node that is in the topology but connected to
> nothing cannot be planned for, and the planner refuses rather than guessing. Half a graph is worse
> than no graph.

A `feeds` edge always needs its `input` qualifier (`IN-4`). Without it the graph cannot say whether
two edges into one machine are redundant supplies or two independent ones, so it is rejected rather
than assumed.

For shared operator-to-API and NUT service paths, also reference the modeled entities in
`ShutdownFlow.spec.communicationPaths`. The [topology guide](../guides/model-your-topology.md)
explains these cross-domain references, derived ordering, and explicit coverage exemptions.

```sh
kubectl get powerinventorynode,powerinfrastructure,powerinventoryedge
```

Check names, supply inputs, and roles against the actual wiring. An empty inventory is expected
only when deliberately using the simple no-graph setup. A complete graph example is in
[Orion](../examples/orion-cluster/README.md).

## 3. Shutdown flow

`ShutdownFlow` turns the UPS readings and topology into a policy. Start with `mode: DryRun` and the
[flow sample](../../config/samples/power_v1alpha1_shutdownflow.yaml).

- **Triggers:** when to act, such as `OnBattery`, and which UPS domains to consider.
- **Groups and targets:** which workloads or nodes each action covers.
- **Tiers and dependencies:** what stops earlier or later; waves are compiled, not authored.
  Use the [tier guide](../guides/assign-shutdown-tiers.md) to choose priorities.
- **Actions and hooks:** what to do to a target. Optional `ShutdownHook` resources and their
  authentication Secrets belong with this policy. Hook delivery is advisory, not confirmed host
  shutdown; topology alone never selects an external shutdown mechanism.
- **Safety and shared paths:** approvals, dry-run settings, and references to the communication
  paths that must survive while the flow executes.

### Node-agent prerequisite

For groups using the built-in node shutdown path, configure `NodePowerAgent` before the flow.
It is delivery infrastructure, not a fourth authoring domain: one CR renders a DaemonSet over its
`nodeSelector`, not one object per machine. Supply `nutServerRefs` and inherited image settings;
use separate agents only for node groups that need different policies. Keep `mode: DryRun` and
`shutdown.actuatorPolicy: Simulate`. See the
[agent sample](../../config/samples/power_v1alpha1_nodepoweragent.yaml).

### Review the compiled plan

```sh
kubectl get powermanagementcluster,upsdevice,nutserver,nodepoweragent,shutdownflow
kubectl describe shutdownflow <name>     # Accepted, Degraded, ExecutionReady conditions
```

The compiled plan, dependency graph, waves, and diagram exports are published in the `ShutdownFlow`
status. Read them before enabling enforcement — that is what dry-run is for. A rejected compile
publishes its reason on `status.compileDiagnostics`, tagged with the stage that produced it.

Two refusals are intentional and worth recognizing:

- **`UnidentifiedUPSDevice`** — a device matched no product capability profile, so nothing has been
  verified about it. Dry-run still compiles the whole plan; enforcement refuses unless
  `spec.safety.allowUnidentifiedDevices: true` records the acceptance in Git.
  [Profiling the device](../guides/profile-an-unknown-ups.md) removes the refusal instead of
  overriding it.
- **`TriggerUnsupportedByAllDevices`** — a trigger references telemetry (such as battery runtime)
  that none of the targeted devices report, so the plan could never fire.

## Testing without hardware

[docs/examples/simulation/](../examples/simulation/README.md) drives scripted
`Online`/`OnBattery`/`LowBattery` transitions through a real NUT driver. Three scenarios: one UPS
with no topology, a small cluster with a router and switch, and a cascaded UPS → PDU → rack layout.
The latter two derive their wave structure from tiers rather than authored ordering.

## Next

As the setup grows, change the domain that owns the concern: add a device or adjust its readings
in **UPS/NUT**, record changed wiring in **topology**, and change triggers or shutdown order in
**shutdown flow**. Keep references between domains explicit; grouping several resources in one file
does not merge their responsibilities. Review the dry-run plan again before
[enabling actuation](../guides/enable-actuation.md).
