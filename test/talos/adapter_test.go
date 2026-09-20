//go:build talos

package talos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactAdoption(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	body := []byte("test ISO bytes")
	digest := sha256.Sum256(body)
	pin := hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	local := filepath.Join(root, "caller.iso")
	if err := os.WriteFile(local, body, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  Config
		fail bool
	}{
		{"remote", Config{ISO: server.URL, ISOChecksum: pin}, false},
		{"local unpinned", Config{ISO: local}, false},
		{"local pinned", Config{ISO: local, ISOChecksum: "sha256:" + pin}, false},
		{"empty constructor fixture", Config{}, false},
		{"missing local", Config{ISO: filepath.Join(root, "missing.iso")}, true},
		{"directory", Config{ISO: root}, true},
		{"unpinned remote", Config{ISO: server.URL}, true},
		{"wrong checksum", Config{ISO: server.URL, ISOChecksum: hex.EncodeToString(make([]byte, 32))}, true},
		{"unsupported scheme", Config{ISO: "ftp://example.com/boot.iso", ISOChecksum: pin}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewSafeMachineContext(context.Background(), tc.cfg)
			if tc.fail {
				if err == nil {
					_ = m.Clean()
					t.Fatal("invalid artifact accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if tc.cfg.ISO != "" {
					data, err := os.ReadFile(m.Config().ISO)
					if err != nil || string(data) != string(body) {
						t.Fatalf("prepared image: %q %v", data, err)
					}
				}
				if err := m.Clean(); err != nil {
					t.Fatal(err)
				}
			}
			states, err := filepath.Glob(filepath.Join(root, "nut-operator-talos-*"))
			if err != nil || len(states) != 0 {
				t.Fatalf("constructor leaked state: %v %v", states, err)
			}
			if _, err := os.Stat(local); err != nil {
				t.Fatalf("caller ISO removed: %v", err)
			}
		})
	}
}

func TestArtifactCancellationRemovesOnlyConstructorState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	_, err := NewSafeMachineContext(ctx, Config{ISO: server.URL, ISOChecksum: hex.EncodeToString(make([]byte, 32))})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled download leaked files: %v %v", entries, err)
	}
}
