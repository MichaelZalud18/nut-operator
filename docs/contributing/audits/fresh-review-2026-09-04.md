# Fresh Repository Review: 2026-09-04

Components: Planning & Execution Logic, Storage & Audit, NUT Server / upsd, Telemetry & Triggers,
Inventory System, Node Agent / DaemonSet.
Audience: contributors.

Evidence snapshot of the working tree, including existing uncommitted implementation and test
changes. This review did not change production code. Open work is owned by
[tasks.md](../../tasks.md); this document records the observed failures, not completion status.

Scope correction, 2026-09-05: F-130 is withdrawn under
[SB-1](../design/scope-boundaries.md#executor-restarts-and-idempotency). Missing restart continuity
is not a product defect. Related F-131/F-132 acceptance criteria no longer require resume recovery.

The review covered controller/planner/executor integration, NUT and actuator boundaries, inventory
and NetBox, audit persistence, admission, and test/CI configuration. Existing settled decisions
remain in force: no recovery workflow engine, no hook retry subsystem, and infeasibility remains
warn-and-run. The findings below concern missing or inconsistent implementation of those contracts.

## Validation Evidence

- `go test ./api/... ./cmd/... ./internal/... ./test/utils -count=1` passed with the existing
  toolchain and local socket access, including controller and webhook envtest suites.
- `bin/golangci-lint run --timeout=5m` passed with zero issues.
- `go test -race ./internal/executor ./internal/audit ./internal/controller ./cmd/node-actuator
  ./internal/nut ./internal/netbox -count=1` failed in
  `TestDriverSupervisorPIDStabilitySurvivesManyRepeatedAddRemoveCycles`: the stable PID changed at
  cycle 13 during an add. The other selected packages passed; no Go data-race diagnostic was emitted.
  See F-143 for the fixture boundary that needs correction before interpreting this as a product bug.
- A temporary atomic-write variant, `TestFreshReviewAtomicDriverUpdatesKeepPID`, passed three
  race-enabled runs of 15 add/remove cycles each (45 cycles total, 93 seconds).
- Fifteen temporary Go-overlay assertions exercised fourteen reported findings using fake clients,
  injected clocks, or a fake HTTP transport. Each assertion described the intended behavior and
  failed on the reviewed implementation. These were review experiments, not committed regression
  tests or failures of the normal suite. F-130's intended behavior was subsequently withdrawn as
  out of scope. The remaining in-scope findings can use the recorded results for permanent coverage.
- No image builds, Kind/k3s/Talos runs, physical shutdowns, live GitHub checks, or fresh dependency
  and secret scans were performed. Passing local tests is not evidence that those gates passed.

## F-126: Wave Authorization

**High.** [Input assembly](../../../internal/controller/shutdownflow_execution.go#L259) reduces
approval to the initial flow mode; [execution](../../../internal/executor/executor.go#L329) computes
dry-run once. The initial reconcile validates flow approval, but subsequent waves do not reread it.
[Node release assembly](../../../internal/controller/shutdownflow_execution.go#L696) also omits the
agent's independent mode/approval check. An already-armed operand is not proof of current approval.
This violates EX-4 and EX-6.

`TestFreshReviewFlowRevocationAtWaveBoundary` changed the live flow to DryRun and removed approval
during wave one. Both waves remained effectful. Cover independent agent revocation and failed
authorization reads when implementing the execution-boundary checks.

## F-127: Stale Release Evidence

**High.** [All groups](../../../internal/controller/shutdownflow_execution.go#L493), concrete targets,
and node-release booleans are assembled before any wave runs. Although
[nodeClearance](../../../internal/controller/shutdownflow_execution.go#L776) uses the direct reader,
that read occurs too early. [The executor](../../../internal/executor/executor.go#L930) later trusts
the copied readiness and clearance evidence. EX-8 and EX-9 require current enumeration and clearance.

`TestFreshReviewClearanceRecheckedBeforeHandoff` created a running Pod between input construction and
the halt wave. The real Kubernetes action runner wrote one halt signal without error. The inverse
case also needs coverage: an earlier drain must be able to clear an initially occupied node.

## F-128: Control-Plane Quorum

**High.** [Structural inputs](../../../internal/planner/types.go#L29) and
[node-membership adaptation](../../../internal/shutdownflow/adapter.go#L279) do not carry control-plane
quorum state. Tier and node-clearance edges exist, but they do not implement PL-23, PL-24, or the live
quorum recheck in EX-18. [Handoff](../../../internal/kubeactions/runner.go#L961) writes each selected
node's signal without checking that later orchestration can survive.

`TestFreshReviewControlPlaneReleaseCannotPrecedeLaterWork` supplied three control-plane-labeled
nodes covered by one agent and a later Notify group depending on their release. Compilation returned
two waves, no blocked releases, and no diagnostic. The missing constraint applies while later work
needs orchestration; it must not prohibit an explicitly terminal control-plane shutdown forever.

## F-129: Eligible Power-Domain Scope

**High.** [affectedPowerDomains](../../../internal/planner/scope.go#L89) unions all configured
triggers. [Reconciliation](../../../internal/controller/shutdownflow_controller.go#L177) compiles
before evaluating eligibility, and execution consumes those waves unchanged. The actual selected
UPS devices reach execution metadata and power observation, not a reduction of the action graph.
This misses OD-14 when a flow has more than one domain-specific trigger.

`TestFreshReviewUntriggeredDomainDoesNotExecute` compiled two rack-specific groups and evaluated UPS A
on battery with UPS B online. Only A was eligible, but the executor invoked both groups. Regression
coverage must retain shared, mixed, and unresolved groups conservatively while omitting groups proven
to belong only to unaffected domains.

## F-130: Durable Execution Identity (Withdrawn)

**Withdrawn 2026-09-05: outside project scope, not fixed in code.** The original High rating relied
on EX-14/OD-17's former restart-continuity promise. That promise conflicted with the intended
idempotent-action boundary and has been superseded by
[SB-1](../design/scope-boundaries.md#executor-restarts-and-idempotency).

The observation remains factual: trigger holds default to the current observation time, and
[execution identity](../../../internal/controller/shutdownflow_execution.go#L1199) includes that
episode boundary. `TestFreshReviewRestartKeepsImmediateTriggerIdentity` re-read an unpatched API
object one second later and derived a different ID. It did not demonstrate an unsafe repeated action.

Stable identity across a crash, durable proof of completed actions, and exact wave recovery are not
requirements. Repeat safety belongs to the actions themselves (EX-26), including external hook
effects, and must not depend on the same execution ID. F-130 is removed from the open tracker, not
moved to post-v1 or replaced with a resume-hardening task. Existing resume code/schema remains.

## F-131: Storage Failure Before Execution

**High.** [recordShutdownFlowAudit](../../../internal/controller/shutdownflow_controller.go#L349)
returns on unready storage or `OpenAuditStore` failure before constructing the spool or reaching
execution. Fallback during an already-open writer's failure cannot cover this earlier failure point.
This contradicts EX-20's nonblocking audit contract.

`TestFreshReviewStorageOpenFailureUsesSpool` used an eligible flow, an enabled spool, ready storage
status, and an audit connector returning an unavailable error. No action ran. Failure handling also
needs bounded history, replay, and record I/O: handling returned errors alone does not cover stalled
storage. Preserve validation and authorization, and report failed evidence separately from action
outcomes. Missing durable resume evidence must not become a new execution prerequisite (SB-1).

## F-132: Reconciliation Occupied By Execution

**High; source-traced.** [Execution](../../../internal/controller/shutdownflow_controller.go#L422)
runs inline in Reconcile. [Manager setup](../../../internal/controller/shutdownflow_controller.go#L310)
does not override controller-runtime's default single worker. A long Wait, drain, or hook in one flow
therefore delays every other ShutdownFlow. The
[status heartbeat](../../../internal/controller/shutdownflow_controller.go#L292) is also patched only
after that flow returns, so active progress cannot satisfy EX-29's cadence/change contract.

Use bounded execution ownership with per-flow serialization and observable progress, not untracked
background goroutines. A blocked first flow, a second eligible flow, and a fake publication clock
can test the behavior without hardware. Increasing worker count alone does not fix active status or
cancellation. This is an in-process execution/publication issue, not a restart-continuity requirement.

## F-133: Credential-Based Driver Override

**High.** [Credential resolution](../../../internal/controller/nutserver_render.go#L648) copies all
Secret keys. [Rendering](../../../internal/controller/nutserver_render.go#L493) merges them over
driver options and emits them after the validated driver declaration. Admission's reserved-key and
driver-allowlist checks apply to the UPSDevice spec, not this merged configuration.

`TestFreshReviewCredentialSecretCannotOverrideDriver` rendered a `dummy-ups` device with credential
data containing `driver: usbhid-ups`. The resulting section contained both driver declarations and
returned no error. This is a configuration-policy bypass, not demonstrated host code execution.
Validate reserved keys at the merge boundary and add end-to-end Secret-resolution fixtures.

## F-134: Authoritative Telemetry Bypasses TLS Policy

**High; source-traced.** [Telemetry target resolution](../../../internal/controller/upsdevice_controller.go#L287)
does not carry the selected NUTServer's TLS settings.
[The NUT client](../../../internal/nut/client.go#L88) opens ordinary TCP and immediately sends
`LIST VAR`; no STARTTLS negotiation or peer verification occurs in the production poll path.
This happens even with NUTServer TLS Required. Encryption of upsmon traffic does not protect these
separate operator observations used to decide when to shut down.

The read-only client sends no NUT password, so this finding is about telemetry integrity and policy
enforcement, not observed credential disclosure. Test required-mode downgrade refusal, configured
CA and server-identity verification, explicit plaintext mode, cancellation, and handshake deadlines
with local protocol fixtures, then verify compatibility against the shipped NUT image.

## F-135: NetBox Redirect Credential Boundary

**Medium.** [Pagination](../../../internal/netbox/client.go#L183) uses the supplied/default HTTP
client without restricting redirects. [absoluteURL](../../../internal/netbox/client.go#L228)
protects JSON pagination URLs, but an HTTP redirect happens inside `Do` before that check can run.

`TestFreshReviewRedirectCannotLeakToken` used a fake transport returning a redirect from HTTPS to
HTTP on the same hostname at another port. The redirected request carried the Authorization token.
No real token or external request was used. Restrict every redirect to the approved origin without
mutating a caller-owned HTTP client, and retain legitimate same-origin pagination.

## F-136: Target Identity And History Scope

**Medium.** [PlannerTarget](../../../internal/shutdownflow/adapter.go#L423) records selector presence
and reference counts, not the actual workload/namespace selectors and referenced object identities.
Node membership helps only for targets that resolve to nodes. In addition,
[history lookup](../../../internal/controller/shutdownflow_controller.go#L176) uses the previous
status hash before compiling the new plan.

`TestFreshReviewPlanHashBindsSelectors` changed a ScaleWorkload selector from `app=web` to
`app=database`; the compiled hash did not change. Generation-based deduplication still distinguishes
a spec edit, but structural plan identity and compatible-history selection do not. Test canonical
ordering as well as changed selectors/ref names, and reject old-target history on the first reconcile
after a structural edit.

## F-137: Zero-Length Action History

**Medium.** [executeGroup](../../../internal/executor/executor.go#L923) sets `completedAt` equal to
`startedAt` and never updates it after work. Group and action rows persist both identical values;
[history reads](../../../internal/audit/history.go#L91) derive durations from their difference.
Nonpositive observations cannot inform the planner's execution-history estimates.

`TestFreshReviewElapsedDuration` advanced an injected clock by 17 seconds inside the runner. The
persisted elapsed duration was zero. Cover waits, failures, timeout outcomes, rehearsals, and a real
writer/history-reader round trip rather than only supplying prebuilt nonzero history samples.

## F-138: Wait Timeout And Budget Mismatch

**High.** [Wait handling](../../../internal/executor/executor.go#L943) sleeps on `actionCtx` before
the group timeout context is created. [Group compilation](../../../internal/planner/compiler.go#L330)
budgets `group.Timeout`, not a Wait's `params.duration`, so a valid wait can be absent from feasibility
and adaptive timing. Linear-step adapter budgets also need regression coverage.

`TestFreshReviewWaitHonorsTimeout` observed a one-hour sleep request with no group deadline despite
a one-second timeout. `TestFreshReviewWaitDurationAppearsInCompiledBudget` compiled a one-hour Wait
to zero estimated duration. Both are deterministic and use no wall-clock hour-long sleep. Test the
whole declared/effective timing path in dry-run and enforce modes, preserving warn-and-run for plans
that genuinely do not fit.

## F-139: False Handoff Success Evidence

**Medium.** [recordNodeReleases](../../../internal/executor/executor.go#L1079) derives accepted and
released fields from approval/readiness/clearance, without the action runner's write result.
It is called even when [handoff](../../../internal/kubeactions/runner.go#L961) fails, including a
partially written multi-node batch.

`TestFreshReviewFailedHandoffIsNotRecordedAsAccepted` returned a Secret-update error from the runner
but recorded `Accepted=true` and `Released=true`. Carry actual per-node publication outcomes and
issued signal metadata into audit records; do not infer a physical halt from a successful Secret
write. Test complete failure, partial success, and cancellation independently.

## F-140: Per-Device Trigger Holds

**Medium.** [Trigger evaluation](../../../internal/trigger/evaluator.go#L178) collects all matching
devices, sets a decision eligible when any one device's hold elapses, and publishes every matched
device as selected. A newly matching device therefore enters the eligible set too early.

`TestFreshReviewTriggerDoesNotSelectDeviceBeforeItsHold` gave UPS A a completed one-minute hold and
UPS B a new OnBattery transition. Both were selected. Keep matching diagnostics separate from
eligible-device selection and test staggered transitions/reset behavior together with F-129's
runtime domain scoping.

## F-141: External Certificate Rotation Watch

**Medium.** [The Secret mapper](../../../internal/controller/nutserver_watch.go#L138) finds only
UPSDevice credential references. The TLS
[restart digest](../../../internal/controller/nutserver_render.go#L1494) is correct when reconciliation
runs, but an external `serverCertificateRef` Secret does not itself enqueue the server. A healthy
direct-driver server has no unconditional periodic requeue to repair this promptly.

`TestFreshReviewExternalTLSRotationEnqueuesServer` supplied a NUTServer referencing an externally
owned certificate Secret. The mapper returned zero requests. Test watch routing and digest/rollout
behavior together, followed by a conditional image test of the certificate actually served after
rotation. Do not reopen unsupported mutual-TLS functionality to fix this watch gap.

## F-142: Accepted Failure Policies Are Inert

**Medium; source-traced.** The [API](../../../api/v1alpha1/shutdownflow_types.go#L836) accepts
`ContinueSafeSteps`, abort notifications, and per-step `continueOnError`.
[The planner adapter](../../../internal/shutdownflow/adapter.go#L170) carries some of these into
identity, but the [execution adapter](../../../internal/controller/shutdownflow_execution.go#L493)
does not convey them to execution. Non-hook action failure stops the remaining flow regardless.

Either implement the documented supported policy or reject unsupported settings explicitly before
v1; do not silently accept promises the runtime ignores. A matrix spanning admission, adaptation,
action failure, safe tail steps, and notifications can prove this locally. Keep the settled
advisory RunHook failure behavior and do not add a retry/workflow engine.

## F-143: Driver PID-Stability Fixture Race

**Medium; test reliability.** The race-enabled suite failed
[the repeated churn test](../../../internal/controller/nutserver_driver_supervisor_repeated_restart_test.go#L34)
at cycle 13: the supposedly unchanged driver's PID changed. The normal suite had passed earlier.
[setConfiguredUPS](../../../internal/controller/nutserver_driver_supervisor_component_test.go#L190)
uses truncating writes to both its independent configured-device list and `ups.conf` while the
[supervisor](../../../internal/controller/nutserver_render.go#L976) reads them. It can observe an empty
or inconsistent fixture state that is not a faithful atomic projected-file update.

This does not establish that production F-124 behavior regressed. Correct the fixture boundary,
retain script logs on failure, and revalidate repeated add/remove and actual image behavior before
changing production supervision or weakening assertions. The tests are local software tests;
physical UPS hardware is not required.

The temporary `TestFreshReviewAtomicDriverUpdatesKeepPID` variant replaced truncating writes with
write-to-temporary-file plus rename, without modifying the supervisor. It passed three race-enabled
runs, totaling 45 churn cycles. This supports the fixture-race explanation; it does not prove that
the independent mock configuration list perfectly models a real projected volume or NUT process.

*Closed 2026-09-06.* Confirmed the fixture-race diagnosis rather than assuming it, then fixed it in
the permanent harness rather than only in a temporary variant: `setConfiguredUPS`,
`touchServerConfig`, and the direct `ups.conf` write in
`TestDriverSupervisorRestartsADriverWhoseOwnConfigurationChanges` (all in
`nutserver_driver_supervisor_component_test.go`) now go through a new `atomicWriteFile` helper —
write to a `.tmp` sibling, then `os.Rename` — for every file the running supervisor reads live.
`mustWriteExecutable` was left as a direct write on purpose: it only ever runs in setup, before
`start()`, before any reader exists. Verified against the same failure mode the review found: `go
test ./internal/controller/... -race -run 'TestDriverSupervisor|TestF124' -count=1` passed three
times in a row (three separate invocations, not `-count=3` in one process, to rule out any
in-process warm-up effect), and the broader `-race` sweep the review itself ran (`internal/executor
internal/audit internal/controller cmd/node-actuator internal/nut internal/netbox`) stayed clean.
This closes the test-reliability finding only; it says nothing new about production `F-124`
behavior, which this pass did not touch.

## F-133 and F-135 Closure (2026-09-12)

Both findings were reproduced with permanent regression tests before applying fixes.

- **F-133 [High]: closed.** The renderer rejects credential Secret keys `driver`, `port`, `mode`,
  `authconf`, and `repeater_disable_strict_start`, including case variants. Secrets still override
  ordinary driver authentication options. Tests resolve real Secret objects through the fake
  Kubernetes client into rendering and assert rejection without returning configuration or
  including credential values in errors. Existing valid credential merge tests remain passing.
- **F-135 [Medium]: closed.** The NetBox client copies the supplied HTTP client and checks each
  redirect against the configured origin before following it. Scheme, port, subdomain, and host
  changes are rejected; redirect userinfo is also rejected. Same-origin redirects retain auth,
  loops stop at ten requests, and a caller's stricter redirect policy remains effective. The
  original HTTP client is not modified. Existing pagination and CLI coverage remains passing.

Validation: race-enabled NetBox and CLI suites, focused renderer/credential tests, and package
lint. No deployment, physical UPS, or live NetBox service was used. This closes these two
configuration/transport boundaries only, not the other shutdown-safety findings in this audit.
