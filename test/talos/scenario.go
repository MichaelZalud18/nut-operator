//go:build talos

package talos

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/diagnostics"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/scenario"
	"github.com/spectrocloud/peg/pkg/machine/types"
)

// runMachineScenario registers cleanup before Create. Provisioning and assertions
// remain explicit caller steps. Failed starts keep state, including when ownership
// cannot be established; the independent workflow cleanup remains the fallback.
func runMachineScenario(ctx context.Context, m types.Machine, steps []scenario.Step) (scenario.Report, error) {
	start := scenario.Step{Name: "start owned Talos guest", Timeout: 2 * time.Minute,
		Run: func(ctx context.Context, scope *lifecycle.Scope) error {
			if err := registerMachine(scope, m); err != nil {
				return err
			}
			_, err := m.Create(ctx)
			return err
		}}
	return scenario.Run(ctx, scenario.Plan{
		Timeout: 27 * time.Minute, CleanupTimeout: 30 * time.Second, DiagnosticTimeout: 20 * time.Second,
		Steps: append([]scenario.Step{start}, steps...),
		OnFailure: func(ctx context.Context, report scenario.Report) error {
			_, err := captureMachineDiagnostics(ctx, m.Config().StateDir, report)
			return err
		},
	})
}

func registerMachine(scope *lifecycle.Scope, m types.Machine) error {
	return scope.Add("Talos guest", func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline, ok := ctx.Deadline()
		if !ok {
			return fmt.Errorf("cleanup deadline required")
		}
		return SafeStop(m, time.Until(deadline))
	}, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return m.Clean()
	})
}

// Only publish console streams and step outcomes. Machine configuration and
// kubeconfig contain credentials and are deliberately excluded from the bundle.
func captureMachineDiagnostics(ctx context.Context, root string, report scenario.Report) (diagnostics.Bundle, error) {
	collectors := []diagnostics.Collector{{Name: "steps", Timeout: time.Second,
		Collect: func(ctx context.Context, w io.Writer) error {
			for _, step := range report.Steps {
				if err := ctx.Err(); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "%s duration=%s failed=%t\n", step.Name, step.Duration, step.Err != nil); err != nil {
					return err
				}
			}
			return nil
		}}}
	for _, name := range []string{"stdout", "stderr"} {
		collectors = append(collectors, diagnostics.Collector{Name: name, Timeout: 5 * time.Second,
			Collect: func(ctx context.Context, w io.Writer) error {
				f, err := os.Open(filepath.Join(root, name))
				if err != nil {
					return err
				}
				defer func() { _ = f.Close() }()
				buf := make([]byte, 32*1024)
				for {
					if err := ctx.Err(); err != nil {
						return err
					}
					n, readErr := f.Read(buf)
					if _, err := w.Write(buf[:n]); err != nil {
						return err
					}
					if readErr == io.EOF {
						return nil
					}
					if readErr != nil {
						return readErr
					}
				}
			}})
	}
	return diagnostics.Capture(ctx, diagnostics.Options{
		Parent: root, Timeout: 15 * time.Second, MaxBytes: 1024 * 1024,
	}, collectors)
}
