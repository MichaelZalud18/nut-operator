# FAQ

Components: Cross-cutting.

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

## What if my UPS does not have a packaged capability profile?

Most won't, and that is expected. UPS hardware varies enormously in what it reports, and the
project can only verify devices it owns. Two steps:

**1. Build one with the probe helper.** Create a `UPSCapabilityProbe` pointing at your `UPSDevice`:

```yaml
apiVersion: power.zalud.io/v1alpha1
kind: UPSCapabilityProbe
metadata:
  name: unidentified-rack-a-ups
spec:
  deviceRef:
    name: rack-a-ups
```

The operator reads what the device actually reports and writes a ready-to-apply profile into
`status.draftProfile`, along with the variables it saw, any non-standard names worth a look, and
suggested aliases where a standard reading appears to have arrived under a different name:

```sh
kubectl get upscapabilityprobe unidentified-rack-a-ups \
  -o jsonpath='{.status.draftProfile}' > my-ups-profile.yaml
```

Review it before applying. The draft declares only what the device demonstrably reported, and the
actuation section is deliberately left empty — actuation commands can cut power to equipment, so
support is declared only after it has been verified against the firmware you are running. Anything
the helper inferred rather than observed is marked as a suggestion in a comment.

**2. Send it upstream.** `status.issueReport` is the same findings formatted for a GitHub issue,
with a verification checklist:

```sh
kubectl get upscapabilityprobe unidentified-rack-a-ups \
  -o jsonpath='{.status.issueReport}'
```

Open an issue with that and we will add the profile to the bundled catalog, so the next person with
your hardware does not have to repeat the work. Contributions are how the catalog grows.

Probing is advisory throughout. A probe never changes how a device resolves, never feeds the
planner, and never runs while power is failing — it reads a device and writes a draft, nothing more.

**Until a profile matches, `Enforce` mode is blocked.** A device with no profile is not a device
with reduced capability; it is a device nothing has been verified about. Dry-run still compiles and
publishes the full plan, so you can review exactly what would happen — but the operator will not cut
power to real nodes on an unverified device's signal. If you accept that risk deliberately, set
`spec.safety.allowUnidentifiedDevices: true` on the flow, which records the decision in Git where it
can be reviewed (OD-31).

## Will upgrading the operator overwrite capability profiles I wrote myself?

No. Profiles you write are `UPSCapabilityProfile` custom resources living in your cluster; the
bundled catalog ships inside the operator image. An upgrade replaces the bundled set and never
touches your resources.

Where both describe the same device, yours wins. Matching walks a fixed precedence chain — exact
model and firmware, then exact model, then model glob, then driver family, then the
unidentified-device profile — and within any tier a profile you supplied outranks a bundled one. So
overriding a bundled profile takes nothing more than writing your own; there is no catalog to fork
and nothing to disable.

The bundled catalog is deliberately small. Profiles marked `ProjectVerified` have been exercised
against real hardware, and the project cannot verify devices it does not own — which is why
contributed profiles are how the catalog grows.

## Do I need PostgreSQL?

For production, yes. For development, no (`storage.mode: Disabled`).

PostgreSQL holds the record: audit events, execution history, what actually shut down and in what
order. It does not hold decisions — compiled plans live in Kubernetes, and a PostgreSQL outage
degrades auditability without halting power response (SB-11, GP-3).

CloudNativePG is the recommended in-cluster implementation. If you have PostgreSQL outside the
cluster, `ExternalPostgres` is actually the more resilient choice for this workload, because a
database outside the cluster is not in the shutdown path of the event it is recording.

## Why does a Kubernetes operator need a database when most operators don't?

Most operators don't need one because reconciliation makes history disposable: lose everything you
knew, and the next reconcile re-derives current state from `spec` plus the live cluster. What
happened along the way goes out as Events and metrics, and nobody misses it.

That does not work here. The events worth recording happen while the cluster is going down, and what
they leave behind is absence — there is no status to read afterward, because the nodes are off.
Which nodes released, in what order, against what battery reading, and why a wave stopped early
cannot be re-derived from a cluster that no longer exists. Separately, telemetry snapshots arrive
one row per device per poll indefinitely, which is not a load etcd should carry.

So Kubernetes holds desired state and current summaries, and PostgreSQL holds history (GP-3). The
arrangement is uncommon for an operator but not unprecedented: Argo Workflows archives completed
workflows to PostgreSQL for the same reason, and Tekton Results does the same. Full reasoning is in
`docs/contributing/design/audit-storage-schema.md`.

## What happens to audit records if PostgreSQL is down during a shutdown?

Nothing stops. Power response never waits on the audit trail (SB-11).

If `spec.storage.auditSpool` is enabled, records PostgreSQL refuses are appended to a local JSONL
journal on a durable volume, and the `ShutdownFlow` reports `AuditSpoolFallback`. The next reconcile
that successfully opens the audit store drains the journal back into PostgreSQL and deletes it. The
records carry their original IDs and every insert is an upsert, so replaying is a no-op if part of it
already landed.

The journal is capped (`maxSize`, default `64Mi`). If it fills, the operator stops spooling and says
so under a separate `AuditSpoolFull` reason, because losing audit records is a different problem than
delaying them — and filling the volume during a power event would be worse than either.

If the spool is not enabled, records PostgreSQL refuses are lost, and the flow still executes.

## Why doesn't it bring my cluster back up?

Recovery is a genuinely different problem. Shutdown runs while the control plane still exists;
bring-up starts from hardware settings (BIOS, PDU delayed-start) before any orchestration exists to
help. NUT's own scope ends at clean shutdown for the same reason.

Recovery orchestration is not owned by this project (SB-1, OD-1). External recovery systems can
consume the published dependency graph and advisory startup wave projections, but `nut-operator`
does not execute bring-up.

What actually powers hardware back on is out-of-band by necessity: BIOS "restore on AC loss", PDU
outlet delayed-start (`outlet.n.delay.start`), Wake-on-LAN, or a BMC/IPMI call. All of them work
without a running operating system, which is the whole requirement. `status.startupWaves` gives a
recovery system the order to apply them in.

Kured is not the tool for this. It reboots nodes that are already running, so a powered-off node has
nothing on it to act. The two are complementary but unrelated: Kured handles reboots for OS
patching, this handles shutdown for power events, and neither brings hardware up from cold.

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
