# Configuration

Components: Cross-cutting.
Audience: operators.

**Five resources produce a compiled shutdown plan.** Apply them in the order below; each depends on
the one before it. Everything here defaults to dry-run — nothing on this page can power off a node.
Turning that on is [its own guide](../guides/enable-actuation.md).

| # | Resource | One per | What it does |
| --- | --- | --- | --- |
| 1 | `PowerManagementCluster` | cluster | Operand namespace, storage, operand images, tier definitions. Everything else inherits from it. |
| 2 | `UPSDevice` | UPS | How to reach one UPS, and which power domain it supplies. |
| 3 | `NUTServer` | group of UPS devices | Runs `upsd` and the NUT drivers for the devices it selects. |
| 4 | `NodePowerAgent` | group of nodes that behave alike | One DaemonSet across every node its `nodeSelector` matches. |
| 5 | `ShutdownFlow` | plan | The triggers and the groups. Compiles into ordered waves. |

Two further resources — `PowerInventoryNode` and `PowerInventoryEdge` — describe power topology.
**Most single-UPS setups do not need them.** See [Topology](#topology-when-you-need-it) for when
they start mattering, and for the one way to get them wrong.

## Three things that stop a first run

Each is a deliberate refusal rather than a bug, and each is easier to clear before you start than to
diagnose halfway through.

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

**On a default-deny-egress cluster, egress policy is yours to author.** The bundle ships ingress
rules only. Without an egress policy the manager cannot reach `upsd`, PostgreSQL, or any hook
endpoint, and the failure looks like unreachable hardware rather than like blocked traffic. Start
from the template in `config/network-policy/egress/` — see
[Network controls](../reference/security.md#network-controls). Skip this if your cluster does not
default-deny egress.

## Apply the resources

**1. `PowerManagementCluster`.** Cluster-wide policy: the operand namespace, the storage backend,
shutdown tier definitions, and the operand images every other resource inherits. Set images here
once — `NUTServer` and `NodePowerAgent` have no built-in defaults, and inherit through their
`managementClusterRef`. Sample:
[config/samples/power_v1alpha1_powermanagementcluster.yaml](../../config/samples/power_v1alpha1_powermanagementcluster.yaml).

**2. `UPSDevice`.** One per UPS: its network endpoint, its `powerDomains`, and a credential Secret
reference if the device needs one. SNMP community strings and SNMPv3 material go in the Secret,
never in the spec. Connectivity is network-only — USB and serial attachment is out of scope.

**3. `NUTServer`.** Renders `upsd` plus the NUT drivers for the devices it selects, by label
(`deviceSelector`) or by name (`deviceRefs`). Read
[Three things that stop a first run](#three-things-that-stop-a-first-run) before applying this one.

**4. `NodePowerAgent`.** An **agent fleet**, not a per-node object. One CR renders one DaemonSet
across every node matching its `nodeSelector`; omit the selector and it covers every node. You write
one per group of nodes that should behave differently — control plane versus workers, say — not one
per machine. `nutServerRefs` is required. `spec.mode` defaults to `DryRun`.

**5. `ShutdownFlow`.** The plan: `triggers` (required) and `groups`. `spec.mode` defaults to
`DryRun`, which compiles and publishes the whole plan — wave ordering, estimated duration,
feasibility verdict, and the reasoning — without touching a node.

## Topology: when you need it

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

A complete worked topology is in [docs/examples/orion-cluster/](../examples/orion-cluster/README.md).

## NUT protocol TLS

Skip this section if you set `spec.tls.mode: Disabled`.

| Field | Renders |
| --- | --- |
| `serverCertificateRef` | `CERTFILE` in `upsd.conf`. Point it at a `kubernetes.io/tls` Secret in the operand namespace; an init container concatenates `tls.crt` and `tls.key` into the single chain-then-key PEM NUT expects. |
| `serverCARef` | `CERTPATH` in every monitoring agent's `upsmon.conf`, plus `CERTVERIFY 1` and `FORCESSL 1`. Without it agents encrypt but cannot authenticate `upsd`, and report a `NUTTLSDowngraded` condition. |
| `verifyClientCertificates` | `CERTREQUEST 2` in `upsd.conf`. Off by default — see below. |
| `disableWeakProtocols` | `DISABLE_WEAK_SSL true`, raising the minimum from TLS 1.0 to TLS 1.2. On by default. |

Two constraints are worth knowing before turning on `verifyClientCertificates`. NUT honors
`CERTREQUEST` under OpenSSL only from 2.8.6 onward — on older `upsd` builds it is silently a no-op —
and the operator does not issue client certificates to `upsmon`, so enabling it locks out every
`NodePowerAgent` unless something outside the operator supplies them.

Because `CERTVERIFY` and `FORCESSL` are process-global in `upsmon`, an agent that monitors several
`NUTServer`s settles on the weakest of their modes and reports the downgrade rather than cutting off
the laxer server. Keep `spec.tls.mode` consistent across the servers a single agent monitors.

## Verify the configuration

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

The plan compiles and publishes here, and stops. The judgement calls that make it *correct* — which
UPS feeds what, what stops first, what must outlive everything — are in
[Guides](../guides/README.md).
