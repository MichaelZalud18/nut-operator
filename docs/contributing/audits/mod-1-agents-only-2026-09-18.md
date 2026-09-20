# MOD-1: Agents-Only Installation Research

Components: Modular Deployment Profiles; Node Agent / DaemonSet.
Audience: contributors.

Research basis: checkout `cf599655ab54b1522663774d508f0b6453a8ab11`, with unrelated
planner and documentation changes already present. No Hadron/Talos harness, planner,
production controller, or API changes were made for this investigation.
Status belongs to [MOD-1](../../tasks-post-v1.md#modular-deployment-profiles).

## Recommendation

Prefer a selectively enabled manager using the existing binary and agent renderer over
maintaining standalone DaemonSet templates. Retain NodePowerAgent as the configuration API,
add typed external NUT targets, and package only the controllers, admission, CRD and RBAC
that profile needs. This is a recommendation, not an approved or implemented profile.

The central unresolved decision is **who authorizes shutdown without the planner**.
Direct NUT connectivity is feasible with the shipped monitor image. A useful shutdown
profile cannot be obtained just by omitting NUTServer or changing an address:

- Existing `Operator` authority requires the executor and its private signal channel. Without
  that writer, a standalone monitor/actuator pair cannot fulfill US-1's shutdown outcome.
- Proposed `LocalNUT` authority can fulfill an explicitly unordered, per-node shutdown model,
  but requires revising OD-37/SB-3 and the SB-2b sequencing boundary. It must never be fallback
  for unavailable orchestration. It supplies no Kubernetes drain or control-plane-last guarantee.
- A public external shutdown-request API is another design, owned with MOD-3. Do not invent
  it inside MOD-1 or expose raw halt-Secret writes as that API.

If LocalNUT is declined and no external request contract is selected, US-1 remains unsatisfied;
a monitoring-only package must not be advertised as node-shutdown protection.

## Current Dependency Trace

| Boundary | Current implementation | Agents-only consequence |
| --- | --- | --- |
| Startup | `cmd/main.go` registers all 12 controllers; there is no controller/profile selector | A smaller manifest alone cannot selectively start the manager |
| Target API | `NodePowerAgent.spec.nutServerRefs` is required and nonempty | No typed direct external endpoint exists |
| Resolution | `nodepoweragent_targets.go` reads NUTServer, its selected UPSDevices, monitor Secret and optional management cluster; derives Service address and pod-selector egress | Fake NUTServer objects are not a supported external-endpoint adapter |
| Configuration | `nodepoweragent_config.go` emits netclient/secondary configuration, one power value per monitor and a local signal writer | Reuse rendering and token validation, but extend target and supply inputs |
| Lifecycle | Agent reconciliation creates ConfigMap, configuration Secret, ServiceAccount, signal Secret, NetworkPolicy and DaemonSet | Standalone templates must replace this ownership and update behavior |
| Cross-component reads | Reconcile lists inventory coverage and ShutdownFlows; watches both; signal revocation consults flow execution evidence | Omitting those CRDs requires explicit profile-specific branches, not empty lists or swallowed API errors |
| Admission | NodePowerAgent defaulting/validation and render-safety checks enforce modes and approval | Keep these checks; do not disable all webhooks to make a minimal profile boot |
| Actuator | Reads only the projected signal mount; no Kubernetes token, listener, or NUT credentials | A local file is deliberately not an authorized input |
| Storage | Agent operands do not query PostgreSQL; SB-11 still requires production audit storage for the full product | A profile-specific no-database exception needs an explicit decision and reduced audit contract |

ManagementClusterRef is optional when namespace and images are explicit. That removes one
configuration dependency, not the inventory/flow reads or required NUTServer target model.
The existing shipped BYO-cert bundle contains 12 CRDs and broad full-product RBAC, not a
minimal agent installer. Its 36 ClusterRoles include resource-facing roles; that count is
not a claim that the manager is bound to all of them.

## Candidate Comparison

| Concern | Standalone manifests | Selective manager, proposed |
| --- | --- | --- |
| Runtime | Two containers per selected node; no manager | Same operands plus one small manager Deployment |
| Custom APIs/admission | None if using raw manifests | NodePowerAgent CRD and its admission/certificate bootstrap |
| Configuration updates | Installer/user owns validation, Secret/config changes and rollout | Reuse reconcile/status/Secret-watch machinery after removing profile-inapplicable dependencies |
| Authorization | No supported API for local release; raw manifest access alone does not preserve CR approval semantics | Explicit mode/approval contract can remain attached to NodePowerAgent |
| Upgrade burden | Duplicate security, mounts, probes, TLS and config templates or build a shared generator | Shared renderer; profile-aware registrations and narrower generated packaging |
| Observability | Container logs/probes; no CR readiness/coverage | Agent status, conditions and events; no fabricated planner/halt-history claims |
| Startup demonstrated | Direct monitor and quiet Simulate actuator in Docker | Not implemented; current binary has no selective startup mode |

The manifest option has the lowest process count, but a maintained installer with comparable
validation and lifecycle behavior would need additional tooling. The measured operand footprint
does not establish that one extra manager is prohibitively expensive. Prefer reuse, then measure
the selective manager before declaring a supported minimum. Do not split new services or binaries
solely to reduce the controller set.

## Measurements and Reproducible Probe

Run `python3 -B hack/research/mod1_probe.py` from the repository root with Docker available.
The probe uses only cached images, captures immutable local image IDs before starting, creates
an internal network with no published ports, and deletes only its recorded container/network IDs.
All containers run as UID 65532 without Linux capabilities, host PID access, Kubernetes credentials,
or Docker socket mounts. Agent/actuator root filesystems are read-only with private writable state.
Credentials are synthetic; plaintext NUT is confined to this disposable network, not proposed as
a production default. The server is a separate real upsd/dummy-ups fixture, not a managed relay.

The strengthened run passed with explicit actuator readiness and both UPS status checks:

- Single monitored critical UPS (`OB LB`): upsmon invoked the shipped local signal writer.
- Critical plus healthy UPS (`OL`), both power value 1 and `MINSUPPLIES 1`: no local signal
  during the ten-second observation. Both readings were queried from the monitor container.
- The ready Simulate actuator had a distinct quiet channel and did not accept the local signal.
  This preserves the shipped mount boundary; it does **not** implement or validate LocalNUT.
- All owned containers and the internal network were removed successfully.

Single samples from `docker stats --no-stream` on ARM64 Docker:

| Process/container | Memory usage | CPU sample |
| --- | --- | --- |
| Single-UPS monitor, holding local writer after signal | 2.055 MiB | 0.00% |
| Two-UPS monitor | 1.684 MiB | 0.10% |
| Quiet Simulate actuator | 2.840 MiB | 0.00% |

These are short fixture observations, not peaks, sizing recommendations, Kubernetes overhead,
or an agents-only-versus-manager runtime benchmark. The latter cannot be measured honestly
before selective startup exists. Current configured requests are 15m CPU / 48Mi per agent pair,
with combined limits 150m / 96Mi (`defaultNodePowerAgentResources`). The full manager manifest
requests 10m / 64Mi with limits 500m / 128Mi; those are configuration, not measured consumption.

Cached local image identities and Docker-reported sizes (not registry compressed download sizes):

| Image | Local image ID | Bytes |
| --- | --- | --- |
| `upsmon-agent` | `sha256:517433a635daa2dfa0f361c8a01c3087c3d2b58909a53f99fe5ba8422b2c257b` | 16486470 |
| `node-actuator` | `sha256:f8608efd45d85b24c06c8e3c4e9e6fbe1657347f5c6b0ef0310a45251e4b8733` | 13398212 |
| `nut-operator` | `sha256:e6b7f9d01fbc570415e2ef7cee2b8bf5793a7f69b201ce2410f391a30a14c0ed` | 35359517 |
| External test `nut-server` | `sha256:796b06a8e81dbbd7b10ea513d21a1530fe0a7354b8158fd4a35bd23e97307bab` | 16719519 |

These cached builds are not asserted to match the reviewed commit or a promoted release.
Source-level tests below exercise the current checkout independently.

## Proposed Contract and Decisions Needed

1. **Authority:** select LocalNUT explicitly for this profile, or defer shutdown support.
   Keep Operator and LocalNUT mutually exclusive with no fallback. LocalNUT explicitly delegates
   local shutdown decisions to the configured NUT source; separation of credentials from privilege
   still matters, but a compromised monitor would gain signal authority in that mode.
2. **External targets:** specify UPS name, host, port, username/password Secret keys, TLS requirement
   and trust Secret. Keep Secrets in the operand namespace initially. Preserve escaping and reject
   malformed tokens. Require explicit egress destinations/ports; portable NetworkPolicy cannot
   select an arbitrary DNS name. Compare stable IP targets with authored CIDRs plus DNS access;
   never silently allow all outbound traffic. Do not synthesize NUTServer or UPSDevice objects.
3. **Supply model:** bind each agent fleet to nodes with the same actual supply mapping. Author
   monitor power values and minimum required supplies, or initially restrict support to one supply.
   F-45 remains a prerequisite. A shared list of all UPSes is not each node's supply map.
4. **Authorization lifecycle:** retain Actuate plus explicit approval and safe defaults. Define
   mode-change, approval-removal, configuration-generation, stale-signal and rollout behavior
   without ShutdownFlow. A static approval copied into a pod is not equivalent to the executor's
   fresh API authorization check. Choose the revocation/freshness contract explicitly; do not
   remove flow checks and call the local writer authorized. No database or exactly-once subsystem
   is implied. A local episode may outlast signal TTL while the writer holds; re-arm/expiry behavior
   needs a deliberate bounded contract.
5. **Profile packaging:** gate controller/watch/admission registration; remove inventory coverage,
   flow rollout holds/revocation and unrelated CRDs/RBAC from this profile only. Preserve full-mode
   behavior. Decide the replacement local-event rollout policy and the no-PostgreSQL audit exception.

The pinned [NUT 2.8.5 upsmon configuration manual](https://github.com/networkupstools/nut/blob/v2.8.5/docs/man/upsmon.conf.txt)
defines weighted supplies and critical-state handling, including communication loss while on
battery and secondary HOSTSYNC behavior. LocalNUT would inherit those shutdown semantics,
not merely react to every ONBATT notification. Test upstream FSD consumption separately from
OB+LB; it does not approve the project's deferred outbound FSD feature (OD-19).
The repository builds NUT 2.8.5: do not assume TLS options described in newer online documentation
work in this image. Validate CA/hostname behavior, auth failures, and trust rotation on the exact
image; do not silently weaken mixed-target TLS requirements.

## Validation and Remaining Boundary

Passed on the current checkout:

```sh
go test ./internal/nodeagent ./cmd/power-signal-writer ./cmd/node-actuator -count=1
go test ./internal/controller -run 'Test(UpsmonReadiness|Default|NothingHoldsRollouts|SignalIsRevoked|FreshSignal|RevokedSignalKeys|NodePowerAgentRequestsForSecret)' -count=1
```

These cover current stale/wrong-node/invalid signal handling, flow binding, dry-run/mode gates,
writer reuse and selected readiness/revocation behavior. They do not validate a new authority API.
No real host shutdown was run; existing guest qualification remains a separate workstream.

After authority/profile decisions, the remaining comparison needs an isolated selective-manager
startup/render prototype with omitted CRDs genuinely absent, least-privilege RBAC, admission and
Secret rotation. Measure idle/startup resource use under the same workload as the standalone
candidate. Then cover real external upsd OB+LB and FSD to Simulate in LocalNUT, Operator isolation,
no fallback, approval removal/mode switching, stale/wrong-node requests, single/redundant supply
cases, communication loss, and TLS. MOD-5 owns supported-profile acceptance after approval.
The investigation establishes feasibility and constraints; it does not claim this profile ships.
