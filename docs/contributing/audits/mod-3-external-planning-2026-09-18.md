# MOD-3: aggregation with external planning

Components: Modular Deployment Profiles; NUT Server / upsd; Node Agent / DaemonSet.
Date: 2026-09-18.
Scope: source at `cf599655ab54b1522663774d508f0b6453a8ab11` plus shared working-tree changes;
isolated protocol experiment against a captured cached image, not a promoted-image claim.
Task owner: [MOD-3](../../tasks-post-v1.md#modular-deployment-profiles).
Subsequent scope selection assigns telemetry-only consumption to v1 MOD-4; the investigation
below records the evidence preceding that split.

## Recommendation

Separate the read-only aggregation case from external execution. Use the proposed MOD-4
managed NUT profile for aggregation, and NUT protocol reads as the first telemetry boundary.
An external planner can consume those readings without ShutdownFlow objects. This requires
neither a new telemetry service nor exposing internal compiled-plan artifacts.

Aggregation plus built-in node shutdown is a different product contract. There is currently
no supported external release-request API. Prefer an authenticated Kubernetes request resource
if that use case is selected, reusing the existing signal validation/publication machinery.
This is a proposal, not approval to add an API or permit external writes to halt Secrets.

## Capability matrix

| Capability | Current evidence | Proposed boundary |
| --- | --- | --- |
| Direct managed network UPS drivers | Existing NUTServer renderer; independent of flow objects | MOD-4 packaging |
| Remote NUT aggregation | Plain repeater protocol works; generated authconf fails with pinned 2.8.5 | Resolve NS-11 before advertising managed relay support |
| Read NUT variables without planning | Live two-server probe passed with no manager, agents or database | Document names, connection/auth/TLS, unavailable/stale behavior |
| Read normalized UPSDevice status | Existing UPSDevice controller polls independently of ShutdownFlow | Optional telemetry extension, with capability-profile CRD/read dependencies |
| Run manager with planning controllers omitted | No selector in current manager; all 12 controllers registered | Shared selective-manager work, not just omitting CR instances |
| External planner uses built-in agents | No public request boundary; agent renderer still reads flows/inventory | Separate approved execution contract, sharing MOD-1 dependency work |
| Raw signal-Secret writes or synthetic plans | Internal implementation details | Never the supported caller interface |
| Confirm external host halt through hook | Not implemented; MOD-2 hooks are advisory | MOD-2 completion-contract decision |

Source owners: `cmd/main.go`, `internal/controller/nutserver_render.go`,
`nutserver_config.go`, `upsdevice_controller.go`, `internal/kubeinventory/resolver.go`,
`internal/controller/shutdownflow_release_safety.go` and `shutdownflow_release_validation.go`.
UPSDevice reconciliation reads capability profiles even when no planner runs. Its telemetry
recorder returns without database work when there is no management cluster. That implementation
fact does not change the full-install SB-11 storage requirement.

## Live relay experiment and compatibility finding

Run from the repository root:

```sh
# Reproduces the current generated option and fails on the captured 2.8.5 image:
python3 -B hack/research/mod3_relay_probe.py
# Diagnostic comparison only; this is not the managed renderer configuration:
python3 -B hack/research/mod3_relay_probe.py --legacy-no-authconf
```

The script uses cached image `example.com/nut-server:v0.0.1`, resolving it before startup to
`sha256:796b06a8e81dbbd7b10ea513d21a1530fe0a7354b8158fd4a35bd23e97307bab`
(16,719,519 image bytes). It creates a private internal Docker network, two unprivileged
read-only containers, synthetic configuration and no published ports. No host PID, Linux
capabilities, Kubernetes token, Docker socket, manager, agents or database is present.
Drivers are launched manually for this protocol probe; it does not exercise the managed
driver-supervisor sidecar or Kubernetes lifecycle.

The generated-shape configuration failed with:

```text
Fatal error: 'authconf' is not a valid variable name for this driver.
```

The image reports NUT 2.8.5, also the default in `images/nut-server/Dockerfile`. The renderer
unconditionally emits `authconf` for upstreamNUT; local Dockerfile patches only address
readiness PONG behavior. This is a concrete renderer/runtime mismatch, not proof that every
published image has the captured digest. NUT's current manual documents authconf as a 2.8.6
addition; the tagged 2.8.5 driver lacks it.
[NUT manual](https://networkupstools.org/docs/man/dummy-ups.html),
[2.8.5 driver source](https://raw.githubusercontent.com/networkupstools/nut/v2.8.5/drivers/dummy-ups.c).

With only authconf omitted, the downstream relay returned status OL, charge 97 and runtime
600. After editing the upstream fixture, downstream status became `OB LB DISCHRG`, exactly
matching the upstream. The first update assertion incorrectly expected only `OB LB`; it was
corrected to compare upstream/downstream plus required OB/LB and absence of OL. The final
comparison run passed and every probe removed its owned containers and network.

This proves basic variable forwarding and updates, not upstream authenticated TLS, failure
freshness, command forwarding, driver-supervisor integration or a supported modular install.
Plaintext is confined to the disposable network. Omitting authconf must not silently discard
requested credentials or TLS trust. NS-11 owns selecting a compatible implementation and
real-binary regression coverage, including explicit rejection of unsupported auth modes.

## Proposed external execution contract

If built-in agents are required, define the request before implementation:

| Area | Required decision / acceptance |
| --- | --- |
| Caller identity | Kubernetes RBAC for submitting requests; no direct signal Secret permissions; define approval principal and immutable request fields |
| Target | Explicit node identity including UID, agent identity/current generation and configuration; reject selection or pod drift |
| Mode and approval | Explicit rehearsal/enforce intent plus fresh per-agent approval at publication; never automatic LocalNUT fallback |
| Lifetime | Bounded expiry and stale-request rejection; define replay/rearm behavior without promising exactly-once execution |
| Ordering | External planner owns request order and dependencies; operator retains live clearance, telemetry and control-plane safety checks |
| Cancellation | Cancel before publication; published/accepted halt cannot be promised reversible. Surface the publication boundary explicitly |
| Results | Distinguish rejected, simulated, published and actuator evidence; publication is not confirmed machine power-off |
| Audit | Define durable request evidence and any profile-specific storage exception explicitly |

Existing release validation checks observed agent generation, signal destination, ready pod
placement/configuration, fresh telemetry, workload clearance, control-plane safety and approval.
Those checks currently live with ShutdownFlow reconciliation. Extracting a shared execution
boundary requires preserving these reads and resolving their profile dependencies; copying
only the Secret-writing function would bypass the contract.

Existing focused release-safety and authorization tests were run; they validate the current
boundary, not this proposed API. Request-contract tests for expiry, wrong node, revocation and
cancellation must accompany the selected design. MOD-3 stays open for that decision and the
selective startup/telemetry profile; no external execution endpoint was implemented.
