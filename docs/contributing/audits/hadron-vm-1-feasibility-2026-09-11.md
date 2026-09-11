# Hadron VM-1 Runner Feasibility

Evidence for `VM-1`: does a standard GitHub-hosted Linux runner offer usable KVM acceleration, before
`VM-2` pins a Hadron + k3s artifact and pays for the network egress and disk footprint that carries.

Probe: `.github/workflows/hadron-vm-probe.yml` (`workflow_dispatch` only) running
`hack/hadron-vm-probe.sh` on `ubuntu-latest`.

## Result: usable

Four dispatched runs on 2026-09-11, each fixing a real gap the previous run's evidence pointed at
rather than assuming the cause:

| Run | Result | Finding |
| --- | --- | --- |
| [34641964539](https://github.com/MichaelZalud18/nut-operator/actions/runs/34641964539) | `kvm-unavailable` | `/dev/kvm` present (CPU flags and device node both there) but not read/write accessible to the `runner` user — a udev group-membership gap on this runner image, not a hardware limit. |
| [34643493885](https://github.com/MichaelZalud18/nut-operator/actions/runs/34643493885) | `boot-failed` | KVM access fixed (udev rule + `setfacl` fallback, matching Kairos's own `reusable-qemu-test.yaml`). New gap: `/boot/vmlinuz-*` is root-only-readable by default on this image — an unrelated Ubuntu hardening default. |
| [34643770776](https://github.com/MichaelZalud18/nut-operator/actions/runs/34643770776) | `ok` | Kernel image copied out via `sudo` first. KVM-accelerated boot reached kernel init in 1.0s. Peak memory reported as `unknown` — `/usr/bin/time -v` never got to write its report because the probe kills QEMU as soon as it sees the boot marker. |
| [34644072181](https://github.com/MichaelZalud18/nut-operator/actions/runs/34644072181) | `ok` | Peak memory now read from `/proc/<pid>/status` `VmHWM` while QEMU is still running. Full result below. |

Final measured result (run 34644072181, `ubuntu-latest`, kernel `6.17.0-1022-azure`):

- KVM-accelerated boot reached kernel init in **1.0s**.
- Peak QEMU RSS: **179,868 KB** (~176 MiB).
- Free disk: **87G available before** installing probe tooling, **86G after** (`qemu-system-x86`,
  `qemu-utils`, `acl` packages) — roughly 1 GiB, on a 145G total runner disk.

## What this settles

A standard GitHub-hosted Linux runner can host a real KVM-accelerated guest, once two runner-image
defaults are worked around: `/dev/kvm` group access (udev rule + ACL fallback) and `/boot/vmlinuz-*`
read access (copy out via `sudo`). Neither is a hardware or hypervisor-policy limit, and both fixes
are already folded into `hack/hadron-vm-probe.sh` and `hadron-vm-probe.yml`.

This does not measure the actual Hadron + k3s artifact's boot time, memory, or disk footprint — this
probe deliberately boots only the runner's own host kernel with no rootfs, to isolate the
infrastructure question from the artifact question. `VM-2` still needs its own measurement once a
pinned artifact exists.
