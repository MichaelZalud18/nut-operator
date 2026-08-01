# Scope Boundaries

Status: working document. Records what `nut-operator` is and is not responsible for.

These boundaries were settled across a design session (2026-07) and reconciled against the
repository at commit-time of writing. They are stable identifiers — `SB-n` values do not get
reused or renumbered.

## Sources of Truth and Precedence

| Domain | Authority |
| --- | --- |
| What currently exists — CRD shapes, rendering behavior, implemented reconcilers | Repository |
| Physical and structural architecture | Repository |
| Planner and application logic, orchestration semantics | This document and the design decisions it records |

Where this document and the repository disagree on planner or orchestration logic, this document
wins and the repository is what changes. Where they disagree on what is currently implemented,
the repository wins.

## Governing Principles

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

---

## SB-1 · Shutdown and recovery are separate concerns

Shutdown orchestration and bring-up orchestration are distinct control paths. NUT's own scope
stops at clean shutdown; conventional bring-up is BIOS/PDU behavior plus service recovery on boot.

Whether recovery is in-project at all, and if so in which version, is **open** (OD-1).

## SB-2 · Relationship to NUT

**SB-2a · NUT is the power-state path, unconditionally.** The operator never speaks to UPS
hardware directly. All UPS interaction is mediated by NUT. A device with no viable NUT network
driver is unsupported; it is not a candidate for a bypass path.

What varies by device is what returns over that path, not whether the path is used. Device
variation is capability-profile content (SB-9) and resolves in three tiers:

1. **Transport** — does a network driver exist for this device. Binary admission gate on
   `UPSDevice`, partly enforced today by the driver allowlist.
2. **Telemetry richness** — which NUT variables the driver actually reports. Two devices both
   speaking `snmp-ups` may differ by an order of magnitude in exposed variables.
3. **Actuation** — outlet control, delayed start, safe-shutdown handshake. See SB-2c.

**SB-2b · NUT's native threshold model is an input, never the sequencer.** NUT can trigger on
low-battery status, remaining runtime, or battery percentage. Those feed operator triggers.
Cluster ordering, wave computation, and dependency resolution belong to the operator and are
never delegated to NUT.

**SB-2c · NUT is a UPS control interface, not a read-only sensor.** Where the device supports it,
NUT can command outlets off and on and perform the safe-shutdown handshake that lets the UPS cut
power after the last client is down. Nothing in the implementation uses this today. It is an
unclaimed capability surface, gated per-device by capability profile.

## SB-3 · The node agent holds local flow; it holds no cluster authority

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
carries no Kubernetes API token unless a future feature explicitly requires one. The `actuator`
container carries no NUT credentials and no policy authority.

## SB-4 · Two containers in the node agent for v1

All decision logic lives in the unprivileged container. The privileged executor stays small,
dumb, and fire-and-forget behind a minimal verb API. No third container, no second pod, no
sidecar proliferation.

**Deferred to v2 (OD-10):** a third container or separate DaemonSet enabling USB-attached UPS
support. This is a deliberate future extension, not an oversight.

Recorded so the future reversal is not mistaken for a contradiction: the current network-only
posture is load-bearing for the security narrative. `docs/images.md` and `docs/architecture.md`
both justify the absence of host device mounts, host device access, and privileged mode on the
grounds that UPS reachability is network-only. USB support must arrive with its own isolated
actuation boundary and its own security rationale.

## SB-5 · Kured is out of scope

Kured's trigger is host and service health — OS patch reboots. That is not a power event, so
GP-1 excludes it. The overlap that initially looked worth absorbing does not meaningfully exist.

No dependency, no coordination shim, no feature parity commitment. `ActuatorPolicy` remains
`Disabled | Stub | SystemdPoweroff` with no reboot verb.

If a reboot verb is ever added to the executor, the motivation is collision-avoidance with a
separately installed Kured, not feature value.

**Coexistence caveat for documentation:** both projects cordon nodes. A Kured reboot interleaving
with a power event is possible and should be documented as an operational note.

