# FAQ

Answers to the questions the design most often prompts. Internal identifiers (SB-n, GP-n, etc.)
reference the design documents; users can ignore them.

## Does this replace Kured?

No, and it deliberately does not try to.

Kured reboots nodes because the OS needs patching. `nut-operator` shuts nodes down because power is
failing. The end action looks similar — both cordon, both drain, both act on the host — but the
triggers are different products. This project's rule is that trigger provenance defines scope: if
the initiating signal is not power state, it is out of scope (GP-1, SB-5).

The two coexist. One operational caveat: both cordon nodes, so a Kured reboot interleaving with a
power event is possible. The power flow treats an already-cordoned node like any other node it must
clear.

## Why network-reachable UPS devices only? My UPS is USB.

Because it keeps the entire UPS path unprivileged.

USB and serial UPS access requires host device mounts and, in most community images, privileged
containers. Network drivers (`snmp-ups`, `netxml-ups`, and the rest of the allowlist) require
neither, which is why the NUT server pods run non-privileged with read-only roots and no host
access (RB-1, RB-3).

USB and serial support live outside the network-first baseline. If added, they use their own
isolated boundary rather than weakening the unprivileged network path (SB-4, OD-10).

## Do I need NetBox?

No. NetBox shaped the data model heavily, but the default build ships without it.

Topology — which UPS feeds which node, which switch carries which control path — is declared
through CRDs by default. NetBox is an optional provider that can supply the same information from
your existing inventory. If you run NetBox, the operator renders a snapshot of it at reconcile
time; it never queries NetBox live during a power event (SB-8, IN-14).

## Can I add my own capability profiles, and will an upgrade overwrite them?

Yes, and no.

Capability profiles are `UPSCapabilityProfile` custom resources. Profiles you create live in your
cluster as CRs; the profiles that ship with the operator live inside the operator image. Upgrading
the operator replaces the bundled set and never touches your resources.

Where both cover the same device, yours wins. Profile matching walks a fixed precedence chain —
exact model and firmware, then exact model, then model glob, then driver family, then the
universal floor — and within any tier, a profile you supplied outranks a bundled one. You do not
need to fork the catalog or disable bundled profiles to override one.

The bundled catalog is deliberately small. Profiles marked `ProjectVerified` have been exercised
against real hardware; the project cannot verify devices it does not own. If you have a device
that is not covered, a contributed profile is welcome.

## Do I need PostgreSQL?

For production, yes. For development, no (`storage.mode: Disabled`).

PostgreSQL holds the record: audit events, execution history, what actually shut down and in what
order. It does not hold decisions — compiled plans live in Kubernetes, and a PostgreSQL outage
degrades auditability without halting power response (SB-11, GP-3).

CloudNativePG is the recommended in-cluster implementation. If you have PostgreSQL outside the
cluster, `ExternalPostgres` is actually the more resilient choice for this workload, because a
database outside the cluster is not in the shutdown path of the event it is recording.

## Why doesn't it bring my cluster back up?

Recovery is a genuinely different problem. Shutdown runs while the control plane still exists;
bring-up starts from hardware settings (BIOS, PDU delayed-start) before any orchestration exists to
help. NUT's own scope ends at clean shutdown for the same reason.

Recovery orchestration is not owned by this project (SB-1, OD-1). External recovery systems can
consume the published dependency graph and advisory startup wave projections, but `nut-operator`
does not execute bring-up.

## Is there a UI?

No dedicated UI ships in v1.

Kubernetes is the interface: CRDs, GitOps-managed manifests, `kubectl`, Events, logs, status, and
PostgreSQL audit records. A future UI can exist as a separate subscriber of the published planner
artifacts, not as part of the core operator.

## Can this shut my nodes down by accident?

It is designed so that it cannot shut anything down until you have said so twice.

Every layer defaults to dry-run. Real host shutdown requires both an enforcement approval on the
`ShutdownFlow` and an actuation approval on the `NodePowerAgent` — two separate annotations,
reviewable in Git, visible in status before they take effect (GP-2, RB-4). Dry-run executes the
entire flow except the effects, so you can rehearse the real plan, not a simulation of one.

## What happens if the operator can't see the UPS during an outage?

The design treats this as a first-class scenario rather than an edge case. The communication path
between operator and UPS — every switch on it — is modeled in the topology, and those devices are
ordered late in shutdown precisely so the event pipeline stays alive until the end. Telemetry loss
raises an explicit condition, and stale power data can never produce an optimistic feasibility
verdict (PL-32, RS-17).

## Why is host shutdown isolated in an actuator?

Host shutdown is the dangerous boundary, so it is kept out of the NUT client and out of the planner.
The `upsmon` container observes power state without host authority. The actuator receives only the
approved, fresh signal file and performs the local host action under the selected actuator policy.
