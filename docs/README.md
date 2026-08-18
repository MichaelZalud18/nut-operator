# nut-operator documentation

Components: Foundation & Documentation.

`nut-operator` decides what your cluster shuts down first when the power fails, and carries that
order out. A UPS buys minutes; spending them well means shedding what is disposable, quiescing what
holds state, draining the workers, and stopping the control plane last — rather than losing every
machine at once when the battery ends.

**Most of what makes a shutdown correct is something you supply.** How the racks are actually wired,
which workloads are disposable, what has to outlive everything else, whether a given machine may be
powered off at all. None of that is derivable from a cluster, and a wrong answer surfaces during a
power failure. So these docs are weighted toward the decisions rather than the API — the
[decisions](#decisions-you-have-to-make) section is the one to read slowly.

## First hour

In order. Each step is verifiable before the next one matters, and nothing here can power off a node.

1. **[Install the operator](guides/install.md).** Everything defaults to dry-run.
2. **[Model one UPS](decisions/modeling-your-topology.md)** and the equipment it feeds.
3. **[Assign shutdown tiers](decisions/shutdown-tiers.md)** to a handful of workloads.
4. **Compile a plan and read it.** `kubectl get shutdownflow -o yaml` — the compiled waves,
   the estimated duration, and the feasibility verdict against your UPS's reported runtime.

At the end of that you have a reviewable plan and have changed nothing about how your cluster
behaves. Going further — actually letting it stop a machine — is
[its own decision](decisions/dry-run-to-actuate.md).

## Guides

Doing things.

- [Installation](guides/install.md) — prerequisites, both install paths, configuration walkthrough,
  upgrade and uninstall order, troubleshooting, and how to prove the cluster can really halt a node.

## Decisions you have to make

The judgement calls the operator cannot make for you. Each one states what hangs on the answer.

- [Preparing the hardware](decisions/physical-setup.md) — wiring, network reachability, and
  checking that what you wrote down matches the room. The step with no software fallback.
- [Modeling your topology](decisions/modeling-your-topology.md) — turning real wiring into `feeds`
  and `carries` edges, and what each one changes.
- [Assigning shutdown tiers](decisions/shutdown-tiers.md) — the ordering vocabulary, and how to
  pick numbers you will not want to renumber later.
- [Choosing what is last-ditch](decisions/last-ditch.md) — what must still be running while
  everything else stops, and the cost of naming too much.
- [Tier-overrun policy](decisions/tier-overrun-policy.md) — what to do when a tier runs past its
  budget and the battery does not wait.
- [From dry-run to actuate](decisions/dry-run-to-actuate.md) — the approval gates, what each one
  turns on, and what to verify before crossing.

## Reference

Looking things up.

- [Glossary](reference/glossary.md) — the vocabulary, including which words this project refuses to
  use for ordering.
- [Architecture](reference/architecture.md) — the components and how they fit together.
- [Metrics](reference/metrics.md) — every published metric and what to alert on.
- [Security](reference/security.md) — privilege boundary, RBAC scope, network and credential
  controls, supply chain.
- [Images](reference/images.md) — image strategy, build controls, and digest verification.
- Diagrams: [system architecture](diagrams/system-architecture.md),
  [pod placement](diagrams/example-pod-placement.md).

## Worked examples

Every manifest in these is schema-validated in CI.

- [Orion cluster](examples/orion-cluster/README.md) — one fully authored flow, every edge explicit.
  Read this to learn what the fields mean.
- [Simulation scenarios](examples/simulation/README.md) — tiers only, wave structure derived.
  Read these to learn how the planner behaves when you leave it room.

## Design and contribution

Why the system is shaped the way it is. Written as implemented — a requirement described here is a
requirement that exists.

- [Scope boundaries](design/scope-boundaries.md) — what the project is and is not, plus the
  decision registry of record.
- [Settled questions](design/settled-questions.md) — read before proposing a design change.
- [Decision index](design/decision-index.md) — the map across the design set.
- [Shutdown flow](design/shutdown-flow.md) — the compiled plan model and published artifacts.
- Requirements: [planner](design/planner-requirements.md), [executor](design/executor-requirements.md),
  [resolver](design/resolver-requirements.md).
- Operands: [NUT server](design/nut-server-operand.md), [node agent](design/node-agent-operand.md),
  [upstream relay](design/upstream-nut-relay.md).
- Contracts and models: [inventory provider](design/inventory-provider-contract.md),
  [capability profiles](design/capability-profiles.md),
  [telemetry and triggers](design/telemetry-and-triggers.md),
  [audit storage schema](design/audit-storage-schema.md),
  [adaptive execution](design/adaptive-execution-tier-pointer.md),
  [shutdown hooks](design/shutdown-hooks.md),
  [resiliency and partitions](design/resiliency-and-partitions.md),
  [scaling and sizing](design/scaling-and-sizing.md).
- [FAQ](design/faq.md)
- Audit records live in [`audits/`](audits/) — dated findings and evidence, `F-n` identifiers.
- [Project tasks](tasks.md) — what is left before v1. [Post-v1](tasks-post-v1.md).
