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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// NewSafeMachine never calls Create(), so these run with no qemu binary, no /dev/kvm, and no
// KVM feasibility at all -- they prove configuration and verified artifact acquisition,
// not that a guest actually boots. Booting is
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
	if creds1.User == "kairos" || creds1.Pass == "kairos" {
		t.Error("must never fall back to PEG upstream's static kairos/kairos default credential")
	}

	cfg1, cfg2 := m1.Config(), m2.Config()
	if cfg1.SSH.Pass != creds1.Pass || cfg2.SSH.Pass != creds2.Pass {
		t.Error("the credential returned to the caller must match what was actually configured on the machine")
	}
	if cfg1.SSH.Port != creds1.Port || cfg2.SSH.Port != creds2.Port {
		t.Error("the returned SSH port must match the configured forwarding port")
	}
}

func TestNewSafeMachineAllocatesPrivateUniqueState(t *testing.T) {
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
		t.Fatal("expected a per-machine state directory")
	}
	if m1.Config().StateDir == m2.Config().StateDir {
		t.Error("expected two concurrent machines to get different state directories, got the same one twice")
	}
	for _, dir := range []string{m1.Config().StateDir, m2.Config().StateDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("state dir %s: %v", dir, err)
		} else if info.Mode().Perm() != 0700 {
			t.Errorf("state permissions = %o, want 0700", info.Mode().Perm())
		}
	}
}

func TestNewSafeMachineRejectsUnpinnedRemoteISO(t *testing.T) {
	_, _, err := NewSafeMachine(Config{ISO: "https://example.invalid/hadron.iso"})
	if err == nil {
		t.Fatal("expected an error for a URL ISO with no checksum, got nil")
	}
}

func TestNewSafeMachineRejectsInvalidChecksumsBeforeDownload(t *testing.T) {
	for _, checksum := range []string{
		"sha512:" + strings.Repeat("0", 128),
		"sha256:00",
		"sha256:" + strings.Repeat("z", 64),
		"sha256:" + strings.Repeat("0", 64) + ":extra",
		"md5:" + strings.Repeat("0", 32),
	} {
		t.Run(checksum[:6], func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte("unchecked image"))
			}))
			defer srv.Close()
			m, _, err := NewSafeMachine(Config{ISO: srv.URL + "/image.iso", ISOChecksum: checksum})
			if m != nil {
				t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })
			}
			if err == nil {
				t.Error("invalid checksum accepted")
			}
			if requests.Load() != 0 {
				t.Errorf("made %d requests before rejecting invalid checksum", requests.Load())
			}
		})
	}
}

func TestNewSafeMachineDownloadFailureReturnsErrorWithoutLeakingState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("download failure panicked instead of returning an error: %v", recovered)
		}
	}()
	_, _, err := NewSafeMachine(Config{
		ISO: srv.URL + "/image.iso", ISOChecksum: strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("expected a connection error")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("failed construction leaked %d state directories", len(entries))
	}
}

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

	data, err := os.ReadFile(m.Config().ISO)
	if err != nil || string(data) != isoContent {
		t.Errorf("downloaded content = %q, err = %v", data, err)
	}
}

func TestNewSafeMachineRejectsFailedDownloadsAndRemovesPartialState(t *testing.T) {
	for _, scenario := range []string{"http-error", "truncated", "checksum-mismatch", "untrusted-tls", "timeout"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TMPDIR", root)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch scenario {
				case "http-error":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "truncated":
					w.Header().Set("Content-Length", "1000")
					_, _ = w.Write([]byte("partial"))
				case "timeout":
					<-r.Context().Done()
				default:
					_, _ = w.Write([]byte("unverified"))
				}
			})
			srv := httptest.NewUnstartedServer(handler)
			if scenario == "untrusted-tls" {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			m, _, err := NewSafeMachineContext(ctx, Config{
				ISO: srv.URL + "/image.iso", ISOChecksum: strings.Repeat("0", 64),
			})
			if err == nil || m != nil {
				t.Fatalf("failed artifact accepted: machine=%v, err=%v", m, err)
			}
			if scenario == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("expected deadline error, got %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Errorf("partial state remains: %v, err=%v", entries, err)
			}
		})
	}
}

func TestNewSafeMachineRejectsCancelledConstruction(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := NewSafeMachineContext(ctx, Config{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestNewSafeMachineVerifiesLocalISOWhenChecksumSupplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.iso")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("fixture"))
	for _, checksum := range []string{fmt.Sprintf("%x", sum), fmt.Sprintf("SHA256:%X", sum)} {
		m, _, err := NewSafeMachine(Config{ISO: path, ISOChecksum: checksum})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(m.Config().StateDir) })
	}
	if _, _, err := NewSafeMachine(Config{ISO: path, ISOChecksum: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("local ISO with mismatched checksum accepted")
	}
}

func TestNewSafeMachineRejectsUnsupportedArtifactScheme(t *testing.T) {
	if _, _, err := NewSafeMachine(Config{ISO: "ftp://example.invalid/image.iso"}); err == nil {
		t.Fatal("unsupported artifact scheme accepted")
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
