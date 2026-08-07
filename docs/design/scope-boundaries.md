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
`Disabled | Stub | SystemdPoweroff` with no reboot verb.

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

## Repository-Derived Boundaries

*Components: NUT Server / upsd, Planning & Execution Logic, Operator Maturity & Hardening.*

Constraints already encoded in the implementation. Listed here so the boundary set is complete.

**RB-1 · Network-reachable UPS devices only.** Local USB and serial drivers are out of scope for
the API so that generated NUT server pods require no host device mounts and no privileged access
for UPS connectivity. See SB-4 for the extension path.

**RB-2 · Network driver allowlist.** The set is `snmp-ups`, `netxml-ups`, `powerman-pdu`,
`apcupsd-ups`, and `dummy-ups` for tests. Drivers are added deliberately, not by discovery.

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

---

## Open Decisions

| ID | Decision | Blocks |
| --- | --- | --- |
| OD-8r | Resolver behavior on malformed or missing model strings from the topology provider: reject, floor-match with warning, or configurable | Resolver design |
| OD-9 | Degrade mechanics for trigger-capability mismatch — folded into capability schema doc | Capability schema doc |
| OD-10 | USB and serial UPS support: version target and isolation model | v2 scoping |
| OD-18 | Tier inversion: lower-tier workload on higher-tier node. Node cannot clear under PL-20 while the workload runs. Options: compile-time validation, opt-in migration, node blocking. Node-local PVCs constrain migration | Planner tier compilation |
| OD-19 | FSD usage: whether NUT's forced-shutdown broadcast becomes the final release signal or is deliberately declined in favor of the executor's signal file. Affects whether shutdown is observable through standard NUT tooling | Executor design |
| OD-20 | Instant command scope and gating: which NUT instant commands and writable variables enter scope, how they are gated given they can cut power to equipment, and which capability profile fields declare support. Bounded by OD-1 on anything touching power-return | Capability schema |
| OD-21 | Driver configuration ownership: whether driver name, poll interval, and driver-specific parameters move from `UPSDevice` spec into capability profiles, or remain in spec with profiles supplying defaults and validation. Hybrid — profile default, spec override — is the likely answer (RS-5 pattern) | Capability schema |
| OD-22 | Firmware-conditional quirks: structured quirk objects with firmware constraints, versus firmware-ranged selectors and version-scoped profiles | Capability schema |
| OD-24 | Non-NUT power device actuation: second actuation path for power devices without NUT drivers, or permanently topological. Decided alongside OD-10, since both concern control surfaces outside the RB-1/SB-2a NUT-network-only posture | v2 scoping |
| OD-25 | PDU profile kind: schema shape for the parallel PDU capability kind, and which machinery is factored out of `UPSCapabilityProfile` for shared use. Scaffolding only in v1 | PDU scaffolding |
| OD-26 | Provenance field semantics: whether `provenance` is advisory metadata or affects resolution — for example, whether a `Community` profile requires explicit opt-in or emits a warning condition when matched | Capability schema |
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
| OD-15 | Probe-history persistence uses PostgreSQL `capability_profile_verifications` rows for "last verified against firmware X" and drift evidence. |
| OD-16 | A node with no modeled `carries` path is a warning (`CommunicationPathUnmodeled`), not a hard failure, and `communicationPathExempt` marks the deliberate cases. Silent-assume stays excluded: the gap is always stated. |
| OD-23 | Telemetry variable aliasing lives in the profile telemetry section. A natively reported canonical name always outranks an alias; aliasing is one-directional and total; applied and shadowed aliases are both recorded as diagnostics. |
| OD-31 | A UPS that matches no product capability profile blocks `Enforce` mode, naming the devices, unless `spec.safety.allowUnidentifiedDevices` records acceptance. Dry-run compilation and review are unaffected. The catch-all profile is renamed from "universal floor" to the unidentified-device profile. |

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

- Tier-inversion handling (tracked as OD-18): a lower-tier workload sitting on a higher-tier node.
  The node cannot clear under PL-20 while the workload is still running. Options include
  compile-time validation, opt-in migration, and node blocking; node-local PVCs constrain the
  migration path.
- Whether an in-cluster audit store should be protected by tier 0 or tier 1 placement in addition
  to the local spool.
- Label key and central CR shape.
