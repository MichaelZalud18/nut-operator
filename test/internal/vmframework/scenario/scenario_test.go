package scenario

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/lifecycle"
)

func TestFailureStopsLaterStepsAndRetainsState(t *testing.T) {
	failure := errors.New("start failed")
	diagnosticFailure := errors.New("diagnostic failed")
	var events []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report, err := Run(ctx, Plan{Timeout: time.Second, CleanupTimeout: time.Second, DiagnosticTimeout: time.Second,
		Steps: []Step{{Name: "partial-start", Timeout: time.Second, Run: func(ctx context.Context, s *lifecycle.Scope) error {
			if err := s.Add("guest", func(ctx context.Context) error { events = append(events, "stop"); return ctx.Err() }, func(context.Context) error { events = append(events, "remove"); return nil }); err != nil {
				return err
			}
			events = append(events, "start")
			cancel()
			return failure
		}}, {Name: "must-not-run", Timeout: time.Second, Run: func(context.Context, *lifecycle.Scope) error { t.Fatal("later step ran"); return nil }}},
		OnFailure: func(ctx context.Context, r Report) error {
			events = append(events, "diagnose")
			if ctx.Err() != nil || len(r.Steps) != 1 {
				t.Fatal("diagnostics lost independent context or outcome")
			}
			r.Steps[0].Name = "changed"
			return diagnosticFailure
		}})
	if !errors.Is(err, failure) || !errors.Is(err, diagnosticFailure) || report.Steps[0].Name != "partial-start" || report.CleanupErr != nil {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if !reflect.DeepEqual(events, []string{"start", "diagnose", "stop"}) {
		t.Fatal(events)
	}
}

func TestTimeoutCannotBeSwallowed(t *testing.T) {
	for _, total := range []bool{false, true} {
		t.Run(map[bool]string{false: "step", true: "total"}[total], func(t *testing.T) {
			plan := Plan{Timeout: time.Second, CleanupTimeout: time.Second, Steps: []Step{{Name: "wait", Timeout: 10 * time.Millisecond, Run: func(ctx context.Context, _ *lifecycle.Scope) error { <-ctx.Done(); return nil }}}}
			if total {
				plan.Timeout = 10 * time.Millisecond
				plan.Steps[0].Timeout = time.Second
			}
			report, err := Run(context.Background(), plan)
			if !errors.Is(err, context.DeadlineExceeded) || len(report.Steps) != 1 || !errors.Is(report.Steps[0].Err, context.DeadlineExceeded) {
				t.Fatalf("%+v %v", report, err)
			}
		})
	}
}

func TestCleanupFailureCannotPass(t *testing.T) {
	failure := errors.New("stop failed")
	report, err := Run(context.Background(), Plan{Timeout: time.Second, CleanupTimeout: time.Second, Steps: []Step{{Name: "setup", Timeout: time.Second, Run: func(_ context.Context, s *lifecycle.Scope) error {
		return s.Add("guest", func(context.Context) error { return failure }, func(context.Context) error { t.Fatal("removed live state"); return nil })
	}}}})
	if !errors.Is(err, failure) || !errors.Is(report.CleanupErr, failure) {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestPanicStillStopsAndPreserves(t *testing.T) {
	stopped := false
	defer func() {
		if recover() != "boom" || !stopped {
			t.Fatal("panic/cleanup contract lost")
		}
	}()
	_, _ = Run(context.Background(), Plan{Timeout: time.Second, CleanupTimeout: time.Second, Steps: []Step{{Name: "panic", Timeout: time.Second, Run: func(_ context.Context, s *lifecycle.Scope) error {
		if err := s.Add("guest", func(context.Context) error { stopped = true; return nil }, func(context.Context) error { t.Fatal("panic state removed"); return nil }); err != nil {
			t.Fatal(err)
		}
		panic("boom")
	}}}})
}

func TestValidateEntirePlanBeforeStarting(t *testing.T) {
	_, err := Run(context.Background(), Plan{Timeout: time.Second, CleanupTimeout: time.Second, Steps: []Step{{Name: "first", Timeout: time.Second, Run: func(context.Context, *lifecycle.Scope) error { t.Fatal("invalid plan started"); return nil }}, {Name: "first"}}})
	if err == nil {
		t.Fatal("invalid plan accepted")
	}
}

func TestSuccessRemovesAndDiagnosticsTimeoutIsReported(t *testing.T) {
	removed := false
	step := Step{Name: "setup", Timeout: time.Second, Run: func(_ context.Context, s *lifecycle.Scope) error {
		return s.Add("guest", func(context.Context) error { return nil }, func(context.Context) error { removed = true; return nil })
	}}
	plan := Plan{Timeout: time.Second, CleanupTimeout: time.Second, Steps: []Step{step}}
	if _, err := Run(context.Background(), plan); err != nil || !removed {
		t.Fatalf("cleanup: %v removed=%t", err, removed)
	}
	plan.Steps = []Step{{Name: "fail", Timeout: time.Second, Run: func(context.Context, *lifecycle.Scope) error { return errors.New("failure") }}}
	plan.DiagnosticTimeout = 10 * time.Millisecond
	plan.OnFailure = func(ctx context.Context, _ Report) error { <-ctx.Done(); return nil }
	report, err := Run(context.Background(), plan)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(report.DiagnosticsErr, context.DeadlineExceeded) {
		t.Fatalf("%+v %v", report, err)
	}
}
