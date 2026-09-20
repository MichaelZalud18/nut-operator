//go:build hadron && talos

package vmframework_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/hadron"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/artifact"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/readiness"
	"github.com/MichaelZalud18/nut-operator/test/talos"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

// This contract test invokes both real constructors without Create: it proves
// composition and private state, not guest boot or Kubernetes readiness.
func TestSharedPreparationFeedsBothAdapters(t *testing.T) {
	const body = "fixture boot image"
	sum := sha256.Sum256([]byte(body))
	pin := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	constructors := map[string]func(context.Context, string) (types.Machine, error){
		"hadron": func(ctx context.Context, path string) (types.Machine, error) {
			m, _, err := hadron.NewSafeMachineContext(ctx, hadron.Config{ISO: path, ISOChecksum: pin})
			return m, err
		},
		"talos": func(ctx context.Context, path string) (types.Machine, error) {
			return talos.NewSafeMachineContext(ctx, talos.Config{ISO: path, ISOChecksum: pin})
		},
	}
	states := map[string]bool{}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path, err := artifact.Prepare(context.Background(), server.Client(), artifact.Source{
				Location: server.URL, SHA256: pin,
			}, filepath.Join(root, "boot.iso"))
			if err != nil {
				t.Fatal(err)
			}
			m, err := construct(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := m.Clean(); err != nil {
					t.Error(err)
				}
			})
			if states[m.Config().StateDir] {
				t.Fatal("adapters share mutable machine state")
			}
			states[m.Config().StateDir] = true
			// Each adapter can provide its own context-aware condition. This
			// fixture checks configuration; live callers check SSH/API readiness.
			err = readiness.Wait(context.Background(), readiness.Options{
				Timeout: time.Second, Interval: time.Millisecond,
			}, func(context.Context) error {
				if m.Config().ISO != path || m.Config().ISOChecksum != pin {
					return fmt.Errorf("adapter did not retain the verified artifact")
				}
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Clean(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("machine cleanup removed caller-owned artifact: %v", err)
			}
		})
	}
}
