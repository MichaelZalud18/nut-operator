package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

type timedActionRunner func(context.Context, Action) (ActionOutcome, error)

func (f timedActionRunner) RunAction(ctx context.Context, action Action) (ActionOutcome, error) {
	return f(ctx, action)
}

func TestActionCompletionMeasuresElapsedTime(t *testing.T) {
	for _, mode := range []string{"success", "failure", "timeout", "wait", "cancelled-wait"} {
		t.Run(mode, func(t *testing.T) {
			start := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
			now := start
			var mu sync.Mutex
			advance := func() { mu.Lock(); now = now.Add(7 * time.Second); mu.Unlock() }
			writer := &fakeAuditWriter{}
			e := Executor{Writer: writer, Clock: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }, NewID: sequenceIDs(), Runner: timedActionRunner(func(ctx context.Context, _ Action) (ActionOutcome, error) {
				if mode == "timeout" {
					<-ctx.Done()
				}
				advance()
				if mode == "timeout" {
					return ActionOutcome{}, ctx.Err()
				}
				if mode == "failure" {
					return ActionOutcome{}, errors.New("fixture failure")
				}
				return ActionOutcome{Outcome: OutcomeSucceeded}, nil
			}), Sleep: func(context.Context, time.Duration) error {
				advance()
				if mode == "cancelled-wait" {
					return context.Canceled
				}
				return nil
			}}
			group := Group{Name: "work", Action: "ScaleWorkload"}
			if mode == "timeout" {
				group.Timeout = time.Millisecond
			}
			input := Input{ExecutionID: "test", ShutdownFlow: "test", PlanConfigHash: "test-plan", Approved: true, Mode: ModeEnforce, Waves: []Wave{{Index: 0, Groups: []string{"work"}}}}
			if mode == "wait" || mode == "cancelled-wait" {
				group.Action = ActionWait
				group.WaitDuration = 7 * time.Second
				input.Mode = ModeDryRun
			}
			input.Groups = []Group{group}
			_, err := e.Execute(context.Background(), input)
			wantError := mode == "failure" || mode == "timeout" || mode == "cancelled-wait"
			if (err != nil) != wantError {
				t.Fatalf("unexpected execution error: %v", err)
			}
			if len(writer.groups) != 1 || len(writer.actionAttempts) != 1 {
				t.Fatalf("missing records: groups=%d attempts=%d", len(writer.groups), len(writer.actionAttempts))
			}
			g, a := writer.groups[0], writer.actionAttempts[0]
			if g.CompletedAt == nil || g.StartedAt == nil || g.CompletedAt.Sub(*g.StartedAt) != 7*time.Second {
				t.Fatalf("wrong group duration: %#v", g)
			}
			if a.CompletedAt == nil || a.StartedAt == nil || a.CompletedAt.Sub(*a.StartedAt) != 7*time.Second {
				t.Fatalf("wrong action duration: %#v", a)
			}
			if !g.ObservedAt.Equal(*g.CompletedAt) || !a.ObservedAt.Equal(*a.CompletedAt) {
				t.Fatal("observation timestamp differs from completion")
			}
			if mode == "success" {
				// Use the elapsed interval stored in the audit record, as the history query does.
				plan, diagnostics, err := planner.CompileWithHistory(planner.StructuralInputs{
					SourceID: "completion-test",
					Triggers: []planner.Trigger{{Type: "OnBattery"}},
					Groups:   []planner.Group{{Name: "work", Action: "ScaleWorkload", Target: planner.Target{WorkloadSelector: true}, Timeout: planner.Duration{Duration: time.Minute}}},
				}, planner.TelemetryInputs{}, planner.HistoryInputs{GroupDurations: map[string][]time.Duration{"work": {g.CompletedAt.Sub(*g.StartedAt)}}})
				if err != nil {
					t.Fatalf("compile: %v (%v)", err, diagnostics)
				}
				if plan.ObservedDuration.Duration != 7*time.Second {
					t.Fatalf("lost measured duration: %s", plan.ObservedDuration.Duration)
				}
			}
		})
	}
}
