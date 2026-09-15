# Modular Deployment Research and Proposals

Components: Modular Deployment Profiles.
Audience: contributors.

Research transferred and reconciled on 2026-09-15. This document preserves the implementation
basis, alternatives, safety constraints, and detailed acceptance criteria for
[US-1 through US-4](user-stories.md). Current task status and release placement belong to
[MOD-1 through MOD-5](../../tasks.md#modular-deployment-profiles), not this document.
These are proposals and evaluation criteria, not proof of supported deployment profiles.

## Decision Boundaries

The existing [scope registry](scope-boundaries.md) and [settled questions](settled-questions.md)
remain authoritative. LocalNUT would require explicit changes to OD-37/SB-3 and the sequencing
boundary; full-product storage requirements remain in place until a profile-specific exception is
recorded. No OD is closed by moving this research. MOD-4 is the requested managed-NUT profile;
its controller-set and packaging choices still need validation. Keep findings dated when the
implementation basis changes, rather than turning a proposal into a current-behavior claim.

## MOD-1

investigate the minimum agents-only installation (`US-1`). Compare reusing
the shipped operand images with standalone manifests against a selectively enabled, lightweight
operator. The existing DaemonSet has separate upsmon and actuator containers; copying one image
does not supply configuration, credential separation, signal delivery, or lifecycle management.
Trace required controllers, CRDs, admission/certificates, RBAC, projected Secrets, inventory,
telemetry, and audit dependencies. Determine whether existing NUT endpoints can be consumed
directly without a managed relay. Define who authorizes shutdown and through what supported API;
do not promote internal signal Secrets to a public API or enable autonomous fallback implicitly
(OD-37). **Testable now:** isolated render/startup experiments for both candidates, measured
resource/dependency comparison, dry-run and unauthorized/stale-request rejection. Recommend the
smallest maintainable option with explicit security and upgrade tradeoffs. Guest power-off
qualification reuses the separate actuator test boundary; packaging tests do not prove it.
**Research transferred 2026-09-15:** the current renderer already uses `MODE=netclient`,
`MONITOR ... secondary`, and the project-owned `power-signal-writer` as `SHUTDOWNCMD`.
Reuse that upstream netclient model rather than adding a second trigger service. Compare a typed
external NUT target (UPS name, host/port, Secret-backed credentials, TLS trust) against current
NUTServer-only refs; reuse upsmon rendering/readiness without fake NUTServer/UPSDevice objects.
Investigate mutually exclusive `Operator` and `LocalNUT` authorization modes: Operator keeps
the executor-issued projected Secret; LocalNUT would explicitly authorize the local upsmon
signal. No implicit fallback or combined Operator-or-LocalNUT mode. Keep LocalNUT a proposal
until OD-37/SB-3 and sequencing scope are deliberately revised and its approval/identity contract
is defined; existing flow-binding safeguards cannot simply be disabled to make it work.
**Security prerequisites:** upsmon must not acquire `CAP_SYS_BOOT`, host PID access, Talos
credentials, or host-actuation code. Only the actuator may halt a host. Revisit the deferred
`F-45` multi-supply assumptions before any LocalNUT implementation: hardcoded `MONITOR` power
and `MINSUPPLIES` stop being inert when local signals are authorized. Keep the existing `OD-19`
outbound FSD broadcast decision separate from consuming upstream FSD in this proposed mode.
Prefer a selectively enabled agents-only manager unless comparison shows disproportionate cost;
its current inventory coverage and ShutdownFlow rollout-hold dependencies must become optional,
not merely unused CRDs left installed. **Acceptance after design approval:** real external upsd
without managed NUTServer; real OB+LB/FSD reaches Simulate only in LocalNUT; Operator ignores
local signals; no mode fallback; stale/wrong-node/unauthorized requests refused; no planner,
inventory, PostgreSQL, or ShutdownFlow dependency in the agents-only package.

## MOD-2

verify orchestration with an existing host-shutdown system (`US-2`) before
designing another actuator mechanism. Build a public-safe example and component fixture using
authored inventory, `ShutdownHook`, and `ShutdownFlow` `RunHook`, with a fake HTTP receiver and
explicit rehearsal invocation. Verify a mixed flow can use built-in agents for some nodes and
hooks for others without requiring an agent for hook-only work. `PowerInventoryNode.nodeName`
identifies a Kubernetes Node, not an arbitrary external host; establish how external-host
identity, UPS scope, and communication dependencies are represented without fabricated Nodes.
`RunHook` does not enumerate node targets: verify explicit per-host/group hook declarations and
static request data before claiming automatic per-host dispatch. **Testable now:** request
targeting, Secret-backed authentication, endpoint allowlisting, dry-run with/without rehearsal,
repeat-safe receiver behavior, timeout/failure evidence, and ordering against surrounding work.
Distinguish delivery from completed shutdown: hooks remain advisory under OD-33/OD-34. Document
which story outcomes already work and only open implementation tasks for demonstrated gaps;
stronger completion/abort semantics require an explicit design decision.

## MOD-3

investigate aggregation with external planning (`US-3`), reusing `MOD-1`'s
dependency comparison. Separate aggregation-only from aggregation-plus-agents requirements.
Establish what runs today with no `ShutdownFlow`, whether planning controllers can be omitted,
and which install/runtime dependencies remain mandatory. Identify existing NUT/telemetry
interfaces and the missing, if any, supported boundary for externally ordered execution.
Compare existing resource composition, selective operator settings, and standalone operands;
do not require clients to synthesize compiled plans or write internal halt Secrets.
**Testable now:** isolated startup and telemetry consumption without planning, followed by
contract tests for whichever request boundary the investigation recommends, including approval,
targeting, stale requests, and cancellation. Prove execution safeguards remain enforced when
planning is external. Produce a supported/proposed capability matrix and a scoped implementation
recommendation; no new network service is assumed. Future profile tests should run conditionally
on their owning components, APIs, and packaging, independently of Hadron qualification.

## MOD-4

define and implement a managed NUT-only profile (`US-4`), distinct from
aggregation with external planning (`MOD-3`). Support operator-managed `UPSDevice`/`NUTServer`
rather than making users reconstruct a working operand from a raw nut-server image.
**Existing basis:** NUTServer can omit `managementClusterRef`, select UPSDevices, create its
standalone namespace, and render configuration, auth, TLS, Deployment, Service, NetworkPolicy,
readiness, and PodDisruptionBudget; currently this path needs `spec.image.repository`.
The operand is one upsd plus a separate driver-supervisor sharing `/etc/nut` and `/run/nut`.
Direct image use remains a low-level development/diagnostic building block, not the primary
supported install: otherwise users must reimplement config/credential generation, sidecar
lifecycle, TLS mounts, exposure, policy, readiness, reload/restart, and upgrade behavior.
**Profile decisions first:** compare NUTServer plus UPSDevice/NUTServer admission against that
set plus the UPSDevice reconciler. NUTServer reads and validates selected device specs itself;
useful device status/telemetry must justify the reconciler's capability/telemetry dependencies.
Inspect admission dependencies too. Prefer the existing manager binary with explicit controller
selection and profile-scoped CRDs, admission, RBAC, and manifests over a second operator binary
unless measurements justify one. Do not start unused controllers or grant their permissions.
No PowerManagementCluster, NodePowerAgent, ShutdownFlow, planner/executor, actuation, inventory,
or PostgreSQL dependency. Record the profile-specific exception to full-product SB-11 rather
than implying PostgreSQL is optional for the existing full installation.
**Usability/API work:** provide a release-owned operand image default. Retain OperatorManaged
admin/monitor credentials and ExistingSecret support. Keep TLS Required and provide or document
certificate bootstrap; do not weaken TLS for convenience. Current generated ingress permits
same-namespace clients and the manager, not generic clients elsewhere. Add explicit reviewable
namespace/pod selectors and/or CIDRs for cross-namespace or external clients; NodePort or
LoadBalancer exposure alone is not permission under an enforcing CNI. Account for actual source
identity after service routing rather than promising CIDR behavior without testing it.
**Testable now; Conditional:** install into a clean cluster with only this profile's resources;
a real dummy-ups fixture must produce a Ready two-container operand and queryable upsd Service.
Prove approved cross-namespace/external access and denied unapproved access under enforced
policy; auth/TLS, device add/remove/config changes, isolated driver restart, reconcile and upgrade
behavior use the same contracts as the full product. Assert both absent controller watches and
RBAC inability to mutate planner/host-actuation resources. Profile testing follows `MOD-5`.

## MOD-5

add representative acceptance coverage for each supported deployment
profile, after the owning MOD decision approves it. This is a conditional follow-up, not approval
of every proposed profile or a combinatorial matrix of component subsets.
**US-1:** existing NUT with no managed NUTServer or built-in planner; preserve approved-mode
authorization, dry-run, targeting, stale-request rejection, and privilege separation (`MOD-1`).
**US-2:** a real ShutdownFlow combining agents with ShutdownHook external actuation, including
authentication, explicit targeting, timeout/failure evidence, repeat-safe delivery, and ordering.
Delivery is not evidence that a host stopped; preserve the existing advisory hook contract.
**US-3:** aggregation/telemetry without the built-in planner/ShutdownFlow path; exercise the
authorized external execution boundary selected by `MOD-3`, including approval, targeting,
stale requests, and cancellation. **US-4:** the clean managed-NUT-only install in `MOD-4`.
**Testable now once selected; Conditional:** reuse component/Kind tests; run on owning API,
component, and packaging changes. Reuse VM Linux/Talos qualification instead of re-proving host
power-off in every package test. Only profiles selected for v1 become v1 release gates.
