# Scope Boundaries

Components: varies by boundary — each `## SB-n` heading below carries its own tag.

This document records what `nut-operator` is and is not responsible for. These identifiers are
stable: `SB-n` values do not get reused or renumbered.

## Sources of Truth and Precedence

| Domain | Authority |
| --- | --- |
| CRD shapes, rendering behavior, reconcilers, and packaged manifests | Repository |
| Physical and structural architecture | Repository |
| Planner and application logic, orchestration semantics | This document and the design decisions it records |

Where this document and the repository disagree on planner or orchestration logic, this document
wins and the repository changes.

## Governing Principles

*Components: Cross-cutting.*

**GP-1 · Trigger provenance defines scope.** If the initiating signal is not power state, it is
out of scope — regardless of how similar the resulting node action looks. Cordoning a node for a
power event and cordoning a node for an OS patch produce the same API call and are different
products.

**GP-2 · Dangerous behavior is opt-in, reviewable, and visible before it is possible.** Dry-run is
the default at every layer. Real actuation requires explicit, separate approvals that are legible
in Git and in `/status` before they can affect a host.

**GP-3 · Kubernetes holds desired state; PostgreSQL holds the record.** Compiled plans and current
state summaries live in CR status. Event history, telemetry streams, and execution records never do.

**GP-4 · Consume signals, do not rebuild them.** The operator reads from existing monitoring and
inventory systems. It does not become one.

**GP-5 · Declared in the failure path; derived only to confirm or alarm.** Anything consumed while
power is failing is authored, structural input. Runtime-derived or externally queried data may
verify that input and raise conditions on mismatch, but never feeds decisions directly.

**GP-6 · Kubernetes is the interface.** The v1 product is operated through CRDs, status, Events,
logs, and PostgreSQL audit records. GitOps is the primary configuration mechanism, and `kubectl`
is sufficient for day-to-day operation.

**GP-7 · Publish facts, not commands.** The operator publishes compiled plans, dependency graphs,
waves, progress, and explanations for subscribers. Subscribers may visualize, document, monitor,
or recover from those facts, but they do not own shutdown planning or host actuation.

---

## SB-1 · Shutdown and recovery are separate concerns

*Components: Planning & Execution Logic, Outputs & Publishing.*

Shutdown orchestration and bring-up orchestration are distinct control paths. NUT's own scope
stops at clean shutdown; conventional bring-up is BIOS/PDU behavior plus service recovery on boot.

Recovery orchestration is out of scope for this project (OD-1, closed). External recovery systems
may subscribe to published planner artifacts, including advisory startup wave projections, but the
operator does not execute recovery and does not own bring-up policy.

## SB-2 · Relationship to NUT

*Components: Telemetry & Triggers, NUT Server / upsd, Capability Profiles.*

**SB-2a · NUT is the power-state path, unconditionally.** The operator never speaks to UPS
hardware directly. All UPS interaction is mediated by NUT. A device with no viable NUT network
driver is unsupported; it is not a candidate for a bypass path.

What varies by device is what returns over that path, not whether the path is used. Device
variation is capability-profile content (SB-9) and resolves in three tiers:

1. **Transport** — does a reviewed network driver or upstream NUT relay path exist for this
   device. Binary admission gate on `UPSDevice`, enforced by the driver allowlist and the
   `spec.upstreamNUT` contract.
2. **Telemetry richness** — which NUT variables the driver actually reports. Two devices both
   speaking `snmp-ups` may differ by an order of magnitude in exposed variables.
3. **Actuation** — outlet control, delayed start, safe-shutdown handshake. See SB-2c.

**SB-2b · NUT's native threshold model is an input, never the sequencer.** NUT can trigger on
low-battery status, remaining runtime, or battery percentage. Those feed operator triggers.
Cluster ordering, wave computation, and dependency resolution belong to the operator and are
never delegated to NUT.

