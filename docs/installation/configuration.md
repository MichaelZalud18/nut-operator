# Configuration

Components: Cross-cutting.
Audience: operators.

What to apply after the operator is running, in order. Everything here defaults to dry-run — nothing
on this page can power off a node. Turning that on is [its own guide](../guides/enable-actuation.md).

## Apply the resources

Each step depends on the one before it.

**1. Capability profiles** (recommended). The bundled catalog describes UPS models the project has
verified:

```sh
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/config/catalog/upscapabilityprofiles.yaml
```

From a clone, `make deploy-catalog` does the same thing.

If your UPS is not in the catalog, that is expected and handled — see
[the FAQ](../contributing/design/faq.md#what-if-my-ups-does-not-have-a-packaged-capability-profile).

**2. `PowerManagementCluster`** — cluster-wide policy: operand namespace, storage backend, shutdown
tiers, and the operand images every other resource inherits. See
[config/samples/power_v1alpha1_powermanagementcluster.yaml](../../config/samples/power_v1alpha1_powermanagementcluster.yaml).

**3. `UPSDevice`** — one per UPS, with its network endpoint and a credential Secret reference.
SNMPv3 credentials go in the Secret, not the spec.

**4. `NUTServer`** — renders `upsd` and the NUT drivers for a set of devices, selected by label.

`spec.tls.mode` defaults to `Required`, which means a `NUTServer` needs a certificate before it
will be accepted. NUT's own protocol TLS is a separate concern from the webhook certificate above,
and the operand wires it as follows:

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

**5. Topology** — `PowerInventoryNode` (requires `nodeName`) and `PowerInventoryEdge` (requires
`from`, `to`, `relation`). This is what tells the planner which UPS feeds which node. An invalid or
incomplete graph blocks `ShutdownFlow` acceptance, by design.

**6. `NodePowerAgent`** — the per-node DaemonSet (requires `nutServerRefs`). Defaults to
`MonitorOnly`.

**7. `ShutdownFlow`** — the plan itself (requires `triggers`). Defaults to `DryRun`, which compiles
and publishes the full plan, including the wave ordering and the reasoning, without touching a node.

A complete worked topology is in [docs/examples/orion-cluster/](../examples/orion-cluster/README.md).
For testing without real hardware, [docs/examples/simulation/](../examples/simulation/README.md) drives
scripted `Online`/`OnBattery`/`LowBattery` transitions through a real NUT driver. Three scenarios:
one UPS with no topology, a small cluster with a router and switch, and a cascaded UPS → PDU → rack
layout. The latter two derive their wave structure from tiers rather than authored ordering.

## Verify the configuration

```sh
kubectl get powermanagementcluster,upsdevice,nutserver,nodepoweragent,shutdownflow
kubectl describe shutdownflow <name>     # Accepted, Degraded, ExecutionReady conditions
```

The compiled plan, dependency graph, waves, and diagram exports are published in the `ShutdownFlow`
status. Read them before enabling enforcement — that is what dry-run is for.

Two refusals are intentional and worth recognizing:

- **`UnidentifiedUPSDevice`** — a device matched no product capability profile, so nothing has been
  verified about it. Dry-run still compiles the whole plan; enforcement refuses unless
  `spec.safety.allowUnidentifiedDevices: true` records the acceptance in Git.
- **`TriggerUnsupportedByAllDevices`** — a trigger references telemetry (such as battery runtime)
  that none of the targeted devices report, so the plan could never fire.

## Next

The plan compiles and publishes here, and stops. The judgement calls that make it *correct* — which
UPS feeds what, what stops first, what must outlive everything — are in
[Guides](../guides/README.md).
