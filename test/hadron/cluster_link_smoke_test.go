//go:build hadron_smoke
// +build hadron_smoke

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
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spectrocloud/peg/pkg/machine/types"
	"golang.org/x/sync/errgroup"
)

// TestHadronClusterLinkConnectivity proves the raw QEMU socket netdev ClusterLink/ClusterNIC
// (network.go) actually creates a working L2 segment between two independently booted guests --
// the two-node topology's real precondition, not yet exercised against real guests.
//
// Deliberately isolated from install/k3s/addressing: guest static addressing on this link has no
// reliable Kairos cloud-config mechanism yet (docs/tasks.md's own note, after finding one
// apparently-documented Kairos feature that silently did not fire). This test never runs the
// unattended install and never needs one -- it uses IPv6 link-local addresses instead, which the
// kernel assigns to any interface with zero configuration (RFC 4862 SLAAC), a guarantee
// independent of Kairos, cloud-init, or anything this project controls. Booting only the live
// installer environment also makes this much faster than the single-guest smoke test's full
// install-and-reboot cycle, and isolates exactly the one new, risky mechanism -- the socket
// netdev itself -- from everything else already proven there.
func TestHadronClusterLinkConnectivity(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	link, err := NewClusterLink()
	if err != nil {
		t.Fatalf("NewClusterLink: %v", err)
	}
	serverMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatalf("RandomClusterMAC (server): %v", err)
	}
	clientMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatalf("RandomClusterMAC (client): %v", err)
	}
	serverNIC, err := link.Server(serverMAC)
	if err != nil {
		t.Fatalf("link.Server: %v", err)
	}
	clientNIC, err := link.Client(clientMAC)
	if err != nil {
		t.Fatalf("link.Client: %v", err)
	}

	server := bootGuest(ctx, t, "server", &serverNIC)
	client := bootGuest(ctx, t, "client", &clientNIC)

	// errgroup, not two bare goroutines calling waitForWithDiagnostics directly: that helper
	// calls t.Fatalf, which the testing package requires to run only on the test's own
	// goroutine. Each goroutine here reports its own error back instead; t.Fatalf is called
	// once, from this function's own goroutine, after both finish.
	//
	// An explicit deadline here, not just the shared parent ctx: a first version of this test
	// let the SSH wait fall through to the overall test timeout with no bound of its own, so a
	// real failure (an SSH auth error, not a timeout at all) took the full ten-minute test budget
	// to surface instead of the few minutes SSH normally takes.
	t.Log("waiting for SSH on both guests")
	sshCtx, sshCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer sshCancel()
	group, groupCtx := errgroup.WithContext(sshCtx)
	for _, g := range []*bootedGuest{server, client} {
		group.Go(func() error {
			return pollGuest(groupCtx, 5*time.Second, time.Minute, func(ctx context.Context) error {
				_, err := guestCommand(ctx, g.creds, "true")
				return err
			}, nil)
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatalf("waiting for SSH on both guests: %v", err)
	}

	clientAddr, _ := linkLocalAddress(ctx, t, client.creds, clientMAC)
	t.Logf("client cluster link-local address: %s", clientAddr)
	_, serverIface := linkLocalAddress(ctx, t, server.creds, serverMAC)
	t.Logf("server cluster interface: %s", serverIface)

	// This environment's BusyBox ping applet cannot address IPv6 at all -- confirmed two ways
	// (its own usage text shows no -4/-6 selection, and a live run found no separate ping6 applet
	// compiled in either), after two straight fix attempts wrongly assumed otherwise. Rather than
	// guess a third ICMP mechanism, a diagnostic-only run inventoried the guest's actual toolset:
	// nc/ncat/telnet/wget/socat are all absent, but curl, openssl, and ssh are present as real
	// binaries (not busybox applets).
	//
	// curl gives a protocol-agnostic connectivity proof: hitting the peer's own sshd (already
	// guaranteed running on both guests, since the host itself depends on it for setup) over HTTP
	// gets back real application data from across the link, not just a local socket-layer event --
	// stronger evidence than any wording in curl's own log. --interface binds the outgoing socket
	// to the server's own cluster interface, the same source-scope association ping's -I flag
	// would have given a link-local IPv6 destination.
	//
	// A first version of this check asserted on curl's own verbose log instead ("Connected to"),
	// which a live run showed was the wrong string for this curl version (8.21.0 logs "Established
	// connection to") -- a cosmetic difference, since that same run's log already contained
	// "SSH-2.0-OpenSSH_10.3" read back from the peer, proof the link carried real bytes both ways.
	out, _ := guestCommand(ctx, server.creds, fmt.Sprintf(
		`curl -6 -v --interface %s --connect-timeout 5 "http://[%s]:22/" 2>&1`, serverIface, clientAddr))
	if !strings.Contains(out, "SSH-2.0-") {
		t.Fatalf("no SSH banner read back over the cluster link:\n%s", out)
	}
	t.Logf("cluster link connectivity confirmed via curl TCP connect:\n%s", out)
}

type bootedGuest struct {
	name  string
	m     types.Machine
	creds Credentials
}

func bootGuest(ctx context.Context, t *testing.T, name string, nic *ClusterNIC) *bootedGuest {
	t.Helper()
	m, creds, err := NewSafeMachineContext(ctx, Config{
		Memory:      "2048",
		CPUs:        "2",
		ISO:         hadronISOURL,
		ISOChecksum: hadronISOChecksum,
		ClusterNIC:  nic,
		CloudConfig: MinimalSSHCloudConfig,
	})
	if err != nil {
		t.Fatalf("NewSafeMachine (%s): %v", name, err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("leaving %s machine state at %s for inspection", name, m.Config().StateDir)
			if err := SafeStop(m, 30*time.Second); err != nil {
				t.Errorf("stop %s: %v", name, err)
			}
			return
		}
		if err := SafeStop(m, 30*time.Second); err != nil {
			t.Errorf("teardown %s: %v", name, err)
			return
		}
		if err := m.Clean(); err != nil {
			t.Errorf("removing stopped %s machine state: %v", name, err)
		}
	})
	if _, err := m.Create(ctx); err != nil {
		t.Fatalf("Create (%s): %v", name, err)
	}
	return &bootedGuest{name: name, m: m, creds: creds}
}

// linkLocalAddress asks the guest itself which interface owns mac and that interface's own
// kernel-assigned IPv6 link-local address, rather than predicting either. Interface naming for a
// second virtio-net-pci device is not something this project controls or has verified, and
// computing the address via RFC 4291's modified EUI-64 algorithm would duplicate logic the guest
// kernel already exposes correctly.
func linkLocalAddress(ctx context.Context, t *testing.T, creds Credentials, mac string) (address, iface string) {
	t.Helper()
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "link-local address for "+mac, func(ctx context.Context) error {
		out, err := guestCommand(ctx, creds, fmt.Sprintf(
			`dev=$(ip -o link show | grep -i %q | awk '{print $2}' | tr -d :) && `+
				`test -n "$dev" && echo "$dev" && ip -6 -o addr show dev "$dev" scope link | awk '{print $4}' | cut -d/ -f1`,
			mac))
		if err != nil {
			return err
		}
		fields := strings.Fields(out)
		if len(fields) < 2 {
			return fmt.Errorf("no link-local address yet:\n%s", out)
		}
		iface, address = fields[0], fields[1]
		return nil
	}, nil)
	return address, iface
}