**SB-2c · NUT is a UPS control interface, not a read-only sensor.** Where the device supports it,
NUT can command outlets off and on and perform the safe-shutdown handshake that lets the UPS cut
power after the last client is down. The operator treats this as a gated capability surface,
declared per device by capability profile.

## SB-3 · The node agent holds local flow; it holds no cluster authority

*Components: Node Agent / DaemonSet.*

The boundary is on **authority**, not on the presence of flow.

In scope for the node agent:

- NUT client observation of UPS state.
- The in-pod signal-file handoff protocol, carrying timestamp, reason, UPS identity, and flow
  identity.
- Staleness rejection on that handoff.
- Local host shutdown execution once released.

Out of scope for the node agent:

- Pod draining.
- Ordering or wave decisions.
- Workload policy of any kind.
- Kubernetes API credentials in the default posture.

The `upsmon` container is unprivileged, runs a read-only root filesystem, drops capabilities, and
carries no Kubernetes API token unless a declared feature explicitly requires one. The `actuator`
container carries no NUT credentials and no policy authority.

### One authorized path to a halt (OD-37)

A node halts because the operator said so. The signal originates from the executor, arrives through
a projected Secret the API server gates, and carries the flow identity, plan hash, and timestamp
that make it attributable and staleness-checkable. That is the only path with authority.

NUT's own `SHUTDOWNCMD` is the conventional way a `upsmon` secondary releases its host, and this
project declines it as an authorization path. The mechanism stays in the codebase — the writer
binary, the signal format, the local file — because the shape is worth keeping and a later version
may want it. It cannot authorize a halt: the actuator watches only the projected Secret, and the
tmpfs the two containers share is not mounted into the actuator at all, so writing to it releases
nothing. There is no supported configuration that turns it on.

This resolves the choice `OD-37` posed against the last-resort-backstop reading. A backstop is
attractive precisely when the operator is unreachable, which is also when ordering matters most,
and `upsmon`'s local view cannot supply it: every agent renders `MONITOR ... 1 ... secondary` with
`MINSUPPLIES 1`, so one UPS reaching OB+LB would fire on every node it covers at once, unordered,
with an execution ID indistinguishable from the executor's. A backstop that halts a fleet in
arbitrary order is not a safety net under it — it is a second, worse shutdown implementation that
engages exactly when nobody is watching. `SB-2b` already reserves sequencing for the operator; this
is that boundary applied to the release signal itself.

The signal is authorization, not a record, and authorization is withdrawn. The operator deletes a
node's key from the Secret once the execution that wrote it stops covering it — a superseded
execution, or the same one after its trigger stops being eligible — so absence is what says the
episode is over. This matters because the actuator's own memory of what it has acted on is per-pod:
any replacement pod, from a rollout or a kubelet restart or an OOM kill, starts empty and would
otherwise read a signal that was already spent. The sharpest case is power restoration, where nodes
boot back up inside the TTL of the signal that halted them. A signal that outlives its episode is a
standing order nobody issued.

One thing is deliberately not withdrawn: a signal whose episode is still live stays, because a pod
that restarts before its node has actually halted should read it and finish the job. The TTL remains
as a backstop for the case where the operator is not around to withdraw anything, which during a
site-wide power event is not hypothetical.

The cost is stated rather than hidden: if the operator cannot deliver a signal, nodes are not
released, and the UPS runs down under a cluster that stays up. That is the failure this project
accepts, because it is observable and bounded, where the alternative is not.

## SB-4 · Two containers in the node agent

*Components: Node Agent / DaemonSet.*

All decision logic lives in the unprivileged container. The privileged executor stays small,
dumb, and fire-and-forget behind a minimal verb API. No third container, no second pod, no
sidecar proliferation.

**Outside the network-first baseline (OD-10):** a third container or separate DaemonSet enabling
USB-attached UPS support. This is a deliberate extension point, not an oversight.

