package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

// Unlike the general recorder, a real database rejects a canceled context.
type cancellationAuditWriter struct {
	fakeAuditWriter
	blockTerminal    bool
	terminalDeadline time.Time
}

func (w *cancellationAuditWriter) RecordShutdownFlowExecution(ctx context.Context, record audit.ShutdownFlowExecution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Phase == PhaseAborted {
		w.terminalDeadline, _ = ctx.Deadline()
		if w.blockTerminal {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	return w.fakeAuditWriter.RecordShutdownFlowExecution(ctx, record)
}

func TestCanceledExecutionRetainsTerminalAudit(t *testing.T) {
	for _, scenario := range []string{"action", "advisory hook", "last advisory hook", "power observation", "unavailable audit"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			writer := &cancellationAuditWriter{blockTerminal: scenario == "unavailable audit"}
			calls := 0
			input := twoWaveApprovalInput()
			if scenario == "advisory hook" || scenario == "last advisory hook" {
				input.Groups[0].Action = ActionRunHook
				if scenario == "last advisory hook" {
					input.Waves = input.Waves[:1]
				}
			}
			executor := Executor{Writer: writer, Runner: runnerFunc(func(ctx context.Context, _ Action) (ActionOutcome, error) {
				calls++
				cancel()
				return ActionOutcome{Outcome: OutcomeBlocked}, ctx.Err()
			})}
			wantCalls := 1
			if scenario == "power observation" {
				wantCalls = 0
				executor.Observer = func(ctx context.Context) (adaptive.PowerObservation, error) {
					cancel()
					return adaptive.PowerObservation{}, ctx.Err()
				}
			}
			started := time.Now()
			result, err := executor.Execute(ctx, input)
			if !errors.Is(err, context.Canceled) || result.Phase != PhaseAborted || calls != wantCalls {
				t.Fatalf("result=%+v error=%v actions=%d", result, err, calls)
			}
			if writer.terminalDeadline.IsZero() || writer.terminalDeadline.Sub(started) > 2*time.Second {
				t.Fatalf("terminal write lacks a short deadline: %v", writer.terminalDeadline)
			}
			if writer.blockTerminal {
				if !errors.Is(result.RecordError, context.DeadlineExceeded) {
					t.Fatalf("missing audit failure: %v", result.RecordError)
				}
				return
			}
			if len(writer.executions) != 2 || writer.executions[1].Phase != PhaseAborted || writer.executions[1].CompletedAt == nil {
				t.Fatalf("cancellation lost terminal audit: %+v", writer.executions)
			}
		})
	}
}
