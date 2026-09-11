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

// Separate build tag from the rest of the package (`hadron_smoke`, not `hadron`): this needs real
// KVM, downloads a ~400MB artifact, and performs a full unattended install and reboot, taking
// minutes rather than seconds. It must never run in the fast, KVM-free component-test job that
// `-tags hadron` runs on every push; only hadron-vm-boot-smoke.yml's workflow_dispatch job builds
// with this tag.
package hadron

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Pinned in docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md: cross-checked
// against both GitHub's asset digest and the release's own .sha256 sidecar. "Hadron" here is a
// real upstream Kairos base distro (kairos-io/hadron), not a name coined for this project.
const (
	hadronISOURL      = "https://github.com/kairos-io/kairos/releases/download/v4.3.0/kairos-hadron-v0.5.1-standard-amd64-generic-v4.3.0-k3sv1.36.4%2Bk3s1.iso"
	hadronISOChecksum = "1488c390e91128e6d8e1f6c2258c3e32881b3ff44de17a700134e2f671f3575c" // pragma: allowlist secret -- a public release checksum, not a credential
)

// TestHadronSingleNodeBoot is VM-2's first live boot proof: download the pinned artifact, run an
// unattended install through the cloud-config in cloudinit.go, and confirm k3s actually comes up.
// This is deliberately the boundary VM-1's own probe stopped short of
// (docs/contributing/audits/hadron-vm-1-feasibility-2026-09-11.md) -- proving the runner *can*
// host a KVM-accelerated guest said nothing about whether a real OS install on top of it works.
//
// Not the two-node harness: this is one disposable guest, torn down at the end of the test. The
// topology, second node, and kubeconfig wiring remain VM-2's open work.
//
// Timing budget, kept deliberately under the workflow's own limits with margin: NewSafeMachine
// caps download+construction at 10 minutes internally; the two waitFor calls below add up to 12
// more; worst case ~22 minutes against the workflow's `go test -timeout=28m` and its 29-minute
// step timeout. `go test -timeout` panics in a watchdog goroutine, not this test's own, so it
// does not run t.Cleanup -- staying comfortably under it in the ordinary case is what lets the
// controlled failure path below (which does clean up) run instead of an uncontrolled kill.
// Widening either wait here without checking this arithmetic reopens that gap.
func TestHadronSingleNodeBoot(t *testing.T) {
	m, creds, err := NewSafeMachine(Config{
		Memory:      "4096",
		CPUs:        "2",
		ISO:         hadronISOURL,
		ISOChecksum: hadronISOChecksum,
		CloudConfig: func(c Credentials) string { return KairosAutoInstallCloudConfig(c, "/dev/vda") },
	})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	t.Cleanup(func() {
		// A first real run of this test is expected to fail at least once, the same way VM-1's
		// probe took four iterations to get right -- and PEG's state dir (stdout/stderr, which
		// capture the guest's own -nographic console since PEG sets no separate -serial target)
		// is the only record of why. Deleting it unconditionally in Cleanup, as a plain
		// SafeTeardown call would, destroys that evidence at exactly the moment it matters most.
		// Only clean up on success; on failure, stop the process (so nothing leaks) but leave the
		// state behind for the workflow's own failure-diagnostics step to read.
		if t.Failed() {
			t.Logf("leaving machine state at %s for inspection (stdout/stderr hold the guest console)", m.Config().StateDir)
			if err := m.Stop(); err != nil {
				t.Logf("stop: %v", err)
			}
			return
		}
		if err := SafeTeardown(m, 30*time.Second); err != nil {
			t.Logf("teardown: %v", err)
		}
	})

	if _, err := m.Create(context.Background()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Logf("waiting for SSH on 127.0.0.1:%s as %s", creds.Port, creds.User)
	waitFor(t, 6*time.Minute, "SSH", func() error {
		_, err := m.Command("true")
		return err
	})

	// The install stanza reboots into the freshly installed disk; k3s only starts on that second
	// boot, which is why this is a second, independent wait rather than assumed to follow
	// immediately once SSH answers on the live/installer environment.
	t.Log("waiting for the provider-kairos k3s-ready stage to run")
	waitFor(t, 6*time.Minute, "k3s readiness", func() error {
		_, err := m.Command("test -f /tmp/k3s-ready")
		return err
	})

	out, err := m.Command("sudo k3s kubectl get nodes --no-headers")
	if err != nil {
		t.Fatalf("kubectl get nodes: %v (output: %s)", err, out)
	}
	t.Logf("k3s nodes:\n%s", out)
	if !strings.Contains(out, "Ready") {
		t.Fatalf("no Ready node in kubectl output:\n%s", out)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = check(); lastErr == nil {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s: %v", timeout, what, lastErr)
}
