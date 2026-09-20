# MOD-4: managed NUT-only profile

Components: Modular Deployment Profiles; NUT Server / upsd.
Date: 2026-09-18.
Scope: source/dependency research at `cf599655ab54b1522663774d508f0b6453a8ab11` plus shared
working-tree changes and fake-client component tests. No profile installer exists from this work.
Task owner: [MOD-4](../../tasks.md#modular-deployment-profiles).

## Recommendation

Use the existing manager binary with an explicit NUT-only profile: NUTServer reconciler,
UPSDevice/NUTServer CRDs and their admission, plus shared manager infrastructure. Initially
omit the UPSDevice reconciler unless normalized Kubernetes telemetry is a selected requirement.
Clients can read the managed upsd Service directly. This keeps US-4 distinct from MOD-3's
external execution interface and avoids inventing a second operator binary.

The existing two-container operand remains the managed unit: upsd and driver-supervisor,
shared runtime/config mounts, readiness, credential separation and update behavior. A raw
nut-server image requires the user to reconstruct those pieces and is not an equivalent install.

## Controller-set comparison

| Item | NUTServer + both admissions | Also UPSDevice reconciler |
| --- | --- | --- |
| Device specs | Reads selected UPSDevice specs and validates before rendering | Same |
| Device status prerequisite | None for rendering; name falls back to object name | Adds normalized telemetry, identity and capability status |
| Other power API reads | Direct render probe needed only UPSDevice and NUTServer | Capability matching lists UPSCapabilityProfile; account for that CRD/RBAC dependency |
| NUT polling | upsd clients; server's upstream TCP probe is not protocol telemetry | Controller polls configured NUT targets |
| Database | NUTServer rendering does not need PostgreSQL | Telemetry recorder is a no-op without managementClusterRef |
| User promise | Managed NUT service and NUTServer readiness; no fresh UPSDevice telemetry promise | Richer status, more API dependencies and polling |
| Fit | Recommended US-4 baseline | Optional MOD-3 telemetry extension after explicit scope decision |

Admission implementations in `internal/webhook/v1alpha1/{upsdevice,nutserver}_webhook.go`
perform defaulting/shape validation without a Kubernetes client dependency. Keep those checks
and renderer validation. Disabling all admission is not a profile design.

## Bounded dependency proof

`internal/controller/mod4_research_test.go` registers only UPSDevice and NUTServer custom kinds
plus required built-in Kubernetes kinds. It invokes the real NUTServer reconciler twice with
an explicit operand image, device spec and namespace, no managementClusterRef, and no device
status. It asserts one two-container Deployment and the device's ups.conf in a Secret. Both
reconciles passed without registering planner, inventory, agent, management-cluster or capability
custom kinds. A dependency on an absent kind through that fake client would fail the probe.

The test deliberately uses TLS Disabled to isolate API dependencies. It does not establish
TLS defaults or secure installation. Existing TLS assembly/Secret-validation and render/watch
tests passed separately. No Kubernetes API server, admission server, watches, real pod readiness,
RBAC enforcement, CNI traffic or resource-cost measurement was exercised by this test.

Reproduce the core probe:

```sh
go test ./internal/controller -run '^TestMOD4' -count=1
```

## Packaging and usability decisions

| Work | Recommended implementation boundary |
| --- | --- |
| Startup selection | Central explicit profile controls reconciler and webhook registration together. Current `cmd/main.go` registers all 12 controllers; omitting objects or CRDs alone is insufficient |
| CRDs/RBAC | Generate the two-CRD profile and permissions for owned workloads/config/Secrets, namespace lifecycle, Services, policy and PDBs, plus manager lease/events/admission needs. Omit planner/actuation permissions |
| Operand image | Supply a distribution-owned default without requiring a dummy PowerManagementCluster; today the standalone render needs explicit image.repository |
| TLS | Preserve Required posture and certificate validation; choose documented BYO certificates or a bootstrap dependency deliberately. Test hostname/CA verification and rotation, not just rendered mounts |
| Credentials | Preserve OperatorManaged admin/monitor credentials and ExistingSecret; keep driver credentials out of upsd's mounts and client credentials appropriately scoped |
| Ingress | Add explicit namespace/pod selectors and/or CIDRs for desired clients; do not rely on Service exposure alone |
| Storage | Record the narrow US-4 no-PostgreSQL exception to SB-11, with logs/events/current status rather than full execution history |
| API promises | Document that UPSDevice status is not populated by the baseline; do not promise normalized telemetry while omitting its reconciler |

The current policy allows same-namespace pods and labeled manager pods across namespaces,
not arbitrary external or cross-namespace clients. Service routing can affect the source
address used by CIDR policy. Allowed/denied traffic must be tested with the chosen Service
type and an enforcing CNI. [Kubernetes NetworkPolicy documentation](https://kubernetes.io/docs/concepts/services-networking/network-policies/).

Upstream relay is optional functionality within the NUT service. The MOD-3 live probe found
that generated authconf is incompatible with the pinned 2.8.5 operand; [NS-11 evidence](mod-3-external-planning-2026-09-18.md#live-relay-experiment-and-compatibility-finding)
must be resolved before including upstreamNUT in profile acceptance. Direct drivers and the
isolated render result do not establish relay compatibility.

## Remaining acceptance

After profile implementation, MOD-5 should install only its resources into an isolated clean
cluster and prove real dummy-ups reads, Ready two-container operands, device add/remove/config
updates, isolated driver restarts, credential/certificate rotation, reconciliation and upgrade.
Assert omitted watches are absent and the manager cannot mutate planner or actuation resources.
Test authenticated/TLS clients, allowed and denied ingress, and actual source identity under
the intended exposure mode. Compare selective-manager cost with the MOD-1 baseline only after
the selective manager exists. Hadron/Talos guest-poweroff tests are not prerequisites for this
non-actuating profile research.

MOD-4 remains open for those decisions, implementation and acceptance. This research changes
neither the settled full-install contract nor advertised supported profiles.
