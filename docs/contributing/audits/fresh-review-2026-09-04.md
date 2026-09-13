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

### F-134 Resolution (2026-09-12)

The selected NUTServer's effective TLS mode, endpoint identity, and public trust material now
travel through `polling.Target` into the production NUT client. The client uses Go's standard
TLS implementation, a TLS 1.2 minimum, hostname verification, and the declared CA or public
serving certificate as its trust anchor. It issues STARTTLS before LIST VAR, refuses required-mode
downgrades, and allows opportunistic plaintext only for the two explicit unsupported/unconfigured
NUT replies. Invalid trust, hostname mismatch, malformed replies, and failed handshakes fail
closed. The total poll deadline covers dialing, negotiation, and reads; cancellation closes the
underlying transport. Capability probes share the same target resolver and inherit this behavior.

Compatibility boundary: the existing renderer's no-certificate configuration remains plaintext,
even though the API mode defaults to Required. This change does not redefine server enablement
or upsmon's separate fleet-level policy. The security reference now spells out that certificate
material plus Required mode are both needed to enable required-TLS operator polling.

Regression fixtures cover required/opportunistic TLS, explicit disabled mode, trust/name errors,
malformed negotiation, downgrade refusal, cancellation, and stalled-handshake deadlines. Raw
client writes are inspected so an attempted plaintext fallback cannot pass merely because the
server closed its connection. Controller tests cover selected-server propagation, trust rotation,
namespace restrictions/inheritance, certificate-only anchoring, and missing trust. Trust-loading
failure now changes previously ready telemetry to Unknown/not-ready and schedules recovery.

The existing conditional image smoke lane now runs the production Go client's LIST VAR against
the real NUT image after certificate rotation. The fixture explicitly starts one dummy driver;
the old handshake/LIST UPS checks alone did not require a connected driver. The new test waits
only for bounded driver-startup states, not TLS failures. The local ARM64 image run passed with
the previously built current-Dockerfile server and upsmon images, including exact leaf checks,
stale-leaf rejection, real Online variables over TLS, and upsmon certificate verification.

Validation: the complete `internal/nut`, `internal/polling`, and `internal/controller` suites
passed with the race detector, including controller envtest. `make lint` reported zero issues;
the smoke script passed Bash syntax validation and the patch passed `git diff --check`.

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

### F-136 Resolution (2026-09-12)

The API adapter now carries a canonical target digest into the planner's target summary.
It binds node, namespace, and workload selectors (including expression operators/values),
node selector requirements, namespace names, every workload-reference identity field, and
agent names. Set-like ordering is normalized on a deep copy; caller-owned slices/maps are
unchanged. Both group and linear-step compilation use the same conversion.

Controller compilation establishes the current plan hash before looking up observed durations.
It then applies only the history requested with that hash. Empty history preserves declared
estimates, rejected plans skip the lookup, and applying observed durations leaves identity
unchanged. The old status hash is no longer used to select compilation history.

The regression first reproduced 24 target-identity collisions across groups and linear steps.
Race-enabled planner/adapter suites pass, including reorder stability and input immutability.
Controller regression coverage exercises stale status after a selector edit, old-target history
isolation, matching new history, declared fallback, and rejected-plan lookup suppression.
This coverage tests the production compile/history callback boundary without a live PostgreSQL
instance. Existing plans acquire a new hash once under the corrected identity and therefore
start with declared estimates until compatible new history is recorded.

Validation: `go test -race` passed for `internal/resolver`, `internal/planner`,
`internal/shutdownflow`, and the complete `internal/controller` suite (including envtest).
The first controller run caught a lifecycle assertion expecting only the execution store close;
it now also accounts for the first-reconcile history lookup's handle. The rerun passed.
`make lint` reported zero issues, `git diff --check` passed, and an independent read-only review
found no concrete issues. F-136 is closed.

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

### F-138 Resolution (2026-09-12)

Group deadlines now start before Wait sleeps, in dry-run and enforce modes. A single compressed
deadline covers the wait and any subsequent runner call. Expiry records `TimedOut` and fails the
group, including when a sleeper incorrectly returns success after its deadline; an expired wait
cannot proceed to the action runner. Parent cancellation remains distinct from group timeout.

