# Preparing the hardware

Components: Inventory System, NUT Server / upsd.

**The decision:** what is actually plugged into what, and can the cluster see the UPS.

This is the only step with no software fallback. Everything downstream — domains, tiers, waves — is
computed from what you record here, and the operator has no way to check your answer against the
room. A miswired assumption produces a plan that compiles cleanly, reads correctly, and shuts down
the wrong things.

## Before the cluster is involved

**The UPS must be reachable over the network.** Local USB and serial attachment are deliberately
unsupported — they would require host device mounts and privileged operand pods, which is a posture
this project declines ([security](../reference/security.md)). In practice that means a UPS with a
network management card, or an appliance that already runs its own NUT server.

Two ways to connect, and you need to know which you have:

- **A direct NUT driver**, from the reviewed allowlist — `snmp-ups` for SNMP-capable management
  cards, `netxml-ups` for Eaton/MGE Network Management Card XML, `apcupsd-ups` for an existing
  apcupsd daemon, and `dummy-ups` for simulation. A driver outside that list is rejected at
  admission rather than passed through.
- **An upstream NUT relay**, for a NAS or appliance already exposing `upsd`. See
  [upstream-nut-relay.md](../design/upstream-nut-relay.md).

**Confirm the UPS answers before you model anything.** If the operator cannot poll it, no plan you
write will ever fire. Reachability, credentials, and the driver choice are all easier to debug now
than mixed in with a compile failure.

## What to write down, while you can still see the cables

For every machine in the cluster, and every piece of equipment between it and a UPS:

- **Which UPS feeds it**, and through what — directly, or via a PDU, or via a second PDU.
- **Which physical input** the power lands on, for anything with two supplies. This is not optional
  detail: dual-fed equipment behaves fundamentally differently from singly-fed equipment, and the
  planner rejects a `feeds` edge that does not say which input it terminates on.
- **What carries its network path** to the UPS and to the control plane. A switch between the
  operator and the UPS is a hard dependency of the entire pipeline — lose it and the operator cannot
  observe the power event at all.
- **Anything not on a UPS.** Not-UPS-backed is a legitimate answer, and it has to be recorded as one.

The distinction that matters most while you are looking at the rack: **a thing that powers something
else, versus a thing that carries its network.** Those become two different edge types and drive
opposite planner behavior. Getting them backwards is the mistake to look for.

## Checking what you recorded

The operator gives you two checks and neither is a substitute for walking the room.

- **The orphan rule.** Every Kubernetes node must be reachable from at least one UPS through `feeds`
  edges, or carry an explicit exemption marker. A node that is neither is a hard validation failure,
  not a warning. This exists because the real risk of derived membership is one unrecorded cable
  silently dropping a node out of every domain — no trigger covers it, and it hard-drops during an
  outage with nothing in any log.
- **The communication-path warning.** A node with no modeled `carries` path raises a diagnostic and
  increments `nutoperator_inventory_communication_path_unmodeled_nodes`. Deliberately weaker than the
  orphan rule: a node with no modeled comms path can still be planned, it just loses communication
  ordering.

Neither check can tell you that a `feeds` edge points at the wrong UPS. Only you can.

## Then

[Model your topology](modeling-your-topology.md) — turning this into `feeds` and `carries` edges.

Contract and rules: [inventory-provider-contract.md](../design/inventory-provider-contract.md).
