// Package readiness provides bounded guest checks with periodic diagnostics.
package readiness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

type Options struct {
	Timeout       time.Duration
	Interval      time.Duration
	DiagnoseEvery time.Duration
}

// Wait immediately checks readiness, then retries failures until its budget or
// the parent context expires. Checks and diagnostics receive the same context
// and must honor it. No detached goroutines outlive the wait. A zero diagnostic
// interval requests diagnostics after every failed check; nil disables them.
// On failure both the cancellation cause and last check error are retained.
func Wait(ctx context.Context, opts Options, check func(context.Context) error, diagnose func(context.Context)) error {
	if opts.Timeout <= 0 || opts.Interval <= 0 || opts.DiagnoseEvery < 0 || check == nil {
		return fmt.Errorf("readiness requires positive timeout/interval, nonnegative diagnostic interval, and a check")
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	var lastErr error
	nextDiagnostic := time.Now().Add(opts.DiagnoseEvery)
	err := wait.PollUntilContextCancel(ctx, opts.Interval, true, func(ctx context.Context) (bool, error) {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		lastErr = check(ctx)
		if lastErr == nil {
			return true, ctx.Err()
		}
		if diagnose != nil && ctx.Err() == nil && !time.Now().Before(nextDiagnostic) {
			diagnose(ctx)
			nextDiagnostic = time.Now().Add(opts.DiagnoseEvery)
		}
		return false, nil
	})
	if err != nil {
		return errors.Join(err, lastErr)
	}
	return nil
}
