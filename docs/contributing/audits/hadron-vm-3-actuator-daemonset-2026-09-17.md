# Hadron VM-3: the real rendered DaemonSet/RBAC

Status: three live runs, 2026-09-17. See `docs/tasks.md`'s `VM-3` entry for current status.

## Scope

[hadron-vm-3-actuator-2026-09-13.md](hadron-vm-3-actuator-2026-09-13.md)'s own milestones all run
the real `node-actuator` image in a hand-built, test-authored Pod (`actuatorPodSpec`) with a
hand-created Secret -- proving the binary and its security context work under a real kernel, but
never through the real `NodePowerAgent`-rendered DaemonSet, ServiceAccount, and RBAC an operator
user actually gets, and never against the real per-node Secret the controller itself owns and
reconciles. This milestone (`test/hadron/actuator_daemonset_smoke_test.go`,
`hadron-actuator-daemonset-smoke.yml`) closes that gap: the same signal-validation scenarios,
re-run against the real rendered manifest, reusing the real-operator-deploy path
`hadron-ups-stack-smoke.yml` and `hadron-outage-flow-smoke.yml` already proved.

Three tests: `TestHadronActuatorDaemonSetRejectsInvalidSignals` (the same five negative-signal
cases as the bare-pod milestone, shared via `invalidActuatorSignalCases` rather than duplicated),
`TestHadronActuatorDaemonSetRejectsAbsentApproval` (a PowerOff `NodePowerAgent` with no
`approvalAnnotation` set must be rejected at admission), and
`TestHadronActuatorDaemonSetHaltsOnAcceptedSignal` (a real accepted signal, a real `reboot(2)`, and
hypervisor-confirmed evidence, mirroring the bare-pod version's own pattern).

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| [35161203889](https://github.com/MichaelZalud18/nut-operator/actions/runs/35161203889) | fail (`stale`/`malformed-timestamp`/`missing-fields` timed out at a 2-minute budget; `RejectsAbsentApproval` failed; `HaltsOnAcceptedSignal` passed) | First live run. Two real problems, initially misdiagnosed as one: the actuator log kept re-logging the *previous* subtest's own rejection reason for over a minute after a new signal was patched in, first attributed to Kubernetes' own documented projected-Secret propagation delay (kubelet sync period + secret cache TTL, up to 2 minutes worst case). `RejectsAbsentApproval` failed on a stale expected-message string -- admission did reject the request, but `internal/resourcevalidation/nodepoweragent.go` raises `field.Required(...)`, which apimachinery renders as `"<path>: Required value: <detail>"`, not the free-text format the assertion expected. |
| [35225219741](https://github.com/MichaelZalud18/nut-operator/actions/runs/35225219741) | fail (same three subtests, now at a 4-minute budget; `RejectsAbsentApproval` now passes; `HaltsOnAcceptedSignal` passes) | The propagation-delay theory was wrong: the same three subtests failed identically even at 4x the original budget, with the actuator log showing total silence for minutes at a time rather than a delayed update. Traced the real mechanism through `internal/controller/nodepoweragent_signals.go`'s `signalStillAuthorized`: every case here names an unresolvable `ShutdownFlow` (`"test-flow"`, never created), so a nil flow falls to `now.Sub(written) <= ttl`. `stale`'s payload is already ten minutes past its own TTL the instant it is written, so the very first reconcile revokes the key from the real Secret before the actuator ever reads it. `malformed-timestamp` and `missing-fields` fail `signalStillAuthorized`'s parse/required-field guard immediately, independent of TTL. `wrong-node` and `future` both pass that same nil-flow check (an on-time or future-dated signal is not stale), so they reliably survive long enough for the actuator's own `InspectSignal` gate to reject them and log why -- this is deterministic, not a race that occasionally goes the other way. |
| [35272002034](https://github.com/MichaelZalud18/nut-operator/actions/runs/35272002034) | **all pass** | `waitForSignalRejectedOrRevoked` now accepts either real outcome for each negative case -- the actuator's own rejection log line, or the signal key's confirmed absence from the real Secret -- instead of assuming the actuator always wins the race against controller revocation. All five negative-signal subtests, the absent-approval admission rejection, and the real accepted-signal halt (hypervisor-confirmed QEMU process exit, never stopped by the test itself) passed cleanly through the real rendered DaemonSet, ServiceAccount, and RBAC. |

VM-3's DaemonSet/RBAC milestone and absent-approval control are now closed.

## Open, deliberately not attempted here

- Revoked approval: whether removing an already-approved annotation is itself rejected by the same
  admission gate (making revocation a ratchet, not a runtime actuator behavior) or takes effect
  some other way has no live evidence yet. Guessing at it risks asserting the wrong mechanism
  entirely rather than leaving it honestly open; see `actuator_daemonset_smoke_test.go`'s own doc
  comment.
- The two-guest survivor topology `VM-4` still needs -- this milestone's guest is standalone.
