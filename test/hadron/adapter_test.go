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
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spectrocloud/peg/pkg/machine/types"
)

// NewSafeMachine never calls Create(), so these run with no qemu binary, no /dev/kvm, and no
// KVM feasibility at all -- they prove the configuration this package builds (and, for a URL
// ISO, PEG's own eager download+checksum step), not that a guest actually boots. Booting is
// VM-1's probe (already answered on GitHub-hosted runners) and the eventual two-node harness.

func TestNewSafeMachineEnablesKVMAndDisablesDefaultNetworking(t *testing.T) {
	m, creds, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })

	cfg := m.Config()

	if !cfg.DisableDefaultNetworking {
		t.Error("expected DisableDefaultNetworking to be set, so PEG's own all-interfaces -nic is never added")
	}
	if cfg.Arch != "x86_64" {
		t.Errorf("Arch = %q, want x86_64 -- VM-1 only proved KVM feasibility on that architecture", cfg.Arch)
	}

	joined := strings.Join(cfg.Args, " ")
	if !strings.Contains(joined, "-enable-kvm") {
		t.Errorf("Args = %q, want it to contain -enable-kvm", joined)
	}
	wantNIC := "-nic user,hostfwd=tcp:127.0.0.1:" + creds.Port + "-:22"
	if !strings.Contains(joined, wantNIC) {
		t.Errorf("Args = %q, want it to contain a loopback-bound %q", joined, wantNIC)
	}
}

func TestNewSafeMachineGeneratesFreshCredentialsEachTime(t *testing.T) {
	m1, creds1, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatalf("NewSafeMachine (1): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m1.Config().StateDir) })

	m2, creds2, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatalf("NewSafeMachine (2): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m2.Config().StateDir) })

	if creds1.Pass == "" {
		t.Fatal("expected a non-empty generated password")
	}
	if creds1.Pass == creds2.Pass {
		t.Error("expected two calls to generate different passwords, got the same one twice")
	}
	if creds1.Port == creds2.Port {
		t.Error("expected two concurrent machines to get different forwarded ports, got the same one twice")
	}
	if creds1.User == "kairos" || creds1.Pass == "kairos" {
		t.Error("must never fall back to PEG upstream's static kairos/kairos default credential")
	}

	cfg1, cfg2 := m1.Config(), m2.Config()
	if cfg1.SSH.Pass != creds1.Pass || cfg2.SSH.Pass != creds2.Pass {
		t.Error("the credential returned to the caller must match what was actually configured on the machine")
	}
}

func TestNewSafeMachineLeavesStateDirToPEG(t *testing.T) {
	m1, _, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatalf("NewSafeMachine (1): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m1.Config().StateDir) })

	m2, _, err := NewSafeMachine(Config{})
	if err != nil {
		t.Fatalf("NewSafeMachine (2): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m2.Config().StateDir) })

	if m1.Config().StateDir == "" || m2.Config().StateDir == "" {
		t.Fatal("expected PEG to auto-assign a state directory when none is supplied")
	}
	if m1.Config().StateDir == m2.Config().StateDir {
		t.Error("expected two concurrent machines to get different state directories, got the same one twice")
	}
	for _, dir := range []string{m1.Config().StateDir, m2.Config().StateDir} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("state dir %s: %v", dir, err)
		}
	}
}

func TestNewSafeMachineRejectsUnpinnedRemoteISO(t *testing.T) {
	_, _, err := NewSafeMachine(Config{ISO: "https://example.invalid/hadron.iso"})
	if err == nil {
		t.Fatal("expected an error for a URL ISO with no checksum, got nil")
	}
}

// PEG's own prepare() downloads a URL ISO synchronously inside machine.New() -- not deferred to
// Create() as a first read of the code suggested -- so this test serves a real, tiny ISO over a
// local HTTP server rather than pointing at a fake remote host. Pointing NewSafeMachine at an
// unreachable host during development instead surfaced a separate, real PEG bug: a failed
// download panics (nil-dereferences resp.HTTPResponse.Status in
// pkg/machine/internal/utils/download.go) rather than returning an error. Recorded in
// docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md; a caller of NewSafeMachine
// with a URL ISO must expect this and guard accordingly (pre-validate reachability, or recover()
// at the call site) until upstream fixes it.
func TestNewSafeMachineAcceptsPinnedRemoteISO(t *testing.T) {
	const isoContent = "not a real ISO, just enough bytes to exercise the download+checksum path"
	sum := sha256.Sum256([]byte(isoContent))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(isoContent))
	}))
	defer srv.Close()

	m, _, err := NewSafeMachine(Config{
		ISO:         srv.URL + "/hadron.iso",
		ISOChecksum: "sha256:" + hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatalf("NewSafeMachine with a checksummed URL: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })

	// A successful return here means prepare() already downloaded the file and verified it
	// against the checksum above -- ISO now points at the downloaded local copy, not the URL.
	if _, err := os.Stat(m.Config().ISO); err != nil {
		t.Errorf("expected the ISO to have been downloaded into the state dir: %v", err)
	}
}

func TestRandomTokenIsURLAndShellSafe(t *testing.T) {
	tok, err := randomToken(20)
	if err != nil {
		t.Fatalf("randomToken: %v", err)
	}
	if tok == "" {
		t.Fatal("expected a non-empty token")
	}
	// Checked as a range, not a literal alphabet string: a hand-typed 32-character string with
	// no repeats reads as a plausible secret to detect-secrets' high-entropy heuristic. This is
	// exactly the lowercased RFC 4648 base32 alphabet (digits 0, 1, 8, 9 excluded by design).
	isLowerBase32 := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')
	}
	for _, r := range tok {
		if !isLowerBase32(r) {
			t.Fatalf("token %q contains %q, outside the lowercase-base32 alphabet -- unsafe to embed unescaped in a QEMU argument", tok, r)
		}
	}
}

// fakeMachine lets SafeTeardown be tested without a real PEG-managed process.
type fakeMachine struct {
	types.Machine // nil embed: panics if a test exercises a method this fake does not override
	stopErr       error
	cleanErr      error
	aliveSequence []bool // Alive() returns these in order, then repeats the last value
	aliveCalls    int
}

func (f *fakeMachine) Stop() error { return f.stopErr }

func (f *fakeMachine) Clean() error { return f.cleanErr }

func (f *fakeMachine) Alive() bool {
	if len(f.aliveSequence) == 0 {
		return false
	}
	idx := f.aliveCalls
	if idx >= len(f.aliveSequence) {
		idx = len(f.aliveSequence) - 1
	}
	f.aliveCalls++
	return f.aliveSequence[idx]
}

func TestSafeTeardownWaitsForExitBeforeCleaning(t *testing.T) {
	f := &fakeMachine{aliveSequence: []bool{true, true, false}}
	if err := SafeTeardown(f, time.Second); err != nil {
		t.Fatalf("SafeTeardown: %v", err)
	}
	if f.aliveCalls < 3 {
		t.Errorf("Alive() called %d times, want at least 3 (poll until false)", f.aliveCalls)
	}
}

func TestSafeTeardownRefusesToCleanAStillAliveProcess(t *testing.T) {
	f := &fakeMachine{aliveSequence: []bool{true}}
	err := SafeTeardown(f, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error when the process never reports exited, got nil")
	}
}
