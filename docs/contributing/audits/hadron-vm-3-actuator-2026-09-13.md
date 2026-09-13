# Hadron VM-3: real actuator on a real guest

Status: design rationale and three milestones, 2026-09-13. See `docs/tasks.md`'s `VM-3` entry for
current status.

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
| [34774417081](https://github.com/MichaelZalud18/nut-operator/actions/runs/34774417081) | fail (`TestHadronActuatorHaltsOnAcceptedSignal`, 123.98s), but the actual mechanism worked | The real, high-severity core succeeded on the first attempt: with a valid, accepted signal already mounted, the guest's own QEMU process exited **on its own** in ~16s of polling, discovered without this test ever calling `SafeStop`/`SafeTeardown` -- hypervisor-confirmed evidence of a genuine actuator-driven halt, independent of the guest's own API. The test still failed because its own doc comment asserted an unverified claim: that `halt gate=SignalAccepted result=pass` was "not itself a race" to capture from a streamed pod log. It was -- the captured log came back completely empty, because the whole log pipeline (container stdout -> containerd -> kubelet -> this same guest's own k3s API server) is itself slower than how fast a small idle guest reaches `reboot(2)` once a valid signal is already waiting. Same class of mistake as the `ping6` assumption in `VM-2`'s own history: stated as settled, disproved by the first live run. |

Fixed: dropped the log-capture assertion entirely. The only hard requirement is now the QEMU
process exiting on its own, discovered by polling its PID and never calling
`SafeStop`/`SafeTeardown` first; whatever the log stream captures (including nothing) is logged
for diagnostic value, and `streamActuatorLog` now reports its own retry errors into the same
channel so a run that never manages to open the stream at all says why instead of coming back
silently empty.

| [34775306450](https://github.com/MichaelZalud18/nut-operator/actions/runs/34775306450) | fail (`TestHadronActuatorRejectsInvalidSignals/wrong-node` 1.28s, `TestHadronActuatorHaltsOnAcceptedSignal` 107.39s) | A real, distinct k3s startup race, in two independently booted guests: `pods "..." is forbidden: error looking up service account default/default: serviceaccount "default" not found`, on guests that had already reported a Ready node via `kubectl get nodes`. A Ready node proves the kubelet came up; it says nothing about the separate controllers that populate the default namespace's own `default` ServiceAccount, which every Pod needs for admission regardless of whether it names one explicitly. The first two live runs happened not to land in this timing window; this one did, twice. `TestHadronActuatorRejectsInvalidSignals/stale` and the earlier `TestHadronActuatorArmsWithNoSignal` in the same run did not hit it -- a timing race, not a deterministic failure. |

Fixed: added one explicit wait for the default ServiceAccount in `bootActuatorReadyGuest`,
benefiting every test built on that guest rather than each independently risking the same race.

| [34775970876](https://github.com/MichaelZalud18/nut-operator/actions/runs/34775970876) | **all pass** (`TestHadronActuatorArmsWithNoSignal` 188.85s, `TestHadronActuatorRejectsInvalidSignals` 123.54s, `TestHadronActuatorHaltsOnAcceptedSignal` 115.73s) | Clean run across all three tests. `TestHadronActuatorHaltsOnAcceptedSignal` this time won the log-capture race and recorded the complete gate chain: `SignalAccepted` (pass) -> `FlowBinding` (pass) -> `ModeAuthorized` (pass) -> `Sync` (started, then completed in 400ms) -> `CapabilityEffective` (pass) -> `SyscallIssued` (pass, "no further output is expected from this container") -- immediately followed by the guest's own QEMU process exiting on its own, hypervisor-confirmed independently of that log. Full corroboration between the two evidence sources this test was designed around, on a run where both happened to succeed. |

VM-3's first three milestones are now closed: the real actuator arms with a real capability
round trip, correctly rejects invalid signals without ever halting the guest, and correctly halts
the guest on a real accepted signal with hypervisor-confirmed evidence.

`TestHadronActuatorRejectsInvalidSignals` was later extended with the remaining
`InspectSignal` rejection reasons a live guest can exercise -- `future` (`SignalFromFuture`),
`malformed-timestamp` (`SignalInvalidTimestamp`), and `missing-fields`
(`SignalMissingRequiredFields`) -- and
[run 34776752512](https://github.com/MichaelZalud18/nut-operator/actions/runs/34776752512) passed
all eight subtests across all three top-level tests cleanly, first attempt.

## Open, deliberately not attempted here

- Remaining negative controls: missing required fields and an invalid/future timestamp. Missing-
  signal-entirely is already covered by the first milestone (`SignalMissing` is the silent normal
  case with no signal volume at all); wrong-node and stale are covered by the second.
- Revoked approval as a negative control -- a controller-level concern (`F-126`), not something
  this actuator-only harness can exercise without also standing up the `ShutdownFlow`/executor
  path.
- Corroborating the accepted-signal halt with the guest's own kernel-level console output
  (`test/hadron/adapter.go` already pipes it to `stdout`/`stderr`, per `smoke_test.go`'s own
  comment) -- a real kernel `kernel_power_off()` message would be independent of the K8s log
  pipeline entirely, unlike the pod-log capture this milestone found too racy to require. Not
  pursued yet since the process-exit check alone is already load-bearing evidence: nothing else in
  this single-purpose guest holds `CAP_SYS_BOOT`, and the two prior tests in this file prove the
  same guest shape does not exit on its own without a valid signal.
- The full `NodePowerAgent` DaemonSet, RBAC, and the real per-node Secret projection -- these
  milestones deploy a bare Pod with the production environment/security context and a hand-built
  Secret, not the full rendered manifest, to isolate "does the real image run for real" from "is the
  rendered manifest correct," which is already covered by existing envtest/Kind coverage against
  fakes.
