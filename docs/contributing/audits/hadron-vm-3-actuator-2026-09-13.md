# Hadron VM-3: real actuator on a real guest

Status: design rationale and first two milestones, 2026-09-13. See `docs/tasks.md`'s `VM-3` entry
for current status.

## Scope, and what this milestone deliberately does not attempt

`VM-3` (`docs/tasks.md`) is to test the shipped Linux actuator (`cmd/node-actuator`) on a real
Hadron guest: missing/expired/wrong-node signals, absent or revoked approval, and a positive case
confirmed through the hypervisor rather than Kubernetes `NotReady` alone. The high-severity part of
that text is the evidence check -- distinguishing guest-initiated shutdown from a QEMU crash,
forced termination, lost SSH, or timeout, since PEG's `Stop()` is host-driven process termination,
not actuator success.

Before building any of that, it's worth being precise about what a real guest kernel actually adds
here that Kind does not, because it is not the whole story `VM-3`'s text might suggest at first
read. `F-61` -- CAP_SYS_BOOT surviving the switch to UID 65532 -- is already closed, and closed
against a real pod (`images/node-actuator/Dockerfile`'s own build comment measures
`CapPrm`/`CapEff`/`CapBnd` in one), via Kind's real kubelet and containerd. Kind is a real
Kubernetes control plane and node; it is not a VM, but the capability question does not need one.
What Kind cannot safely exercise is the syscall itself: `reboot(2)` inside a Kind node would affect
the shared host kernel, not a disposable one, so actuation there has only ever run under
`policyStub`/`Simulate` (`node-agent-daemonset-audit.md:224`). That is the actual gap a real guest
closes -- firing the real syscall and observing a real, independent power-off -- not re-proving the
capability round-trip.

