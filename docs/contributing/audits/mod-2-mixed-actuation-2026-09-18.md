# MOD-2: mixed built-in and external host actuation

Components: Modular Deployment Profiles; Planning & Execution Logic.
Date: 2026-09-18.
Scope: research against `cf599655ab54b1522663774d508f0b6453a8ab11` plus the shared working
tree, including pre-existing planner refactoring. No deployed receiver or VM was exercised.
Task owner: [MOD-2](../../tasks.md#modular-deployment-profiles).

## Recommendation

Reuse ShutdownHook and explicit RunHook groups for advisory integration with an existing
host-shutdown system. Do not introduce another actuator API for that case. Existing agents
can handle Kubernetes nodes in the same flow. The external receiver owns its host identity,
authentication, repeat-safe operation, job state and actual actuation.

This does not fulfill a requirement to confirm an external host has halted before another
node is released. That stronger requirement needs a separately approved completion/failure
contract; changing hooks to block or abort would contradict OD-33/OD-34.

## Source trace and boundaries

| Concern | Evidence and consequence |
| --- | --- |
| Invocation | `internal/kubeactions/runner.go:runHook/runHTTPHook` sends one invocation per group; no node enumeration or automatic fanout |
| Targeting | HTTP data is copied into `data.extensions`; use an explicit host or receiver-owned group |
| Auth | Headers come from referenced Secrets; URL must pass the owning PowerManagementCluster allowlist; redirects are rejected |
| Dry-run | Without a declared rehearsal invocation, validate and record only. With one, contact the author-declared endpoint with dryRun evidence |
| Result | HTTP 2xx is delivery success. No polling of an external job or verification of host power state follows |
| Failure | `internal/executor/executor.go:executeGroup` records hook failure/degradation and returns without engaging abortPolicy |
| Repetition | Event ID contains execution ID, group and current nanoseconds; repeated operations can have different IDs |
| Inventory | PowerInventoryNode identifies a Kubernetes Node. A physical PowerInfrastructure entity can describe topology, but does not bind a hook to external-host release semantics |
| Power scope | `shutdownflow.PlannerGroupNodes` has no membership for a static hook without a node target. Planner pruning retains global groups; static host data is not a power-domain selector |
| Communication | Explicit group dependencies and site network design must keep the receiver available; `communicationPaths` has no Hook service value, and static host data cannot infer these dependencies |

CloudEvents supplies an event envelope and event identity, not a host-completion protocol.
The receiver must supply that application behavior. [CloudEvents specification](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md).

## Bounded experiments

The [public example](../../examples/mixed-hooks/README.md) extends the existing authored
quickstart inventory and flow. `test/quickstart/mod2_research_test.go` reads its hook YAML,
adds an explicit tier-3 hook before worker draining, and compiles four ordered waves.
Built-in worker/control releases remain valid; the hook has no synthetic Kubernetes membership.

`internal/kubeactions/mod2_research_test.go` uses the production runner, fake Kubernetes
client and an in-process HTTP transport acting as the receiver. It proved:

- Static external host data and Secret-backed authentication reach the receiver.
- Default dry-run makes zero requests; explicit rehearsal makes a request with no fake effect.
- Two enforce invocations produce two deliveries but one fake ensure-stopped effect.
- Removing the endpoint allowlist rejects delivery before contacting the receiver.
- HTTP 503 and context timeout both mark the executor degraded while its second, Wait group
  completes. Hook success is therefore not a prerequisite for subsequent work.

The compile fixture demonstrates mixed agent/hook planning. The executor fixture uses a
harmless Wait after the hook, not a real agent release. Its in-memory stopped flag illustrates
repeat safety only; durable state, host rearm/start cycles and authenticated real TLS require
receiver-specific integration. Direct runner tests do not prove admission, RBAC or flow approval.

Validation passed:

```sh
go test ./internal/kubeactions ./internal/executor ./test/quickstart
```

This also covers existing redirect rejection, CloudEvent payload and dry-run behavior.
No external endpoint, host action, Kind cluster or Hadron/Talos task was touched.

## Decisions and follow-through

MOD-2 remains open for the user-story boundary: advisory external work versus a hard
confirmed-halt dependency. If advisory is sufficient, promote the example after defining its
receiver integration and testing actual network/auth/rehearsal behavior. If hard gating is
required, first design external identity, affected-power membership, completion evidence,
timeouts and abort semantics together. Do not silently extend RunHook or fake Kubernetes Nodes.

Use separate scoped flows or explicit per-host/group declarations for now. Do not claim
automatic per-host dispatch, external-node release accounting or exactly-once execution.
