# Shutdown Hooks

Status: design, 2026-08-10. Answers the direction recorded in
[pre-shutdown-hook-transport.md](../audits/pre-shutdown-hook-transport.md) (`F-44`) and bounded by
`SB-15`.

Components: Planning & Execution Logic.

`HK-n` identifiers are stable and are not reused or renumbered.

## The problem

A system that is about to lose power often wants to do something first: snapshot a database, quiesce
a filesystem, flush a NAS write cache, park a VM. Ordering is already expressible — two groups at two
tiers says "run this while still serving, then stop it" exactly. The hook is what is missing.

Today `RunWorkflow` looks engine-neutral and is not (`F-44`). It takes `workflow.apiVersion` and
`workflow.kind`, then always builds an Argo-shaped body — `workflowTemplateRef`, `entrypoint`,
`arguments.parameters` — and the operator's RBAC grants only `argoproj.io/workflows`. A non-Argo
target is nominally addressable and practically unusable: the object is created with fields the
target CRD does not define, and the operator could not create it anyway.

That is the same defect class as `F-25` and `F-33` — a field that advertises a capability that does
not exist — and it is worse here, because the discovery happens during an outage.

## The shape of the answer

**HK-1 · A hook targets systems Kubernetes cannot address.** This is the decision everything else
follows from. The systems most likely to need a pre-shutdown routine are the ones the operator does
not manage: a NAS, a bare-metal database, a hypervisor, a switch. None of those has a CRD, so a
Kubernetes-object hook cannot reach the systems that most need one. HTTP is therefore the primary
transport and the Kubernetes object is the special case, not the reverse.

**HK-2 · Hooks are their own resource.** A `ShutdownHook` is referenced by name from a group rather
than expanded inline in `params`. It is reusable across flows, reviewable in Git on its own, and it
keeps `ShutdownFlow` readable — the "write the invocation elsewhere and reference it" shape the rest
of the API already uses for capability profiles and credentials.

**HK-3 · HTTP with a CloudEvents-shaped body is the primary transport.** It is the interop lingua
franca: Tekton emits CloudEvents to a configured sink, Argo Events consumes them, Alertmanager
receivers accept the same POST. Anything that accepts an HTTP POST becomes reachable, in or out of
the cluster, with no CRD and no operator-side plugin.

The body carries what a receiver needs to act and to correlate afterward: execution ID, plan hash,
flow, group, tier, wave index, trigger reason, and the power observation at the moment of the call.
Those are the same identifiers the audit trail uses, so a receiver's own logs join to the operator's
records without a translation step.

**HK-4 · A Kubernetes-object transport is the second option.** It takes a user-supplied object body,
so any GVK works and `batch/v1` `Job` needs no engine at all. Argo becomes one example rather than the
assumption. RBAC is the honest constraint here: the operator can only create what it has been granted,
so this transport names the permission it needs and fails validation rather than at execution when it
does not have it.

## Failure-path constraints

These are what separate a shutdown hook from an ordinary notifier. Each exists because the call
happens while a battery drains.

**HK-5 · Declared ahead of the outage** (`GP-5`). Endpoints, bodies, credentials, and timeouts are
authored structural input. Nothing about a hook is discovered, resolved, or assembled mid-outage.

**HK-6 · Bounded, and the bound is the flow's to set.** Every call carries a timeout declared by the
author and compressed with every other declared duration (`EX-11`). What a system can be given in a
relaxed flow is not what it can be given when the flow is racing.

**HK-7 · A hook never holds a wave.** A failed or slow hook degrades, is recorded, and the flow
continues. A one-hour snapshot may simply not be affordable during the outage it was meant to protect
against, and the operator does not get to decide that the snapshot matters more than the cluster
shutting down cleanly.

Whether an opt-in *waiting* mode should exist is deliberately open (`OD-33`). What is already
decided: the default is that shutdown proceeds, and any waiting mechanism must be opt-in, bounded,
and must state what happens when the battery budget runs out first.

**HK-8 · Secrets are referenced, never inline.** Credentials come from a `Secret` reference, matching
`credentialSecretRef` elsewhere in the API. TLS verification stays on; there is no
`insecureSkipVerify`.

**HK-9 · Outbound endpoints are allowlisted on `PowerManagementCluster`** (`GP-2`). This is the
operator's only outbound egress to arbitrary hosts, and it is reachable from a namespaced resource a
workload author controls. The allowlist makes the blast radius reviewable in Git before an outage
rather than discoverable after one.

## Dry-run

`EX-5` makes dry-run a faithful rehearsal of everything except effects, and hooks are exactly where
that is hardest: the runner is not invoked in dry-run, so a wrong URL, a rotated credential, or an
endpoint that moved is discovered during the outage.

**HK-10 · A hook declares its own rehearsal.** The author supplies a dry-run invocation alongside the
real one — a different endpoint, the same endpoint with a rehearsal flag, or a read-only call that
proves reachability and auth without side effects. The operator cannot know which calls are safe to
repeat against a system it does not manage, so it does not guess; the author states it.

At minimum, dry-run records the request it *would* have sent, so a rehearsal proves the invocation is
well-formed even when no rehearsal endpoint is declared.

## What this is not

`SB-15` holds the line: the operator invokes a hook, bounds it, and publishes what happened. Retries,
backoff, DAGs, branching, artifact passing, and templating from prior results are workflow-engine
concerns. Reaching a real engine stays fully supported — that is what HK-3 is for.

## Open decisions

**OD-33 · Hook waiting.** Whether an opt-in bounded wait on hook completion should exist, and what
happens when the runtime budget expires first. Default is decided (proceed); the mechanism is not.

**OD-34 · Hook failure and abort policy.** Whether a failed hook can ever engage `abortPolicy`, or
whether it is always advisory. HK-7 says it never holds a wave, which is not the same question as
whether it can mark the flow degraded.

## Until this is built

`RunWorkflow` stops advertising neutrality it does not have. Admission rejects a `workflow.apiVersion`
or `workflow.kind` the operator has no RBAC to create, naming the GVK it is limited to and pointing
here. That is the `F-25`/`F-33` remedy applied consistently: a field that cannot do what it claims is
either implemented or refused, never left to fail quietly in the failure path.
