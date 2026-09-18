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

// VM-2's own remaining open work: "the actual two-node topology" -- a real k3s server and a real
// k3s agent joining it, proven over the raw ClusterLink segment TestHadronClusterLinkConnectivity
// already proved carries traffic. That test deliberately never installed or ran k3s at all,
// isolating the socket netdev link itself from everything else; this one is the "everything else."
//
// The raw link has no DHCP (network.go's own doc comment), and this project's one attempt so far
// at trusting an apparently-documented Kairos cloud-config feature without live evidence (the
// k3s-ready stage, cloudinit.go's own history) silently did not fire. Rather than risk the same
// mistake with a declarative network-config stanza with "no clear, reliable" documented shape
// (docs/contributing/audits/vm-test-research-2026-09-15.md's own VM-2 note), each guest's static
// address on the ClusterLink segment is assigned imperatively over SSH after boot, using the same
// evidence-based interface-discovery-by-MAC approach TestHadronClusterLinkConnectivity's own
// linkLocalAddress already established (guest kernels always assign a link-local IPv6 address to
// any interface with zero configuration; the interface's own name for a second virtio-net-pci
// device is not something this project controls or has otherwise verified).
//
// Sequencing is real, not a simplification: the agent's own cloud-config needs the server's
// node-token, which the server does not generate until its own first boot completes, so the
// server must be fully installed and Ready before the agent's cloud-config can even be rendered,
// let alone before that guest is created. There is no useful parallel version of this boot.
//
// Unverified against a real guest as of this writing, the same status every milestone in this
// session started at.
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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

// clusterJoinServerIP and clusterJoinAgentIP are this test's own static addresses on the private
// ClusterLink segment -- never reachable from anywhere but the two guests on this one link
// (network.go's own doc comment), so any private range is safe; a /30 is the textbook size for a
// point-to-point pair (exactly two usable host addresses). Chosen by this test, not discovered:
// unlike the interface name, nothing about which address to use depends on anything the guest
// kernel or Kairos decides.
const (
	clusterJoinServerIP  = "192.168.100.1"
	clusterJoinAgentIP   = "192.168.100.2"
	clusterJoinSubnetLen = "30"
)

