# Shared VM test framework

Components: VM Test Coverage.
Audience: contributors.

The framework separates repeatable host-side mechanics from guest provisioning and scenario
assertions. Hadron and Talos remain concrete adapters. Shared modules live under
`test/internal/`, keeping them internal to test tooling without introducing another Go module
or a standalone framework dependency. Task ownership and completion evidence live in the
[VM tracker](../../tasks.md#vm-test-coverage).

## Duplication inventory and extraction decisions

The [comprehensive source review](../audits/vm-framework-comprehensive-review-2026-09-20.md)
accounts for every original harness file, with a reproducible function inventory, duplicate
candidates, manual scenario-block analysis and a file-by-file disposition map. Its dated snapshot
contains 56 files and 194 Go functions. The inventory is bounded to the two harnesses and their
owning workflows/support scripts; it does not claim to find every repeated statement block.

Shared modules cover artifacts, readiness, host commands, private workspaces, resource lifecycle,
Kubernetes observations, signal fixtures, image references/build plans and workflow conventions.
The existing process ownership module remains authoritative for Go QEMU ownership; Python
emergency cleanup remains independently usable after Go process failure. Guest configuration,
image delivery, network topology and scenario assertions retain their concrete owners.

`internal/polling` is the product's NUT telemetry poller, not a generic guest readiness loop.
Reusing it would couple VM infrastructure to telemetry policy. `readiness` instead retains the
existing Kubernetes polling primitive used by both harnesses.

## Module boundaries and composition

The shared flow is explicit: prepare a pinned artifact in a private directory, construct the
chosen adapter, start its owned machine, perform adapter-specific provisioning, and use bounded
readiness checks around that provisioning. Scenario code then performs its assertions. Cleanup
uses `vmprocess` to confirm the original process has exited before removing machine state.

`artifact` depends on the standard library. `readiness` depends on context/time and the existing
Kubernetes wait utility. Neither imports PEG, a guest adapter, Docker, or a Kubernetes client.
`command`, `workspace`, `lifecycle`, `image` and `signalfixture` use standard-library mechanics;
`kube` uses client-go and shared readiness, while `workflow` reads YAML declarations.
`scenario` composes the lifecycle scope without importing either guest adapter; `diagnostics`
retains explicitly selected, bounded streams. `fixture` composes cluster identity checks with
server-assigned namespace identities and preconditioned deletion. Talos imports shared artifact
preparation and readiness at its adapter boundaries; its single-node boot scenario composes
scenario/lifecycle and diagnostic bundles. Talos actuator fixtures use the same lifecycle
registration and diagnostics with generated, identity-checked namespace fixtures. Shared signal
fixtures construct Talos's invalid payloads and Secret patches; each subcase refreshes its clock
immediately before delivery. Shared workflow validation checks every Talos job's bounded steps,
cleanup ordering and removal preconditions, while Talos retains its expected entry-point checks.
`vmprocess` depends on PEG's machine interface and Linux pidfds and remains behind VM build tags.
No shared module assumes SSH exists or installs a guest, applies an arbitrary Kubernetes manifest,
or performs a host shutdown on behalf of its caller. The fixture module can explicitly create and
delete its own namespace through the caller-supplied client; it never adopts an existing namespace.

The [module README](../../../test/internal/vmframework/README.md) defines the public function
contracts and test commands. The [adapter composition tests](../../../test/internal/vmframework/adapters_test.go)
exercise both real constructors with prepared artifacts, keeping state directories distinct and
proving that machine cleanup does not delete a caller-owned image. Additional contracts compose
private workspaces, image/kubectl command plans, signal patches and lifecycle cleanup. These tests
do not execute Docker/kubectl or boot VMs. A separate framework-only composition contract combines
scenario failure, private workspace retention, namespace cleanup and bounded diagnostic capture
with a fake Kubernetes client. This is not API-server/garbage-collection qualification.

## Incremental adoption

Talos single-node boot keeps API provisioning and the exactly-one-Ready-node assertion visible
in `test/talos/boot_smoke_test.go`. Cleanup is registered before Create, uses the retained
vmprocess identity, and removes state only after successful execution and verified stop.
Failed scenarios collect bounded console/step evidence before stopping, and retain private
machine state. Uploads select only console and diagnostic logs, never machine configuration or
kubeconfig. The manual workflow runs boot and cancellation on separate runners because Talos
uses fixed forwarded ports. The cancellation case cancels after verified startup, before
provisioning; it cannot interrupt PEG calls that ignore context. Independent Python cleanup and
external workflow deadlines remain required.

The Talos actuator fixture records the cluster UID through the credentials generated for its
owned guest before creating an operand namespace. It retains the namespace handle, rechecks it
before agent/signal writes, and passes its generated name explicitly to observations and
manifests. The VM owns the entire disposable cluster: successful actuation intentionally powers
off its API server, so teardown stops the verified machine and discards its state instead of
requiring namespace deletion against an unavailable API. This integration does not qualify the
fixture module's API deletion/garbage-collection path. Actuation and negative-signal assertions
remain scenario-owned; lifecycle cleanup never supplies shutdown evidence.

1. Adopt `readiness.Wait` inside each adapter's existing `waitForWithDiagnostics`/`pollGuest`
   boundary, preserving scenario-specific failure messages and diagnostic cadence. Confirm that
   parent deadlines, cancelled checks, and cancelled diagnostics retain their failure causes.
2. Adopt `artifact.Prepare` inside `NewSafeMachineContext` independently for Hadron and Talos.
   Keep public adapter configuration stable. Decide explicitly how an empty ISO is handled by
   the constructor: the shared module rejects an empty source, while existing component fixtures
   can construct a machine without an ISO. Preserve the constructor's ability to skip preparation.
3. Reuse the existing `vmprocess.Wrap` integration. Keep process ownership, retained diagnostics,
   and guest-shutdown evidence separate. Failed or unverifiable starts must preserve state.
4. Migrate repeated scenario fixtures under VM-8 after the primitive contracts are qualified.
   Adopt command/workspace/lifecycle composition before replacing shared-checkout mutation.
   Introduce kube, signalfixture, image and workflow helpers with their documented boundaries;
   preserve independent Python emergency cleanup when migrating workflow callers.

Artifact adoption changes remote file publication from direct download to verified exclusive
publication, and validates the existence/type of unpinned local artifacts earlier. It therefore
needs explicit adapter regression coverage, rather than an assumed drop-in replacement.

For each migrated adapter, qualify a real boot/readiness/cleanup run against the exact revision,
then exercise startup cancellation with retained failure diagnostics. Scenario migration also
requires the affected actuator and composed-flow runs; it must preserve externally retained
shutdown-cause evidence and negative-signal proof. Coordinate shared-checkout edits and VM jobs
with the owning work. Kind retains its independent lifecycle. Immediate migration is not a
condition of building and testing these foundational modules.
