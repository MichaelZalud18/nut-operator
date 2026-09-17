# Talos VM-7: milestone 1, deterministic bring-up

Status: three live runs, 2026-09-17. See `docs/tasks.md`'s `VM-7` entry for current status.

## Scope

`VM-7` is to qualify a Talos guest on the existing PEG/QEMU harness `test/hadron` already proved,
not a parallel framework. `test/talos` is a new guest adapter reusing PEG directly: Talos has no
SSH and no cloud-init equivalent, so machine configuration is applied after boot over Talos's own
gRPC API instead. Milestone 1 (`TestTalosNodeBootstraps`,
`test/talos/boot_smoke_test.go`, `talos-vm-boot-smoke.yml`): pin the Talos artifact, generate and
apply machine configuration through the adapter, reach the Talos API from the host, bootstrap one
Kubernetes node, fetch kubeconfig, and verify a real Node Ready from the host, then prove clean
owned teardown. Deliberately not `TalosShutdown` qualification -- `VM-7`'s own text gates that on
this bring-up being deterministic first.

Pinned artifacts, checksum-verified against the release's own `sha256sum.txt`: Talos v1.12.9
`metal-amd64.iso` and `talosctl-linux-amd64`.

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| [35161202282](https://github.com/MichaelZalud18/nut-operator/actions/runs/35161202282) | fail (`context deadline exceeded` waiting for the maintenance API at a 3-minute budget) | First live run. The captured console showed only SeaBIOS/isolinux text ("Booting `Talos ISO`"), nothing further -- consistent with Talos's kernel having no `console=ttyS0`, so nothing is captured on that serial console even on a successful boot. Read as a timing problem (CD-ROM-backed ISO boot under nested KVM plus Talos's own first-boot initialization taking longer than budgeted) and widened to 8 minutes. |
| [35225217822](https://github.com/MichaelZalud18/nut-operator/actions/runs/35225217822) | fail (same wait, now at an 8-minute budget, still `context deadline exceeded`) | The timing theory was wrong -- but this run's own error was far more informative: `talosctl version --insecure` actually reached `apid` and got a real gRPC reply, `rpc error: code = Unimplemented desc = API is not implemented in maintenance mode`. The probe itself was invalid for this pinned build, confirmed against a locally downloaded `talosctl` binary's own `get --help` text (`-i`/`--insecure`: "get resources using the insecure ... maintenance service") and community-documented use of `get disks --insecure` for exactly this purpose (inspecting available disks before an install). Switched `talosMaintenanceAPIReachable` to `get disks --insecure`. |
| [35271999079](https://github.com/MichaelZalud18/nut-operator/actions/runs/35271999079) | **pass** | Full milestone 1 flow succeeded: maintenance API reachable, machine configuration generated and applied, secure API reachable after install and reboot, cluster bootstrapped, kubeconfig fetched, and a real Node reported Ready from outside the guest through the forwarded Kubernetes API. Clean owned teardown. |

VM-7's milestone 1 (deterministic bring-up) is now closed.

## Open, deliberately not attempted here

- Milestone 2, `TalosShutdown` qualification: the same missing/expired/wrong-node signal,
  authorization/revocation, targeting, and privilege-boundary standards as the Linux VM tests,
  gated on this bring-up being deterministic first (now true). Reuse VM-3's evidence checks and
  negative controls.
- `VM-8`'s shared guest/cluster fixture extraction across Hadron and Talos adapters -- `test/talos`
  still duplicates `test/hadron`'s own ISO-download/verify/teardown primitives in the small,
  self-contained form that milestone's own doc comment describes as deliberate until a third guest
  adapter makes the overlap self-evident.
