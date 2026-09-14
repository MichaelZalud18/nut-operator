# User Stories

Audience: product design and contributors.

These stories capture requested user outcomes, not approved implementation designs or supported
installation profiles. Investigations are tracked as `MOD-1` through `MOD-3` in
[project tasks](../../tasks.md#modular-deployment-profiles). Implementation and release placement
remain undecided; the stories do not override current safety contracts.

## US-1: Existing NUT, Shutdown Agents Only

As an administrator with existing network NUT servers and a small Kubernetes cluster, I want to
install only the node-shutdown components, so I can protect my cluster without deploying another
NUT server or adopting the planner.

Desired outcomes:

- Document the minimum installation and every mandatory dependency.
- Connect agents to existing NUT endpoints and credentials without requiring a managed relay.
- Expose configuration and shutdown control through a documented, authorized API.
- Preserve dry-run behavior, explicit actuation approval, and least-privilege separation.

Decision to resolve: whether shutdown is authorized by local NUT conditions, an external caller,
or explicitly selectable modes. Autonomous shutdown must not become an implicit fallback for
failed orchestration. This would revisit the current OD-37 boundary, not silently bypass it.

Current context: NodePowerAgent is configured through Kubernetes CRDs. The actuator consumes an
operator-issued projected Secret and exposes no network listener or Kubernetes API credentials.
That internal handoff is not yet a supported standalone external-control API.

## US-2: Orchestration With Custom Host Actuation

As an administrator with my own host-shutdown system, I want the operator to orchestrate the
shutdown sequence while delegating each host's actuation to my existing system, so I can keep
my established shutdown mechanisms.

Desired outcomes:

- Select built-in or custom actuation per host or host group.
- Define authentication, targeting, deadlines, failure reporting, and repeat-safe requests.
- Distinguish request acceptance from evidence that the host actually stopped.
- Support integration without granting arbitrary host privileges to the orchestration component.

Current context: built-in policies cover Linux PowerOff, TalosShutdown, Simulate, and Disabled.
ShutdownHooks already offer HTTP and Kubernetes integration points for calling an existing shutdown
system without a DaemonSet. Start by verifying that composition, not assuming a new actuator API is
needed. Hooks report bounded delivery, not confirmed shutdown, and failures are advisory.
`PowerInventoryNode` describes Kubernetes nodes; arbitrary external-host mapping and per-host hook
dispatch need verification rather than assuming manual inventory provides them. Exactly-once
delivery and restart/resume continuity are not implied by this story.

## US-3: NUT Aggregation and Agents With External Planning

As an administrator with my own planning system, I want to use NUT aggregation and node agents
without the built-in planner, so my system can determine shutdown order while reusing those
components.

Desired outcomes:

- Install aggregation and agents independently of planning-specific dependencies.
- Consume telemetry through a documented interface and submit authorized shutdown requests.
- Avoid synthesizing internal compiled plans or writing internal signal Secrets directly.
- Keep authorization and actuation safeguards when the planning implementation is external.

Current context: upstream NUT relaying exists, but the authorized shutdown path is coupled to
ShutdownFlow/executor operation. The needed boundaries are telemetry, planning, execution, and
actuation; these stories do not prescribe separate microservices or a new public resource schema.

See [scope boundaries](scope-boundaries.md), [node agent](node-agent-operand.md),
[upstream relay](upstream-nut-relay.md), and [shutdown hooks](shutdown-hooks.md) for existing contracts.
