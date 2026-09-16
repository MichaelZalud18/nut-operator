//go:build talos

package talos

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPollGuestCancellation(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "check", true: "diagnostic"}[diagnostic], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			called := false
			block := func(ctx context.Context) { called = true; <-ctx.Done() }
			check := func(ctx context.Context) error {
				if !diagnostic {
					block(ctx)
				}
				return errors.New("not ready")
			}
			err := pollGuest(ctx, time.Millisecond, 0, check, block)
			if !called || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("called=%v, err=%v", called, err)
			}
		})
	}
}

func TestPollGuestCancelledBeforeCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := pollGuest(ctx, time.Second, time.Minute, func(context.Context) error {
		t.Fatal("check called after cancellation")
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPollGuestSuccess(t *testing.T) {
	if err := pollGuest(context.Background(), time.Second, time.Minute, func(context.Context) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
}
