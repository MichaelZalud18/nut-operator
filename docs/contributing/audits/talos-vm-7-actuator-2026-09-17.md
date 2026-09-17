# Talos VM-7: milestone 2, TalosShutdown actuator qualification

Status: four live runs, 2026-09-17. See `docs/tasks.md`'s `VM-7` entry for current status.

## Scope

Milestone 2 (`TestTalosActuator*`, `test/talos/actuator_smoke_test.go`,
`talos-actuator-smoke.yml`), gated on milestone 1's deterministic bring-up
([talos-vm-7-bootstrap-2026-09-17.md](talos-vm-7-bootstrap-2026-09-17.md), closed the same day):
qualify the shipped `TalosShutdown` actuator policy against a real Talos node, reusing
test/hadron's own VM-3 evidence standard -- the same five negative-signal cases, an
absent-approval admission control, and a real accepted-signal halt confirmed by the guest's own
QEMU process exiting on its own, never stopped by the test itself.

Talos has no SSH and no `ctr images import` equivalent, so this milestone needed new harness
plumbing test/hadron never did: a throwaway local OCI registry per guest
(`test/talos/registry_smoke_test.go`), with `genConfig`'s own new `registryMirrors` parameter
(`talosctl gen config --registry-mirror`) redirecting the guest's image pulls there through QEMU
user-mode networking's host gateway address (`10.0.2.2`).

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| [35276370893](https://github.com/MichaelZalud18/nut-operator/actions/runs/35276370893) | fail (all three tests; cleanup also failed) | Two distinct, confirmed bugs. `startLocalRegistry` trimmed `docker run`'s entire combined stdout+stderr as the container ID; on a cold image cache that blob is pull-progress text followed by the ID on its own last line, corrupting every subsequent docker command -- reproduces only on the very first pull of `registry:2`, exactly why it hit the first subtest and not the two after (their run reused the now-cached image). Separately, the workflow's state-root prefix (`talos-actuator-smoke-`) didn't match `hack/talos-cleanup.py`'s own hardcoded `talos-smoke-` safety guard, so cleanup failed closed exactly as designed rather than silently skipping. |
| [35279119437](https://github.com/MichaelZalud18/nut-operator/actions/runs/35279119437) | fail (targeted rerun, `RejectsAbsentApproval` only) | Both prior bugs fixed; cleanup succeeded. New failure: the controller-manager Deployment's pod sat Pending forever with no container statuses at all -- never scheduled, not an image-pull failure the new registry-mirror plumbing might explain. Added a diagnose callback to that wait to capture pod status directly instead of guessing further. |
| [35280480105](https://github.com/MichaelZalud18/nut-operator/actions/runs/35280480105) | **pass** (targeted rerun, `RejectsAbsentApproval` only) | Root cause confirmed offline before this run, not guessed: Talos taints control-plane nodes `NoSchedule` by default, and this single all-in-one node had no `cluster.allowSchedulingOnControlPlanes` override. Verified the fix (`genConfig`'s own `--config-patch` with a plain JSON merge object -- this CLI version rejects JSON6902 against its multi-document config output) by generating a config locally and grepping the result before touching a live guest. |
| [35281326987](https://github.com/MichaelZalud18/nut-operator/actions/runs/35281326987) | **all pass** | Full run, all three tests: all five negative-signal subtests (`wrong-node`, `stale`, `future`, `malformed-timestamp`, `missing-fields`), the absent-approval admission rejection, and the real accepted-signal halt (hypervisor-confirmed QEMU process exit) passed cleanly on the first attempt after the scheduling fix. `waitForTalosSignalRejectedOrRevoked` -- built from test/hadron's own DaemonSet-milestone lesson about controller-side signal revocation racing the actuator's own rejection -- needed no further correction here, unlike that lesson's own first two live runs. |

VM-7's milestone 2 is now closed. Combined with milestone 1's own closure and the revoked-approval
question resolved via envtest
([hadron-vm-3-actuator-daemonset-2026-09-17.md](hadron-vm-3-actuator-daemonset-2026-09-17.md#revoked-approval)),
`VM-7` is fully closed.

## Open, deliberately not attempted here

- Revoked approval on a Talos-target agent specifically: the envtest evidence closing this
  question for `VM-3` is policy-agnostic (`ValidateUpdate` re-runs the same admission check
  regardless of `actuatorPolicy`), so it already covers `TalosShutdown` too -- not re-proven here
  as a separate case.
- `VM-8`'s shared guest/cluster fixture extraction across Hadron and Talos adapters remains
  deferred; this milestone duplicates a handful of small helpers (`runMake`, `runKubectl`,
  `splitImageRef`, the negative-signal case table) from test/hadron in the same small,
  self-contained form its own doc comments describe as deliberate until a third guest adapter
  makes the overlap self-evident.
