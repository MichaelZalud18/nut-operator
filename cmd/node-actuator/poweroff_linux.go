//go:build linux

package main

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// capSysBoot is CAP_SYS_BOOT's bit position in the capability sets.
const capSysBoot = 22

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
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("read capabilities: %w", err)
	}

	index := capSysBoot / 32
	bit := uint32(1) << (capSysBoot % 32)

	if data[index].Permitted&bit == 0 {
		return fmt.Errorf("CAP_SYS_BOOT is not in this process's permitted set, so reboot(2) would fail with EPERM: " +
			"the container must request the capability and the binary must carry cap_sys_boot=p")
	}
	if data[index].Effective&bit != 0 {
		return nil
	}

	data[index].Effective |= bit
	if err := unix.Capset(&header, &data[0]); err != nil {
		return fmt.Errorf("raise CAP_SYS_BOOT into the effective set: %w", err)
	}
	return nil
}

var rebootPoweroff = func() error {
	if err := raiseSysBoot(); err != nil {
		return err
	}
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
