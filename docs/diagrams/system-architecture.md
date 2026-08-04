# System Architecture Diagram

Components: Cross-cutting.

This diagram shows the finalized service shape for `nut-operator`: Kubernetes resources are the
interface, the operator compiles and publishes planning facts, PostgreSQL stores durable history,
and node-local agents own the final host boundary.

```mermaid
flowchart TD
  subgraph Interface["Primary interface"]
    GitOps["GitOps manifests"]
    Kubectl["kubectl, CR status, Events, logs"]
  end

  GitOps --> CRDs["power.zalud.io CRDs"]
  Kubectl --> CRDs

  subgraph API["Declarative API"]
    PMC["PowerManagementCluster"]
    UPS["UPSDevice"]
    ServerAPI["NUTServer"]
    AgentAPI["NodePowerAgent"]
    FlowAPI["ShutdownFlow"]
    Inventory["PowerInventoryNode / PowerInventoryEdge / PowerInfrastructure"]
    Profiles["UPSCapabilityProfile catalog"]
  end

  CRDs --> API

  subgraph Operands["Managed operands"]
    NUT["NUT server pods / upsd"]
    AgentDS["NodePowerAgent DaemonSet"]
    Upsmon["upsmon container"]
    Actuator["node-actuator container"]
  end

  UPS -->|"network NUT / SNMP / relay telemetry"| NUT
  ServerAPI --> NUT
  AgentAPI --> AgentDS
  AgentDS --> Upsmon
  AgentDS --> Actuator

  subgraph ControlPlane["Operator control plane"]
    Telemetry["Telemetry normalizer"]
    Resolver["Inventory and capability resolver"]
    Planner["ShutdownFlow planner"]
    Publisher["Artifact publisher"]
    Executor["Execution engine"]
    Actions["Kubernetes action runners"]
  end

  NUT --> Telemetry
  UPS --> Telemetry
  Inventory --> Resolver
  Profiles --> Resolver
  Telemetry --> Planner
  Resolver --> Planner
  FlowAPI --> Planner

  Planner --> Publisher
  Planner --> Executor
  Executor --> Actions
  Actions -->|"scale, cordon, drain, workflow hooks"| Kubernetes["Kubernetes workloads and nodes"]
  Executor -->|"release and signal handoff evidence"| Actuator
  Actuator --> Host["host shutdown boundary"]

  subgraph Published["Published planning facts"]
    Status["CR status"]
    Artifacts["compiled plan, dependency graph, waves, progress, explanations"]
    Diagrams["Mermaid, Graphviz/DOT, D2"]
    Audit[("PostgreSQL / CNPG audit store")]
  end

  Publisher --> Status
  Publisher --> Artifacts
  Publisher --> Diagrams
  Publisher --> Audit
  Executor --> Audit

  Published --> Subscribers["External subscribers: monitoring, docs, dashboards, recovery orchestration"]
```

The operator owns planning and shutdown execution. Recovery, dashboards, documentation generators,
and other automation consume the published facts instead of becoming part of the core project.
