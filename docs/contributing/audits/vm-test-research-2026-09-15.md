# VM Test Research and Evidence

Components: VM Test Coverage; Node Agent / DaemonSet; Operator Maturity & Hardening.
Audience: contributors.

Transferred from the active tracker on 2026-09-15. This is the dated research, detailed acceptance
contract, and milestone history behind [the open VM tasks](../../tasks.md#vm-test-coverage), not a
second status tracker or a new test run. Historical descriptions of unfinished work may be
superseded by later evidence in the same record. In particular, single-guest host-side kubeconfig
access was verified; two-node cluster qualification is a separate obligation.

## Scope and Evidence Owners

Use one PEG/QEMU harness with guest adapters: Hadron Linux/k3s now, Talos qualification proposed.
Keep Kind and VM fixtures separate. Component/Kind tests own logical policy matrices; VMs prove
real kernel/API actuation and the production-to-guest handoff. No physical UPS is required.
One control plane plus one disposable worker is the initial scope; 6 GiB combined guest RAM is
an estimate, not a measured minimum. Full task completion is recorded only in the owning tracker.

- [Runner feasibility](hadron-vm-1-feasibility-2026-09-11.md) owns the KVM feasibility experiment.
- [PEG evaluation](hadron-vm-2-peg-evaluation-2026-09-11.md) owns upstream/tooling findings and runs.
- [Actuator evidence](hadron-vm-3-actuator-2026-09-13.md) owns the original Linux guest experiments.
- [Operator and UPS-stack evidence](hadron-vm-4-operator-2026-09-13.md) owns those milestone runs.
- [Kind measurements](kind-modularity-2026-09-13.md) inform the separate logical test boundary.

## Recorded Decisions

These summarize decisions already recorded in the linked experiments, not newly closed OD items.

| Choice | Rationale and qualification limit |
| --- | --- |
| Adopt PEG through a thin adapter | Reuse upstream lifecycle tooling while explicitly fixing verification, exposure, cancellation, and ownership gaps |
| Loopback-only management and private run state | Do not inherit exposed SSH listeners or shared credentials; allocated ports still need collision protection |
| Point-to-point QEMU ClusterLink | Fits the initial two-guest scope without privileged bridge/tap setup; link connectivity alone does not prove cluster join |
| Poll actual Kubernetes Ready | The tested Kairos readiness-stage marker did not fire; a healthy cluster cannot be inferred from that marker |
| Use the shipped BYO-certificate deployment path | Exercise a real supported installation without adding cert-manager to a disposable guest solely for serving certificates |
| Require independent guest-shutdown evidence | Host stop, process disappearance, NotReady, and connection loss alone cannot prove successful actuation |
| Keep Talos behind a guest adapter | Reuse generic lifecycle/safety without assuming SSH; bring-up and TalosShutdown remain separate milestones |

## Detailed Criteria and Milestone History

The following task narratives retain their recorded dates and test limitations. Their unchecked
work is summarized in tasks.md; update implementation status there rather than maintaining a
parallel checklist here. Exact pins and commands describe the cited experiments, not upgrade
instructions for future runs.

## VM-8

extract reusable guest/cluster fixtures from repeated Hadron actuator,
manager, and UPS-stack scenario setup. Own boot/start, readiness, client/kubeconfig acquisition,
image import where applicable, guest process-exit verification, and bounded teardown in the
fixture; individual scenarios keep their behavior/assertions visible. Preserve cancellation,
resource ownership, failure artifacts, and independent shutdown-cause evidence.
Put guest-specific provisioning behind adapters: Hadron can use SSH/Kairos/k3s; Talos uses machine
config, Talos API/talosctl, and Kubernetes. The generic layer must not assume SSH exists.
**Testable now; Conditional:** reuse component failure/cancellation tests plus existing live
Hadron scenarios after extraction. This cleanup has value independently of Talos and is VM-7's
prerequisite. Keep it separate from TEST-1's Kind helpers. Defer a standalone library until
Hadron/k3s, Talos, actuator, and manager/UPS scenarios establish a useful shared contract.

## VM-7

qualify a Talos guest using the existing
PEG/QEMU harness, not a parallel Talos framework. Build on VM-8's generic fixture and preserve
artifact verification, loopback-only management, networking, process ownership, evidence capture,
and bounded cleanup. **Milestone 1:** pin Talos image/version/checksum; use supported tooling to
generate/apply machine configuration through a guest adapter; reach the Talos API from the host;
bootstrap one Kubernetes node; obtain kubeconfig and verify actual Node Ready from the host;
prove clean owned teardown. Keep Talos credentials isolated and out of logs/artifacts.
**Milestone 2, only after deterministic bring-up:** run the shipped TalosShutdown path with the
same missing/expired/wrong-node signal, authorization/revocation, targeting, and privilege-boundary
standards as the Linux VM tests. Negative cases leave the guest running; positive evidence comes
from outside the guest and distinguishes guest-requested shutdown from QEMU crash, forced kill,
connection loss, or timeout. Reuse VM-3's evidence checks and negative controls.
**Testable now; Conditional:** fake/component provisioning/cancellation tests first, then actual
disposable Talos guest API and shutdown qualification. No physical UPS or site cluster needed;
a passing Hadron test cannot close Talos acceptance. Coordinate image/job wiring with VM-5 and
public instructions with VM-6; only gate on repeated, bounded, reproducible guest success.

**Milestone 1 closed 2026-09-17** ([run 35271999079](https://github.com/MichaelZalud18/nut-operator/actions/runs/35271999079),
pass, after two earlier live runs each found a real, distinct bug): artifact pinned/verified,
machine configuration generated and applied over the insecure maintenance API, secure API reachable
after install and reboot, cluster bootstrapped, kubeconfig fetched, and a real Node Ready confirmed
from outside the guest. Full history and evidence table in
[talos-vm-7-bootstrap-2026-09-17.md](talos-vm-7-bootstrap-2026-09-17.md)
(`test/talos/boot_smoke_test.go`, `talos-vm-boot-smoke.yml`).

**Milestone 2 closed 2026-09-17** ([run 35281326987](https://github.com/MichaelZalud18/nut-operator/actions/runs/35281326987),
all pass, after three earlier live runs each found a real, distinct bug): the same five
negative-signal cases, an absent-approval admission control, and a real accepted-signal halt
(hypervisor-confirmed QEMU process exit) all pass against the real `TalosShutdown` actuator policy.
Full history and evidence table in
[talos-vm-7-actuator-2026-09-17.md](talos-vm-7-actuator-2026-09-17.md)
(`test/talos/actuator_smoke_test.go`, `talos-actuator-smoke.yml`). `VM-7` is now fully closed.

## VM-2

implement a reproducible two-node VM harness using established virtualization
tooling and declarative Kairos configuration. Pin OS/k3s artifacts, checksums, and test image
identities; give each run private networking, fresh credentials/storage, and an explicit
kubeconfig. Refuse mutations unless cluster identity and target VM/node mapping match; do not
change the user's default context or mount host block devices. Keep the manager, PostgreSQL,
and simulated UPS on the surviving node, with observation outside the target guests. Teardown
must be bounded, run on failure/cancellation, and remove only resources owned by that run.
**Testable now; Conditional:** leave Kind helpers and `make test-e2e` unchanged; explicitly
scope the isolated VM entry point in contributor guidance when it is implemented.
**Upstream reuse check: done.** PEG evaluated and adopted via a thin adapter (`test/hadron`,
build-tag-gated); see `docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md` for the
license/maintenance/dependency/KVM/SSH-exposure/cleanup findings and how each is closed at the
adapter layer. Remaining for `VM-2` itself: the actual two-node topology, pinned Hadron + k3s
artifact, kubeconfig wiring, and the multi-VM teardown/mutation-refusal safety checks below.
**Adapter hardening (2026-09-11):** regression tests cover process-exit confirmation independent
of PEG's deleted PID file [High], strict SHA-256 validation [Medium], and bounded/cancellable
downloads that return errors and remove partial state [Medium]. Existing unit CI runs the tagged
component tests without KVM; this is not the `VM-5` guest-test job. See the PEG evaluation for
the reproduced failures, fixes, and remaining harness boundaries.
**Single-guest boot: verified live (2026-09-12).** `kairos-hadron-v0.5.1-standard-amd64-generic-v4.3.0-k3sv1.36.4+k3s1.iso`
(`sha256:1488c390e91128e6d8e1f6c2258c3e32881b3ff44de17a700134e2f671f3575c`, cross-checked against
both GitHub's asset digest and the release's own `.sha256` sidecar) — "Hadron" names a real
upstream project (`kairos-io/hadron`, a minimal from-scratch Linux distro Kairos combines with a
Kubernetes distro to build release artifacts), not a project codename. `test/hadron/cloudinit.go`
builds a cloud-init NoCloud seed ISO carrying an unattended-install cloud-config with the
adapter's fresh per-run credentials and `k3s.enabled: true`, attached via PEG's `DataSource`, with
the install device `/dev/vda` (PEG attaches disks as `virtio-blk-pci`, not the `/dev/sda` generic
Kairos docs assume). `hadron-vm-boot-smoke.yml` (`workflow_dispatch`-only, following `VM-1`'s
pattern) booted the pinned artifact, ran the unattended install, and confirmed a genuinely Ready
k3s node via `kubectl get nodes -o json` in 103.83s
([run 34705344528](https://github.com/MichaelZalud18/nut-operator/actions/runs/34705344528)).
Three earlier live runs each found one real, distinct gap before this passed — see
`docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md` for the full sequence — most
notably that the `provider-kairos.bootstrap.after.k3s-ready` cloud-config stage Kairos's own docs
document never fires on this version; k3s itself was healthy well before that was diagnosed, so
readiness is polled via `kubectl` directly, not that stage's marker file. This proves one
disposable guest boots and reaches a working k3s node; the two-node topology, kubeconfig wiring,
and multi-VM safety checks below remain open.
**Inter-guest networking, component-tested but not yet boot-verified (2026-09-12):** the single
verified boot above proves one guest works in isolation, not that two can talk to each other --
PEG's own networking (already replaced once, for the management NIC) gives each guest an
isolated user-mode NAT stack with no path to any other guest, which is the actual precondition
for a k3s agent ever joining a k3s server. `test/hadron/network.go` adds `ClusterLink` /
`ClusterNIC`: a private, host-only virtio-net segment between exactly two guests over a raw QEMU
socket netdev on `127.0.0.1` (`listen=`/`connect=`, not a bridge or tap device, so no elevated
runner privileges are needed). Point-to-point rather than multicast, deliberately: this only
ever needs to serve the "one control-plane VM and one disposable worker" scope stated above, and
a point-to-point TCP-backed socket is less likely to hit CI-runner-specific multicast/IGMP
restrictions than `-netdev socket,mcast=...` would. Each side gets its own fresh, randomly
generated locally-administered MAC (`RandomClusterMAC`, `52:54:00:` OUI) — the two peers only
need to differ from each other, since the segment reaches nothing else, but this still never
reuses a static value, consistent with every other credential in this package. Component tests
cover role/port/MAC wiring and rejection of invalid MACs or an unconstructed `Link`.
`hadron-cluster-link-smoke.yml` (`workflow_dispatch`-only) boots two live-installer guests over
this link and pings between their kernel-assigned IPv6 link-local addresses — no install, no
k3s, isolating exactly this one new mechanism from everything the single-guest workflow already
proved. `TestSmokeWorkflowsReserveCleanupBudget` (`workflow_test.go`) now checks both smoke
workflows, not just the first, and caught this new one's own step-timeout-vs-job-timeout budget
being wrong before any live run. **Passed 2026-09-13** ([run 34766743049](https://github.com/MichaelZalud18/nut-operator/actions/runs/34766743049),
42.39s): a real SSH banner read back between two independently booted guests over the raw QEMU
socket netdev link. Full evidence table, including the seven host-side tooling failures along the
way (guest login, a missing package, BusyBox's `ping` applet having no IPv6 support at all, a
curl-version log-wording mismatch) — never the `ClusterLink` mechanism itself — in
[hadron-vm-2-peg-evaluation-2026-09-11.md](hadron-vm-2-peg-evaluation-2026-09-11.md).
The raw L2 segment still carries no DHCP of its own — a future harness needing static or
negotiated addressing on it (rather than the IPv6 link-local addresses this test used
deliberately) still needs its own mechanism.
Checked, not assumed: Kairos's own reference docs do not show a clear, reliable static-network
cloud-config mechanism, and this project already has one direct cautionary tale about trusting an
apparently-documented Kairos cloud-config feature that silently did not fire (the `k3s-ready`
stage, above) — writing speculative network-config YAML now, with no way to verify it live, risks
repeating that. Left open rather than guessed at.
**Host-side kube API access: verified live (2026-09-12).** `Config.ForwardKubeAPI` forwards a
second loopback-bound port to the guest's k3s API server (`Credentials.KubeAPIPort`), and
`test/hadron/kubeconfig.go`'s `Kubeconfig` fetches the guest's own k3s-generated kubeconfig over
SSH and rewrites only its server URL's port to match — not the host, which VM-2's own successful
live run already proved is `127.0.0.1` by default, so the certificate's Subject Alternative Name
check (which only inspects the host/IP, never the port) needs no changes. Needed regardless of
whether the eventual harness ends up single- or two-node: something outside any guest needs API
access either way.
[Run 34712598425](https://github.com/MichaelZalud18/nut-operator/actions/runs/34712598425)
fetched the kubeconfig, built a real `client-go` clientset from it, and listed nodes through the
forwarded port from outside the guest entirely — passed on the first attempt, no iteration
needed. This is the actual "kubeconfig wiring" `VM-2`'s own text names as open work, not a proxy
for it.
**High-severity safety checks:** mismatched cluster/VM identity must refuse mutation; partial
startup and cancellation must clean up only the current run. Bind forwarded management ports to
loopback (done in `test/hadron`) and prove concurrent runs cannot target or delete each other's
resources. Private state-directory allocation is covered by `test/hadron`; a sampled free SSH
port is not a reservation or proof of concurrent-run isolation. Missing PID evidence fails closed;
the future harness must separately track never-started VMs and verify target/process ownership.
**Timeout/cleanup hardening (2026-09-12): locally component-tested; live cancellation rehearsal
pending.** [Medium] SSH handshake, session creation, commands, polling, and diagnostics now honor
cancellation/deadlines. [High] The workflow reserves job-level cleanup margin, bounds compilation
and execution externally, and replaces broad QEMU killing with run-directory-scoped pidfd cleanup
before artifact upload. State deletion requires successful process cleanup. In-process failure
cleanup confirms exit without deleting diagnostic evidence; cleanup errors fail the smoke test.
[Medium] Readiness checks parse the single node's actual Ready condition rather than accepting
the substring in NotReady. Regression coverage includes stuck SSH phases, cancellation, workflow
budget/order, process ownership, repeated cleanup, and negative readiness cases.
**Remaining limits:** PEG startup/seed tooling is not fully context-aware; external workflow
deadlines remain necessary. A forcibly lost runner cannot execute cleanup; local component tests
do not prove hosted cancellation behavior. Full VM/node identity and port-collision guards below
remain open; the cleanup ownership test does not close the two-node harness acceptance criteria.
**Open — not yet applicable to the single-guest smoke test, but real, specific work once the
two-node harness exists (do not close as N/A without building these):**

1. **Mismatched cluster/VM identity must refuse mutation.** No pre-existing cluster exists for
   the current smoke test to target, so there is nothing to mismatch against yet. The harness
   needs an explicit identity check (expected cluster/kubeconfig identity vs. actual, expected
   VM/node mapping vs. actual) before any mutating action, refusing rather than proceeding on a
   mismatch.
2. **Concurrent runs must not target or delete each other's resources.** Each `workflow_dispatch`
   run of the current smoke test gets its own dedicated GitHub-hosted runner, so there is nothing
   to collide with yet. Once the two-node harness runs multiple VMs within one job -- and once
   more than one harness run can execute concurrently (e.g., two manually triggered runs, or a
   future automated trigger) -- state directories, forwarded ports, and any shared identifiers
   must be run-scoped and verified not to collide, not merely assumed unique the way a single
   `os.MkdirTemp`/`freeport.GetFreePort()` call already is today.

## VM-3

test the shipped Linux actuator on Hadron, including missing/expired/
wrong-node signals and absent or revoked approval. Negative cases must leave the guest running;
the approved positive case must stop only the intended disposable VM, confirmed through the
hypervisor rather than Kubernetes `NotReady` alone. Preserve the existing security context and
approval gates; do not grant blanket privileges. **Testable now; Conditional:** real guest
OS-boundary coverage, separate from Talos API and physical-machine qualification.
**High-severity evidence check:** distinguish guest-initiated shutdown from QEMU crash, forced
termination, lost SSH, or timeout. Capture the shutdown cause and process outcome outside the
guest before teardown; process disappearance alone is insufficient. PEG's `Stop()` is host-driven
termination, not actuator success ([QEMU implementation](https://github.com/spectrocloud/peg/blob/d8627da0983c42bde4d5b21dee650205fd1fb3b7/pkg/machine/qemu.go)).
Add negative controls proving these failure modes cannot satisfy the shutdown assertion, and
reuse the same evidence checks in `VM-4`. A false pass would hide a broken shutdown path.
**First three milestones closed 2026-09-13** ([run 34775970876](https://github.com/MichaelZalud18/nut-operator/actions/runs/34775970876),
all pass): the real, shipped `node-actuator` image arms correctly (`CAP_SYS_BOOT` survives a
real kubelet/containerd round trip), correctly rejects wrong-node and stale signals without ever
halting the guest, and correctly halts the guest on a real accepted signal -- full gate chain
captured (`SignalAccepted` -> `FlowBinding` -> `ModeAuthorized` -> `Sync` -> `CapabilityEffective`
-> `SyscallIssued`) and independently corroborated by the guest's own QEMU process exiting on its
own, without the test ever calling `SafeStop`/`SafeTeardown`. Three real, distinct bugs were
found and fixed along the way (a wrong assumption about capturing a racy pod log, and a genuine
k3s default-ServiceAccount startup race) -- full history and evidence table in
[hadron-vm-3-actuator-2026-09-13.md](hadron-vm-3-actuator-2026-09-13.md)
(`test/hadron/actuator_smoke_test.go`, `hadron-actuator-smoke.yml`).

**DaemonSet/RBAC milestone and absent approval closed 2026-09-17**
([run 35272002034](https://github.com/MichaelZalud18/nut-operator/actions/runs/35272002034), all
pass, after two earlier live runs each found a real, distinct bug): the same negative-signal and
accepted-signal scenarios, now against the real `NodePowerAgent`-rendered DaemonSet, ServiceAccount,
and RBAC, plus a PowerOff `NodePowerAgent` with no `approvalAnnotation` correctly rejected at
admission. Full history and evidence table in
[hadron-vm-3-actuator-daemonset-2026-09-17.md](hadron-vm-3-actuator-daemonset-2026-09-17.md)
(`test/hadron/actuator_daemonset_smoke_test.go`, `hadron-actuator-daemonset-smoke.yml`).

**Revoked approval resolved 2026-09-17** via envtest, not a live guest run -- pure admission-webhook
behavior with no guest-OS boundary to prove. `ValidateUpdate` re-runs the full admission check
against every update regardless of what changed, so removing an already-approved annotation is
itself rejected: approval is a ratchet at the admission layer, not a runtime actuator behavior. See
[hadron-vm-3-actuator-daemonset-2026-09-17.md](hadron-vm-3-actuator-daemonset-2026-09-17.md#revoked-approval)
(`internal/webhook/v1alpha1/nodepoweragent_webhook_test.go`). `VM-3` is now fully closed.

## VM-4

drive a simulated UPS outage through actual NUT telemetry, trigger evaluation,
planning, execution, draining, signal delivery, and guest power-off. Assert survivor availability,
current authorization/release evidence (`F-126`/`F-127`), enforced network policy, and audit results;
manual signal injection alone is not this end-to-end test. Add a second worker for ordered and
concurrent release scenarios only after measuring capacity. **Testable now; Conditional:**
retain logs and hypervisor evidence outside stopped guests; harness-owned reset is not operator
recovery, and restart/resume continuity remains outside scope (SB-1).
**Checked, not assumed (2026-09-13): `test/e2e` does not already cover this end to end.** It
drives real telemetry transitions from a scripted `dummy-ups` fixture and separately hand-writes
a shutdown signal into a projected Secret to prove the actuator accepts it, but nothing there
creates a `ShutdownFlow`, waits for trigger evaluation, and asserts execution/drain/poweroff --
this is genuinely new ground, not a Hadron variant of an existing Kind test.
**First milestone passed 2026-09-13** ([run 34777697857](https://github.com/MichaelZalud18/nut-operator/actions/runs/34777697857),
262.16s, first attempt): `TestHadronOperatorManagerDeploys` (`test/hadron/operator_smoke_test.go`,
`hadron-operator-smoke.yml`) gets the real CRDs/RBAC/manager Deployment running inside a Hadron
guest's k3s -- via `config/byo-cert` and `hack/webhook-cert.sh` (this repo's own no-cert-manager
deploy path, chosen deliberately: "this operator's job is to run correctly while the cluster is
losing power," per that overlay's own comment, so installing cert-manager into a throwaway guest
just to get a serving certificate would be a second, unrelated thing to prove reliable).
**Second milestone passed 2026-09-13** ([run 34781621589](https://github.com/MichaelZalud18/nut-operator/actions/runs/34781621589),
547.22s): `TestHadronOperatorRunsRealUPSStack` (`hadron-ups-stack-smoke.yml`) additionally
builds/imports the real `nut-server` and `upsmon-agent` images and applies a real
`UPSDevice`/`NUTServer`/`NodePowerAgent` fixture -- the same shape `test/e2e`'s own
signal-delivery spec already proves against Kind -- confirming the real, operator-rendered
`NodePowerAgent` DaemonSet reaches Ready on a real guest kernel, `DryRun`/`Simulate` so nothing
can halt the guest yet.
**Third milestone passed 2026-09-16** ([run 35053758898](https://github.com/MichaelZalud18/nut-operator/actions/runs/35053758898),
559.20s): `TestHadronShutdownFlowProducesRealSignal` (`hadron-outage-flow-smoke.yml`) drives a real
`ShutdownFlow` (`mode: Enforce`) whose trigger evaluates real UPS telemetry, whose executor compiles
and runs a real wave, and whose `AgentShutdown` step writes a real signal that the already-proven
node-actuator (`VM-3`) accepts -- the operator-produced signal "manual signal injection alone is
not this end-to-end test" required. Needed far more real infrastructure than either prior
milestone: a real PostgreSQL audit store (SB-11 makes it required for Enforce-mode execution) and a
genuinely cleared node for `AgentShutdown`'s own node-clearance precondition (EX-9), including a
real product bug found and fixed along the way (`upsdevice_controller.go`'s telemetry polling had
no ClusterIP fallback, unlike the agent's own monitoring, which F-71 already fixed). Full rationale
and the nine-run evidence table in
[hadron-vm-4-operator-2026-09-13.md](hadron-vm-4-operator-2026-09-13.md).
The two-guest topology, real drain/eviction against a live workload Pod, and the
network-policy/audit assertions remain open.

## VM-5

integrate the proven harness as a separate, initially manually triggered
Actions job. Consume images built from the exact revision under test, using the existing
digest-based image workflow where applicable, rather than rebuilding while VMs run or pulling
an unrelated `main` image. After successful repeated runs, add component-based triggers for
actuator, planner/executor, NUT integration, policy, packaging, and harness changes. Bound job
time/concurrency and artifact retention, use minimal token permissions, and require no site
secrets or access to a persistent private environment. **Conditional:** make it a required
check only after runner feasibility, resource use, and test reliability are demonstrated.
**Upstream workflow check: done (2026-09-12), not reusable.** Kairos's own
[reusable QEMU workflow](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-qemu-test.yaml)
needs `QUAY_USERNAME`/`QUAY_PASSWORD` registry secrets this project has no reason to hold, and
runs on self-hosted `fast`-labeled runners for nearly every test -- the opposite of what `VM-1`
proved usable (standard GitHub-hosted `ubuntu-latest`). Its diagnostic patterns are still worth
knowing: the same KVM ACL+udev fix this project found independently, and a libvirt-bridge
(`virbr0`) approach to VM-to-VM networking, an alternative to `network.go`'s own point-to-point
`ClusterLink` worth revisiting only if that turns out not to work live or a future harness needs
more than two nodes. See `docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md`.
**Image-build check: done (2026-09-12), not needed.** The pinned Hadron artifact already meets
every `VM-2` requirement, and all customization so far (credentials, install target, k3s
enablement) happens at boot time through the cloud-config `DataSource`, not at image-build time
-- exactly the "pinned published artifact" case this check's own text says to prefer. Kairos's
[Factory workflow](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-factory.yaml)
stays unevaluated further unless that changes.
