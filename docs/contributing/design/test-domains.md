# Test Domains

Components: Cross-cutting; VM Test Coverage.
Audience: contributors.

This document defines the test layers, their responsibilities and the evidence each provides.
Use it to choose where a behavior belongs and to interpret a test result. Work is tracked in
[Project Tasks](../../tasks.md); completion records and dated run evidence belong in
[completed tasks](../../tasks-completed.md) and [audits](../audits/).
Commands, dependency pins and workflow triggers are owned by the [Makefile](../../../Makefile),
[workflows](../../../.github/workflows/) and [contributor guide](../../../CONTRIBUTING.md#development-checks).

## Evidence model

A result applies to its asserted behavior, fixture, revision and tested artifact. A test name or
workflow definition is not a passing result. Build tags, name filters and opt-in settings determine
which assertions execute. Focused runs do not qualify other scenarios or their coexistence.

Use the least expensive layer that exercises the required boundary. Keep detailed policy matrices
in component tests, Kubernetes integration in envtest or Kind, and operating-system shutdown proof
in disposable guests. Shared fixtures own setup and cleanup; scenarios retain their assertions.

## Unit and component tests

Package tests exercise deterministic planning, inventory/capability resolution, telemetry,
triggers, adaptive execution, authorization, target refresh and signal validation. Failure,
repeat-safety, deadline and concurrency cases do not require guest boot or physical hardware.
Transport fixtures can use real loopback HTTP/TLS and stalled sockets. Database fakes exercise
caller behavior rather than PostgreSQL semantics.

The modular target composes hooks, planning and signal publication and checks profile startup and
packaging. Its [coverage map](modular-acceptance.md) describes the fixtures. Distinct fake cached
and fresh snapshots test stale-state handling; they do not exercise actual cache propagation,
admission, resource versions or kubelet behavior.

**Owners:** `api/`, `cmd/`, `internal/`, `test/quickstart/`, `test/utils/`; `make test`,
`make test-modular-components`, and `.github/workflows/test.yml`.

## Kubernetes API and admission: envtest

Envtest provides a real API server and etcd for validation, persistence, resource versions,
status subresources and installed admission webhooks. Tests that start a manager also exercise
its watches, cache and reconciliation. Direct validator/reconciler calls establish their narrower
boundary even when located in the same package.

Authorization tests can change an object through the API and verify subsequent reads. Quorum
publication tests use declared membership, Node readiness and pending signal Secrets under
[EX-18](executor-requirements.md). Concurrent publication assertions inspect persisted keys and
receipts as well as returned errors.
`shutdownflow_quorum_envtest_test.go` exercises the complete publication validator against
real Node, Pod, agent and Secret objects, including terminal create/update rejection without
partial keys and successful batches with matching receipts.

Envtest has no scheduler, kubelet, garbage collector or guest. Synthetic Node/Pod status is test
input, not running-cluster evidence. Rendered security contexts and policies require runtime tests
to establish enforcement. Quorum membership/readiness checks do not directly probe etcd health.

**Owners:** `internal/controller/`, `internal/webhook/v1alpha1/`, `make test`, and the envtest
matrix in `.github/workflows/test.yml`.

## Cluster integration: Kind

Kind exercises the installed manager and operands with real scheduling, Secret projection,
service routing, admission and a policy-enforcing CNI. Simulated UPS devices provide repeatable
NUT and SNMP inputs. Scenarios cover:

- Telemetry transitions, driver supervision/readiness, and recovery after driver or Pod failure.
- Agent placement, node-specific signal delivery and signal-expiry rejection.
- Logical ShutdownFlow execution through hooks, scale/drain, audit and Simulate actuation.
- Execution-time flow-mode and selected-agent generation changes at an observable hook barrier,
  with original-execution audit checks and a successful Simulate baseline.
- Webhook certificate bootstrap, rotation, admission and expiry metrics.
- Managed NUT-only installation, client policy, TLS/auth, configuration/secret changes, relay
  behavior, and exclusion of unrelated APIs/controllers/permissions.
- Installer reapplication and manager replacement over existing resources.

Integration safety assertions follow an identified execution and an observable boundary. A
change to authorization or selection must be admitted before the relevant recheck. Tests separate
stale execution effects from a separately authorized replacement execution. Sleeps or eventual
Secret absence alone cannot prove that no transient publication occurred; observation and audit
must cover the interval.

Topology is owned by `test/e2e/kind-config.yaml`. Multiple nodes do not imply multiple control
planes. Kind nodes share the host kernel: host PowerOff must never be used as a guest simulation.
Simulate signals establish logical handoff, not physical shutdown or actual HA control-plane loss.

Reapplying the same schema/image establishes reconciliation continuity. Historical migration
compatibility requires explicit source/destination versions and migration assertions. Restarting
a manager does not imply the execution-resume guarantee excluded by
[SB-1](scope-boundaries.md#executor-restarts-and-idempotency).

**Owners:** `test/e2e/` with the `e2e` tag, `hack/test-kind.py`, `make test-e2e`, and
`.github/workflows/test-e2e.yml`. Artifact acceptance uses immutable image references supplied by
the image workflow; checkout-built runs identify their separately built images.

## Kubernetes garbage collection

Operand deletion and legacy-finalizer migration use an isolated real cluster so Kubernetes can
collect dependent objects. Assertions check dependent removal and preservation of unrelated
resources. Successful API deletion alone does not prove garbage collection.

**Owners:** controller deletion scenarios, `make test-operand-deletion` and
`hack/test-operand-deletion.sh`. This layer needs neither an installed operator nor host actuation.

## Real service contracts

### PostgreSQL

The audit suite exercises real migrations, writes, history, retention, replay and locked-writer
fallback. Spool tests cover bounds, concurrency and repeat-safe replay. Execution tests separately
check that evidence failure is reported without changing the shutdown action's outcome.
PostgreSQL component evidence does not establish CNPG failover.

**Owners:** `internal/audit/`, `internal/storage/`, the `postgres` tag, `make test-postgres`,
`hack/test-postgres.sh`, and `.github/workflows/test-postgres.yml`.

### NetBox

The importer suite exercises authentication, pagination, cables, metadata, filtering, deterministic
CR output and inventory compilation against a disposable real NetBox service. Invalid input is
rejected. This qualifies the import contract; NetBox is not a shutdown-time dependency.

**Owners:** `test/netbox/`, `make test-netbox`, `hack/test-netbox.py`, and
`.github/workflows/test-netbox.yml`. See the [service fixture](../../../test/netbox/README.md).

## Container artifacts

Image tests exercise packaged executables, entrypoints, filesystem/user assumptions, NUT protocol,
TLS negotiation, supervision and readiness with the actual operand binaries. Image IDs/digests
associate evidence with the artifact. Build success alone does not establish runtime permissions;
a host-built executable does not qualify a different container image.

**Owners:** `images/`, `hack/smoke-image.sh`, NUT smoke harnesses, Makefile image-test targets,
and `.github/workflows/images.yml`.

## Disposable guests and host actuation

### Guest adapters and harness safety

Hadron and Talos use PEG/QEMU with OS-specific provisioning. Hadron owns SSH, cloud-init/k3s and
guest image import. Talos owns machine configuration, Talos API access and registry-based delivery.
Shared lifecycle responsibilities are verified boot artifacts, private state, bounded startup,
readiness, cancellation, original-process identity and cleanup. A common fixture must not require
SSH from an OS that does not provide it.

Fast adapter tests use fake machines, loopback endpoints or disposable child processes to exercise
construction failures, command arguments, deadlines and cleanup decisions. Guest runs additionally
exercise those mechanisms under QEMU. Same-host concurrency requires explicit port/state isolation;
separate CI runners alone do not prove it.

**Owners:** `test/hadron/` and `test/talos/`; component tags `hadron` and `talos`, with
`hadron_smoke` or `talos_smoke` added for guest scenarios. Cleanup scripts and their Python tests
own external cleanup checks; matching VM workflows own runner setup and retained artifacts.

### Linux and Talos actuation

Linux guest tests exercise the shipped actuator's capability handling, host PID namespace and
power-off syscall. Talos tests exercise TalosShutdown against the real Talos API. Negative signals
and absent approval must leave the guest running. Policy-independent authorization/admission
matrices stay in component/API tests rather than being duplicated per OS.

A halt requires evidence that the intended guest exited because of accepted actuation, observed
outside the guest before cleanup. Node NotReady, Pod termination, API acceptance or a harness kill
is insufficient. Target exit and survivor availability are separate assertions.

**Owners:** actuator and rendered-DaemonSet scenarios in `test/hadron/`, actuator scenarios in
`test/talos/`, and their corresponding smoke workflows.

### Outage-to-halt composition

Guest outage scenarios connect NUT telemetry, trigger/planner/executor behavior, workload actions,
signal delivery, actuation and audit. The configured actuator policy determines the evidence:
Simulate proves logical handoff; PowerOff/TalosShutdown with external exit observation proves
host actuation. A complete drain-to-halt claim requires these steps in the same execution,
survivor availability and durable evidence outside the stopped guest.

Network-policy assertions require an allowed and a denied client reaching the same service;
unreachability alone cannot establish policy enforcement. HA shutdown requires a matching
topology and delivery/guest-exit observations, beyond API-level quorum arithmetic.

**Owners:** outage-flow scenarios in `test/hadron/` and matching workflows. Detailed logic
matrices remain in component/Kind tests; guest scenarios concentrate on composition and the OS boundary.

## Test-resource ownership and cancellation

Infrastructure tests use disposable resources and private kubeconfigs. Setup verifies ownership;
teardown is bounded, preserves the test result and reports cleanup failures separately. A
cancellation rehearsal observes removal and unchanged external configuration without cleaning on
behalf of the runner. Process exit is verified before deleting state used as ownership evidence.
Runner loss and SIGKILL are outside cooperative cleanup guarantees.

**Owners:** Kind runner/lifecycle harnesses and guest adapter/cleanup tests. See
`make test-kind-harness`, `make test-kind-lifecycle-harness` and `make test-kind-lifecycle`.

## Static analysis, manifests and supply chain

Scanners check configured patterns, dependency databases and artifact inventories. Findings require
triage; skipped or failed scanners are not passes. A clean scan is bounded by its rules, database
and inputs and does not establish the absence of every vulnerability or secret.

Manifest checks validate examples against generated CRDs, compare installers with their sources,
submit installer resources to a real API server and check repository hygiene. Schema validity
does not prove admission behavior or a ready installation; runtime tests supply that evidence.

**Owners:** `make security-scan`, `.github/workflows/security.yml`,
`.github/workflows/hygiene.yml`, and sample/installer validation scripts under `hack/`.

## Physical compatibility boundary

Simulated NUT/SNMP devices supply deterministic protocol and orchestration inputs. Compatibility
claims for a particular UPS, network card, firmware or battery/runtime behavior require
device-specific evidence. Guest shutdown qualifies an OS actuation path, not physical UPS
operation. Recovery orchestration and power restoration remain outside the operator's scope.
