# Shared VM test framework

Components: VM Test Coverage.
Audience: contributors.

The framework separates repeatable host-side mechanics from guest provisioning and scenario
assertions. Hadron and Talos remain concrete adapters. Shared modules live under
`test/internal/`, keeping them internal to test tooling without introducing another Go module
or a standalone framework dependency. Task ownership and completion evidence live in the
[VM tracker](../../tasks.md#vm-test-coverage).

## Duplication inventory and extraction decisions

This inventory compares named functions in the two implementations, including behavior that
textual duplication checks can miss. It is not conditioned on adding a third guest adapter.

| Concern and source owners | Assessment | Module decision |
| --- | --- | --- |
| `downloadISO`, `verifyISO`, `parseISOChecksum`, and the artifact validation portion of `NewSafeMachineContext` in [Hadron](../../../test/hadron/adapter.go) and [Talos](../../../test/talos/adapter.go) | Identical checksum/download mechanics, with duplicated validation around them. Cloud-init and machine configuration differ. | `vmframework/artifact`: pinned preparation with bounded downloads, private staging, and exclusive publication. Keep guest configuration outside it. |
| `pollGuest` in [Hadron](../../../test/hadron/wait.go) and [Talos](../../../test/talos/wait.go) | Identical retry/diagnostic behavior and duplicated cancellation tests. | `vmframework/readiness`: preserve shared-context diagnostics and error causes; require an explicit positive budget. |
| `SafeStop`/`SafeTeardown` in both adapters and [vmprocess](../../../test/internal/vmprocess/machine.go) | Process identity is guest-independent and already has one shared implementation. | Reuse the startup ownership guard; do not create a second process registry or cleanup implementation. VM-10 owns its qualification. |
| `preserveFile` and `runMake` in [Hadron operator fixtures](../../../test/hadron/operator_smoke_test.go) and [Talos actuator fixtures](../../../test/talos/actuator_smoke_test.go); `runKubectl`/`applyManifest` helpers in the scenario files | Same command mechanics, but failure policy is coupled to `testing.T`, inherited environment, and mutable repository files. | Candidate for a later explicit execution workspace. Define environment, private kubeconfig, file restoration, output/redaction, and cancellation contracts before extracting; copying command wrappers would preserve the isolation problems. |
| [Hadron kubeconfig](../../../test/hadron/kubeconfig.go) and [Talos API provisioning](../../../test/talos/talosctl.go) | Both yield a Kubernetes client configuration, through different trust and bootstrap paths. Hadron retrieves and rewrites k3s config; Talos provisions API credentials and fetches config. | Keep acquisition in adapters. A later shared client constructor should consume an explicit private kubeconfig and verified cluster identity, never the ambient context. |
| `buildOperandImageTarball` in [Hadron actuator fixtures](../../../test/hadron/actuator_smoke_test.go) and image helpers in [Talos registry fixtures](../../../test/talos/registry_smoke_test.go) | Both deliver images, but Hadron imports archives while Talos consumes a reachable registry/mirror. | Keep delivery mechanisms separate. Share immutable image descriptors only after both delivery contracts and cleanup ownership are explicit. |
| Approved-agent, invalid-signal, and halt helpers in [Hadron DaemonSet fixtures](../../../test/hadron/actuator_daemonset_smoke_test.go) and [Talos actuator fixtures](../../../test/talos/actuator_smoke_test.go) | Similar fixtures conceal different capabilities and actuation transports. Existing rejection and shutdown evidence gaps need correction. | Keep assertions visible. VM-9 and VM-11 must define correct evidence before shared helpers can enforce it. Process exit alone is not guest shutdown. |
| Workflow budget assertions in [Hadron](../../../test/hadron/workflow_test.go) and [Talos](../../../test/talos/workflow_test.go), plus Python cleanup scripts | Common resource-lifetime rules, with different workflow sets and private-root names. | Candidate for shared declarative workflow checks. Keep emergency process cleanup usable outside the Go test process; extracting Go helpers must not remove that fallback. |

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
`vmprocess` depends on PEG's machine interface and Linux pidfds and remains behind VM build tags.
No shared module assumes SSH exists or installs a guest, applies a Kubernetes manifest, or
performs a host shutdown on behalf of its caller.

The [module README](../../../test/internal/vmframework/README.md) defines the public function
contracts and test commands. The [adapter composition tests](../../../test/internal/vmframework/adapters_test.go)
exercise both real constructors with prepared artifacts, keeping state directories distinct and
proving that machine cleanup does not delete a caller-owned image. Those tests do not boot VMs.

## Incremental adoption

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
   Extract additional execution/client/image modules only with the concrete boundaries above.

Artifact adoption changes remote file publication from direct download to verified exclusive
publication, and validates the existence/type of unpinned local artifacts earlier. It therefore
needs explicit adapter regression coverage, rather than an assumed drop-in replacement.

For each migrated adapter, qualify a real boot/readiness/cleanup run against the exact revision,
then exercise startup cancellation with retained failure diagnostics. Scenario migration also
requires the affected actuator and composed-flow runs; it must preserve externally retained
shutdown-cause evidence and negative-signal proof. Coordinate shared-checkout edits and VM jobs
with the owning work. Kind retains its independent lifecycle. Immediate migration is not a
condition of building and testing these foundational modules.
