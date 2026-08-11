# Pre-Shutdown Hook Transport

Status: findings record and design direction, 2026-08-06, extended 2026-08-09. The direction below is
now designed in [shutdown-hooks.md](../design/shutdown-hooks.md) (`HK-1`–`HK-10`), and the scope limit
is recorded as `SB-15`.

Components: Planning & Execution Logic.

Examines the `RunWorkflow` action's claim to be engine-neutral, and what a hook that reaches
non-Kubernetes systems would have to look like. Findings continue the shared `F-n` namespace from
F-43.

## The user-facing need

Ordinary and already expressible: run a workload's own pre-shutdown routine — a database snapshot, a
quiesce — at a high tier while the workload keeps serving, then shut it down at a lower tier. Two
groups at two tiers says exactly that. Ordering is not the problem. The hook is.

## Findings

**F-44 · `RunWorkflow` is parameterized for any GVK and hardcoded to Argo.** `workflowObject` takes
`workflow.apiVersion` and `workflow.kind`, defaulting to `argoproj.io/v1alpha1 Workflow`, which reads
as engine-neutral. It then always builds an Argo-shaped body — `workflowTemplateRef`, `entrypoint`,
`serviceAccountName`, `arguments.parameters` — and the operator's RBAC grants only
`argoproj.io/workflows`.

So a non-Argo target is nominally addressable and practically unusable: the object is created with
fields the target CRD does not define, and the operator lacks permission to create it anyway. The
parameterization advertises a capability that does not exist, which is the same shape as `F-25` and
`F-33`.

## Direction decided (2026-08-06)

**Prefer a transport-generic hook over Kubernetes-native ones.** Custom hooks are most likely to
target things the operator does not manage and Kubernetes cannot address — a NAS, a bare-metal
database, a hypervisor, a switch. None of those have a CRD, so a Kubernetes-object hook cannot reach
the systems that most need one.

Proposed shape, to be designed before building:

- **A dedicated `ShutdownHook` resource** referenced by name from a group, rather than today's
  `params` map. Reusable across flows, reviewable in Git on its own, and it keeps `ShutdownFlow`
  readable — the "write the invocation elsewhere and reference it" shape.
- **HTTP delivery with a CloudEvents-shaped body as the primary transport**, carrying execution ID,
  plan hash, flow, group, tier, wave, and trigger context. This is the interop lingua franca: Tekton
  emits CloudEvents to a configured sink, Alertmanager posts to webhook receivers, Argo Events
  consumes them. Anything that accepts an HTTP POST becomes reachable, in or out of the cluster.
- **A Kubernetes-object transport as a second option**, taking a user-supplied object body so any GVK
  works. `batch/v1` `Job` needs no engine at all, and Argo becomes one example rather than the
  assumption.
- **Failure-path constraints**, which are what separate this from an ordinary notifier: hooks are
  declared ahead of time (`GP-5`, nothing discovered mid-outage); every call is bounded by a short
  timeout; a failed or slow hook degrades and is recorded but never holds the wave; secrets are
  referenced, never inline; TLS verification stays on; and outbound endpoints likely want an
  allowlist on `PowerManagementCluster` per `GP-2`, since this would be the operator's only outbound
  egress to arbitrary hosts.

## Waiting on a hook — deliberately undecided

Whether the executor blocks on a hook's completion is TBD. What **is** decided: the default is that
shutdown proceeds anyway. A hook that has not finished never becomes a reason to keep nodes up while
battery runtime drains — a one-hour snapshot may simply not be affordable during the outage it was
meant to protect against. Any future waiting mechanism is opt-in, bounded, and must state what
happens when the budget runs out first.

## Dry-run — needs a deliberate answer

The runner is never invoked in dry-run (`internal/executor`'s `if !dryRun` guard), so hooks
structurally cannot fire. That is correct, and it means dry-run currently proves nothing about a
hook: a wrong URL, a rotated credential, or an endpoint that has moved is discovered during the
outage.

At minimum, dry-run should record the request it *would* have sent. The candidate worth designing
against: let the hook declare its own dry-run invocation alongside the real one, so the author
decides what a safe rehearsal means for their system — a different endpoint, the same endpoint with a
rehearsal flag, or a read-only call that proves reachability and auth without side effects. That
keeps the operator out of guessing which calls are safe to repeat, which it cannot know for a system
it does not manage.

## Scope boundary to record

Owning workflow orchestration is out of scope (`GP-4`, `GP-7`): the operator invokes a hook and
publishes the fact, and never becomes the engine that runs it. Recorded as `SB-15` in
`docs/design/scope-boundaries.md`.
