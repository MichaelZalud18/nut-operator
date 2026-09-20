//go:build talos

package talos

import (
	"context"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/readiness"
)

// pollGuest preserves the caller's deadline and diagnostic cadence through shared readiness.
// Callbacks must honor ctx. Smoke callers always supply a bounded context.
func pollGuest(ctx context.Context, interval, diagnoseEvery time.Duration, check func(context.Context) error, diagnose func(context.Context)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	budget := time.Duration(1<<63 - 1)
	if deadline, ok := ctx.Deadline(); ok {
		budget = time.Until(deadline)
		if budget <= 0 {
			return context.DeadlineExceeded
		}
	}
	return readiness.Wait(ctx, readiness.Options{
		Timeout: budget, Interval: interval, DiagnoseEvery: diagnoseEvery,
	}, check, diagnose)
}
