# Published Planner Artifacts

Components: Outputs & Publishing.

`nut-operator` is a planner and executor that publishes rich planning artifacts. It is not a
dashboard product.

## Interface Boundary

There is no dedicated UI for v1. The primary interface is Kubernetes:

- CRDs declare desired state.
- GitOps manages configuration changes.
- `kubectl` is sufficient for ordinary operations.
- CR status, Kubernetes Events, controller logs, and PostgreSQL audit records expose current state
  and history.

A future UI is a separate subscriber. It consumes the same artifacts as every other integration and
does not participate in reconciliation, planning, or execution.

## Published Facts

The operator publishes facts, not external commands:

- Current execution state.
- Compiled execution plan.
- Dependency graph.
- Shutdown waves.
- Advisory startup wave projection.
- Wave progress.
- Planner explanations and diagnostics.
- Edge explanations and provenance.

The planner owns graph construction and explanation. The executor owns execution progress and
evidence. The publisher makes those facts available through Kubernetes status, Events, logs, and
PostgreSQL records.

## Explanations

Every dependency explains itself.

Examples:

- Declared: `applications requires databases` because the `ShutdownFlow` author declared it.
- Derived: `node-a depends on switch-2` because inventory says `switch-2` carries the node's
  control path.
- Policy: `control-plane-1 stays late` because quorum policy requires a minimum viable control
  plane until terminal waves.

These explanations travel with the graph artifact so users and external systems can answer "why"
without log archaeology.

## Subscribers

Subscribers consume published artifacts. They do not own shutdown planning.

Examples:

- Recovery orchestration.
- Dashboards.
- Documentation generators.
- Monitoring systems.
- Future automation.

The boundary is:

> `nut-operator` owns planning and shutdown.
>
> Other systems consume the published plan.

Recovery is a subscriber concern. The operator may publish advisory startup wave projections, but it
does not execute recovery or become the bring-up controller.

## Visualization

Graph visualization is exported, not hand-built into a frontend:

- Mermaid.
- Graphviz/DOT.
- D2.

AI-assisted diagramming tools can consume those exports or the structured artifact. Rendered
diagrams are views of the plan, not sources of truth.
