# nut-operator documentation

Components: Foundation & Documentation.

`nut-operator` decides what your cluster shuts down first when the power fails, and carries that
order out. A UPS buys minutes; spending them well means shedding what is disposable, quiescing what
holds state, draining the workers, and stopping the control plane last — rather than losing every
machine at once when the battery ends.

**Most of what makes a shutdown correct is something you supply.** How the racks are actually wired,
which workloads are disposable, what has to outlive everything else, whether a given machine may be
powered off at all. None of that is derivable from a cluster, and a wrong answer surfaces during a
power failure. So this documentation is weighted toward the decisions rather than the API —
[Guides](guides/README.md) is the section to read slowly.

## First hour

In order. Each step is verifiable before the next one matters, and nothing here can power off a node.

1. **[Install the operator](installation/README.md).** Everything defaults to dry-run.
2. **[Configure the resources](installation/configuration.md)**, in the order they depend on
   each other.
3. **[Model one UPS](guides/model-your-topology.md)** and the equipment it feeds.
4. **[Assign shutdown tiers](guides/assign-shutdown-tiers.md)** to a handful of workloads.
5. **Compile a plan and read it.** `kubectl get shutdownflow -o yaml` — the compiled waves, the
   estimated duration, and the feasibility verdict against your UPS's reported runtime.

At the end of that you have a reviewable plan and have changed nothing about how your cluster
behaves. Going further — actually letting it stop a machine — is
[its own decision](guides/enable-actuation.md).

## Sections

**[Concepts](concepts/README.md)** — what the system is. The control plane and its two operands, how
a power event moves through them, and where the pods land.

**[Installation](installation/README.md)** — prerequisites and the two install paths,
[configuration](installation/configuration.md) in dependency order, and
[upgrade and uninstall](installation/upgrade-and-uninstall.md).

**[Guides](guides/README.md)** — the six judgement calls, in the order you hit them: preparing the
hardware, modeling the topology, assigning tiers, choosing what is last-ditch, setting the
tier-overrun policy, and enabling actuation.

**[Reference](reference/README.md)** — [API reference](reference/api.md),
[glossary](reference/glossary.md), [metrics](reference/metrics.md),
[security](reference/security.md), and [image strategy](reference/images.md).

**[Examples](examples/README.md)** — [Orion cluster](examples/orion-cluster/README.md), one fully authored
flow with every edge explicit; and [simulation scenarios](examples/simulation/README.md), tiers only,
with the wave structure derived. Every manifest in both is schema-validated in CI.

**[Troubleshooting](troubleshooting.md)** — symptoms and causes.

**[Contributing](contributing/README.md)** — the design set and the audits behind it, plus
[what is left before v1](tasks.md).