// TestHadronClusterJoin boots a real k3s server, reads its own generated node-token, boots a real
// k3s agent configured to join it over the ClusterLink segment, and confirms two genuinely
// distinct Ready nodes from outside both guests entirely.
func TestHadronClusterJoin(t *testing.T) {
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
	agentMAC, err := RandomClusterMAC()
	if err != nil {
		t.Fatalf("RandomClusterMAC (agent): %v", err)
	}
	serverNIC, err := link.Server(serverMAC)
	if err != nil {
		t.Fatalf("link.Server: %v", err)
	}
	agentNIC, err := link.Client(agentMAC)
	if err != nil {
		t.Fatalf("link.Client: %v", err)
	}

	t.Log("booting the k3s server guest")
	_, serverCreds := bootClusterJoinGuest(ctx, t, "server", Config{
		Memory:      "4096",
		CPUs:        "2",
		ISO:         hadronISOURL,
		ISOChecksum: hadronISOChecksum,
		ClusterNIC:  &serverNIC,
		CloudConfig: func(c Credentials) string {
			return KairosAutoInstallServerCloudConfig(c, "/dev/vda", clusterJoinServerIP)
		},
		ForwardKubeAPI: true,
	})

	t.Log("waiting for SSH on the server guest")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "server SSH", func(ctx context.Context) error {
		_, err := guestCommand(ctx, serverCreds, "true")
		return err
	}, nil)

	t.Log("waiting for a Ready k3s node on the server")
	waitForWithDiagnostics(t, ctx, 10*time.Minute, "server k3s readiness", func(ctx context.Context) error {
		out, err := guestCommand(ctx, serverCreds, "sudo k3s kubectl get nodes --request-timeout=20s -o json")
		if err != nil {
			return err
		}
		if !hasReadyNode(out) {
			return fmt.Errorf("no Ready node yet:\n%s", out)
		}
		return nil
	}, nil)

	t.Log("assigning the server's static ClusterLink address")
	serverIface := assignClusterLinkAddress(ctx, t, serverCreds, serverMAC, clusterJoinServerIP)
	t.Logf("server ClusterLink interface: %s (%s/%s)", serverIface, clusterJoinServerIP, clusterJoinSubnetLen)

	t.Log("reading the server's own generated node-token")
	token, err := guestCommand(ctx, serverCreds, "sudo cat /var/lib/rancher/k3s/server/node-token")
	if err != nil {
		t.Fatalf("reading node-token: %v", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		t.Fatalf("node-token was empty")
	}

	t.Log("booting the k3s agent guest")
	_, agentCreds := bootClusterJoinGuest(ctx, t, "agent", Config{
		Memory:      "2048",
		CPUs:        "2",
		ISO:         hadronISOURL,
		ISOChecksum: hadronISOChecksum,
		ClusterNIC:  &agentNIC,
		CloudConfig: func(c Credentials) string {
			return KairosAutoInstallAgentCloudConfig(c, "/dev/vda",
				"https://"+clusterJoinServerIP+":6443", token)
		},
	})

	t.Log("waiting for SSH on the agent guest's live installer environment")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "agent SSH", func(ctx context.Context) error {
		_, err := guestCommand(ctx, agentCreds, "true")
		return err
	}, nil)

	// The agent's cloud-config has the same install:/reboot: stanza the server's does; SSH above
	// only proves the live installer environment answers, not that the install-and-reboot cycle
	// has actually happened yet. Assigning the static address too early would target the live
	// installer's own transient network state, which does not carry over to the rebooted,
	// installed system at all -- the same class of "proved the wrong environment" mistake this
	// package has hit before. Unlike the server, this cannot wait on k3s-agent itself reaching
	// Ready (the whole reason it isn't Ready yet is the address this step is about to assign), so
	// it waits on the k3s-agent systemd unit merely existing instead -- `systemctl status` prints
	// a distinct "could not be found" only when the unit is entirely absent, true in the live
	// installer environment and false the moment Kairos's own install has provisioned it,
	// regardless of whether the service has managed to start yet.
	t.Log("waiting for the agent guest to reach its installed, rebooted system")
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "agent installed system", func(ctx context.Context) error {
		// "; true" forces exit 0 regardless of systemctl's own exit status (non-zero for a unit
		// that exists but is not active -- expected here, since it is retrying a connection to an
		// address that does not exist until the next step) so guestCommand's own err reflects
		// only a real SSH-level failure (e.g. mid-reboot, not yet listening), never this command's
		// own exit code. The determination is purely textual: systemctl prints a distinct "could
		// not be found" only when the unit is entirely absent.
		out, err := guestCommand(ctx, agentCreds, "sudo systemctl status k3s-agent 2>&1; true")
		if err != nil {
			return err
		}
		if strings.Contains(out, "could not be found") {
			return fmt.Errorf("k3s-agent unit does not exist yet (still the live installer environment):\n%s", out)
		}
		return nil
	}, nil)

	t.Log("assigning the agent's static ClusterLink address")
	agentIface := assignClusterLinkAddress(ctx, t, agentCreds, agentMAC, clusterJoinAgentIP)
	t.Logf("agent ClusterLink interface: %s (%s/%s)", agentIface, clusterJoinAgentIP, clusterJoinSubnetLen)

	// The first three live runs (2026-09-17/18) showed the agent's k3s-agent service never
	// reaching the server. The first two showed the agent's own ens5 vanished entirely from
	// `ip -4 addr show` by the first later diagnostic snapshot -- not merely link-down, gone from
	// the listing altogether -- while the server's own ens5 (same ClusterLink mechanism, only the
	// Server()/Client() role differs -- network.go's own socket netdev listen/connect split)
	// persisted unchanged across every sample. The third run's own attempt at catching this
	// (waitForWithDiagnostics) proved the address present, moved on immediately, and the interface
	// was still gone about a minute later -- waitForWithDiagnostics returns on the *first* success,
	// so it only ever proved a single instant, not that the address survives. This checks
	// continuously across a fixed window instead, failing the moment it's ever observed missing,
	// with a kernel-log capture at exactly that point rather than waiting out the full final
	// wait's generic diagnostics.
	t.Log("confirming the agent's ClusterLink address survives the two minutes after assignment")
	confirmAgentClusterLinkAddressSurvives(ctx, t, agentCreds, agentIface, clusterJoinAgentIP, 2*time.Minute)

	t.Log("fetching kubeconfig and waiting for two distinct Ready nodes from outside both guests")
	var nodeNames []string
	waitForWithDiagnostics(t, ctx, 5*time.Minute, "two Ready nodes", func(ctx context.Context) error {
		kubeconfig, err := Kubeconfig(ctx, serverCreds)
		if err != nil {
			return fmt.Errorf("fetching kubeconfig: %w", err)
		}
		restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
		if err != nil {
			return fmt.Errorf("parsing kubeconfig: %w", err)
		}
		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return fmt.Errorf("building client: %w", err)
		}
		nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing nodes: %w", err)
		}
		if len(nodes.Items) != 2 {
			return fmt.Errorf("expected exactly two nodes, got %d", len(nodes.Items))
		}
		nodeNames = nodeNames[:0]
		for _, node := range nodes.Items {
			if !nodeReadyCondition(node) {
				return fmt.Errorf("node %q is not Ready yet", node.Name)
			}
			nodeNames = append(nodeNames, node.Name)
		}
		if nodeNames[0] == nodeNames[1] {
			return fmt.Errorf("both nodes report the same name %q -- not two distinct nodes", nodeNames[0])
		}
		return nil
	}, func(ctx context.Context) {
		// The first live run (2026-09-17) showed firewalld/ufw already "inactive" on both guests
		// (disabling them was a no-op, ruling that theory out with direct evidence) and a raw curl
		// to the server timing out at exactly the 5s budget (http_code=000), consistent with either
		// nothing listening on the far end or the underlying link itself not passing traffic once
		// k3s/flannel are running -- but the first attempt at checking "is anything listening on
		// 6443" used `ss` piped through a `grep` that can exit non-zero and swallow output on zero
		// matches, and this minimal Kairos image's `ss` was never actually confirmed present. Every
		// command below appends "; true" so a real answer is captured regardless of any individual
		// tool's own exit code, and falls back across tools rather than assuming one exists.
		out, err := guestCommand(ctx, agentCreds, "sudo systemctl status k3s-agent --no-pager -l 2>&1 | head -30; "+
			"echo ---k3s-agent-journal---; sudo journalctl -u k3s-agent --no-pager -n 40 2>&1; "+
			"echo ---agent-firewall---; sudo systemctl is-active firewalld ufw 2>&1; "+
			"echo ---agent-curl-server-6443---; "+
			"curl -sk --max-time 5 -o /dev/null -w 'http_code=%{http_code} time_total=%{time_total}\\n' "+
			"https://"+clusterJoinServerIP+":6443/cacerts 2>&1; "+
			"echo ---agent-ping-server---; sudo ping -c2 -W2 "+clusterJoinServerIP+" 2>&1; "+
			"echo ---agent-iface---; ip -4 addr show 2>&1; ip route show 2>&1; "+
			"true")
		if err != nil {
			t.Logf("diagnostic snapshot command itself failed: %v\noutput so far:\n%s", err, out)
		} else {
			t.Logf("agent diagnostic snapshot:\n%s", out)
		}
		serverOut, err := guestCommand(ctx, serverCreds, "echo ---server-firewall---; sudo systemctl is-active firewalld ufw 2>&1; "+
			"echo ---server-k3s-status---; sudo systemctl is-active k3s 2>&1; "+
			"echo ---server-k3s-journal---; sudo journalctl -u k3s --no-pager -n 20 2>&1; "+
			"echo ---server-listening---; "+
			"(sudo ss -tlnp 2>&1 || true; sudo netstat -tlnp 2>&1 || true; sudo cat /proc/net/tcp 2>&1 || true) | head -40; "+
			"echo ---server-iptables---; sudo iptables -S 2>&1 | head -30; "+
			"echo ---server-iface---; ip -4 addr show 2>&1; "+
			"true")
		if err != nil {
			t.Logf("server diagnostic snapshot command itself failed: %v\noutput so far:\n%s", err, serverOut)
			return
		}
		t.Logf("server diagnostic snapshot:\n%s", serverOut)
	})
	t.Logf("confirmed two distinct Ready nodes: %v", nodeNames)
}

