# Orion Cluster Example

Components: Foundation & Documentation.

This example mirrors a controller, standard, and burst node topology without embedding private addresses, hostnames, or credentials.

The important design shape is:

- `orion-controller`: the controller or control-plane node group, shut down last.
- `orion-standard`: the steady-state worker group, drained and powered down during conservation.
- `orion-burst`: optional burst capacity, shed first when present.
- `orion-core`: the physical power domain covered by a network-reachable UPS and NUT server.

Expected node labels:

```yaml
power.example.com/controller: "true"
power.example.com/node-group: controller
power.example.com/node-group: standard
power.example.com/node-group: burst
power.example.com/power-domain: orion-core
```

Expected namespace or workload labels:

```yaml
power.example.com/shutdown-tier: "4" # ordinary applications
power.example.com/shutdown-tier: "3" # data workloads
power.example.com/shutdown-tier: "2" # storage and late infrastructure
```

The conservation flow uses dependency edges as the source of truth:

```text
burst-capacity -> application-workloads -> data-workloads -> storage-services -> standard-nodes -> controller-node
```

Apply order for a real cluster would be the management cluster, network UPS and NUT server, node agents, then the flow.
