package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func checksum(body string) string {
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

func TestParseSHA256(t *testing.T) {
	want := checksum("image")
	for _, input := range []string{want, "sha256:" + want, "SHA256:" + strings.ToUpper(want)} {
		got, err := ParseSHA256(input)
		if err != nil || hex.EncodeToString(got) != want {
			t.Fatalf("parse %q: %x, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "sha512:" + want, "abc", strings.Repeat("z", 64)} {
		if _, err := ParseSHA256(input); err == nil {
			t.Fatalf("accepted invalid checksum %q", input)
		}
	}
}

func TestRemoteValidationPrecedesDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid source reached network")
	}))
	defer server.Close()
	for _, source := range []Source{
		{Location: server.URL},
		{Location: server.URL, SHA256: "sha512:" + checksum("image")},
		{Location: "file:///tmp/image", SHA256: checksum("image")},
		{Location: "https://", SHA256: checksum("image")},
		{},
	} {
		root := t.TempDir()
		if _, err := Prepare(context.Background(), nil, source, filepath.Join(root, "boot.iso")); err == nil {
			t.Fatalf("accepted invalid source: %+v", source)
		}
		assertFiles(t, root, 0)
	}
}

func TestDownloadAndLocalVerification(t *testing.T) {
	const body = "pinned guest image"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "boot.iso")
	path, err := Prepare(context.Background(), server.Client(), Source{server.URL, checksum(body)}, destination)
	if err != nil || path != destination {
		t.Fatalf("prepare: %q, %v", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != body {
		t.Fatalf("download contents: %q, %v", data, err)
	}
	assertFiles(t, root, 1)
	for _, pin := range []string{"", "sha256:" + checksum(body)} {
		if local, err := Prepare(context.Background(), nil, Source{path, pin}, ""); err != nil || local != path {
			t.Fatalf("local prepare: %q, %v", local, err)
		}
	}
	if _, err := Prepare(context.Background(), nil, Source{path, checksum("wrong")}, ""); err == nil {
		t.Fatal("accepted local checksum mismatch")
	}
}

func TestFailedDownloadsPreserveDiagnostics(t *testing.T) {
	for _, mode := range []string{"checksum", "status", "truncated", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "status":
					w.WriteHeader(http.StatusNotFound)
				case "truncated":
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("short"))
				case "cancel":
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					cancel()
					<-r.Context().Done()
				default:
					_, _ = w.Write([]byte("wrong checksum"))
				}
			}))
			defer server.Close()
			root := t.TempDir()
			log := filepath.Join(root, "console.log")
			if err := os.WriteFile(log, []byte("retain me"), 0600); err != nil {
				t.Fatal(err)
			}
			path, err := Prepare(ctx, server.Client(), Source{server.URL, checksum("expected")}, filepath.Join(root, "boot.iso"))
			if err == nil || path != "" {
				t.Fatalf("failed download published: %q, %v", path, err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			data, err := os.ReadFile(log)
			if err != nil || string(data) != "retain me" {
				t.Fatalf("diagnostics lost: %q, %v", data, err)
			}
			assertFiles(t, root, 1)
		})
	}
}

func TestConcurrentDownloadNeverOverwrites(t *testing.T) {
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		<-release
		_, _ = w.Write([]byte("image"))
	}))
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "boot.iso")
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			_, err := Prepare(context.Background(), server.Client(), Source{server.URL, checksum("image")}, destination)
			results <- err
		})
	}
	<-arrived
	<-arrived
	close(release)
	workers.Wait()
	first, second := <-results, <-results
	if (first == nil) == (second == nil) {
		t.Fatalf("expected exactly one publisher: %v / %v", first, second)
	}
	assertFiles(t, root, 1)
	if _, err := Prepare(context.Background(), server.Client(), Source{server.URL, checksum("image")}, destination); err == nil {
		t.Fatal("existing destination accepted")
	}
}

func TestCancellationAndInvalidLocalFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, nil, Source{Location: "unused"}, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Verify(ctx, "unused", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("directory accepted as artifact")
	}
	if err := Verify(context.Background(), "unused", []byte("bad")); err == nil {
		t.Fatal("invalid digest length accepted")
	}
}

func assertFiles(t *testing.T, root string, want int) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != want {
		t.Fatalf("expected %d retained files, got %v: %v", want, entries, err)
	}
}
