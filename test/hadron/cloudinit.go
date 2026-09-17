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
// kairos.io/docs/examples/k3s-stages documents a `stages: 'provider-kairos.bootstrap.after.
// k3s-ready':` hook for exactly this purpose (touching a marker file once k3s is ready), and an
// earlier version of this function rendered it. A live run against the pinned Hadron artifact
// (docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md, 2026-09-12) found that hook
// never fires on this Kairos version: k3s itself came up genuinely healthy -- CoreDNS, Traefik,
// and metrics-server all Ready within about 40 seconds of the k3s.service starting, confirmed via
// `systemctl status k3s` and its journal -- while the marker file never appeared in ten minutes of
// polling. Rather than carry a cloud-config feature proven not to fire on the target version,
// this renders neither the stage nor the marker; callers poll `sudo k3s kubectl get nodes`
// directly instead, which the same run proved is a real, working signal.
//
// This is deliberately not the default behavior of NewSafeMachine: the adapter's own contract is
// generic VM lifecycle, not an opinion about what any given guest OS should do on first boot.
// Callers that want this wrap it to match Config.CloudConfig's func(Credentials) string shape,
// e.g. `func(c Credentials) string { return KairosAutoInstallCloudConfig(c, "/dev/vda") }`.
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

// disableHostFirewallRunCmd is a plain cloud-init `runcmd:` (not a Kairos-specific stage like the
// k3s-ready hook this project already found silently does not fire -- runcmd is a standard
// cloud-init primitive, and this same cloud-config's own k3s:/k3s-agent:/users: directives are
// already live-proven to apply to the installed system in this test, not only the live installer)
// that best-effort disables firewalld and ufw, matching k3s's own documented installation
// requirement: "It is recommended to turn off firewalld" / "turn off ufw"
// (https://docs.k3s.io/installation/requirements). Whether either ships active on this Kairos
// build is unconfirmed either way; each command is a no-op (`|| true`) if that service is absent,
// so this is safe to always render.
const disableHostFirewallRunCmd = `runcmd:
- systemctl disable --now firewalld || true
- systemctl disable --now ufw || true
`

// KairosAutoInstallServerCloudConfig is KairosAutoInstallCloudConfig plus a k3s `--tls-san` for
// tlsSAN, a second address (the ClusterLink static address VM-2's two-node join assigns after
// boot) k3s's own self-signed server certificate must also validate for.
//
// The ordering problem this sidesteps: the raw ClusterLink segment (network.go) has no DHCP, and
// this project already found once that an apparently-documented Kairos cloud-config networking
// feature can silently not fire (the k3s-ready stage below); rather than risk repeating that with
// a declarative network-config stanza, the join test assigns the static address itself via a
// plain `ip addr add` over SSH after boot. But k3s starts automatically during that same first
// boot, before any such SSH command can run, and bakes its certificate's SANs in at that moment.
// Declaring the address as a `--tls-san` in advance -- which needs no interface to actually carry
// that address yet, only to be listed as an acceptable value -- avoids depending on which of the
// two racing steps (k3s startup, the SSH-driven address assignment) actually finishes first.
func KairosAutoInstallServerCloudConfig(creds Credentials, device, tlsSAN string) string {
	return fmt.Sprintf(`#cloud-config
install:
  device: %q
  reboot: true
  auto: true
k3s:
  enabled: true
  args:
  - --tls-san=%s
users:
- name: %s
  passwd: %s
  groups:
  - admin
`+disableHostFirewallRunCmd, device, tlsSAN, creds.User, creds.Pass)
}

// KairosAutoInstallAgentCloudConfig renders a Kairos cloud-config for a k3s agent joining an
// already-running server at serverURL (e.g. "https://192.168.100.1:6443") using token -- the real
// value read from that server's own /var/lib/rancher/k3s/server/node-token after it comes up, not
// a placeholder. The `k3s-agent` stanza and its `env` map (K3S_URL/K3S_TOKEN) are confirmed
// against kairos.io/docs/examples/multi-node, not guessed -- unlike the k3s-ready stage this
// project already found does not fire on this Kairos version, this project has no live evidence
// yet that k3s-agent's own wiring works either; that is exactly what this join test proves.
func KairosAutoInstallAgentCloudConfig(creds Credentials, device, serverURL, token string) string {
	return fmt.Sprintf(`#cloud-config
install:
  device: %q
  reboot: true
  auto: true
k3s-agent:
  enabled: true
  env:
    K3S_URL: %s
    K3S_TOKEN: %s
users:
- name: %s
  passwd: %s
  groups:
  - admin
`+disableHostFirewallRunCmd, device, serverURL, token, creds.User, creds.Pass)
}

// MinimalSSHCloudConfig renders a cloud-config that only creates a login using creds -- no
// install stanza, no k3s. For guests that only ever need to be SSH-reachable in their live
// installer environment and never install to disk, such as a networking-only test: attaching no
// CloudConfig at all leaves the guest with no account matching Config's generated Credentials,
// since nothing else creates one. Found live (2026-09-12): a guest booted with no CloudConfig
// failed every SSH attempt with "unable to authenticate," not a connectivity problem -- the
// generated password was configured on the host side (SSH's own client config) but never
// delivered to anything inside the guest that could accept it.
func MinimalSSHCloudConfig(creds Credentials) string {
	return fmt.Sprintf(`#cloud-config
users:
- name: %s
  passwd: %s
  groups:
  - admin
`, creds.User, creds.Pass)
}
