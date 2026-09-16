package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
)

func TestExecutorJoinsOverlapOnObservationFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	reads := 0
	e := Executor{
		Observer: func(context.Context) (adaptive.PowerObservation, error) {
			reads++
			if reads == 2 {
				return adaptive.PowerObservation{}, errors.New("observation failed")
			}
			return adaptive.PowerObservation{OnBattery: true}, nil
		},
		Sleep: func(ctx context.Context, _ time.Duration) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		},
	}
	tier4, tier3 := int32(4), int32(3)
	done := make(chan error, 1)
	go func() {
		_, err := e.Execute(ctx, Input{ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: ModeDryRun, TierOverrunPolicy: TierOverrunPolicyOverlap,
			Waves:  []Wave{{Index: 0, ShutdownTier: &tier4, Duration: time.Millisecond, Groups: []string{"slow"}}, {Index: 1, ShutdownTier: &tier3, Groups: []string{"later"}}},
			Groups: []Group{{Name: "slow", Action: ActionWait, WaitDuration: time.Hour}, {Name: "later", Action: ActionWait}},
		})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("overlapped action never started")
	}
	select {
	case <-canceled:
	case <-done:
		t.Fatal("executor returned while an overlapped action still owned resources")
	case <-time.After(time.Second):
		t.Fatal("observation failure did not cancel prior action")
	}
	select {
	case <-done:
		t.Fatal("executor returned before action cleanup")
	default:
	}
	// Release through the deferred close, then join from a later defer.
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err == nil {
				t.Error("observation failure lost")
			}
		case <-time.After(time.Second):
			t.Error("executor did not join")
		}
	})
}