Recorded so any later reversal is not mistaken for a contradiction: the network-only posture is
load-bearing for the security narrative. `docs/images.md` and `docs/architecture.md`
both justify the absence of host device mounts, host device access, and privileged mode on the
grounds that UPS reachability is network-only. USB support must arrive with its own isolated
actuation boundary and its own security rationale.

## SB-5 · Kured is out of scope

*Components: Cross-cutting.*

Kured's trigger is host and service health — OS patch reboots. That is not a power event, so
GP-1 excludes it. The overlap with power-event orchestration does not meaningfully exist.

No dependency, no coordination shim, no feature parity commitment. `ActuatorPolicy` remains
`Disabled | Simulate | PowerOff` with no reboot verb.

If a reboot verb is ever added to the executor, the motivation is collision-avoidance with a
separately installed Kured, not feature value.

**Coexistence caveat for documentation:** both projects cordon nodes. A Kured reboot interleaving
with a power event is possible and is documented as an operational note.

## SB-6 · Health and hardware monitoring are out of scope

*Components: Cross-cutting.*

Per GP-1 and GP-4. Node Problem Detector is excluded for the same reason as Kured — its trigger is
node health, not power. MachineHealthCheck and descheduler are likewise out.

The operator consumes health and readiness signals as planning inputs. It does not detect,
diagnose, or remediate node faults.

## SB-7 · Power budget modeling is in scope

*Components: Planning & Execution Logic.*

Runtime remaining and load-shedding arithmetic — what shedding a given set of workloads buys in
additional minutes — are in scope and were identified as the differentiating capability.

Shed-value arithmetic requires per-workload power attribution. The architecture keeps that
calculation separate from basic UPS threshold evaluation so ordinary shutdown safety does not
depend on perfect power attribution.

## SB-8 · NetBox is a heavy design influence and a zero-weight runtime dependency

*Components: Inventory System, Capability Profiles.*

NetBox shapes the data model substantially. The default build ships without it.

Field ownership:

| Source | Owns |
| --- | --- |
| NetBox | Device identity, model, power feeds, roles, rack and topology placement, power dependency mapping |
| Capability registry | Behavioral capability, device quirks, actuation support, telemetry guarantees |

Naming and rack moves stay NetBox-side so they cannot drift into capability profiles. Behavioral
logic stays operator-side so it cannot drift into inventory. The merge happens in the operator,
never in either source.

Consequence: topology input is an interface with at least two implementations — declarative CRD
as the default, NetBox as an optional provider. Capability profiles are the operator's regardless
of which topology provider is active.

The provider interface is a contract the operator owns, not a NetBox schema transcription — see
`inventory-provider-contract.md`. Attributes cross the boundary only where a planner rule consumes
them.

Profile storage is resolved: profiles are CRDs plus bundled operator data, referenced from NetBox at
most via a custom field pointing at a profile name, never maintained in it (OD-7, closed). There is
no field-level merge between sources — the topology provider supplies matching keys, profiles supply
match selectors; resolution is a lookup (OD-8, dissolved; validation of malformed provider keys
tracked as OD-8r).

## SB-9 · Capability profiles are versioned as artifacts, not per device

*Components: Capability Profiles.*

Semantic version on the profile itself. Bump only when behavior changes. Map device model, and
optionally firmware, to a best-matching profile version at resolution time.

Explicitly not tracked: per-physical-device versions, or every hardware and firmware combination.
The pattern is the same one used to decouple an API schema version from an implementation version.

Profiles are structured in two sections: declared NUT variables (telemetry), and declared behaviors
and quirks (actuation). Corrections that reduce declared capability are MAJOR bumps even though they
are behaviorally fixes, because plans validated against the prior version may fail trigger
validation under the corrected one.

## SB-10 · Workload shutdown orchestration is core scope; the mechanism is stock Kubernetes

*Components: Planning & Execution Logic.*

Orchestrating the ordered shutdown of cluster services is a core product function, not something
delegated elsewhere. The boundary is on mechanism, not ambition.

Mechanism constraints, already specified in `docs/shutdown-flow.md`:

