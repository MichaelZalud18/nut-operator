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

In order. Each step is verifiable before the next one matters, and nothing here can power off a node.

1. **[Install the operator](installation/README.md).** Everything defaults to dry-run.
2. **[Configure the resources](installation/configuration.md)**, in the order they depend on
   each other.
3. **[Model one UPS](guides/model-your-topology.md)** and the equipment it feeds — needed once a
   machine has two supplies or something sits between the UPS and the host, skippable while one UPS
   feeds everything.
4. **[Assign shutdown tiers](guides/assign-shutdown-tiers.md)** to a handful of workloads.
5. **Compile a plan and read it.** `kubectl get shutdownflow -o yaml` — the compiled waves, the
   estimated duration, and the feasibility verdict against your UPS's reported runtime.

At the end of that you have a reviewable plan and have changed nothing about how your cluster
behaves. Going further — actually letting it stop a machine — is
[its own decision](guides/enable-actuation.md).

## Sections

Pages declare `Components:` and `Audience:` under the title.

**[Concepts](concepts/README.md)** — the control plane, operands, power-event path, and pod
placement.

**[Installation](installation/README.md)** — prerequisites and the install itself, the
[webhook certificate decision](installation/webhook-certificate.md),
[configuration](installation/configuration.md) in dependency order, and
[upgrade and uninstall](installation/upgrade-and-uninstall.md).

**[Guides](guides/README.md)** — hardware prep, topology modeling, tiers, last-ditch workloads,
tier-overrun policy, actuation, NetBox import, and unknown UPS profiling.

**[Reference](reference/README.md)** — [API reference](reference/api.md),
[glossary](reference/glossary.md), [metrics](reference/metrics.md),
[security](reference/security.md), and [image strategy](reference/images.md).

**[Examples](examples/README.md)** — worked and simulated manifests, schema-validated in CI.

**[Troubleshooting](troubleshooting.md)** — symptoms and causes.

**[Contributing](contributing/README.md)** — the design set and the audits behind it, plus
[what is left before v1](tasks.md).
