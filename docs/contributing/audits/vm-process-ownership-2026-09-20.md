# VM process ownership investigation — 2026-09-20

Components: VM Test Coverage.
Audience: contributors.

## Report and retained evidence

[Run 35532067290](https://github.com/MichaelZalud18/nut-operator/actions/runs/35532067290)
at `1c41131` rejected the agent guest during ownership capture with
`QEMU does not reference the owned state directory`. The server passed the same guard.
The retained console artifact contains BIOS output for the agent; both stderr files are empty.
The original error did not record the observed monitor argument, command-line length or pidfd
exit state, so it cannot establish a path mismatch versus an exit/read race.

The guard validates the executable, requires PEG's exact monitor option for the private state
directory, and retains a pidfd for subsequent cleanup. Its ownership acceptance rules have not
been relaxed. Commit `6a9a367` adds targeted mismatch diagnostics (monitor option, expected monitor,
command-line byte count and exit observation), without dumping unrelated command arguments.
It also adds a CI-required packaged-QEMU regression: a paused, device-free process without
KVM, guest OS, disk or network, captured immediately and after a short delay.

[Component CI run 35534027894](https://github.com/MichaelZalud18/nut-operator/actions/runs/35534027894)
passed all three unit/envtest matrix jobs, both tagged adapter suites, the shared ownership suite
including installed QEMU, and all seven Python cleanup tests. This is component evidence, not
full guest shutdown acceptance.

## Original guard passed a subsequent two-guest run

[Run 35533818621](https://github.com/MichaelZalud18/nut-operator/actions/runs/35533818621)
at `e7749b1` used the unchanged ownership guard. Both guests passed Create, booted and joined as
two Ready nodes. The manager deployed, real NUT telemetry transitioned OnBattery, and the agent
node was cordoned with its workload evicted. The failure occurred afterward in the agent-pod
lookup. This disproves a deterministic inability of that guard to start the two-node topology;
it does not explain or erase the earlier intermittent rejection.

The instrumented [run 35534057478, attempt 2](https://github.com/MichaelZalud18/nut-operator/actions/runs/35534057478/attempts/2)
at `6a9a367` also passed both guest starts and real cordon/eviction. Its final lookup error was
explicitly `expected exactly one Running NodePowerAgent DaemonSet pod, got 0 Running of 0 total`.
No ownership rejection occurred. Attempt 1 failed before QEMU installation because an unrelated
Microsoft package repository returned HTTP 403; retrying the unchanged revision passed setup.

## Separate deterministic selector defect

The two-node manifest names its NodePowerAgent `hadron-two-node-outage-agent`. The post-drain
call used `waitForExactlyOneRunningAgentPod`, whose selector is hardcoded to
`power.zalud.io/nodepoweragent=hadron-outage-agent`. It therefore queried a different agent.
Commit `088f7d7` uses the existing named-agent helper with the two-node fixture's actual name,
retaining the per-attempt timeout changes from `483ba7c`.

A final `context deadline exceeded` does not demonstrate that one API call consumed the entire
budget: `pollGuest` only returns the last check error, and this call supplies no diagnostic
callback. A late cancelled request can obscure preceding empty-list results. The earlier
single-request diagnosis was stronger than the available logging supports.

The cross-node Flannel fix is separate and remains supported by its recorded live network-policy
pass. This investigation does not change networking, guest provisioning, drain logic or controller
behavior. Live qualification and remaining work belong in the [VM tracker](../../tasks.md#vm-test-coverage).