- Workload controllers are scaled, suspended, or quiesced. Deleting their pods directly is an
  exceptional override only, because controllers recreate pods.
- Pods are execution instances — useful for eviction, wait conditions, and diagnostics, not the
  long-lived policy unit.
- Namespaces are grouping and policy boundaries. A normal flow does not delete them.
- Services are the traffic-withdrawal and readiness boundary. Backing workloads remain
  responsible for their own graceful shutdown.
- Nodes are terminal graph vertices. A node cannot power off until every workload, storage
  operation, and cluster responsibility assigned to it has cleared.

Third-party ecosystem integrations are opt-in. `ServiceMonitor` rendering is available when
Prometheus Operator support is enabled.

## SB-11 · PostgreSQL is a required production component; the implementation is the user's choice

*Components: Storage & Audit.*

The durable store is PostgreSQL. CNPG is the recommended in-cluster implementation, not a
privileged assumption of the design. `PowerStorageMode` is `Disabled | ExternalPostgres | CNPG`,
defaulting to `CNPG`, with `Disabled` supported for development only.

PostgreSQL sits on the **record** path, not the **decision** path. Compiled plans and desired
state live in CRs; the planner compiles from spec. A PostgreSQL outage degrades auditability. It
must not halt power response when the shutdown-time audit spool is configured.

Resilience note worth documenting for users: for the audit store specifically, `ExternalPostgres`
is the more resilient option, because a database outside the cluster is not in the shutdown path
of the event it exists to record. It cannot be the default — it requires infrastructure the user
may not have — but it reduces reliance on the local spool during cluster shutdown.

## SB-12 · Three-tier observability

*Components: Storage & Audit, Outputs & Publishing.*

| Tier | Content | Destination |
| --- | --- | --- |
| Info | Human-readable operational events | Logs, events |
| Debug | Operator decisions, compiled plan detail, wave rationale | Logs, CR status |
| Audit | Immutable record of what actually executed | PostgreSQL |

Three distinct artifacts: the event log, the execution log, and the compiled plan record.

The compiled dependency graph is stored as structured data, never as formatted text, so that it
stays queryable and renderable. "Show me the compiled graph" and "why was this node in wave four"
must be answerable from stored structure, not from log parsing.

## SB-13 · Planner implementation defaults

*Components: Planning & Execution Logic.*

Not a boundary. This default can change without renegotiating scope.

The planner is a Go package inside the operator repository, kept behind an interface so it stays
substitutable. Any decoupling happens at the interface level — a second implementation
reachable over gRPC, for instance — not at the language level.

## SB-14 · No embedded UI in v1

*Components: Outputs & Publishing.*

The project is fully usable through Kubernetes resources, CRDs, Events, logs, and PostgreSQL audit
records. A dedicated web UI is not part of v1.

If a UI exists later, it is a completely separate consumer of the operator's APIs and published
planner artifacts. It must not become part of the core reconciliation, planning, or execution path.

---

## SB-15 · The operator invokes hooks; it never becomes a workflow engine

*Components: Planning & Execution Logic.*

A pre-shutdown hook lets a system run its own routine before it loses power — a database snapshot, a
quiesce, a NAS flushing its cache. The operator's part is to **call it, bound it, and publish what
happened**. Everything past that belongs to whatever the hook reaches (`GP-4`, `GP-7`).

Specifically out of scope: retry policy, backoff, DAGs or fan-out between hooks, conditional
branching, artifact passing, templating a hook body from prior results, and any notion of a hook
"pipeline". Argo Workflows, Tekton, and Airflow already exist and are better at this. An operator that
grew those features would be a worse version of one of them, holding the failure path.

The boundary is easiest to see at the failure edge. When a hook is slow or fails, an engine's job is
to decide what to run next; this operator's job is to record it and keep shutting the cluster down,
because the battery does not wait for a retry budget. That difference is not a missing feature — it is
what the component is for.

