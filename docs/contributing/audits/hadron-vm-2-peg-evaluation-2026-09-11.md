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
refused). `test/hadron/adapter_test.go` works around this by serving a real, tiny fixture over a
local `httptest.Server` rather than depending on network reachability in tests; `NewSafeMachine`'s
doc comment tells callers passing a URL `ISO` to pre-validate reachability or `recover()` at the call
site until this is fixed upstream. Worth an upstream issue once `VM-2`'s harness actually depends on
a remote artifact URL.

### Cancellation and run-owned cleanup

`Stop()` calls into `mudler/go-processmanager` and terminates the QEMU process directly — it does not
SSH in or otherwise ask the guest to shut down. This confirms what `VM-3`'s own task text already
anticipated: *"PEG's `Stop()` is host-driven termination, not actuator success."* Shutdown-evidence
work must never treat a successful `Stop()` as proof of anything the actuator did.

`Clean()` unconditionally `os.RemoveAll`s `MachineConfig.StateDir` with no check that the process has
actually exited first. A caller that calls `Clean()` immediately after `Stop()` without confirming
exit risks removing state (disk images, the monitor socket) out from under a process that has not
finished tearing itself down.

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
4. Leave `StateDir` to PEG's own `os.MkdirTemp`-backed allocation (already unique per process)
   rather than inventing a second naming scheme that could collide with it.
5. Always confirm the process has actually exited (`Alive()`, bounded by a timeout) before calling
   `Clean()`, since `Clean()` itself performs no such check.
6. Never treat `Stop()` succeeding as shutdown evidence — `VM-3`'s evidence-capture design already
   accounts for this.

Implemented as `test/hadron` (build-tag-gated, matching how `test/e2e` isolates its own heavier
test-only dependencies from the default build graph): `NewSafeMachine` builds a `types.Machine`
with all of the above applied, and `SafeTeardown` sequences `Stop()` → confirm exit → `Clean()`.
Booting the actual pinned Hadron + k3s artifact, the two-node topology, and kubeconfig wiring
remain open — this is the adapter layer `VM-2` required before any of that.

Writing a second custom VM lifecycle framework instead of this would mean re-solving process
supervision, SSH connection retry/health-check, and file transfer from scratch for no benefit over
patching in six adapter-level responsibilities around a maintained library already used in production
by the project whose artifacts this harness boots.
