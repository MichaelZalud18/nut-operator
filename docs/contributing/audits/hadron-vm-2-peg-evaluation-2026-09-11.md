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

None of this has booted a real guest yet. Component tests (`cloudinit_test.go`) only prove the
seed ISO is built and attached with the right content -- the same boundary `VM-1`'s own probe drew
before its first live run found two runner-image defaults (`/dev/kvm` permissions,
`/boot/vmlinuz-*` readability) that no amount of static analysis would have surfaced. A
`workflow_dispatch`-only single-guest boot smoke test, following `VM-1`'s exact pattern, is the
next concrete step before attempting the two-node topology.