// bootClusterJoinGuest boots one guest with the given Config, registering the same
// failure-preserving/success-cleaning teardown every other Hadron smoke test uses (leave state
// behind for the workflow's own failure-diagnostics step on failure; clean up only on success).
func bootClusterJoinGuest(ctx context.Context, t *testing.T, name string, cfg Config) (types.Machine, Credentials) {
	t.Helper()
	m, creds, err := NewSafeMachineContext(ctx, cfg)
	if err != nil {
		t.Fatalf("NewSafeMachine (%s): %v", name, err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("leaving %s machine state at %s for inspection (stdout/stderr hold the guest console)", name, m.Config().StateDir)
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
	return m, creds
}

// confirmAgentClusterLinkAddressSurvives polls dev's address every 3s across the whole of window,
// failing once a bad observation (address missing, or the SSH check itself erroring) repeats on
// the very next poll rather than the first time it merely reappears -- unlike
// waitForWithDiagnostics, which returns on the check function's *first* success and so can only
// prove the address existed at one instant, not that it survives. Live evidence (2026-09-17/18):
// two runs showed the agent's ClusterLink address present immediately after assignment and gone
// again within about a minute; a third run's SSH check itself started erroring with "context
// deadline exceeded" at almost exactly the same point instead, which the first version of this
// function (added for the first two runs) treated as an immediate hard failure with no
// diagnostics at all -- if that's the same underlying event surfacing differently (the guest
// itself briefly unresponsive, not only its second NIC), failing on one isolated SSH blip would
// misdiagnose transient CI noise as the bug and never capture anything. Tolerating exactly one bad
// poll (while still logging and attempting diagnostics on it) separates the two before deciding.
func confirmAgentClusterLinkAddressSurvives(ctx context.Context, t *testing.T, creds Credentials, dev, ip string, window time.Duration) {
	t.Helper()
	deadline := time.Now().Add(window)
	const maxConsecutiveBad = 2
	consecutiveBad := 0
	for {
		out, err := guestCommand(ctx, creds, fmt.Sprintf("ip -4 addr show dev %s 2>&1", dev))
		var badReason string
		switch {
		case err != nil:
			badReason = fmt.Sprintf("checking address: %v", err)
		case !strings.Contains(out, ip):
			badReason = fmt.Sprintf("address %s no longer present on %s:\n%s", ip, dev, out)
		}
		if badReason != "" {
			consecutiveBad++
			t.Logf("agent ClusterLink address check failed (%d/%d consecutive): %s", consecutiveBad, maxConsecutiveBad, badReason)
			diagOut, diagErr := guestCommand(ctx, creds, "echo ---dmesg-tail---; sudo dmesg 2>&1 | tail -100; "+
				"echo ---kernel-journal---; sudo journalctl -k --no-pager -n 100 2>&1; "+
				"echo ---recent-journal---; sudo journalctl --no-pager -n 150 2>&1; "+
				"echo ---ip-link-all---; ip link show 2>&1; "+
				"echo ---uptime---; uptime 2>&1; "+
				"true")
			if diagErr != nil {
				t.Logf("agent diagnostic snapshot itself failed: %v\noutput so far:\n%s", diagErr, diagOut)
			} else {
				t.Logf("agent diagnostic snapshot at consecutive-bad=%d:\n%s", consecutiveBad, diagOut)
			}
			if consecutiveBad >= maxConsecutiveBad {
				t.Fatalf("agent ClusterLink address %s did not survive on %s: %s", ip, dev, badReason)
			}
		} else {
			consecutiveBad = 0
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context canceled confirming agent ClusterLink address survives: %v", ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

// nodeReadyCondition matches the actual Ready condition, not the substring also present in
// NotReady. Identical to hasReadyNode's own check, but operating on a single already-fetched
// corev1.Node rather than re-parsing a JSON blob for exactly one node -- this test's own wait
// needs to inspect two nodes together (count, names, and each one's own readiness), which
// hasReadyNode's single-node-JSON shape does not fit.
func nodeReadyCondition(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// assignClusterLinkAddress finds the interface owning mac (the same evidence-based
// discover-by-MAC approach TestHadronClusterLinkConnectivity's own linkLocalAddress uses, for the
// same reason: interface naming for a second virtio-net-pci device is not something this project
// controls or has verified) and assigns it ip/clusterJoinSubnetLen, bringing the interface up.
// Fails the test on any step's error rather than continuing with an unaddressed link.
func assignClusterLinkAddress(ctx context.Context, t *testing.T, creds Credentials, mac, ip string) (iface string) {
	t.Helper()
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "ClusterLink interface for "+mac, func(ctx context.Context) error {
		out, err := guestCommand(ctx, creds,
			fmt.Sprintf(`ip -o link show | grep -i %q | awk '{print $2}' | tr -d :`, mac))
		if err != nil {
			return err
		}
		found := strings.TrimSpace(out)
		if found == "" {
			return fmt.Errorf("no interface found for MAC %s yet", mac)
		}
		iface = found
		return nil
	}, nil)

	out, err := guestCommand(ctx, creds, fmt.Sprintf(
		"sudo ip addr add %s/%s dev %s && sudo ip link set %s up",
		ip, clusterJoinSubnetLen, iface, iface))
	if err != nil {
		t.Fatalf("assigning %s/%s to %s: %v\n%s", ip, clusterJoinSubnetLen, iface, err, out)
	}
	return iface
}
