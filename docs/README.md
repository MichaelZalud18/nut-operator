# nut-operator documentation

Components: Foundation & Documentation.
Audience: operators and evaluators.

`nut-operator` decides what your cluster shuts down first when the power fails, and carries that
order out. A UPS buys minutes; spending them well means shedding what is disposable, quiescing what
holds state, draining the workers, and stopping the control plane last — rather than losing every
machine at once when the battery ends.

**Most of what makes a shutdown correct is something you supply.** Rack wiring, workload priority,
last-ditch workloads, and node actuation policy are operator decisions. Start with
[Guides](guides/README.md) before enabling anything that can halt a node.

## First hour

First [install the operator](installation/README.md) and configure its shared cluster settings.
Then follow the [configuration quick start](installation/configuration.md) in three domains:

1. **[UPS devices and NUT](installation/configuration.md#1-ups-devices-and-nut).** Devices,
   NUT servers, credential/TLS Secrets, capability and behavior profiles, polling, and thresholds.
2. **[Topology](installation/configuration.md#2-topology).** Hosts and supporting infrastructure,
   power feeds, supply inputs, communication paths, and roles.
3. **[Shutdown flow](installation/configuration.md#3-shutdown-flow).** Triggers, targets, tiers,
   ordering, and actions. Configure supporting node agents where needed and review the compiled
   plan in `DryRun` before enabling effects.

At the end of that you have a reviewable plan and have changed nothing about how your cluster
behaves. Going further — actually letting it stop a machine — is
[its own decision](guides/enable-actuation.md).

## Sections

Pages declare `Components:` and `Audience:` under the title.

**[Concepts](concepts/README.md)** — the control plane, operands, power-event path, and pod
placement.

**[Installation](installation/README.md)** — prerequisites and the install itself, the
[webhook certificate decision](installation/webhook-certificate.md),
[configuration](installation/configuration.md) organized by those three domains, and
[upgrade and uninstall](installation/upgrade-and-uninstall.md).

**[Guides](guides/README.md)** — hardware prep, topology modeling, tiers, last-ditch workloads,
tier-overrun policy, actuation, NetBox import, and unknown UPS profiling.

**[Reference](reference/README.md)** — [API reference](reference/api.md),
[glossary](reference/glossary.md), [metrics](reference/metrics.md),
[security](reference/security.md), and [image strategy](reference/images.md).

**[Examples](examples/README.md)** — worked and simulated manifests, schema-validated in CI.

**[Troubleshooting](troubleshooting.md)** — symptoms and causes.

**[Contributing](contributing/README.md)** — the design set and the audits behind it, plus
[engineering tasks](tasks.md), [v1 release readiness](tasks-v1-release.md), and
[completed work](tasks-completed.md).
