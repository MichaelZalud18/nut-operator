//go:build linux

package main

import "syscall"

var rebootPoweroff = func() error {
	return syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
}
