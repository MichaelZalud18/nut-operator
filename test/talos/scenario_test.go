//go:build talos

package talos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/diagnostics"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/scenario"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmprocess"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

type failedStartMachine struct {
	types.Machine
	root    string
	cleaned bool
}

func (m *failedStartMachine) Config() types.MachineConfig {
	return types.MachineConfig{StateDir: m.root}
}
func (m *failedStartMachine) Create(ctx context.Context) (context.Context, error) {
	return ctx, errors.New("fixture startup failure")
}
func (m *failedStartMachine) Clean() error { m.cleaned = true; return nil }

func TestScenarioFailedUnverifiedStartRetainsEvidence(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"stdout", "stderr"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture console"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := &failedStartMachine{root: root}
	report, err := runMachineScenario(context.Background(), vmprocess.Wrap(m), []scenario.Step{{
		Name: "must not provision", Timeout: time.Second,
		Run: func(context.Context, *lifecycle.Scope) error { t.Fatal("provisioned after failed start"); return nil },
	}})
	if err == nil || report.CleanupErr == nil || report.DiagnosticsErr != nil || m.cleaned {
		t.Fatalf("unverified start lost failure/state: %+v %v cleaned=%t", report, err, m.cleaned)
	}
	files, err := filepath.Glob(filepath.Join(root, "vm-diagnostics-*", "stdout.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("no retained console: %v %v", files, err)
	}
}

func TestDiagnosticsExcludeConfigurationAndBoundConsole(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		"stdout": strings.Repeat("x", 1024*1024+1), "stderr": "console error",
		"kubeconfig": "private credential", "talosconfig": "private credential",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := captureMachineDiagnostics(context.Background(), root, scenario.Report{})
	if !errors.Is(err, diagnostics.ErrLimit) {
		t.Fatalf("truncation hidden: %v", err)
	}
	files, err := os.ReadDir(bundle.Directory)
	if err != nil || len(files) != 3 {
		t.Fatalf("unexpected bundle: %v %v", files, err)
	}
	for _, entry := range bundle.Entries {
		if entry.Name == "stdout" && (!entry.Truncated || entry.Bytes != 1024*1024) {
			t.Fatalf("unbounded output: %+v", entry)
		}
		data, err := os.ReadFile(entry.Path)
		if err != nil || strings.Contains(string(data), "private credential") {
			t.Fatalf("credential capture: %v", err)
		}
	}
}
