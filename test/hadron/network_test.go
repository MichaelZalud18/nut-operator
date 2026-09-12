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
	"os"
	"strings"
	"testing"
)

func TestClusterLinkServerAndClientDialTheSamePort(t *testing.T) {
	link, err := NewClusterLink()
	if err != nil {
		t.Fatal(err)
	}
	serverMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatal(err)
	}
	clientMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatal(err)
	}
	server, err := link.Server(serverMAC)
	if err != nil {
		t.Fatal(err)
	}
	client, err := link.Client(clientMAC)
	if err != nil {
		t.Fatal(err)
	}

	serverArgs := strings.Join(server.args, " ")
	clientArgs := strings.Join(client.args, " ")

	if !strings.Contains(serverArgs, "listen=127.0.0.1:") {
		t.Errorf("server args = %q, want a listen= endpoint", serverArgs)
	}
	if !strings.Contains(clientArgs, "connect=127.0.0.1:") {
		t.Errorf("client args = %q, want a connect= endpoint", clientArgs)
	}
	if strings.Contains(serverArgs, "connect=") || strings.Contains(clientArgs, "listen=") {
		t.Errorf("roles crossed: server=%q client=%q", serverArgs, clientArgs)
	}

	// Both sides must name the exact same port -- that's the whole point of building both from
	// one Link -- and each must carry its own distinct MAC.
	portOf := func(args, key string) string {
		i := strings.Index(args, key)
		if i < 0 {
			t.Fatalf("missing %q in %q", key, args)
		}
		rest := args[i+len(key):]
		if end := strings.IndexAny(rest, " ,"); end >= 0 {
			rest = rest[:end]
		}
		return rest
	}
	serverPort := portOf(serverArgs, "127.0.0.1:")
	clientPort := portOf(clientArgs, "127.0.0.1:")
	if serverPort != clientPort {
		t.Errorf("server port %q != client port %q, want the same port on both sides", serverPort, clientPort)
	}
	if !strings.Contains(serverArgs, "mac="+serverMAC) {
		t.Errorf("server args missing its own MAC %q: %q", serverMAC, serverArgs)
	}
	if !strings.Contains(clientArgs, "mac="+clientMAC) {
		t.Errorf("client args missing its own MAC %q: %q", clientMAC, clientArgs)
	}
	if serverMAC == clientMAC {
		t.Error("server and client generated the same MAC twice in a row -- broken randomness, not just bad luck")
	}
}

func TestClusterNICRejectsInvalidMAC(t *testing.T) {
	link, err := NewClusterLink()
	if err != nil {
		t.Fatal(err)
	}
	for _, mac := range []string{"", "not-a-mac", "52:54:00:00", "zz:zz:zz:zz:zz:zz"} {
		if _, err := link.Server(mac); err == nil {
			t.Errorf("Server(%q) accepted an invalid MAC", mac)
		}
		if _, err := link.Client(mac); err == nil {
			t.Errorf("Client(%q) accepted an invalid MAC", mac)
		}
	}
}

func TestClusterNICRejectsUnconstructedLink(t *testing.T) {
	var zero ClusterLink
	mac, err := RandomClusterMAC()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zero.Server(mac); err == nil {
		t.Error("Server on a zero-value ClusterLink accepted, want an error")
	}
	if _, err := zero.Client(mac); err == nil {
		t.Error("Client on a zero-value ClusterLink accepted, want an error")
	}
}

func TestRandomClusterMACIsLocallyAdministeredAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		mac, err := RandomClusterMAC()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(mac, "52:54:00:") {
			t.Fatalf("MAC %q does not use the expected 52:54:00 OUI prefix", mac)
		}
		if seen[mac] {
			t.Fatalf("MAC %q generated twice in 50 draws", mac)
		}
		seen[mac] = true
	}
}

func TestClusterNICAttachesToMachineConfig(t *testing.T) {
	link, err := NewClusterLink()
	if err != nil {
		t.Fatal(err)
	}
	mac, err := RandomClusterMAC()
	if err != nil {
		t.Fatal(err)
	}
	nic, err := link.Server(mac)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := NewSafeMachine(Config{ClusterNIC: &nic})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	// Create() was never called, so there is no process to stop -- just the state directory
	// NewSafeMachine leaves behind for a caller to hand to Create, same as every other
	// NewSafeMachine test that never boots a real guest.
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })
	joined := strings.Join(m.Config().Args, " ")
	if !strings.Contains(joined, "mac="+mac) {
		t.Errorf("machine Args = %q, want the cluster NIC's MAC", joined)
	}
}
