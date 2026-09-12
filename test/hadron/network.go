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
	"crypto/rand"
	"fmt"
	"net"
	"strconv"

	"github.com/phayes/freeport"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

// ClusterLink is a private, host-only virtio-net segment between exactly two Hadron guests, built
// from a raw QEMU socket netdev bound to 127.0.0.1 -- not a bridge or tap device, so it needs no
// elevated privileges and nothing outside this host can reach it.
//
// This is the wiring VM-2's two-node topology needs that PEG provides no equivalent for: PEG's
// own default networking (already replaced for the management NIC -- see NewSafeMachine's
// DisableDefaultNetworking) gives each guest its own isolated user-mode NAT stack with no path to
// any other guest, host-only or otherwise. Two Hadron guests booted independently today cannot
// reach each other at all.
//
// A raw L2 segment carries no DHCP server of its own. ClusterNIC only wires the link itself; each
// guest still needs its own address on it configured some other way (a cloud-config
// network-config stanza, most likely) -- that wiring is not built yet.
type ClusterLink struct {
	port int
}

// NewClusterLink allocates the loopback port both sides of the pair need before either guest is
// constructed. Like NewSafeMachine's own SSH port, this is a sampled free port, not a held
// reservation -- the same TOCTOU caveat documented there applies here too, narrower in practice
// only because the window between sampling and actually calling Create on the listening side is
// short.
func NewClusterLink() (ClusterLink, error) {
	port, err := freeport.GetFreePort()
	if err != nil {
		return ClusterLink{}, fmt.Errorf("allocating cluster link port: %w", err)
	}
	return ClusterLink{port: port}, nil
}

// Server returns this guest's side of the link as the listener. Exactly one side of a pair must
// be Server; the other must be Client on the same Link -- two Servers or two Clients never
// connect, and PEG's own Create() would just report a normal-looking success for a guest with a
// NIC attached to nothing.
func (l ClusterLink) Server(mac string) (ClusterNIC, error) {
	return newClusterNIC(l, true, mac)
}

// Client returns this guest's side of the link as the connector, dialing the Server side.
func (l ClusterLink) Client(mac string) (ClusterNIC, error) {
	return newClusterNIC(l, false, mac)
}

// ClusterNIC is one guest's end of a ClusterLink, ready to attach via Config.ClusterNIC.
type ClusterNIC struct {
	args []string
}

func newClusterNIC(l ClusterLink, server bool, mac string) (ClusterNIC, error) {
	if l.port == 0 {
		return ClusterNIC{}, fmt.Errorf("cluster link has no port; construct it with NewClusterLink")
	}
	if _, err := net.ParseMAC(mac); err != nil {
		return ClusterNIC{}, fmt.Errorf("cluster NIC MAC address: %w", err)
	}
	endpoint := "listen=127.0.0.1:" + strconv.Itoa(l.port)
	if !server {
		endpoint = "connect=127.0.0.1:" + strconv.Itoa(l.port)
	}
	return ClusterNIC{args: []string{
		"-netdev", "socket,id=cluster0," + endpoint,
		"-device", "virtio-net-pci,netdev=cluster0,mac=" + mac,
	}}, nil
}

// option adapts this NIC's raw arguments to a types.MachineOption, the same extension point
// withArgs already uses for the management NIC and -enable-kvm.
func (n ClusterNIC) option() types.MachineOption {
	return withArgs(n.args...)
}

// RandomClusterMAC returns a fresh, locally-administered MAC address (QEMU's own 52:54:00 OUI
// prefix) suitable for a ClusterNIC. The two peers on one link only need to differ from each
// other, not be globally unique -- the segment is not reachable from anywhere else -- but this
// generates a fresh value per call anyway, consistent with never reusing a static value anywhere
// in this package (see Credentials).
func RandomClusterMAC() (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating cluster MAC: %w", err)
	}
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", suffix[0], suffix[1], suffix[2]), nil
}
