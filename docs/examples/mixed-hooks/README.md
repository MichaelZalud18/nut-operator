# Mixed agents and advisory external shutdown

Components: Modular Deployment Profiles; Planning & Execution Logic.
Audience: operators and contributors.

This extends the authored [quickstart](../quickstart/README.md) inventory and flow with
an explicitly named external host. It composes the existing operator installation and hooks;
it does not install or implement an external host-shutdown service. `.test` endpoints are
placeholders to replace with your service, which must implement the
[receiver contract](receiver-contract.md).

The example's policy is deliberate: request `external-a` to stop on **either** quickstart
power-domain trigger, before worker draining. This is a global advisory action, not an inferred
association between that host and one UPS. If a host should stop only for a particular UPS,
place its hook in a flow with only that domain's triggers and review overlap with other flows.
Do not copy a host-specific hook into a multi-domain flow and expect automatic pruning.

Keep the quickstart agents in DryRun/Simulate. Add this group to its flow before the
worker drain group, and retain the existing built-in AgentShutdown groups:

```yaml
- name: external-host-a
  action: RunHook
  hookRef: {namespace: quickstart-power, name: external-host-a}
  shutdownTier: 3
  before: [drain-workers]
  timeout: 10s
```

The companion [hook](hook.yaml) supplies the literal host identity. There is no implicit
fanout, node selector or agent for `external-a`. Do not create a Kubernetes Node or
PowerInventoryNode for it. A separate hook/group can address another host; a receiver may
instead accept an explicitly named external group whose membership it owns.

In the existing quickstart PowerManagementCluster, add the following under `spec`, replacing
the hostname consistently in the allowlist and both URLs in `hook.yaml`:

```yaml
hooks:
  allowedEndpoints:
    - scheme: https
      host: hooks.example.test
      port: 443
      pathPrefix: /hosts
```

Supply `quickstart-power/external-hook-auth`, key `token`, through your secret workflow. Its
value is the complete Authorization header expected by the receiver, such as a bearer header.
Do not place the value in this YAML, hook data or command-line arguments. The receiver must
serve a certificate trusted by the manager; preserve hostname/CA verification.

Keep the receiver and its network available through hook delivery. Use explicit `before` or
`requires` relationships to any authored groups that stop those dependencies. Existing
`spec.communicationPaths` supports shared `OperatorAPI` and `NUT` paths; it has no `Hook`
service value or automatic mapping from a hook URL to external infrastructure. Document that
external path in your site's topology rather than inventing a Hook field or Kubernetes Node.

After editing the declarations and provisioning the Secret, validate the hook against admission:

```sh
kubectl apply --dry-run=server -f docs/examples/mixed-hooks/hook.yaml
```

Apply the edited management-cluster settings, hook and flow through the normal configuration
workflow. Keep flow and agents in DryRun/Simulate while checking the compiled order and rehearsal
receipt. This example does not grant approval to enable real node or external-host actuation.

Dry-run without `spec.dryRun` sends nothing. The provided explicit rehearsal invocation
does send a request: its receiver must implement a harmless rehearsal operation and check
`data.dryRun`. A 2xx reply records delivery, not confirmed host power-off. A timeout or
failure marks the flow degraded and later groups continue, including built-in shutdown.
This composition is unsuitable for a mandatory “external host halted before node release” gate.

The receiver must make `ensure-stopped` repeat-safe using actual host/job state. CloudEvent
IDs include invocation time and are not a durable per-host deduplication key across retries
or new executions. Host start/rearm policy and durable operation identity belong to the
receiver; a permanent in-memory “already stopped” flag is only a test illustration.

External host identity in static data has no automatic power-domain membership. Global
hook groups can survive flow domain pruning. Scope flows and hook declarations deliberately;
do not infer affected external hosts from Kubernetes inventory or `selectedUPSDevices` alone.

Repeatable checks:

```sh
go test ./test/quickstart -run TestMOD2 -count=1
go test ./internal/kubeactions -run TestMOD2 -count=1
make test-modular-components
```

The first compiles this hook into the authored quickstart flow without an external Node
or agent. The second uses the production HTTP runner with an in-process fake transport
and executor to check auth, allowlisting, rehearsal, repetition and advisory failure/timeout.
Neither proves a deployed receiver, real host shutdown, admission/RBAC or network reachability.

The owned Kind logical ShutdownFlow scenario additionally exercises a harmless receiver beside
the real Simulate agent, Secret headers, network ingress, rehearsal/default dry-run, repeated
delivery, advisory failure/timeout, allowlisting and PostgreSQL ordering evidence. It reuses the
suite's pinned Python fixture image, with no host privileges or Kubernetes token. HTTP is confined
to that isolated fixture; the external integration example retains HTTPS. Run it through the
owned runner, which creates and removes its own cluster and private kubeconfig:

```sh
python3 -B hack/test-kind.py --focus 'logical ShutdownFlow'
```

Execution results belong in the [MOD-2 completion record](../../tasks-completed.md#modular-deployment-profiles);
the command's presence is not evidence that a particular checkout or image passed.
