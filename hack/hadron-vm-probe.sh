#!/usr/bin/env bash
set -euo pipefail

# hadron-vm-probe.sh -- VM-1: standard GitHub-hosted Linux runner feasibility probe.
#
# Answers whether this runner can host a *usable* KVM-accelerated guest at all, before VM-2 pins a
# Hadron + k3s artifact and pays for the network egress and disk footprint that carries. GitHub's
# own runner-image reference does not commit to nested-virtualization support for standard Linux
# runners, so this has to be measured rather than assumed (docs/tasks.md, Hadron VM Test Coverage).
#
# "Usable" means more than an advertised /dev/kvm node or vmx/svm CPU flags: both can be present
# on a runner that still denies real access (missing group membership, a seccomp/cgroup policy, a
# nested-virt restriction the flags don't reflect). This script only calls KVM usable once a real
# guest kernel has proven it executed under `-accel kvm` with no fallback list -- QEMU is asked to
# fail outright rather than silently downgrade to TCG software emulation, because a probe that
# "passes" on emulated acceleration would tell VM-2 through VM-4 nothing true about the
# infrastructure they need.
#
# Deliberately does not download, pin, or boot the Hadron + k3s artifact itself -- that is VM-2's
# job, and only worth doing once this probe has run clean.
#
# Result contract: prints one line starting with HADRON_PROBE_RESULT (grep-able by a caller) and,
# when GITHUB_STEP_SUMMARY is set, appends a human-readable summary. Exit codes distinguish
# unavailable infrastructure from an actual failure of code in this script or QEMU:
#   0 - KVM-accelerated boot proven
#   1 - static signals looked usable but the boot probe itself failed (worth investigating)
#   2 - infrastructure does not offer usable KVM (not a product or script defect)

REPORT_MARKER="HADRON_PROBE_RESULT"
BOOT_TIMEOUT_SECS="${BOOT_TIMEOUT_SECS:-30}"
SERIAL_LOG="$(mktemp)"
TIME_LOG="$(mktemp)"
KERNEL_TMP=""
trap 'rm -f "$SERIAL_LOG" "$TIME_LOG" "$KERNEL_TMP"' EXIT

log() { printf '%s\n' "$*" >&2; }

emit_result() {
  # $1: ok | kvm-unavailable | boot-failed
  # $2: human-readable detail
  local status="$1" detail="$2"
  printf '%s status=%s detail=%s\n' "$REPORT_MARKER" "$status" "$detail"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
      echo "## Hadron VM-1 feasibility probe"
      echo ""
      echo "- Status: **${status}**"
      echo "- Detail: ${detail}"
      echo "- Arch: $(uname -m)"
      echo "- Kernel: $(uname -r)"
      echo "- Free disk (/, KB avail): $(df --output=avail / | tail -1 | tr -d ' ')"
    } >>"$GITHUB_STEP_SUMMARY"
  fi
}

# --- static feasibility signals -------------------------------------------------------------

log "Checking static KVM signals..."

if ! grep -Eq '(vmx|svm)' /proc/cpuinfo; then
  emit_result kvm-unavailable "no vmx/svm flag in /proc/cpuinfo -- this runner's CPU does not advertise hardware virtualization"
  exit 2
fi

if [ ! -e /dev/kvm ]; then
  emit_result kvm-unavailable "/dev/kvm does not exist -- CPU advertises virtualization but the runner did not expose a KVM device"
  exit 2
fi

if [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
  emit_result kvm-unavailable "/dev/kvm exists but is not read/write accessible to $(id -un) -- likely a permission or group-membership gap, not a hardware limit"
  exit 2
fi

# --- actual boot probe -----------------------------------------------------------------------

log "Static signals look usable. Attempting a real KVM-accelerated guest boot..."

command -v qemu-system-x86_64 >/dev/null 2>&1 || {
  emit_result kvm-unavailable "qemu-system-x86_64 is not installed on this runner"
  exit 2
}

KERNEL="/boot/vmlinuz-$(uname -r)"
if [ ! -e "$KERNEL" ]; then
  emit_result boot-failed "host kernel image ${KERNEL} does not exist; nothing to boot the probe guest with"
  exit 1
fi
if [ ! -r "$KERNEL" ]; then
  # Ubuntu ships /boot/vmlinuz-* root-only-readable by default (hardening against a local
  # KASLR-offset disclosure) -- an ordinary permission default, not a KVM feasibility signal.
  # Copy it out to a world-readable temp file via sudo rather than treating this as a boot
  # failure; if sudo itself cannot read it, that is worth failing loudly on.
  KERNEL_TMP="$(mktemp)"
  if sudo cp "$KERNEL" "$KERNEL_TMP" 2>/dev/null && sudo chmod 0644 "$KERNEL_TMP"; then
    KERNEL="$KERNEL_TMP"
  else
    emit_result boot-failed "host kernel image ${KERNEL} is not readable and could not be copied out via sudo"
    exit 1
  fi
fi

start_time=$(date +%s.%N)

# `-accel kvm` (no `:tcg` fallback list) makes QEMU refuse to start rather than quietly switching
# to software emulation if KVM turns out not to be usable despite the static checks above. There
# is no rootfs, so the guest is expected to panic looking for init -- the boot marker on the
# serial console is the proof point, not a completed boot. `/usr/bin/time -v` wraps the process
# because the guest itself never boots far enough to have its own memory ceiling worth measuring;
# QEMU's own RSS while doing KVM-accelerated execution is the number of interest.
set +e
/usr/bin/time -v qemu-system-x86_64 \
  -accel kvm \
  -m 512M \
  -nographic \
  -no-reboot \
  -kernel "$KERNEL" \
  -append "console=ttyS0 panic=-1" \
  -serial file:"$SERIAL_LOG" \
  2>"$TIME_LOG" &
qemu_pid=$!

booted=0
for _ in $(seq 1 "$BOOT_TIMEOUT_SECS"); do
  if grep -q 'Linux version' "$SERIAL_LOG" 2>/dev/null; then
    booted=1
    break
  fi
  if ! kill -0 "$qemu_pid" 2>/dev/null; then
    break
  fi
  sleep 1
done

kill "$qemu_pid" 2>/dev/null || true
wait "$qemu_pid" 2>/dev/null
set -e

end_time=$(date +%s.%N)
boot_seconds=$(awk -v a="$start_time" -v b="$end_time" 'BEGIN { printf "%.1f", b - a }')
peak_kb=$(grep -oP 'Maximum resident set size \(kbytes\): \K[0-9]+' "$TIME_LOG" 2>/dev/null || echo "unknown")

if [ "$booted" -ne 1 ]; then
  emit_result boot-failed "qemu did not print a kernel boot marker within ${BOOT_TIMEOUT_SECS}s"
  log "----- qemu stderr/time output -----"
  cat "$TIME_LOG" >&2 || true
  log "----- guest serial output -----"
  cat "$SERIAL_LOG" >&2 || true
  exit 1
fi

emit_result ok "KVM-accelerated boot reached kernel init in ${boot_seconds}s, peak QEMU RSS ${peak_kb} KB"
