# VM-2 Upstream Reuse Check: PEG

Evaluation of [`github.com/spectrocloud/peg`](https://github.com/spectrocloud/peg) against `VM-2`'s
contract, before extending any custom VM lifecycle code. Findings verified against PEG's actual
source at the pinned commit, not a summary of its README, and cross-checked against how
[Kairos's own test suite](https://github.com/kairos-io/kairos/blob/master/tests/tests_suite_test.go)
uses it in production CI today.

Pinned commit: `d8627da0983c42bde4d5b21dee650205fd1fb3b7` (2026-08-13) — the same commit Kairos's own
`tests/go.mod` currently pins (`v0.0.0-20260813125620-d8627da0983c`), which is real-world evidence
this exact version is in active upstream use, not an arbitrary point-in-time pick.

## Checks

### License and maintenance

Apache-2.0, compatible with this repo's own license. Not archived. Last push 2026-08-13 (a month old
at evaluation time), 6 open issues, no tagged releases — consumed via commit pseudo-version, the same
way Kairos itself consumes it. Small (single-digit stars), but maintained by Spectro Cloud, the same
organization co-developing Kairos, for exactly this purpose.

### Dependency compatibility

Requires Go 1.25.0; this repo is on 1.26.6. Declares old `onsi/ginkgo/v2 v2.1.4` and
`onsi/gomega v1.20.1` as minimums against this repo's already-newer `v2.27.4`/`v1.39.0` — Go's
minimum version selection resolves to the newer versions already in the build, so no downgrade or
duplicate-version conflict. Pulls in `ipfs/go-log` transitively for its zap-based logging wrapper,
which is a heavier and more unusual dependency footprint than a VM test tool needs; not a blocker,
but worth its own `grype`/`govulncheck` pass once vendored rather than assuming it inherits this
repo's existing clean scans for free.

### KVM use per architecture

**Gap, with a working fix.** PEG's own `qemu.go` sets no acceleration flag by default on x86_64 —
whatever QEMU's build-time default is, unproven, exactly the silent-fallback risk `VM-1`'s probe
exists to rule out — and hardcodes `"-machine", "virt,accel=tcg,acpi=on,gic-version=2"`
unconditionally on aarch64, which a caller cannot override without a second, conflicting `-machine`
argument.

The fix for x86_64 is real and already proven in production: `MachineConfig.Args []string` is
appended directly into the constructed QEMU command line, and Kairos's own `tests_suite_test.go`
uses exactly this to add `-enable-kvm` when `Arch == "x86_64"`. `-enable-kvm` behaves like `-accel
kvm` for this purpose — QEMU fails outright rather than silently downgrading to TCG if it cannot
actually get KVM — so it satisfies the same no-silent-fallback requirement `VM-1` established.

aarch64 stays TCG-only under PEG with no override path found. Not a blocker: GitHub-hosted runners
for this project are x86_64 (`VM-1`'s own finding). Recorded here as a real limitation to revisit
only if arm64 coverage is ever needed.

### SSH authentication and listener exposure

**Gap, with a clean native fix — corrected from an earlier pass of this evaluation.** PEG's default
networking hardcodes `"-nic", fmt.Sprintf("user,hostfwd=tcp::%s-:22", ...)` with no host bind
address, which QEMU resolves to listening on all interfaces, not loopback. A first read of `qemu.go`
in isolation suggested this was unfixable short of patching PEG, since a second `-nic` argument added
via `Args` would add a redundant NIC rather than override the existing one. Reading `config.go`
against `Create()`'s actual gating logic (`if !q.machineConfig.DisableDefaultNetworking { ... }`)
found the real fix: `types.DisableDefaultNetworking` skips PEG's own `-nic` line entirely, after which
`Args` is free to supply a caller-owned `-nic user,hostfwd=tcp:127.0.0.1:PORT-:22` that genuinely
binds loopback-only, with no patch required.

`controller.sshConfig()` already dials `127.0.0.1:<port>` for this project's own use of the guest
(`Command`, `SendFile`, `ReceiveFile`), so the exposure this fixes is specifically an external actor
on the runner's network reaching the guest's SSH server, not this project's own control path. Kairos's
own usage compounds the unfixed default with a static credential (`SSH_USER`/`SSH_PASS`, defaulting to
`kairos`/`kairos`) — acceptable for their own isolated runners, not something to reuse as-is; this
package always generates a fresh random credential instead, which `VM-2`'s own text already requires
independent of this finding. `controller.sshConfig()` also sets
`HostKeyCallback: ssh.InsecureIgnoreHostKey()`, a reasonable default for a freshly created,
loopback-only, single-use guest with no persistent host identity to verify against — not treated as a
gap.

### Artifact download (found while building the adapter, not one of the six required checks)

**Real bug, not a design gap.** `machine.New()` downloads a URL `ISO` synchronously via its own
`prepare()` step (not deferred to the later `Create()`, which the initial reading of `qemu.go` in
isolation assumed) and verifies it against `ISOChecksum`. Pointing it at an unreachable host during
adapter development reproduced a genuine PEG bug: `pkg/machine/internal/utils/download.go` reads
`resp.HTTPResponse.Status` unconditionally right after issuing the request, which panics rather than
returning an error when the request fails before an HTTP response exists (DNS failure, connection
refused). A successful fixture download did not cover this failure. The 2026-09-11 hardening pass
reproduced the panic with a refused loopback connection, then removed PEG's download wrapper from
the adapter path. It now uses the existing `grab/v3` dependency directly, checks `Response.Err()`,
and only gives PEG a local file. `NewSafeMachineContext` supports caller cancellation, and both
constructors impose a ten-minute download deadline. Failed construction removes its private state
directory, including partial downloads. No caller-side reachability check or panic recovery is
required. Severity: **Medium**, fixed at the adapter boundary; the pinned upstream bug remains.

The same pass found that PEG silently skips unknown checksum algorithms. Requiring a nonempty
string was insufficient: an unsupported algorithm or extra colon could accept unverified bytes.
The adapter now accepts only SHA-256 (bare 64-digit hex or `sha256:<hex>`), validates it before
network access, and configures `grab` to verify the bytes and delete mismatched downloads. Supplied
checksums for local files are also verified. Severity: **Medium**, fixed with negative regressions.

### Cancellation and run-owned cleanup

`Stop()` calls into `mudler/go-processmanager` and terminates the QEMU process directly — it does not
SSH in or otherwise ask the guest to shut down. This confirms what `VM-3`'s own task text already
anticipated: *"PEG's `Stop()` is host-driven termination, not actuator success."* Shutdown-evidence
work must never treat a successful `Stop()` as proof of anything the actuator did.

`Clean()` unconditionally `os.RemoveAll`s `MachineConfig.StateDir` with no check that the process has
actually exited first. A caller that calls `Clean()` immediately after `Stop()` without confirming
exit risks removing state (disk images, the monitor socket) out from under a process that has not
finished tearing itself down. The original adapter's `Alive()` polling did not fix this: PEG's
`Stop()` deletes the PID file, and its subsequent `Alive()` lookup interprets the missing file as
an exited process. Severity: **High**. The 2026-09-11 hardening pass reproduced this with a real
child process whose PID file disappears while the process remains alive. `SafeTeardown` now retains
an `os.Process` handle before calling `Stop()` and checks that handle until exit or deadline. A
missing/invalid PID or failed exit check preserves state and returns an error. Never-started VMs
need separate lifecycle tracking in the future harness; a missing PID is not evidence of exit.

Collision safety between concurrent runs — unique state directories, unique forwarded ports — is
entirely the caller's responsibility; PEG provides no run-identity concept of its own. This is
exactly the property `VM-2`'s "concurrent runs cannot target or delete each other's resources"
safety check is asking about, and it has to be built at the adapter layer, not assumed from adopting
PEG.

## Verdict: adopt via a thin adapter

None of the four gaps found are blocking, and none require forking PEG — they are all addressable in
a thin wrapper around PEG's public `Machine` interface:

1. Always pass `-enable-kvm` via `MachineConfig.Args` on x86_64 (matching Kairos's own proven
   pattern); this package only ever requests x86_64, since `VM-1` proved KVM feasibility on that
   architecture specifically and PEG's aarch64 path hardcodes TCG with no override.
2. Generate a fresh random SSH credential per run; never a static default.
3. Set `DisableDefaultNetworking` and supply a caller-owned, loopback-bound `-nic` argument via
   `Args` instead, rather than trusting PEG's own all-interfaces default.
4. Allocate a private `os.MkdirTemp` state directory before preparing the artifact, supply it to
   PEG, and remove it if construction fails. This also makes failed-download cleanup possible.
5. Capture an OS process handle before `Stop()` removes the PID file; confirm exit through that
   handle before `Clean()`. Do not use PEG's PID-file-dependent `Alive()` as exit evidence.
6. Never treat `Stop()` succeeding as shutdown evidence — `VM-3`'s evidence-capture design already
   accounts for this.
7. Validate SHA-256 syntax and download through `grab/v3` with a deadline and caller cancellation;
   do not pass remote URLs to PEG's panic-prone downloader.

Implemented as `test/hadron` (build-tag-gated, matching how `test/e2e` isolates its own heavier
test-only dependencies from the default build graph): `NewSafeMachine` builds a `types.Machine`
with all of the above applied, and `SafeTeardown` captures process identity, then sequences
`Stop()` -> confirm exit -> `Clean()`. The component suite covers refused connections, HTTP errors,
truncated bodies, untrusted TLS, checksum failures, cancellation/deadlines, state cleanup, missing
PID evidence, and the real PEG stop implementation against disposable child processes. No QEMU
guest is started by these tests. The existing Tests workflow runs them with the race detector.
Booting the actual pinned Hadron + k3s artifact, the two-node topology, and kubeconfig wiring
remain open — this is the adapter layer `VM-2` required before any of that.

Writing a second custom VM lifecycle framework instead of this would mean re-solving process
supervision, SSH connection retry/health-check, and file transfer from scratch for no benefit over
patching in these adapter-level responsibilities around a maintained library already used in production
by the project whose artifacts this harness boots.

## Artifact and cloud-config research (2026-09-11, not yet boot-verified)

Checking the actual Kairos release assets rather than assuming "Hadron" was this project's own
codename found it names a real upstream project: [`kairos-io/hadron`](https://github.com/kairos-io/hadron),
described as "a minimal, from-scratch Linux distro using vanilla components, engineered for
trusted and flexible boot environments." Kairos's build tooling combines that base distro with a
Kubernetes distro (k3s or k0s) to produce release artifacts named
`kairos-hadron-<hadron-version>-<arch>-<board>-<kairos-version>-<k8s-distro><k8s-version>.iso`.
The one pinned here, `kairos-hadron-v0.5.1-standard-amd64-generic-v4.3.0-k3sv1.36.4+k3s1.iso`,
comes from `kairos-io/kairos` release `v4.3.0`. Checksum
`sha256:1488c390e91128e6d8e1f6c2258c3e32881b3ff44de17a700134e2f671f3575c`, cross-checked against
both GitHub's own asset digest metadata and the release's separately published `.sha256` sidecar
file — two independent sources agreeing, not a single fetch taken on faith. Its bundled k3s
(1.36.4) matches `k8s: ["1.34", "1.35", "1.36"]` in `.github/workflows/test.yml`'s own matrix, the
newest version this repo already tests against.

Kairos's unattended-install cloud-config schema (`install:` with `device`/`reboot`/`auto`, `k3s:
enabled: true`, a `users:` entry) is documented; the generic docs example uses `/dev/sda` for
`install.device`. That is wrong for a PEG-booted guest: `qemu.go`'s `genDrives` attaches user
disks as `virtio-blk-pci`, which Linux enumerates under the virtio-blk naming scheme
(`/dev/vda`, ...), not the SCSI/SATA scheme `/dev/sda` implies. Passing the doc's example device
verbatim would have targeted a device that does not exist, only discoverable after a real,
multi-minute install-and-reboot cycle failed against a downloaded artifact -- exactly the kind of
mistake worth catching by reading PEG's actual drive-attachment code instead of copying a generic
example.

`test/hadron/cloudinit.go` implements this: `buildNoCloudISO` shells out to
`genisoimage`/`mkisofs` (the same tool cloud-init's own `cloud-localds` wraps) to build a
volume-labeled `cidata` seed ISO, and `KairosAutoInstallCloudConfig` renders the install/k3s/users
YAML using the adapter's own freshly generated credentials -- never a static default, consistent
with every other credential in this package. `Config.CloudConfig`, when set, attaches the result
through PEG's existing `DataSource` field. This is opt-in, not automatic: the adapter's own
contract is generic VM lifecycle, not an opinion about what a given guest should do on first boot.

## Single-guest boot: verified live (2026-09-12)

`hadron-vm-boot-smoke.yml` (`workflow_dispatch`-only, following `VM-1`'s exact pattern) exercised
the pinned artifact and cloud-config against a real GitHub-hosted runner four times. Each of the
first three found one real, distinct gap; the fourth passed.

| Run | Result | Finding |
| --- | --- | --- |
| [34666294875](https://github.com/MichaelZalud18/nut-operator/actions/runs/34666294875) | fail (~7.5m) | `/dev/kvm` present but not read/write accessible to the `runner` user — the same runner-image gap `VM-1` found, needing the same udev+ACL fix here independently. |
| [34671188060](https://github.com/MichaelZalud18/nut-operator/actions/runs/34671188060) | fail (~11.5m) | KVM fix worked; new gap found via a full, unfiltered console dump (a `tail -c 50000` in-log dump was tried first and found to get crowded out by a repeating serial getty prompt) — one clean reboot from the live installer into the installed `COS_ACTIVE` disk, console then quiet, but SSH commands kept succeeding the whole 10-minute wait (a working connection running a real command, not a connection failure), consistent with a normally-booted guest. The `/tmp/k3s-ready` marker just never appeared. |
| [34672025391](https://github.com/MichaelZalud18/nut-operator/actions/runs/34672025391) | fail (~11.5m) | Added live diagnostic snapshots (`systemctl status k3s`, its journal) every ~60s instead of guessing at a bigger timeout. All nine snapshots showed `k3s.service` active and CoreDNS/Traefik/metrics-server genuinely Ready within ~40s of the service starting — k3s itself was never the problem. The `provider-kairos.bootstrap.after.k3s-ready` cloud-config stage (kairos.io/docs/examples/k3s-stages) simply never fires on this Kairos version. |
| [34705344528](https://github.com/MichaelZalud18/nut-operator/actions/runs/34705344528) | **pass (103.83s)** | Readiness switched to polling `sudo k3s kubectl get nodes -o json` directly and parsing the real `NodeReady` condition (`hasReadyNode`, `readiness.go`) instead of the broken marker file. SSH reachable in ~35s; a genuinely Ready node (`kairos-2990`) confirmed within ~65s more. |
| [34712598425](https://github.com/MichaelZalud18/nut-operator/actions/runs/34712598425) | **pass (111.55s)** | First live exercise of `Config.ForwardKubeAPI`/`Kubeconfig` (kubeconfig.go): fetched the guest's kubeconfig, built a real `client-go` clientset from it, and listed nodes through the forwarded API port from outside the guest entirely. Passed on the first attempt -- no iteration needed, unlike every prior new mechanism this evaluation exercised for the first time. |

Also hardened along the way, verified by dedicated component tests rather than only by the live
runs: `guestCommand` (`command.go`) bounds the entire SSH exchange by context, since PEG's own
`Command()` has no cancellation of its own; `SafeStop` (`adapter.go`) replaces PEG's unbounded
`kill`-shelling `Stop()` for the test's failure path; `hadron-cleanup.py` uses
`pidfd_open`/`pidfd_send_signal` scoped to each run's own private state root, closing the
PID-reuse race a blanket `pkill -f qemu-system-x86_64` was exposed to; and `workflow_test.go`
parses the actual workflow YAML to regression-test its own safety invariants (every step bounded,
cleanup unconditional, cleanup precedes artifact upload, state removal gated on cleanup success).

This proves one disposable Hadron guest boots unattended, reaches a genuinely Ready k3s node, and
is reachable via its kubeconfig from outside the guest, all on a standard GitHub-hosted runner. It
does not prove the two-node topology, guest-to-guest connectivity, concurrent-run isolation, or any
shutdown/actuation behavior -- those remain `VM-2`'s open work and `VM-3`/`VM-4`.

## VM-5's upstream reuse checks (2026-09-12)

`VM-5`'s own text calls for two checks before wiring the harness into CI: whether Kairos's
reusable QEMU workflow is worth reusing for setup/diagnostics, and whether its Factory workflow is
worth adopting for image builds. Read both, not summaries of their READMEs.

**Reusable QEMU workflow
([reusable-qemu-test.yaml](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-qemu-test.yaml)):
not directly reusable, and the check found concrete reasons why, not just caution.** It requires
`QUAY_USERNAME`/`QUAY_PASSWORD` secrets (registry credentials for temp CI images this project has
no reason to manage) and runs on a `fast`-labeled self-hosted runner for every test except one --
infrastructure this project does not have, and the opposite of what `VM-1` specifically proved
usable (standard GitHub-hosted `ubuntu-latest`). Its diagnostic *patterns* are worth knowing,
independent of reusing the workflow itself: the KVM ACL+udev fix this project already found
independently, OVMF firmware auto-detection for SecureBoot testing (not needed here), and --
notably -- its `USE_BRIDGE_NETWORK` path sets up a real libvirt bridge (`virbr0`) for VM-to-VM
communication, a heavier-privilege, N-VM-capable alternative to this project's own point-to-point
`ClusterLink`/`ClusterNIC` (`network.go`), worth revisiting only if the socket-netdev approach
turns out not to work live, or if a future harness ever needs more than two nodes.

**Factory workflow
([reusable-factory.yaml](https://github.com/kairos-io/kairos/blob/master/.github/workflows/reusable-factory.yaml)):
not needed at all, not just not adopted.** It builds custom Kairos images from a Dockerfile and
pushes them to a caller-specified registry -- real capability, usable by an external caller with
their own registry credentials, but built for a use case this project doesn't have. The pinned
`kairos-hadron-v0.5.1-standard-amd64-generic-v4.3.0-k3sv1.36.4+k3s1.iso` artifact already meets
every requirement `VM-2` has, and all customization needed so far (credentials, install target,
k3s enablement) happens at boot time through the cloud-config `DataSource`, not at image-build
time. `VM-5`'s own text already said to prefer a pinned published artifact when it meets the test
requirements before evaluating this workflow at all -- it does, so this stays unevaluated further
unless that changes.

## ClusterLink connectivity: first live attempt found a real bug (2026-09-12)

`hadron-cluster-link-smoke.yml` boots two guests wired by `network.go`'s `ClusterLink`/
`ClusterNIC` and pings between their kernel-assigned IPv6 link-local addresses, deliberately never
running the unattended install (no k3s, no cloud-config customization needed for a pure networking
check).

| Run | Result | Finding |
| --- | --- | --- |
| [34714542243](https://github.com/MichaelZalud18/nut-operator/actions/runs/34714542243) | fail (~11.5m, controlled) | Both guests were booted with no `CloudConfig` at all -- deliberately, since this test needs neither install nor k3s. That also meant neither guest had any account matching the fresh `Credentials` `NewSafeMachine` generated: nothing else creates one. Every SSH attempt failed with `ssh: unable to authenticate`, not a connectivity problem. Separately, the wait itself had no bound of its own -- it fell through to the overall test deadline, so a failure that should have surfaced in well under a minute instead consumed the full ten-minute budget before being reported. |
| [34715782711](https://github.com/MichaelZalud18/nut-operator/actions/runs/34715782711) | fail (~1.5m) | Fixed the SSH login, introduced a new dependency in the same change: `MinimalSSHCloudConfig` means both guests now get a real cloud-init seed ISO built (`buildNoCloudISO`), which needs `genisoimage`/`mkisofs` -- present in `hadron-vm-boot-smoke.yml`'s own install step, never added to this workflow's, since its first version had no `CloudConfig` at all and never needed it. `NewSafeMachine` failed immediately with "neither genisoimage nor mkisofs found on PATH." The fast, controlled failure (1.5m, not a timeout) is itself the payoff of the previous run's bounded-wait fix -- this surfaced immediately instead of only after minutes of waiting. |
| [34716739515](https://github.com/MichaelZalud18/nut-operator/actions/runs/34716739515) | fail (~40s), but the actual mechanism worked | SSH succeeded on both guests. `linkLocalAddress` correctly identified each guest's own cluster interface and its kernel-assigned link-local address by MAC lookup on both sides -- the client's derived address (`fe80::5054:ff:fe8b:a3db`) and the server's interface name (`ens5`, confirming systemd's own predictable naming for a second virtio-net-pci device, not predicted or hardcoded here). Only the `ping` invocation was wrong: this live environment's `ping` is BusyBox v1.37.0, and its own usage text (captured from the actual failure, not assumed from BusyBox's general docs, which describe a differently configured build) showed no `-4`/`-6` flags compiled in. `ping6` is BusyBox's standard alternate applet name for exactly this case. |

Fixed: added `genisoimage` to the workflow's install step, and switched `ping -6` to `ping6`. This
is the first run where every piece up through address discovery on both independently booted
guests actually worked -- the raw QEMU socket netdev ClusterLink itself has not yet failed at
anything; only host-side test tooling has, three times over. `TestSmokeWorkflowsReserveCleanupBudget`
does not catch a missing package or a wrong shell command (it only checks the YAML's own
timeout/cleanup shape), which is itself worth noting as a real limit on what a static workflow
check can catch. Not yet re-run live.
