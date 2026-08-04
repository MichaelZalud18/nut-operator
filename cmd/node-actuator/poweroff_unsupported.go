//go:build !linux

package main

import "fmt"

var rebootPoweroff = func() error {
	return fmt.Errorf("reboot-syscall poweroff is supported only on linux")
}
