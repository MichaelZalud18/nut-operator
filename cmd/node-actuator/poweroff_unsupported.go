//go:build !linux

package main

import "fmt"

func verifySysBootAvailable() error {
	return fmt.Errorf("reboot-syscall poweroff is supported only on linux")
}

var rebootPoweroff = func() error {
	return fmt.Errorf("reboot-syscall poweroff is supported only on linux")
}
