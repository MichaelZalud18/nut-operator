//go:build (hadron || talos) && !linux

package vmprocess

import "fmt"

func captureProcess(_ int, _ string) (processHandle, error) {
	return nil, fmt.Errorf("verified VM cleanup requires Linux pidfds")
}
