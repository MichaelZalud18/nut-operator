# Modular Deployment Research and Proposals

Components: Modular Deployment Profiles.
Audience: contributors.

Research transferred and reconciled on 2026-09-15. This document preserves the implementation
basis, alternatives, safety constraints, and detailed acceptance criteria for
[US-1 through US-4](user-stories.md). Current status belongs to the
[v1 MOD-2/MOD-4/MOD-5 tasks](../../tasks.md#modular-deployment-profiles) and
[post-v1 MOD-1/MOD-3 tasks](../../tasks-post-v1.md#modular-deployment-profiles).
The selected implementation contracts below are not proof of supported deployment profiles.

## Decision Boundaries

The existing [scope registry](scope-boundaries.md) and [settled questions](settled-questions.md)
remain authoritative. **Scope selected 2026-09-18:** v1 includes advisory mixed actuation
(MOD-2), managed NUT-only including telemetry-only consumption (MOD-4), and their acceptance
(MOD-5). Implement MOD-2 first, then MOD-4, with acceptance alongside each. MOD-1 agents-only
and MOD-3 external execution are post-v1. NS-11 remains a v1 compatibility fix.

MOD-4 uses the existing manager with NUTServer reconciliation and UPSDevice/NUTServer admission;
the baseline excludes UPSDevice polling/normalized status. Its narrow no-PostgreSQL exception
is recorded in SB-11. Full-product storage and OD-37 authority remain unchanged. LocalNUT is
still only a proposal requiring explicit authority/sequencing revisions. Keep dated research
as historical evidence; selection is not implementation or live qualification.

## MOD-1

**Post-v1:** authority and profile design precede implementation. This task does not block v1.

[2026-09-18 source trace, isolated probe, measurements, and recommendation](../audits/mod-1-agents-only-2026-09-18.md).
The selective-manager recommendation remains conditional on explicit shutdown-authority and
profile decisions; neither candidate is a supported agents-only shutdown installation today.

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

[Dated research, mixed-flow example and component evidence](../audits/mod-2-mixed-actuation-2026-09-18.md)
support the selected v1 advisory integration. Confirmed halt, completion polling and hard
completion gates are outside this commitment. This does not change OD-33/OD-34.

Implement and qualify orchestration with an existing host-shutdown system (`US-2`) using
the existing hook contract. Finish the public-safe example and component/Kind fixtures using
authored inventory, `ShutdownHook`, and `ShutdownFlow` `RunHook`, with a fake HTTP receiver and
explicit rehearsal invocation. Verify a mixed flow can use built-in agents for some nodes and
hooks for others without requiring an agent for hook-only work. `PowerInventoryNode.nodeName`
identifies a Kubernetes Node, not an arbitrary external host. External identity belongs in
explicit static hook data; document affected-power scope through deliberately scoped flows and
declarations, with authored communication dependencies, without fabricated Nodes or inferred
external membership.
`RunHook` does not enumerate node targets: verify explicit per-host/group hook declarations and
static request data before claiming automatic per-host dispatch. **Testable now:** request
targeting, Secret-backed authentication, endpoint allowlisting, dry-run with/without rehearsal,
repeat-safe receiver behavior, timeout/failure evidence, and ordering against surrounding work.
Distinguish delivery from completed shutdown: hooks remain advisory under OD-33/OD-34. Document
which story outcomes already work and only open implementation tasks for demonstrated gaps;
stronger completion/abort semantics require an explicit design decision.

## MOD-3

**Post-v1:** this task owns authorized external execution through built-in agents. Telemetry-only
aggregation is part of v1 MOD-4; it does not require the external execution API.

[Dated research and capability matrix](../audits/mod-3-external-planning-2026-09-18.md)
separate NUT telemetry from external execution. Plain relay worked in isolation, but generated
authconf failed with the pinned operand (NS-11). A Kubernetes request boundary is proposed only.

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

[Dated controller/dependency comparison](../audits/mod-4-managed-nut-only-2026-09-18.md)
supports the selected NUTServer-plus-both-admissions baseline. The
[NUT-only installation](../../installation/nut-only.md) implements that package;
[dated implementation and acceptance evidence](../audits/mod-4-5-nut-only-acceptance-2026-09-18.md)
records the validation scope and findings.

Implement the selected v1 managed NUT-only profile (`US-4`), including NUT-protocol telemetry
consumption by external systems, distinct from external execution (`MOD-3`). Support managed `UPSDevice`/`NUTServer`
rather than making users reconstruct a working operand from a raw nut-server image.
**Existing basis:** NUTServer can omit `managementClusterRef`, select UPSDevices, create its
standalone namespace, and render configuration, auth, TLS, Deployment, Service, NetworkPolicy,
readiness, and PodDisruptionBudget. The NUT-only profile supplies a distribution-owned default
image; explicit `spec.image` still overrides it.
The operand is one upsd plus a separate driver-supervisor sharing `/etc/nut` and `/run/nut`.
Direct image use remains a low-level development/diagnostic building block, not the primary
supported install: otherwise users must reimplement config/credential generation, sidecar
lifecycle, TLS mounts, exposure, policy, readiness, reload/restart, and upgrade behavior.
**Selected baseline:** NUTServer reconciliation plus UPSDevice/NUTServer admission, without
the UPSDevice reconciler. NUTServer reads and validates selected device specs itself. Document
NUT reads, identity, auth/TLS and unavailable/stale responses as the telemetry interface;
normalized UPSDevice status is not part of this profile. Use the existing manager binary with
explicit registration and profile-scoped CRDs, admission, RBAC and manifests. Do not start
unused controllers or grant their permissions.
No PowerManagementCluster, NodePowerAgent, ShutdownFlow, planner/executor, actuation, inventory,
or PostgreSQL dependency. Apply the approved profile-specific SB-11 exception; PostgreSQL
remains required for the full installation.
**Usability/API work:** provide a release-owned operand image default. Retain OperatorManaged
admin/monitor credentials and ExistingSecret support. Keep TLS Required and provide or document
certificate bootstrap; do not weaken TLS for convenience. Generated ingress permits
same-namespace clients and the manager plus explicit `clientAccess` namespace/pod selectors.
Off-cluster access still requires a reviewed network path and policy; NodePort or
LoadBalancer exposure alone is not permission under an enforcing CNI. Account for actual source
identity after service routing rather than promising CIDR behavior without testing it.
**Testable now; Conditional:** install into a clean cluster with only this profile's resources;
a real dummy-ups fixture must produce a Ready two-container operand and queryable upsd Service.
Prove approved cross-namespace/external access and denied unapproved access under enforced
policy; auth/TLS, device add/remove/config changes, isolated driver restart, reconcile and upgrade
behavior use the same contracts as the full product. Assert both absent controller watches and
RBAC inability to mutate planner/host-actuation resources. Profile testing follows `MOD-5`.

## MOD-5

[Executable component coverage and profile prerequisites](modular-acceptance.md) define the
focused test entry point and the owning install-level evidence. Component success does not
replace clean-cluster qualification of the selected profile.

Complete v1 acceptance for MOD-2 and MOD-4, including MOD-4 telemetry-only consumption.
Use representative scenarios, not a combinatorial matrix of component subsets.
**Post-v1 US-1, owned by MOD-1:** existing NUT with no managed NUTServer or built-in planner; preserve approved-mode
authorization, dry-run, targeting, stale-request rejection, and privilege separation (`MOD-1`).
**US-2:** a real ShutdownFlow combining agents with ShutdownHook external actuation, including
authentication, explicit targeting, timeout/failure evidence, repeat-safe delivery, and ordering.
Delivery is not evidence that a host stopped; preserve the existing advisory hook contract.
**US-3 telemetry-only / US-4, v1:** the clean MOD-4 managed-NUT install serves telemetry without
the built-in planner/ShutdownFlow path. **Post-v1 US-3, owned by MOD-3:** exercise its authorized
external execution boundary, including approval, targeting, stale requests and cancellation.
**Testable now; Conditional:** reuse component/Kind tests; run on owning API,
component, and packaging changes. Reuse VM Linux/Talos qualification instead of re-proving host
power-off in every package test. Only profiles selected for v1 become v1 release gates.
