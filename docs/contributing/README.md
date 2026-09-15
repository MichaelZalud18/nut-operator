# Contributing

Components: Foundation & Documentation.
Audience: contributors.

Why the system is shaped the way it is, and the evidence behind it. For build, test, and PR
mechanics see [CONTRIBUTING.md](../../CONTRIBUTING.md) at the repository root.

**Read [settled questions](design/settled-questions.md) before proposing a design change.** Several
questions get re-raised every few months and are already answered; that page lists each one with the
requirement that settles it and the tell that you are about to re-litigate it.

## Design

Design contracts and intended behavior. Known implementation gaps belong in
[tasks.md](../tasks.md); a requirement's presence here is not evidence that its implementation has
been verified. Requirement identifiers (`PL-n`, `EX-n`, `IN-n`, `NA-n`, …) are stable, never reused,
and never renumbered.

- [Scope boundaries](design/scope-boundaries.md) — what the project is and is not, plus the decision
  registry of record.
- [Decision index](design/decision-index.md) — the map across the design set.
- [User stories](design/user-stories.md) — requested outcomes for architectural review, separate
  from approved implementation tasks.
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
- [Test domains](design/test-domains.md) — what each layer of testing proves, which files and
  workflows own it, and what nothing yet proves.
- [FAQ](design/faq.md)

## Audits

Dated findings and evidence, `F-n` identifiers. Design documents state the contract; audits record
what was inspected or tested and where the implementation differs.

- [Node agent DaemonSet](audits/node-agent-daemonset-audit.md) — the largest, covering the halt path
  end to end.
- [Fresh repository review, 2026-09-04](audits/fresh-review-2026-09-04.md) — execution safety,
  restart/storage behavior, protocol boundaries, and test-reliability evidence (`F-126`–`F-143`).
- [NUT server pod](audits/nutserver-pod-audit.md), [NUT usage](audits/nut-usage-audit.md),
  [quirks, aliasing, firmware](audits/quirks-aliasing-firmware.md), and
  [planner code quality](audits/planner-code-quality.md).
- [Operator maturity benchmarks](audits/operator-maturity-benchmarks.md) — this project measured
  against established operators.
- [Pre-shutdown hook transport](audits/pre-shutdown-hook-transport.md),
  [reconciler watch scoping](audits/reconciler-watch-scoping.md).

## Tracking

- [tasks.md](../tasks.md) — active v1 engineering and investigations, by component.
- [tasks-v1-release.md](../tasks-v1-release.md) — release readiness, gates, and publishing.
- [tasks-post-v1.md](../tasks-post-v1.md) — deliberately deferred.
