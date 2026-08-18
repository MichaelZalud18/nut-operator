# Contributing

Components: Foundation & Documentation.

Why the system is shaped the way it is, and the evidence behind it. For build, test, and PR
mechanics see [CONTRIBUTING.md](../../CONTRIBUTING.md) at the repository root.

**Read [settled questions](design/settled-questions.md) before proposing a design change.** Several
questions get re-raised every few months and are already answered; that page lists each one with the
requirement that settles it and the tell that you are about to re-litigate it.

## Design

Written as implemented — a requirement described here is a requirement that exists. Requirement
identifiers (`PL-n`, `EX-n`, `IN-n`, `NA-n`, …) are stable, never reused, and never renumbered.

- [Scope boundaries](design/scope-boundaries.md) — what the project is and is not, plus the decision
  registry of record.
- [Decision index](design/decision-index.md) — the map across the design set.
- [Settled questions](design/settled-questions.md) — closed questions and how they were closed.
- [Shutdown flow](design/shutdown-flow.md) — the compiled plan model and the published artifacts.
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

## Audits

Dated findings and evidence, `F-n` identifiers. A design document states what is true; the audit
that produced it shows the work.

- [Node agent DaemonSet](audits/node-agent-daemonset-audit.md) — the largest, covering the halt path
  end to end.
- [NUT server pod](audits/nutserver-pod-audit.md), [NUT usage](audits/nut-usage-audit.md),
  [quirks, aliasing, firmware](audits/quirks-aliasing-firmware.md).
- [Operator maturity benchmarks](audits/operator-maturity-benchmarks.md) — this project measured
  against established operators.
- [Pre-shutdown hook transport](audits/pre-shutdown-hook-transport.md),
  [reconciler watch scoping](audits/reconciler-watch-scoping.md).

## Tracking

- [tasks.md](../tasks.md) — what is left before v1, by component.
- [tasks-post-v1.md](../tasks-post-v1.md) — deliberately deferred.