Planner group budgets and declared history fallbacks now include Wait's parsed duration, capped
by an explicit shorter timeout. Linear Wait estimates use the same cap, and the controller carries
successive cumulative-duration differences into linear executor waves. Planning and execution
share the duration parser, retaining zero-pause behavior for malformed or negative group values.

`wait_budget_test.go` covers 16 group/linear and dry-run/enforce combinations through the real
compiler, group adapter, wave adapter, and executor: an unbounded one-hour wait, a longer timeout,
runtime compression, and deadline expiry with `TimedOut` audit evidence. Injected sleep/clock
functions avoid long wall-clock waits; short deadline tests exercise real context cancellation.
Additional executor coverage rejects false sleeper success after expiry. Race-enabled planner,
executor, adapter, and full controller/envtest suites passed; lint passed.

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

## F-137 and F-140 Closure (2026-09-12)

- **F-137 [Medium]: closed.** Group/action completion is sampled after the action or wait returns,
  before audit writes. Injected-clock regressions reproduce the old zero-duration records and
  verify elapsed durations for success, failure, deadline expiry, dry-run waits, and interrupted
  waits. A successful execution's recorded interval is fed into `CompileWithHistory`, producing
  the expected nonzero estimate. This is an in-memory audit-record-to-planner test, not a live
  PostgreSQL replay. Wait deadline placement itself remains F-138.
- **F-140 [Medium]: closed.** Each UPS enters a trigger's selected set only after its own hold
  expires. Pending conditions still retain hold state and matched status. Regressions cover
  staggered transitions, exact hold boundaries, reset/re-entry, and independent immediate triggers.
  A controller test verifies derived domain membership and eligible-only selection in both the
  overall and per-trigger status. This does not close F-129's separate execution-scoping defect.

Validation: race-enabled trigger/executor suites, focused controller boundary tests, and package
lint. No infrastructure, physical UPS, or Hadron changes were required.

## F-141 Controller Fix (2026-09-12)

The Secret mapper now follows NUTServer certificate, server-CA, and client-CA references as well
as selected device credentials. References match both namespace and name, and each server is queued
once even when multiple paths match. TLS notification does not depend on finding credential-using
devices. The existing data-change predicate continues to suppress metadata-only updates.

The new regression failed before the fix for all three TLS reference types. Race-enabled tests now
cover rotation, deletion, recreation, same-name Secrets in other namespaces, and overlapping TLS/
credential references. An isolated envtest API server and Secret informer also drove the production
mapper/predicate into a probe reconciler using the real material validation/digest functions:
rotation changed the digest, deletion failed validation, and recreation restored the rotated digest.
This test does not start the full NUTServer reconciler or an operand pod.

Controller tests and lint passed. The image-level acceptance check was completed separately below.

## F-141 Image Verification (2026-09-12)

`hack/nut-tls-smoke.sh` now checks the exact DER leaf certificate served by the real `upsd`
binary, in addition to CA trust, hostname verification, STARTTLS, and a NUT request over TLS.
It generates a second leaf/key under the same CA and hostname, replaces the server container
with the new combined PEM mounted read-only, and repeats the checks. A negative control pins
the old leaf against the replacement server and must fail specifically at the certificate
comparison, not from a connection or trust failure. This prevents a same-CA stale certificate
from satisfying the rotation assertion. The real `upsmon` client then connects with certificate
verification and forced TLS enabled.

Both server-only and server-plus-agent paths passed locally using ARM64 images freshly built
from the current Dockerfiles. `bash -n` and `git diff --check` passed. The existing Images
workflow already invokes this smoke script, so rotation follows the same conditional image
test lane without adding another workflow or rebuilding a cluster.

F-141 is closed through complementary layers: the controller/envtest checks above cover Secret
notification and material digest changes; the image test covers the new process serving the
new certificate. Container replacement models the operand side of a rollout. This is not a
single Kubernetes end-to-end test of Secret mutation through Deployment rollout, nor a claim
of in-process certificate reload or uninterrupted client reconnection.
