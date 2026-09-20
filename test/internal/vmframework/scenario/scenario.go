// Package scenario composes bounded test steps with owned-resource cleanup.
// It neither chooses a guest adapter nor supplies scenario assertions.
package scenario

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
)

type Step struct {
	Name    string
	Timeout time.Duration
	// Run must honor ctx and register owned cleanup before starting resources.
	Run func(context.Context, *lifecycle.Scope) error
}

type Plan struct {
	Timeout, CleanupTimeout, DiagnosticTimeout time.Duration
	Steps                                      []Step
	// OnFailure runs before stopping resources, under its own bounded context.
	// It must honor ctx. It is not called during panic unwinding.
	OnFailure func(context.Context, Report) error
}

type StepResult struct {
	Name     string
	Duration time.Duration
	Err      error
}

type Report struct {
	Steps                      []StepResult
	DiagnosticsErr, CleanupErr error
}

// Run validates the complete plan before any side effect. Steps run sequentially;
// the first failure prevents later steps. Cleanup always runs, including during a
// panic (which propagates). Step failure/panic suppresses state-removal callbacks.
// Stop failures also preserve state; removal failures can leave partial cleanup. Callbacks
// are cooperative: context deadlines cannot forcibly stop arbitrary Go code.
func Run(parent context.Context, plan Plan) (report Report, retErr error) {
	if err := validate(plan); err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(parent, plan.Timeout)
	defer cancel()
	var scope lifecycle.Scope
	completed := false
	defer func() {
		report.CleanupErr = scope.Finish(parent, plan.CleanupTimeout, !completed || retErr != nil)
		retErr = errors.Join(retErr, report.CleanupErr)
	}()
	for _, step := range plan.Steps {
		if err := ctx.Err(); err != nil {
			retErr = err
			break
		}
		started := time.Now()
		err := runStep(ctx, step, &scope)
		report.Steps = append(report.Steps, StepResult{Name: step.Name, Duration: time.Since(started), Err: err})
		if err != nil {
			retErr = fmt.Errorf("step %s: %w", step.Name, err)
			break
		}
	}
	retErr = errors.Join(retErr, ctx.Err())
	if retErr != nil && plan.OnFailure != nil {
		diagnosticCtx, diagnosticCancel := context.WithTimeout(context.WithoutCancel(parent), plan.DiagnosticTimeout)
		defer diagnosticCancel()
		// Copy the slice so diagnostics cannot rewrite recorded step outcomes.
		snapshot := report
		snapshot.Steps = append([]StepResult(nil), report.Steps...)
		report.DiagnosticsErr = errors.Join(plan.OnFailure(diagnosticCtx, snapshot), diagnosticCtx.Err())
		retErr = errors.Join(retErr, report.DiagnosticsErr)
	}
	completed = true
	return report, retErr
}

func runStep(ctx context.Context, step Step, scope *lifecycle.Scope) error {
	ctx, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()
	return errors.Join(step.Run(ctx, scope), ctx.Err())
}

func validate(plan Plan) error {
	if plan.Timeout <= 0 || plan.CleanupTimeout <= 0 || len(plan.Steps) == 0 || (plan.OnFailure != nil && plan.DiagnosticTimeout <= 0) {
		return fmt.Errorf("steps and positive execution, cleanup and enabled diagnostic budgets required")
	}
	names := map[string]bool{}
	for _, s := range plan.Steps {
		if s.Name == "" || names[s.Name] || s.Timeout <= 0 || s.Run == nil {
			return fmt.Errorf("unique named steps with positive budgets and callbacks required")
		}
		names[s.Name] = true
	}
	return nil
}
