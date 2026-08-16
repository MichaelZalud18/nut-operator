//go:build linux

package main

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// capSysBoot is CAP_SYS_BOOT's bit position in the capability sets.
const capSysBoot = 22

// sysBootPermitted reports whether CAP_SYS_BOOT is in this process's permitted set, and whether it
// has already been raised into the effective set.
func sysBootPermitted() (permitted bool, effective bool, err error) {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return false, false, fmt.Errorf("read capabilities: %w", err)
	}
	index := capSysBoot / 32
	bit := uint32(1) << (capSysBoot % 32)
	return data[index].Permitted&bit != 0, data[index].Effective&bit != 0, nil
}

// verifySysBootAvailable checks at startup that this process could actually power the node off.
//
// Every way of granting CAP_SYS_BOOT to a non-root container fails silently, which is what made
// F-61 a defect rather than an outage: the capability was missing on every mode=Actuate deployment
// for the whole life of the feature, and nothing reported it, because the only code that would have
// noticed runs once, during a power event, on a node nobody is watching.
//
// Checking here converts all of those into a CrashLoopBackOff at rollout. The actuator is the only
// container in the pod that fails; upsmon keeps monitoring beside it, so the cluster loses the
// ability to actuate loudly rather than keeping it in name only. There is no other channel to report
// on -- the pod mounts no ServiceAccount token (F-60) -- so exiting is the signal.
func verifySysBootAvailable() error {
	permitted, _, err := sysBootPermitted()
	if err != nil {
		return err
	}
	if permitted {
		return nil
	}
	return fmt.Errorf("CAP_SYS_BOOT is not in this process's permitted set, so reboot(2) would fail with EPERM. " +
		"One of three things is true: the container securityContext does not request the capability, " +
		"the shipped binary lost its cap_sys_boot file capability (an image registry or builder that " +
		"drops the security.capability extended attribute), or this process was not exec'd as the " +
		"container entrypoint (a capability-dumb parent inside the container loses it to no_new_privs)")
}

// raiseSysBoot moves CAP_SYS_BOOT from the permitted set into the effective set (F-61).
//
// The capability reaches this process as a file capability on the binary, set to permitted-only.
// That distinction is deliberate and is what makes the image runnable at all. A file capability
// marked effective (cap_sys_boot=ep) makes execve fail with EPERM wherever CAP_SYS_BOOT is outside
// the bounding set -- which is every configuration except actuation, including the DryRun/Stub
// default every deployment starts in. The actuator container would have crash-looped on every node
// in the fleet in order to make one rarely-used path work.
//
// Permitted-only masks instead of failing: without the capability in the bounding set the process
// starts with empty sets and this function returns an error, which only ever happens on a path that
// was about to halt the machine. With it, the capability is held but inert until the moment below.
//
// Holding it inert until then is worth something on its own. For the whole life of the process --
// watching a directory, parsing JSON written by its neighbor -- CAP_SYS_BOOT is in the permitted
// set and not the effective one, so a bug reached through that surface cannot halt the host.
func raiseSysBoot() error {
	permitted, effective, err := sysBootPermitted()
	if err != nil {
		return err
	}
	if !permitted {
		return verifySysBootAvailable()
	}
	if effective {
		return nil
	}

	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}
	data[capSysBoot/32].Effective |= uint32(1) << (capSysBoot % 32)
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("raise CAP_SYS_BOOT into the effective set: %w", err)
	}
	return nil
}

// syncFilesystems flushes dirty page cache to disk.
//
// reboot(2) does not do this. Confirmed against the kernel rather than inferred: the
// LINUX_REBOOT_CMD_POWER_OFF branch of SYSCALL_DEFINE4(reboot) calls kernel_power_off(), which runs
// kernel_shutdown_prepare(), do_kernel_power_off_prepare(), migrate_to_reboot_cpu(),
// syscore_shutdown() and machine_power_off() -- none of which touch the filesystem. reboot(2)'s own
// man page states it for this exact command: "If not preceded by a sync(2), data will be lost."
//
// What it buys is bounded and worth stating plainly: dirty pages reach disk. It is not a clean
// unmount and not a read-only remount, so filesystems still come back dirty and journals still
// replay. The difference is how much there is to recover, not whether recovery happens.
var syncFilesystems = func() { unix.Sync() }

var rebootPoweroff = func() error {
	if err := raiseSysBoot(); err != nil {
		return err
	}
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
