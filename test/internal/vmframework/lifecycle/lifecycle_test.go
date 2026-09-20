package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestReverseCleanupAfterCancellationAndPreservation(t *testing.T) {
	var scope Scope
	var calls []string
	for _, name := range []string{"registry", "guest"} {
		err := scope.Add(name, func(ctx context.Context) error {
			if ctx.Err() != nil {
				t.Error("cleanup inherited cancelled parent")
			}
			calls = append(calls, "stop "+name)
			return nil
		}, func(context.Context) error { calls = append(calls, "remove "+name); return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := scope.Finish(ctx, time.Second, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"stop guest", "stop registry"}) {
		t.Fatalf("preservation/order: %v", calls)
	}
	for range 2 {
		if err := scope.Finish(ctx, time.Second, false); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(calls, []string{"stop guest", "stop registry", "remove guest", "remove registry"}) {
		t.Fatalf("repeat cleanup: %v", calls)
	}
}

func TestFailedPartialStartPreservesStateButStopsOtherResources(t *testing.T) {
	var scope Scope
	stopErr := errors.New("ownership unverified")
	otherStopped, removed := false, false
	if err := scope.Add("first", func(context.Context) error { otherStopped = true; return nil },
		func(context.Context) error { removed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := scope.Add("partial", func(context.Context) error { return stopErr }, nil); err != nil {
		t.Fatal(err)
	}
	if err := scope.Finish(context.Background(), time.Second, false); !errors.Is(err, stopErr) || !otherStopped || removed {
		t.Fatalf("partial cleanup lost safety: stopped=%v removed=%v err=%v", otherStopped, removed, err)
	}
}

func TestBudgetAndRegistrationRules(t *testing.T) {
	var scope Scope
	if err := scope.Add("blocked", func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, nil); err != nil {
		t.Fatal(err)
	}
	if err := scope.Add("blocked", func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("duplicate resource accepted")
	}
	if err := scope.Finish(context.Background(), 20*time.Millisecond, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := scope.Add("late", func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("registration after cleanup accepted")
	}
}