Consequences that follow, rather than being separate rules:

- Hooks are declared ahead of the outage (`GP-5`), never discovered or assembled during one.
- Every call is bounded by a timeout the flow author declares, scaled like any other declared
  duration (`EX-11`).
- A failed or slow hook degrades and is recorded; it never holds a wave (`EX-25`).
- The operator does not model hook success as a dependency, so there is nothing to schedule around.

Reaching a real workflow engine stays fully supported — that is what the HTTP/CloudEvents transport
in [shutdown-hooks.md](shutdown-hooks.md) is for. Argo Events consumes CloudEvents, Tekton emits them,
Alertmanager receivers accept the same POST. The operator hands off to an engine; it does not become
one.

---

## Repository-Derived Boundaries

*Components: NUT Server / upsd, Planning & Execution Logic, Operator Maturity & Hardening.*

Constraints already encoded in the implementation. Listed here so the boundary set is complete.

**RB-1 · Network-reachable UPS devices only.** Local USB and serial drivers are out of scope for
the API so that generated NUT server pods require no host device mounts and no privileged access
for UPS connectivity. See SB-4 for the extension path.

**RB-2 · Network driver allowlist.** The set is `snmp-ups`, `netxml-ups`, `apcupsd-ups`, and
`dummy-ups` for tests and upstream relays. Drivers are added deliberately, not by discovery.

**RB-3 · No third-party NUT image as default.** Four project-owned OCI images: `nut-server`,
`upsmon-agent`, `node-actuator`, `operator`. Community NUT images are development scaffolding only
— many assume direct USB access and document privileged container usage, which contradicts RB-1.

**RB-4 · Dual approval gates.** `ShutdownFlow` enforcement and `NodePowerAgent` actuation are
separately approved. A production deployment requires both before host shutdown is rendered.

**RB-5 · Graph is the primary plan model.** Linear `spec.steps` remains available for small or
test installations. It is not the production model because it cannot express dependency
relationships or concurrent branches.

**RB-6 · CRDs are cluster-scoped.** The resource set is `PowerManagementCluster`, `UPSDevice`,
`PowerInfrastructure`, `PowerInventoryNode`, `PowerInventoryEdge`, `UPSCapabilityProfile`,
`UPSCapabilityProbe`, `NUTServer`, `NodePowerAgent`, and `ShutdownFlow`.

`UPSCapabilityProbe` is the one resource that requests an action rather than declaring desired
state. It is advisory tooling (RS-7): it reads a device, drafts a profile into its own status, and
affects nothing else.

**RB-7 · Admission webhooks mirror reconciler safety checks.** Unsafe combinations are rejected
before persistence where admission is enabled and are still surfaced through reconciliation status
for defense in depth.

---

## Consolidated Out of Scope

- Recovery orchestration and startup execution; consumers may use published artifacts, but the
  operator does not own bring-up.
- Dedicated v1 web UI, embedded dashboard, or frontend control plane.
- Kured, and OS-patch reboot orchestration generally.
- Node Problem Detector, MachineHealthCheck, descheduler, and node fault remediation.
- General hardware monitoring.
- General cluster health management.
- Rebuilding any monitoring, alerting, or inventory capability that already exists in the
  operator's environment.
- Local USB and serial UPS connectivity — outside the network-first baseline, see OD-10.
- Delegating shutdown ordering to NUT.
- Any UPS interaction path that bypasses NUT.
- Workflow orchestration behind a pre-shutdown hook — retries, DAGs, branching, artifact passing.
  The operator invokes and publishes; see SB-15.

---

## Open Decisions

