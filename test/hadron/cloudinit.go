//go:build hadron
// +build hadron

/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hadron

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// buildNoCloudISO writes a cloud-init NoCloud datasource ISO (volume label "cidata", the label
// cloud-init's NoCloud source scans for) containing userData, suitable for
// types.MachineConfig.DataSource. Kairos consumes this as its cloud-config on first boot.
//
// Shells out to genisoimage/mkisofs rather than vendoring an ISO9660 writer: it is the same tool
// cloud-init's own cloud-localds helper wraps, is a single well-known package
// (`apt-get install genisoimage`), and this project already shells out to standard tools rather
// than reimplementing OS-level primitives in Go (see hack/*.sh).
func buildNoCloudISO(dir, userData string) (string, error) {
	metaDataPath := filepath.Join(dir, "meta-data")
	userDataPath := filepath.Join(dir, "user-data")
	isoPath := filepath.Join(dir, "seed.iso")

	if err := os.WriteFile(metaDataPath, []byte("instance-id: hadron\nlocal-hostname: hadron\n"), 0600); err != nil {
		return "", fmt.Errorf("writing meta-data: %w", err)
	}
	if err := os.WriteFile(userDataPath, []byte(userData), 0600); err != nil {
		return "", fmt.Errorf("writing user-data: %w", err)
	}

	bin, err := exec.LookPath("genisoimage")
	if err != nil {
		if bin, err = exec.LookPath("mkisofs"); err != nil {
			return "", fmt.Errorf("neither genisoimage nor mkisofs found on PATH: %w", err)
		}
	}

	cmd := exec.Command(bin, "-output", isoPath, "-volid", "cidata", "-joliet", "-rock", userDataPath, metaDataPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building cloud-init seed ISO: %w: %s", err, out)
	}
	return isoPath, nil
}

// KairosAutoInstallCloudConfig renders a minimal Kairos cloud-config that performs an unattended
// install to device, enables the bundled k3s provider, and creates a login using creds -- the
// fresh, per-run credential NewSafeMachine already generated, never a static default.
//
// device must name the actual disk device Kairos will see inside the guest, not a generic
// example. PEG attaches the machine's disk(s) as virtio-blk-pci (see qemu.go's genDrives), which
// Linux enumerates under the virtio-blk naming scheme (/dev/vda, /dev/vdb, ...), not the
// SCSI/SATA naming (/dev/sda) that generic Kairos documentation examples use -- passing the wrong
// scheme would make the install stanza silently target a device that does not exist. For a
// PEG-booted single-disk machine this is "/dev/vda".
//
// This is deliberately not the default behavior of NewSafeMachine: the adapter's own contract is
// generic VM lifecycle, not an opinion about what any given guest OS should do on first boot.
// Callers that want this pass the result as Config.CloudConfig.
func KairosAutoInstallCloudConfig(creds Credentials, device string) string {
	return fmt.Sprintf(`#cloud-config
install:
  device: %q
  reboot: true
  auto: true
k3s:
  enabled: true
users:
- name: %s
  passwd: %s
  groups:
  - admin
`, device, creds.User, creds.Pass)
}
