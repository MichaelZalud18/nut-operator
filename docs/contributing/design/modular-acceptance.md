# Modular acceptance coverage

Components: Modular Deployment Profiles.
Audience: contributors.

MOD-5 tests selected deployment contracts, not every subset of components. Acceptance belongs
with the component or existing Kind fixture that owns the behavior. Do not treat skipped,
unimplemented profile scenarios as a passing supported-profile suite.

The approved v1 scope is MOD-2 advisory mixed actuation and MOD-4 managed NUT-only, including
NUT-protocol telemetry consumers. US-1 and US-3 external execution acceptance belongs with
their post-v1 tasks; it does not block v1 MOD-5 completion.

## Executable component coverage

```sh
make test-modular-components
```

This runs ordinary Go tests, also included in `make test` and the existing Tests workflow.
API/controller changes trigger that workflow; changes to the mixed-hooks YAML fixture also
trigger it. There is no new image, dependency version, VM job or separate infrastructure suite.

| Contract | Test owner | Evidence boundary |
| --- | --- | --- |
| Mixed advisory hook and built-in agent | `internal/controller/modular_acceptance_test.go` | Real reconciliation, trigger evaluation, compilation, execution, loopback HTTPS and signal publication; fake Kubernetes and audit storage |
| Public authored mixed-flow example | `test/quickstart/mod2_research_test.go` | Example decoding/admission validation, inventory resolution and ordered compilation |
| Hook request and receiver behavior | `internal/kubeactions/mod2_research_test.go` | Fake HTTP transport; explicit targets, auth, allowlist, dry-run, repeated operation and advisory failure |
| Fresh agent authorization and release safety | Existing `shutdownflow_release_*_test.go` | Revocation, generation/selection drift, stale telemetry, wrong pod/node, clearance and missing validator; fake API reads/writes |
| NUTServer minimal render dependencies | `internal/controller/mod4_research_test.go` | Only UPSDevice/NUTServer custom kinds registered; default image, client selectors, repeated rendering and rejection of omitted PMC dependency |
| Profile startup and generated bundle | `cmd/profile_test.go`, `hack/nut-only/main_test.go` | Invalid flags/admission disabling refused; generated CRD/webhook/RBAC subset checked against the canonical BYO-cert manifests |

The mixed controller scenario uses an actual ShutdownFlow object with a higher-tier external
hook followed by AgentShutdown for one authored Kubernetes worker. The external host exists
only in explicit hook data, never as a Node, inventory node or agent target. The local HTTPS
receiver verifies the synthetic Secret-backed bearer header and target; its certificate is
trusted by the test client without disabling certificate verification.

Cases cover successful delivery, repeated work under a second flow identity, HTTP 503,
timeout, rejected endpoint allowlist, default dry-run, explicit rehearsal, missing flow approval,
and agent approval revoked during the hook. Tests assert request/signal order, completion and
advisory failure evidence, the exact signal key, and absence of signal Secrets in denied cases.
The receiver's in-memory ensure-stopped state is only a harmless repeat-safety fixture. It is
not durable external-host completion evidence. No process with host actuation privileges runs.

Telemetry freshness is explicitly disabled in the mixed fixture to isolate composition;
the same target separately runs existing fresh/stale telemetry and publication-boundary tests.
Audit storage is fake. This suite does not establish PostgreSQL, admission-server/RBAC behavior,
network policy, kubelet Secret projection or actual host power-off.

## Owned Kind mixed-flow coverage

The existing TEST-2 logical ShutdownFlow scenario includes a harmless external receiver and
five ordered hooks before scale, drain and Simulate-agent publication. It exercises real
admission, Secret-backed auth, receiver ingress policy, explicit rehearsal/default dry-run,
repeat-safe effects, failure/timeout/allowlist outcomes and PostgreSQL action ordering.
It reuses the existing pinned Python fixture image and owned three-node cluster lifecycle.

```sh
python3 -B hack/test-kind.py --focus 'logical ShutdownFlow'
```

The receiver's HTTP endpoint is confined to this isolated test; HTTPS verification has separate
component coverage. This does not prove actual external-host or guest halt. See the
[dated run evidence](../audits/mod-2-kind-acceptance-2026-09-18.md) for results and image identities.

## Owned Kind NUT-only coverage

The serial NUT-only scenario uses the same owned Kind runner and pinned suite images, with a
separate installation lifecycle. It installs only the two power CRDs and their admission entries,
bootstraps webhook certificates with the existing script, and exercises real NUT protocol
clients from allowed and denied namespaces. No planner, agent or database is installed.

```sh
CERT_MANAGER_INSTALL_SKIP=true python3 -B hack/test-kind.py --focus 'NUT-only'
```

`test/e2e/nut_only_test.go` owns TLS/auth failures, ingress, device add/remove/config changes,
credential/certificate rotation, driver recovery, reapplication and manager restart.
`nut_only_relay_test.go` checks generated repeater configuration, update propagation,
rejected unsupported auth modes and strict/non-strict startup. The client requires STARTTLS
with CA/hostname verification. It never issues a shutdown command.
The [dated MOD-4/NS-11 evidence](../audits/mod-4-5-nut-only-acceptance-2026-09-18.md)
records the passing live run, image identities, discovered defects and qualification limits.

## Profile qualification dependencies

| Story | Required before profile acceptance can pass | Owning continuation |
| --- | --- | --- |
| US-1 existing external NUT and agents (post-v1) | Selected shutdown authority, typed targets/supplies, approval/storage contract, implemented selective package | Post-v1 MOD-1, including omitted-controller/CRD/RBAC and real-NUT-to-Simulate acceptance |
| US-2 mixed actuation | Existing advisory contract, public receiver contract and component/Kind scenario | Existing TEST-2 mixed hook/Simulate scenario; do not change hooks into confirmed-halt gates |
| US-3 aggregation only (v1) | MOD-4 NUT-protocol interface, selective startup and compatible upstream rendering implemented | MOD-4 and NS-11; demonstrate queries/updates with planner absent |
| US-3 aggregation plus agents (post-v1) | Approved and implemented external execution request boundary | Post-v1 MOD-3; request approval, targeting, expiry and cancellation tests cannot be replaced by tests of internal Secret writes |
| US-4 managed NUT only (v1) | Selected NUTServer/both-admissions baseline, generated profile/RBAC, image/TLS/client policy and SB-11 exception implemented | MOD-4; clean-cluster readiness, allowed/denied clients, updates, rotation, isolated driver restart and upgrade |

Use existing guest qualification for Linux/Talos power-off rather than repeating it for each
package. The component target above never claims these conditional install tests ran. Current
task status belongs to the [modular task records](../../tasks.md#modular-deployment-profiles).