| ID | Decision | Blocks |
| --- | --- | --- |
| OD-8r | Resolver behavior on malformed or missing model strings from the topology provider: reject, fall back to the unidentified-device profile with a warning, or configurable | Resolver design |
| OD-9 | Degrade mechanics for trigger-capability mismatch — folded into capability schema doc | Capability schema doc |
| OD-10 | USB and serial UPS support: version target and isolation model | v2 scoping |
| OD-19 | FSD usage: whether NUT's forced-shutdown broadcast becomes the final release signal or is deliberately declined in favor of the executor's signal file. Affects whether shutdown is observable through standard NUT tooling | Executor design |
| OD-20 | Instant command scope and gating: which NUT instant commands and writable variables enter scope, how they are gated given they can cut power to equipment, and which capability profile fields declare support. Bounded by OD-1 on anything touching power-return | Capability schema |
| OD-24 | Non-NUT power device actuation: second actuation path for power devices without NUT drivers, or permanently topological. Decided alongside OD-10, since both concern control surfaces outside the RB-1/SB-2a NUT-network-only posture | v2 scoping |
| OD-27 | Timing adaptation parameters: hysteresis count, improvement margin, and scope | Adaptive execution |
| OD-28 | Relationship to OD-12: OD-12 decides what to do with an infeasible plan before it starts; timing adaptation re-decides during | Adaptive execution |
| OD-29 | Tier ascent trigger: what power condition moves the tier pointer up | Adaptive execution |
| OD-30 | Cadence intervals: publish interval during idle versus active flow, and whether it is global or per-flow | Adaptive execution |

## Closed Decisions

