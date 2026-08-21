# Shutdown Hooks

Status: implemented, 2026-08-14. Answers the direction recorded in
[pre-shutdown-hook-transport.md](../audits/pre-shutdown-hook-transport.md) (`F-44`) and bounded by
`SB-15`.

Components: Planning & Execution Logic.
Audience: contributors.

`HK-n` identifiers are stable and are not reused or renumbered.

## The problem

A system that is about to lose power often wants to do something first: snapshot a database, quiesce
a filesystem, flush a NAS write cache, park a VM. Ordering is already expressible — two groups at two
tiers says "run this while still serving, then stop it" exactly. The hook is what is missing.

The removed `RunWorkflow` action looked engine-neutral and was not (`F-44`). It took
`workflow.apiVersion` and `workflow.kind`, then always built an Argo-shaped body —
`workflowTemplateRef`, `entrypoint`, `arguments.parameters` — and the operator's RBAC granted only
`argoproj.io/workflows`. A non-Argo target was nominally addressable and practically unusable: the
object was created with fields the target CRD did not define, and the operator could not create it
anyway.

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

**HK-6 · Bounded, and the bound is the hook's to set.** Every invocation can carry its own timeout.
If it does not, `PowerManagementCluster.spec.hooks.defaultTimeout` applies, then the built-in 10s
default. The executor still compresses the effective timeout with every other declared duration
(`EX-11`). What a system can be given in a relaxed flow is not what it can be given when the flow is
racing.

**HK-7 · A hook never holds a wave.** A failed or slow hook degrades, is recorded, and the flow
continues. A one-hour snapshot may simply not be affordable during the outage it was meant to protect
against, and the operator does not get to decide that the snapshot matters more than the cluster
shutting down cleanly.

`OD-33` closes this for v1alpha1: there is no hook-completion wait mode. The timeout bounds the
delivery attempt only. `OD-34` closes failure behavior: hooks are advisory, and a failed or timed-out
hook marks the owning flow degraded without engaging `abortPolicy`.

**HK-8 · Secrets are referenced, never inline.** Credentials come from a `Secret` reference, matching
`credentialSecretRef` elsewhere in the API. TLS verification stays on; there is no
`insecureSkipVerify`.

**HK-9 · Outbound endpoints are allowlisted on `PowerManagementCluster`** (`GP-2`). This is the
operator's only outbound egress to arbitrary hosts, and it is reachable from a namespaced resource a
workload author controls. The allowlist makes the blast radius reviewable in Git before an outage
rather than discoverable after one.

**HK-11 · A `ShutdownHook` has no status.** The kind carries a spec and nothing else, and no
controller reconciles it.

A hook is a declaration, not a workload. There is no operand behind it and nothing to drift, so
admission is where a malformed hook is told it is wrong. The two things a status could plausibly
carry both belong elsewhere: hook *health* would mean probing declared endpoints on a schedule,
which `GP-4` excludes — the operator consumes signals from existing monitoring rather than becoming
monitoring — and hook *outcome* is already published on the owning `ShutdownFlow` under `OD-34`,
which is the resource that was actually degraded.

The alternative was worse than nothing rather than merely useless. A declared status subresource
that no controller writes leaves every hook reporting `status: {}` forever, which is
indistinguishable from a controller that has stalled.

## Dry-run

`EX-5` makes dry-run a faithful rehearsal of everything except effects, and hooks are exactly where
that is hardest: the operator cannot know whether an external receiver's "test" call is safe.

**HK-10 · A hook declares its own rehearsal.** The author supplies a dry-run invocation alongside the
real one — a different endpoint, the same endpoint with a rehearsal flag, or a read-only call that
proves reachability and auth without side effects. The operator cannot know which calls are safe to
repeat against a system it does not manage, so it does not guess; the author states it.

When `spec.dryRun` is declared, dry-run delivers that invocation as the receiver's authored
rehearsal. When it is omitted, dry-run validates the real invocation's shape and endpoint allowlist,
then records the request it *would* have sent without contacting the target system.

## What this is not

`SB-15` holds the line: the operator invokes a hook, bounds it, and publishes what happened. Retries,
backoff, DAGs, branching, artifact passing, and templating from prior results are workflow-engine
concerns. Reaching a real engine stays fully supported — that is what HK-3 is for.

## Decisions Closed

**OD-33 · Hook waiting.** No opt-in bounded wait exists in v1alpha1. A `ShutdownHook` invocation is a
bounded delivery attempt, not a workflow-completion gate. The hook-level timeout is preferred; the
cluster-level default only fills in omitted hook timeouts.

**OD-34 · Hook failure and abort policy.** Hook failures are advisory. They record failed action
evidence and mark the owning `ShutdownFlow` degraded, but they never engage `abortPolicy` and never
hold the next wave.

## Current Implementation

`ShutdownFlow` uses `RunHook` with `hookRef.namespace` and `hookRef.name`. Admission and controller
validation reject legacy `workflow.*` params, and generated RBAC no longer grants
`argoproj.io/workflows`. Argo remains reachable through the generic HTTP/CloudEvents path or through
an explicitly authored Kubernetes-object hook when the install grants the matching RBAC; it is not a
built-in route.
