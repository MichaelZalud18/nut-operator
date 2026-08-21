# Simulation scenarios

Components: NUT Server / upsd, Node Agent / DaemonSet, Planning & Execution Logic.
Audience: operators.

Runnable power topologies backed by `dummy-ups` and a scripted `.seq` fixture, so triggers,
compilation, wave generation, and release gating can be exercised end to end with no hardware. The
telemetry is real — the `UPSDeviceReconciler` polls a real `upsd` reading a real driver — and only
the device behind it is scripted.

| Scenario | Shape | What it exercises |
| --- | --- | --- |
| [single-domain](single-domain/) | One UPS, one domain, no topology | The fixture mechanism itself: `OnBattery`/`LowBattery` transitions on a 20s loop |
| [homelab](homelab/) | 1 control plane, 3 workers, 1 burst, router + switch | Wave generation from tiers alone, with derived multi-group waves |
| [multistage](multistage/) | UPS → 2 PDUs → 2 racks, 3 control-plane members | Multi-hop `feeds` closure, and racks as concurrent peers |

## Shared prerequisites

Two files in this directory are applied once and used by every scenario:

- **[cluster.yaml](cluster.yaml)** — the `PowerManagementCluster` named `sim-power` that every
  `NUTServer`, `ShutdownFlow`, and `NodePowerAgent` here references. It creates the operand
  namespace and supplies the operand images; a `NodePowerAgent` inherits its images through this
  reference and cannot render without it. Storage is `Disabled`, because these scenarios are for
  reading what the operator decides rather than keeping a durable record of it.
- **[capability-profile.yaml](capability-profile.yaml)** — the `UPSCapabilityProfile` the scripted
  fixtures match. Without it every fixture device falls to the unidentified-device profile, which
  declares `ups.status` and nothing else, so the `RuntimeBelow` and `ChargeBelow` triggers these
  scenarios use have no trusted reading behind them and the flows do not compile.

Each `UPSDevice` here declares `spec.identity.model` matching what its fixture reports, because
profile matching runs on the declared model and never on the reported one. The `IdentityVerified`
condition on the device confirms the two agree.

## Tight versus loose scenarios

[orion-cluster](../orion-cluster/) is deliberately tight: every group carries an explicit `before`
edge, so the compiled plan is exactly the chain that was written. That makes it a good reference for
*what the fields mean*, and a poor test of the planner — it recites a wave structure rather than
deriving one. Its six waves are identical with or without any ordering hints, because the chain
already determines them.

The scenarios here are deliberately loose. Groups carry a tier and nothing else, so ordering across
tiers is derived from tier edges and grouping within a tier is derived from the absence of edges.
Change a tier number and the wave structure changes with it.

That is the point: a loose scenario tells you what the planner *decides*, and it is also where the
planner's decisions can surprise you — see the tier-2 note in [homelab](homelab/).

## Cost of adding one

Low, and it is worth knowing why. A scenario is:

- **One ConfigMap** holding a `.seq` fixture — a handful of `variable: value` blocks separated by
  `TIMER <seconds>` lines. This is the only part that models the UPS, and it is about 40 lines.
- **One `UPSDevice`** with `driver: dummy-ups` and `spec.simulation.sequenceConfigMapRef` instead of
  an endpoint. No credentials, no network device.
- **One `NUTServer`** selecting it by label.
- **Inventory** — one `PowerInventoryNode` per node, one `PowerInfrastructure` per switch/PDU/router,
  one `PowerInventoryEdge` per relationship. This is the bulk of the YAML and the least interesting
  part to write; it is mechanical.
- **Agents and a flow.**

Nothing needs hardware, a real network, or credentials, and several scenarios coexist in one cluster
as long as their power-domain names differ. The expensive part is not the simulation — it is
deciding what the topology should be, which is design work either way.

The one rule worth remembering: **`Feeds` edges require an `input` qualifier** (`IN-4`). Omitting it
is a hard `FeedInputRequired` error, not a warning, because an unqualified feed cannot say whether
two edges are redundant supplies or two separate ones.

## Adding a scenario

Every manifest here is validated against the generated CRD schemas by `make validate-samples`, which
runs in CI with no path filter. A new scenario is covered automatically by being in this directory.