## SB-6 · Health and hardware monitoring are out of scope

Per GP-1 and GP-4. Node Problem Detector is excluded for the same reason as Kured — its trigger is
node health, not power. MachineHealthCheck and descheduler are likewise out.

The operator consumes health and readiness signals as planning inputs. It does not detect,
diagnose, or remediate node faults.

## SB-7 · Power budget modeling is in scope

Runtime remaining and load-shedding arithmetic — what shedding a given set of workloads buys in
additional minutes — are in scope and were identified as the differentiating capability.

Currently unbuilt and under-specified. It carries an unresolved dependency: shed-value arithmetic
requires per-workload power attribution, which no component in the current design provides.

## SB-8 · NetBox is a heavy design influence and a zero-weight runtime dependency

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

Semantic version on the profile itself. Bump only when behavior changes. Map device model, and
optionally firmware, to a best-matching profile version at resolution time.

Explicitly not tracked: per-physical-device versions, or every hardware and firmware combination.
The pattern is the same one used to decouple an API schema version from an implementation version.

Profiles are structured in two sections: declared NUT variables (telemetry), and declared behaviors
and quirks (actuation). Corrections that reduce declared capability are MAJOR bumps even though they
are behaviorally fixes, because plans validated against the prior version may fail trigger
validation under the corrected one.

## SB-10 · Workload shutdown orchestration is core scope; the mechanism is stock Kubernetes

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

Third-party ecosystem integrations are deferred, not prohibited. One opt-in integration exists
today: `ServiceMonitor` rendering when Prometheus Operator support is enabled.

## SB-11 · PostgreSQL is a required production component; the implementation is the user's choice

The durable store is PostgreSQL. CNPG is the recommended in-cluster implementation, not a
privileged assumption of the design. `PowerStorageMode` is `Disabled | ExternalPostgres | CNPG`,
defaulting to `CNPG`, with `Disabled` supported for development only.

PostgreSQL sits on the **record** path, not the **decision** path. Compiled plans and desired
state live in CRs; the planner compiles from spec. A PostgreSQL outage degrades auditability. It
must not halt power response.

Resilience note worth documenting for users: for the audit store specifically, `ExternalPostgres`
is the more resilient option, because a database outside the cluster is not in the shutdown path
of the event it exists to record. It cannot be the default — it requires infrastructure the user
may not have — but it sidesteps the durability problem in OD-6 entirely.

## SB-12 · Three-tier observability

| Tier | Content | Destination |
| --- | --- | --- |
| Info | Human-readable operational events | Logs, events |
| Debug | Operator decisions, compiled plan detail, wave rationale | Logs, CR status |
| Audit | Immutable record of what actually executed | PostgreSQL |

Three distinct artifacts: the event log, the execution log, and the compiled plan record.

The compiled dependency graph is stored as structured data, never as formatted text, so that it
stays queryable and renderable. "Show me the compiled graph" and "why was this node in wave four"
must be answerable from stored structure, not from log parsing.

## SB-13 · Planner implementation defaults (reversible)

Not a boundary. A v1 default that can change without renegotiating scope.

The planner is a Go package inside the operator repository, kept behind an interface so it stays
substitutable. Any future decoupling happens at the interface level — a second implementation
reachable over gRPC, for instance — not at the language level.

---

## Repository-Derived Boundaries

Constraints already encoded in the implementation. Listed here so the boundary set is complete.

**RB-1 · Network-reachable UPS devices only.** Local USB and serial drivers are out of scope for
the API so that generated NUT server pods require no host device mounts and no privileged access
for UPS connectivity. See SB-4 for the deferred v2 path.

**RB-2 · Network driver allowlist.** Initial set: `snmp-ups`, `netxml-ups`, `powerman-pdu`,
`apcupsd-ups`, and `dummy-ups` for tests. Drivers are added deliberately, not by discovery.

**RB-3 · No third-party NUT image as default.** Four project-owned OCI images: `nut-server`,
`upsmon-agent`, `node-actuator`, `operator`. Community NUT images are development scaffolding only
— many assume direct USB access and document privileged container usage, which contradicts RB-1.