| ID | Resolution |
| --- | --- |
| OD-1 | Recovery and startup execution are outside project scope. Other systems consume published artifacts. |
| OD-4 | Numbered shutdown tiers. See the change log entry of 2026-08-03. |
| OD-5 | Startup ordering is an advisory projection for subscribers, not an operator-executed graph. |
| OD-14 | Partial-domain outages compile to a domain-scoped shutdown subgraph when the trigger names a power domain or UPS device that maps to one. The planner prunes only groups whose resolved node membership is wholly outside the affected domain set, removes dependencies on those omitted groups, and keeps ambiguous, mixed-domain, and global groups in the plan. |
| OD-15 | Probe-history persistence uses PostgreSQL `capability_profile_verifications` rows for "last verified against firmware X" and drift evidence. |
| OD-16 | A node with no modeled `carries` path is a warning (`CommunicationPathUnmodeled`), not a hard failure, and `communicationPathExempt` marks the deliberate cases. Silent-assume stays excluded: the gap is always stated. |
| OD-18 | Tier inversion blocks the node. A node running a group scheduled to outlive it is withheld from power-off for the whole flow, and the withheld nodes are published on `status.blockedNodeReleases`. `spec.groups[].tierInversionPolicy: Allow` lets a group accept going down with its node. Migration is declined as a general remedy because node-local storage means there is not always anywhere to migrate to. |
| OD-21 | Driver configuration remains owned by `UPSDevice` spec in NUT vocabulary. Capability profiles declare behavior, aliases, and quirks; they do not default or render `ups.conf`, because that would make the matched profile a second driver-config source of truth. |
| OD-32 | The NUT operands are built from source against OpenSSL rather than installed from a distribution package. NSS supports client certificates on released NUT and OpenSSL does not, but NSS has no `CERTFILE` at all, so server TLS would need a certificate database provisioned in-container instead of the PEM a Kubernetes TLS Secret already projects. Server authentication and verified client connections both work on OpenSSL today; only mutual TLS is missing, and it is out of v1 scope. Revisit only if mutual TLS becomes required before NUT releases OpenSSL support for it. Alpine's NSS linkage was not a deliberate choice — its aport passes both `--with-nss` and `--with-openssl`, and NUT's `configure.ac` tests NSS first. |
| OD-22 | Quirks are structured objects with firmware scope as a field: `firmware.matches` glob patterns and `firmware.below` for a dotted-numeric fix release. Scope is resolved against the matched device, so a quirk fixed in firmware stops following devices past it (`F-26`). Vendor firmware strings are not generally orderable, so `below` accepts only dotted-numeric versions; a device whose firmware cannot be placed keeps the quirk and the diagnostic says why. |
| OD-23 | Telemetry variable aliasing lives in the profile telemetry section. A natively reported canonical name always outranks an alias; aliasing is one-directional and total; applied and shadowed aliases are both recorded as diagnostics. |
| OD-31 | A UPS that matches no product capability profile blocks `Enforce` mode, naming the devices, unless `spec.safety.allowUnidentifiedDevices` records acceptance. Dry-run compilation and review are unaffected. The catch-all profile is renamed from "universal floor" to the unidentified-device profile. |
| OD-25 | PDU capability profiles use a parallel kind, not an extension of `UPSCapabilityProfile`. v1 ships schema, validation, bundled catalog entries, and matcher scaffolding only; no PDU inventory entity, render path, or actuation path consumes them. |
| OD-26 | Dropped for v1. No `provenance` field exists in the API or internal profile model; `ProfileSource` (`CRD` versus `Bundled`) is the only modeled source distinction and it controls match precedence, not trust semantics. |
| OD-33 | No opt-in hook-completion wait exists in v1alpha1. A `ShutdownHook` is a bounded delivery attempt. Hook-level timeout wins, `PowerManagementCluster.spec.hooks.defaultTimeout` fills omitted hook timeouts, and shutdown proceeds after the attempt. |
| OD-34 | Hook failures are advisory. Failed or timed-out hooks record evidence and mark the owning `ShutdownFlow` degraded, but never engage `abortPolicy` and never hold a shutdown wave. |
| OD-37 | The operator path is the only path with authority to halt a node: an executor-issued signal delivered through an API-gated projected Secret, carrying flow identity, plan hash, and timestamp. NUT's local `SHUTDOWNCMD` path is declined as an authorization path and locked down — the writer, the signal format, and the local file stay in the codebase, but the actuator watches only the projected Secret and the shared tmpfs is not mounted into it, so no supported configuration turns it on. Decided against the last-resort-backstop reading: a backstop engages when the operator is unreachable, which is when ordering matters most, and `upsmon`'s local view cannot order anything — `MONITOR ... 1 ... secondary` with `MINSUPPLIES 1` fires on every node one UPS covers, at once, with an execution ID indistinguishable from the executor's. `SB-2b` reserves sequencing for the operator, and this applies that to the release signal. The accepted cost is stated in SB-3: an undeliverable signal means nodes are not released and the UPS runs down under a live cluster — observable and bounded, where an unordered fleet halt is neither. |
| OD-36 | The `clone`, `clone-outlet`, and `failover` drivers are deliberately declined, joining FSD (`F-20`) and `upssched` (`F-21`) as NUT mechanisms this project does not use. `clone` and `clone-outlet` are staged-shutdown sequencers — a virtual UPS presenting earlier thresholds than the device it shadows, so one physical UPS can drive several shutdown stages — and `SB-2b` reserves sequencing for the operator. Admitting either would put a second sequencer in the system with no view of the cluster, which is the same objection that declined `upssched`, and the closer upstream analog makes it more dangerous rather than less: `clone` looks like it would work. `failover` presents several physical devices as one, which sounds like the multi-supply-per-host topology `F-45` records as inexpressible — but that gap is in the render and the inventory model, not the driver layer, so the driver would not close it. Revisit `failover` only if `F-45` is built. All three ship in the operand image as part of `NUTSW_DRIVERLIST` and are not separately configurable at build time, so this decision governs the admission allowlist and the documentation, not the image. |

---

## Change Log

**2026-08-03 — OD-4 closed: numbered shutdown tiers.** The phase taxonomy is an integer tier
assigned to shutdown targets.

Direction: lower number shuts down later. Adding earlier tiers never renumbers critical ones.

Tier semantics:

- Tier 0 — never issued a stop; dies when its node powers off. Workload-only; a node assigned tier
  0 is rejected, since tier 0 means "stops when its node stops" and a node has nothing beneath it.
  Members: the operator's own pod, CNI, kube-system.
