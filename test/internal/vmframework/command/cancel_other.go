//go:build !linux

package command

import (
	"fmt"
	"os/exec"
)

func configureCancellation(_ *exec.Cmd) error {
	return fmt.Errorf("VM command process-group cleanup requires Linux")
}
