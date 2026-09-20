package readiness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryAndDiagnostics(t *testing.T) {
	checks, diagnostics := 0, 0
	opts := Options{Timeout: time.Second, Interval: time.Millisecond}
	err := Wait(context.Background(), opts, func(context.Context) error {
		checks++
		if checks == 3 {
			return nil
		}
		return errors.New("not ready")
	}, func(context.Context) { diagnostics++ })
	if err != nil || checks != 3 || diagnostics != 2 {
		t.Fatalf("checks=%d diagnostics=%d err=%v", checks, diagnostics, err)
	}
}

func TestCallbacksShareDeadlineAndKeepFailure(t *testing.T) {
	for _, blockDiagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "check", true: "diagnostic"}[blockDiagnostic], func(t *testing.T) {
			lastErr := errors.New("guest unavailable")
			var checkDeadline time.Time
			called := false
			err := Wait(context.Background(), Options{Timeout: 20 * time.Millisecond, Interval: time.Millisecond},
				func(ctx context.Context) error {
					checkDeadline, _ = ctx.Deadline()
					if !blockDiagnostic {
						<-ctx.Done()
					}
					return lastErr
				}, func(ctx context.Context) {
					called = true
					deadline, _ := ctx.Deadline()
					if !deadline.Equal(checkDeadline) {
						t.Error("diagnostics received a different deadline")
					}
					<-ctx.Done()
				})
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, lastErr) || called != blockDiagnostic {
				t.Fatalf("called=%v error=%v", called, err)
			}
		})
	}
}

func TestParentCancellationPreventsCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Wait(ctx, Options{Timeout: time.Second, Interval: time.Millisecond}, func(context.Context) error {
		t.Fatal("check called after cancellation")
		return nil
	}, func(context.Context) { t.Fatal("diagnostics called after cancellation") })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSuccessAfterCancellationIsNotReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Wait(ctx, Options{Timeout: time.Second, Interval: time.Millisecond}, func(context.Context) error {
		cancel()
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled check returned success: %v", err)
	}
}

func TestDiagnosticIntervalAndParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	parentDeadline, _ := ctx.Deadline()
	err := Wait(ctx, Options{Timeout: time.Hour, Interval: time.Millisecond, DiagnoseEvery: time.Hour}, func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		if !deadline.Equal(parentDeadline) {
			t.Fatal("extended parent deadline")
		}
		return errors.New("not ready")
	}, func(context.Context) { t.Fatal("diagnostics fired before their interval") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestInvalidOptions(t *testing.T) {
	for _, opts := range []Options{{}, {Timeout: time.Second}, {Timeout: time.Second, Interval: time.Millisecond, DiagnoseEvery: -1}} {
		if err := Wait(context.Background(), opts, func(context.Context) error { t.Fatal("invalid wait ran"); return nil }, nil); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if err := Wait(context.Background(), Options{Timeout: time.Second, Interval: time.Millisecond}, nil, nil); err == nil {
		t.Fatal("nil check accepted")
	}
}