- Tier 1 — final orchestrated stop before node release. Lowest tier valid for nodes. Control-plane
  nodes default here: the operator cannot gracefully stop the control plane it is issuing commands
  through, but the node itself still shuts down cleanly at the OS layer via the actuator, with
  systemd stopping kubelet and etcd in order.
- Tier 2+ — progressively earlier.

Assignment and precedence, total and deterministic: explicit label on the object > selector rule in
central config > default tier. Same override-chain pattern as capability matching (RS-5); not a
field-level merge.

Storage: a central CR holds tier definitions, the default, and selector-based assignment rules,
giving one auditable tier map that hashes cleanly as structural input (PL-42). Per-object labels
override it for one-offs.

Default tier: configurable. Unconfigured, the default is the highest tier present — earliest
shutdown, safest failure. Tiers 0 and 1 are reserved and cannot be set as the default; a
misconfigured default of 0 would silently make everything last-ditch.

Scope: tiers apply to namespaces, workloads, and nodes. The existing `lastDitchRole` on
`PowerInventoryNode` maps to the tier scheme rather than remaining a parallel mechanism.

Cross-kind comparison is invalid. Tier numbers order workloads against workloads and nodes against
nodes. A tier 1 workload does not outrank a tier 2 node. Ordering across the two kinds comes
entirely from node-clearance edges (PL-20): a node cannot power off until its workloads have
cleared, regardless of tier.

Compilation: tier N+1 to tier N becomes derived edges labeled per PL-15, layered on the existing
`requires` mechanism, which continues to order within a tier. One ordering system underneath.

Unlabeled targets take the default tier and raise the unmatched-workload warning where no group
targets them at all.

TBD, not blocking:

- Whether an in-cluster audit store should be protected by tier 0 or tier 1 placement in addition
  to the local spool.
- Label key and central CR shape.

**2026-08-14 — OD-37 closed: one authorized path to a halt.** The operator path is authoritative;
NUT's local `SHUTDOWNCMD` path is declined as an authorization path and locked down. Written up in
SB-3; the mechanics are `F-55`–`F-57`.

Decided against the backstop reading the audit had carried as the likelier arm. The argument that
changed it: a backstop is only reached when the operator is unreachable, and that is exactly when
ordering matters most, so the moment it engages is the moment its inability to order becomes the
whole problem. Every agent renders `MONITOR ... 1 ... secondary` with `MINSUPPLIES 1`, which means
one UPS reaching OB+LB releases every node it covers simultaneously — a fleet-wide halt in arbitrary
order, stamped with an execution ID nothing distinguishes from a planned one.

What is kept: the writer binary, the signal format, and the local file. The shape is sound and a
later version may want it under a different authorization model, so it is disabled rather than
deleted.

Accepted cost, recorded so a later reader does not mistake it for an oversight: when the operator
cannot deliver a signal, nodes are not released, and the UPS discharges under a cluster that stays
up. That failure is visible and bounded. The alternative is not, and a second shutdown
implementation that engages when nobody is watching is worse than none.

**2026-08-15 — signals are withdrawn, not just expired (F-87).** Added to SB-3. The operator deletes
a node's key from the signal Secret once the execution that wrote it stops covering it, so absence
is the record that an episode ended. Previously only the TTL bounded a spent signal, and the
actuator's memory of what it had acted on was per-pod, so any replacement pod inside that window read
a standing order nobody had issued — sharpest on power restoration, where nodes boot inside the TTL
of the signal that halted them.

Rejected: suppressing DaemonSet rollouts during a flow as the guard. It treats the rollout as the
hazard, but a kubelet restart, an OOM kill, or an eviction produce the same empty-state pod without
touching the DaemonSet. Rollout suppression is still worth doing as hygiene and is filed as `F-92`.
Also rejected: moving the actuator's dedupe state to node annotations, which would hand node-patch
RBAC to a container holding `CAP_SYS_BOOT` and undo the API-less posture `OD-37` just established.