**RB-4 · Dual approval gates.** `ShutdownFlow` enforcement and `NodePowerAgent` actuation are
separately approved. A production deployment requires both before host shutdown is rendered.

**RB-5 · Graph is the primary plan model.** Linear `spec.steps` remains available for small or
test installations. It is not the production model because it cannot express dependency
relationships or concurrent branches.

**RB-6 · CRDs are cluster-scoped.** Five resources: `PowerManagementCluster`, `UPSDevice`,
`NUTServer`, `NodePowerAgent`, `ShutdownFlow`.

**RB-7 · Admission webhooks are planned, not present.** Until they land, unsafe combinations are
rejected during reconciliation rather than before persistence.

---

## Consolidated Out of Scope

- Bringing hosts and services back up — pending OD-1.
- Kured, and OS-patch reboot orchestration generally.
- Node Problem Detector, MachineHealthCheck, descheduler, and node fault remediation.
- General hardware monitoring.
- General cluster health management.
- Rebuilding any monitoring, alerting, or inventory capability that already exists in the
  operator's environment.
- Local USB and serial UPS connectivity — deferred, see OD-10.
- Delegating shutdown ordering to NUT.
- Any UPS interaction path that bypasses NUT.

---

## Open Decisions

| ID | Decision | Blocks |
| --- | --- | --- |
| OD-1 | Recovery and startup scope: in-project or not, and in which version | OD-5 |
| OD-4 | Last-ditch phase taxonomy — the actual enumeration behind "must stay until phase X" | Planner design |
| OD-5 | `requires` edge inversion. Shutdown semantics compile `applications requires: [databases]` to `applications -> databases`; the required group shuts down later. Correct for shutdown, inverted for startup, and it will not invert symmetrically against `before` and `after` | Contingent on OD-1 |
| OD-6 | Audit durability during shutdown. The sample flow scales `databases` and `storage` down in early waves while later waves still have execution records to write. Options: local spool draining post-recovery, audit cluster exempted into the last-ditch set, or documented preference for `ExternalPostgres` | Audit writer |
| OD-8r | Resolver behavior on malformed or missing model strings from the topology provider: reject, floor-match with warning, or configurable | Resolver design |
| OD-9 | Degrade mechanics for trigger-capability mismatch — folded into capability schema doc | Capability schema doc |
| OD-10 | USB and serial UPS support: version target and isolation model | v2 scoping |
| OD-15 | Probe-history persistence — "last verified against firmware X" implies a PostgreSQL table in the audit schema that would not otherwise exist | Audit schema doc |
| OD-16 | Missing `carries` coverage — node with no modeled communication path: hard failure or explicit exemption marker. Silent-assume excluded | Inventory validation |

---

## Change Log

**2026-07-31 — initial consolidation.** Extracted from the design session and reconciled against
the repository. Four positions were corrected during reconciliation:

- SB-3 was initially recorded as "the node agent holds no flow." The repository's signal-file
  handoff protocol contradicts this. Corrected to a statement about authority.
- SB-5 was initially recorded as "Kured absorbed, optional, non-central." Corrected to out of
  scope, with GP-1 as the generalized rationale.
- SB-8 was initially recorded as "NetBox is a first-class input." Corrected to optional provider.
- SB-11 was initially recorded as "PostgreSQL is optional memory." The repository contradicts this
  in four places. Corrected to required production component.

**2026-07-31 — capability deconfliction.** SB-8 storage and merge questions resolved (OD-7 closed,
OD-8 dissolved). SB-9 gains the two-section structure and the correction-is-MAJOR rule. OD-8r and
OD-15 added. OD-9 narrowed to degrade mechanics and folded into the capability schema doc.

**2026-07-31 — inventory contract.** GP-5 extracted. SB-8 gains the provider-contract reference.
OD-2 collapsed (one entity set, two relations, logical graph is compiled output) and OD-3 closed
(modeled minimally). OD-16 added.
