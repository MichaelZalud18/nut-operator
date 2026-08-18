# Examples

Components: Foundation & Documentation.
Audience: operators.

Two complete topologies, meant to be read in different ways. Every manifest in both is validated
against the generated CRD schemas in CI, so nothing here can drift from the API without breaking the
build.

- **[Orion cluster](orion-cluster/README.md)** — one power domain, one UPS, three node roles, and a
  conservation flow, with every edge and every ordering relationship written out. Read this to learn
  what the fields mean.
- **[Simulation scenarios](simulation/README.md)** — three topologies backed by `dummy-ups` and a
  scripted fixture, so triggers, compilation, and wave generation run end to end with no hardware.
  The telemetry is real; only the device behind it is scripted. Read these to learn how the planner
  behaves when you leave it room to derive the ordering itself.

The pair is the point. `orion-cluster` is deliberately tight — every group carries explicit
ordering — while the simulation scenarios declare tiers and let the planner compile the waves. Those
are the two ends of how much you can hand this operator versus how much you spell out.

Everything in both is `mode: DryRun` with `actuatorPolicy: Simulate`. Nothing in this directory can
halt a machine as written.