This first milestone (`TestHadronActuatorArmsWithNoSignal`, `test/hadron/actuator_smoke_test.go`)
is scoped narrower than `VM-3` itself on purpose: get the real, shipped actuator image running
inside a real Hadron guest's k3s/containerd/kubelet at all, before wiring any signal, approval
state, or actuation. No signal is ever written in this test. The actuator's own watch loop can only
observe `SignalMissing` -- the normal, silent, non-crashing case
(`cmd/node-actuator/main.go`'s own comment: "SignalMissing is the normal case on every tick and is
deliberately not logged"). What this milestone actually asserts is the one-time startup gate:
`halt gate=CapabilityPermitted result=pass` (`cmd/node-actuator/gates.go`'s `gateCapabilityPermitted`),
proving CAP_SYS_BOOT survived this specific delivery path -- a local `docker save` /
`k3s ctr images import` round trip into the guest's own containerd, not a registry pull -- against
a real kernel underneath, once. Reaching that log line is a legitimate, narrower confirmation than
`F-61` itself, worth having, not a re-litigation of it.

## What this milestone builds

- `test/hadron/command.go` gained `guestCommandStdin`, streaming an `io.Reader` to a remote
  command's stdin (used to import the image tarball) -- shares the same bounded SSH dial/handshake
  path as `guestCommand` via the extracted `dialGuest` helper, rather than duplicating it. A
  `syncWriter` combines `Stdout`/`Stderr` into one buffer safely; a first version pointed both at
  one unsynchronized `bytes.Buffer`, which `-race` caught as a real concurrent-write bug before any
  live run (the library copies each stream in its own goroutine).
- `test/hadron/actuator_smoke_test.go`'s `buildActuatorImageTarball` builds the actual production
  Dockerfile (`images/node-actuator/Dockerfile`) from this checkout via `docker build`/`docker save`
  -- the exact artifact this repository would ship, not a stand-in binary or a hand-rolled security
  context that could quietly diverge from `images.yml`'s real build. `TestHadronActuatorArmsWithNoSignal`
  boots one guest (the same `KairosAutoInstallCloudConfig` path `TestHadronSingleNodeBoot` already
  proved), imports the built image via `guestCommandStdin` and `k3s ctr -n k8s.io images import -`,
  and creates a Pod with production's real `PowerOff`/`Actuate` environment
  (`POWER_AGENT_MODE`, `POWER_ACTUATOR_POLICY`, `POWER_NODE_NAME`) and security context (`SYS_BOOT`
  added, `ALL` dropped, non-root UID 65532, `ReadOnlyRootFilesystem`), with `HostPID: true` matching
  `nodepoweragent_render.go`'s own `hostPoweroff` wiring. `POWER_SIGNAL_PATHS` is left unset
  deliberately, so the actuator falls back to its own derived per-node default path
  (`main.go`'s `defaultSignalPath`) exactly as production does, and that path is never created.
- `.github/workflows/hadron-actuator-smoke.yml`: `workflow_dispatch`-only, following the same
  per-step-timeout/cleanup-before-upload/state-removal-gated-on-cleanup pattern as the other two
  Hadron smoke workflows, checked by the same table-driven `TestSmokeWorkflowsReserveCleanupBudget`.

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| [34768697157](https://github.com/MichaelZalud18/nut-operator/actions/runs/34768697157) | **pass** (171.81s, first attempt) | `halt gate=CapabilityPermitted result=pass detail="CAP_SYS_BOOT is in the permitted set; actuation armed" node=kairos-76ed` -- the real image, imported via `docker save`/`k3s ctr images import` into the guest's own containerd, held CAP_SYS_BOOT through a real kubelet's UID-65532 transition on a real kernel, first try. Two other gate lines fired, neither a failure of what this test asserts: `halt gate=SignalChannel result=fail detail="signal directories cannot be read: /var/lib/power-agent/signals"` (expected -- no signal directory was ever created) and a state-write warning at `/run/actuator/state.json` (expected -- this bare test Pod has no `emptyDir` mounted there, unlike the full rendered manifest; the readiness probe would fail here, which is fine, since nothing in this milestone checks readiness). |
| [34770625393](https://github.com/MichaelZalud18/nut-operator/actions/runs/34770625393) | **pass** (`TestHadronActuatorArmsWithNoSignal` 191.97s, `TestHadronActuatorRejectsInvalidSignals` 132.70s -- `wrong-node` 21.89s, `stale` 5.09s -- all first attempt) | Real signal delivery via a real Kubernetes Secret, both rejected shapes proven distinct and correctly identified: `halt gate=SignalAccepted result=fail detail="SignalWrongNode at /var/lib/power-agent/signals/kairos-8c33.json" node=kairos-8c33-not-this-one executionID=exec-wrong-node`, and separately `halt gate=SignalAccepted result=fail detail="SignalStale at ..." node=kairos-8c33 executionID=exec-stale`. Neither reached `gateModeAuthorized` or `gateSyscallIssued`; the guest answered SSH again immediately after each. |

## Open, deliberately not attempted here

- The accepted-signal path and real `reboot(2)` with hypervisor-confirmed shutdown evidence -- the
  actual high-severity part of `VM-3`. `node-agent-operand.md`'s `OD-27` records that the operator
  has no independent channel to learn what the actuator did; proving this live means watching the
  guest from outside (the QEMU process exiting on its own via ACPI poweroff, not via
  `SafeStop`/`SafeTeardown` killing it) and capturing the actuator's own gate/syscall log lines from
  the guest's serial console (`test/hadron/adapter.go` already pipes it to `stdout`/`stderr`, per
  `smoke_test.go`'s own comment) before the process disappears, since PEG gives no separate signal
  for "guest asked to power off" versus "process died."
- Remaining negative controls: missing required fields, an invalid/future timestamp, and revoked
  approval. Missing-signal-entirely is already covered by the first milestone (`SignalMissing` is
  the silent normal case with no signal volume at all); wrong-node and stale are covered by the
  second (above). Revoked approval is a controller-level concern (`F-126`), not something this
  actuator-only harness can exercise without also standing up the `ShutdownFlow`/executor path.
- The full `NodePowerAgent` DaemonSet, RBAC, and the real per-node Secret projection -- these two
  milestones deploy a bare Pod with the production environment/security context and a hand-built
  Secret, not the full rendered manifest, to isolate "does the real image run for real" from "is the
  rendered manifest correct," which is already covered by existing envtest/Kind coverage against
  fakes.
