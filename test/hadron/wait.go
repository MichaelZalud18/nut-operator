//go:build hadron

package hadron

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// pollGuest gives checks and diagnostics the same deadline. Callbacks must honor ctx.
func pollGuest(ctx context.Context, interval, diagnoseEvery time.Duration, check func(context.Context) error, diagnose func(context.Context)) error {
	var lastErr error
	nextDiagnostic := time.Now().Add(diagnoseEvery)
	err := wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		lastErr = check(ctx)
		if lastErr == nil {
			return true, ctx.Err()
		}
		if diagnose != nil && ctx.Err() == nil && !time.Now().Before(nextDiagnostic) {
			diagnose(ctx)
			nextDiagnostic = time.Now().Add(diagnoseEvery)
		}
		return false, nil
	})
	if err != nil {
		return errors.Join(err, lastErr)
	}
	return nil
}
