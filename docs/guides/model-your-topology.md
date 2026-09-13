# Modeling your topology

Components: Inventory System.
Audience: operators.

**The decision:** how the wiring you recorded becomes edges the planner can compute over.

You declare entities and the relations between them. You never declare which nodes belong to which
power domain — that is derived, and derived membership is the whole point: one computation path, one
source of truth, nothing to reconcile between a list you maintained and a graph the operator walked.

## Two relations, and they are not interchangeable

`feeds(A → B)` — **A supplies power to B.** Losing A means B is on battery or dying.

`carries(A → B)` — **A transports B's NUT or control path.** Losing A means B still has power but is
no longer observable or commandable.

They produce opposite planner behavior. `feeds` drives urgency and domain membership: it is how the
operator knows a power event reaches this node at all. `carries` drives ordering — a carrier is
sequenced *after* the things depending on it, because a switch that dies early takes the operator's
view of everything behind it.

Conflating them makes the compiler unable to tell "shut this down urgently" from "shut this down
last." If you are unsure which one a cable is, ask what breaks when it is removed: power, or
visibility.

## `feeds` needs to say which input

Anything with two power supplies records which physical input each edge lands on. A `feeds` edge
without an input qualifier is a hard `FeedInputRequired` error, not a warning.

This is the field people skip, and it is load-bearing: a node fed from two domains behaves
fundamentally differently from a singly-fed node, and feasibility cannot reason about redundancy
without knowing whether two edges are two real supplies or one supply recorded twice.

## What gets derived from it

- **Power domains** — the transitive closure of `feeds` from each UPS. Named on the UPS, membership
  computed. A node reachable from two UPS roots belongs to both, with no override syntax.
- **Domain-scoped triggers** — a trigger naming a domain selects devices by computed membership, not
  by labels you maintain in parallel.
- **Partial-domain scope** — an outage in one domain prunes only groups *proved* wholly outside it.
  Ambiguous and mixed-domain groups are retained, because a group the planner cannot confidently
  place is a group it must not drop.

A cyclic `feeds` graph is rejected with a `FeedsCycle` diagnostic before any of that runs. A UPS
cannot ultimately feed itself, so a cycle is never a real topology.

## Shared service paths

Node `carries` edges describe how a node remains reachable. Also declare the shared paths the
operator needs throughout a flow: the operator-to-Kubernetes-API path and the NUT telemetry path.
Use inventory entity IDs, not URLs, Kubernetes Service names, or private addresses:

```yaml
spec:
  communicationPaths:
    - service: OperatorAPI
      entities: [control-switch]
    - service: NUT
      entities: [telemetry-switch, nut-host]
```

This is a fragment of a `ShutdownFlow`. The entities must exist in resolved inventory. List all
required endpoints and carriers, including the hosts supplying those services where appropriate;
their upstream `carries` paths and supplying UPS domains are followed automatically. The operator
does not discover routes or infer redundancy. Every listed entity is required, and every action
depends on these paths, including notifications and other work without node targets.

If a shared-path UPS loses power, work depending on that path stays in the plan even if its own UPS
is healthy. Its runtime also constrains the plan. A flow that releases a shared carrier must put
that release last. Under `Overlap`, carrier release waits for outstanding work to stop first.
Preserving service pods and control-plane quorum is distinct from modeling the network path.

Missing paths produce warnings and appear as `Unmodeled` in
`status.publishedArtifact.communicationBudget.coverage`. Declare a deliberate exception explicitly:

```yaml
spec:
  communicationPaths:
    - service: NUT
      exempt: true
```

For individual nodes, use `PowerInventoryNode.spec.communicationPathExempt: true`. Exemptions
acknowledge omitted coverage; they do not assert independent power or reachability, and a node
exemption does not erase existing `carries` dependencies. Do not add exemptions just to clear a
warning. With incomplete shared-service coverage, node-less work conservatively budgets all
modeled carriers. With both services declared or explicitly exempted, it uses the declared paths.

The published artifact includes supply roots, unknown supply, unresolved action targets, and
coverage state. These are modeling facts, not proof of an actual working network path.

## Keep it small

An attribute exists in this model only if a planner rule consumes it. Rack position, site, tenant,
cable type, port count, and serial number stay in whatever system already tracks them. If you want to
add a field, name the planner rule that reads it — that is the test.

## Worked examples

- [Orion cluster](../examples/orion-cluster/README.md) — one fully authored topology and flow.
- [Simulation scenarios](../examples/simulation/README.md) — including a multistage layout where a
  UPS feeds a PDU that feeds a rack.

## Importing from NetBox

If NetBox already owns the wiring record, use
[Importing topology from NetBox](import-netbox-inventory.md) instead of hand-writing these CRs.
The importer is read-only against NetBox and renders ordinary `UPSDevice`, `PowerInfrastructure`,
`PowerInventoryNode`, and `PowerInventoryEdge` YAML. The operator still consumes Kubernetes
resources during shutdown; NetBox is not queried on the failure path.

## Then

[Assign shutdown tiers](assign-shutdown-tiers.md) to the workloads and nodes this topology now covers.

Full contract: [inventory-provider-contract.md](../contributing/design/inventory-provider-contract.md).
