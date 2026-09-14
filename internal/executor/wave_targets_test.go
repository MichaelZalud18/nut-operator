package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

type waveTargetRunner func(context.Context, Action) (ActionOutcome, error)

func (f waveTargetRunner) RunAction(ctx context.Context, action Action) (ActionOutcome, error) {
	return f(ctx, action)
}

func TestWaveTargetsResolveBeforeActionsAndRecordActualMatches(t *testing.T) {
	writer := &fakeAuditWriter{}
	name := "initial"
	var seen []string
	e := Executor{Writer: writer,
		ResolveTargets: func(_ context.Context, group Group) ([]Target, error) {
			return []Target{{Kind: "Deployment", Name: name}}, nil
		},
		Runner: waveTargetRunner(func(_ context.Context, action Action) (ActionOutcome, error) {
			seen = append(seen, action.Group.SelectedTargets[0].Name)
			name = "changed"
			return ActionOutcome{Outcome: OutcomeSucceeded}, nil
		}),
	}
	input := Input{ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Approved: true,
		Waves:  []Wave{{Index: 0, Groups: []string{"a", "b"}}, {Index: 1, Groups: []string{"c"}}},
		Groups: []Group{{Name: "a", Action: "ScaleWorkload"}, {Name: "b", Action: "ScaleWorkload"}, {Name: "c", Action: "ScaleWorkload"}},
	}
	if _, err := e.Execute(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[0] != "initial" || seen[1] != "initial" || seen[2] != "changed" {
		t.Fatalf("matches=%v", seen)
	}
	if len(input.Groups[0].SelectedTargets) != 0 {
		t.Fatal("input mutated")
	}
	if len(writer.groups) != 3 {
		t.Fatalf("audit groups=%+v", writer.groups)
	}
	targets, ok := writer.groups[2].SelectedTargets.([]map[string]string)
	if !ok || len(targets) != 1 || targets[0]["name"] != "changed" {
		t.Fatalf("audit groups=%+v", writer.groups)
	}
}

func TestWaveResolutionFailurePreventsAllWaveActions(t *testing.T) {
	for _, scenario := range []string{"read error", "canceled", "timeout", "late success"} {
		t.Run(scenario, func(t *testing.T) {
			writer := &fakeAuditWriter{}
			runner := &recordingActionRunner{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := Executor{Writer: writer, Runner: runner, ResolveTargets: func(ctx context.Context, group Group) ([]Target, error) {
				if group.Name == "a" {
					return []Target{{Name: "selected"}}, nil
				}
				switch scenario {
				case "canceled":
					cancel()
					return nil, ctx.Err()
				case "timeout", "late success":
					<-ctx.Done()
					if scenario == "late success" {
						return nil, nil
					}
					return nil, ctx.Err()
				default:
					return nil, errors.New("API failed")
				}
			}}
			result, err := e.Execute(ctx, Input{ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Approved: true,
				Waves: []Wave{{Groups: []string{"a", "b"}}}, Groups: []Group{{Name: "a", Action: "ScaleWorkload"}, {Name: "b", Action: "ScaleWorkload", Timeout: time.Millisecond}},
			})
			if err == nil || result.Phase != PhaseAborted || len(runner.actions) != 0 {
				t.Fatalf("result=%+v err=%v actions=%v", result, err, runner.actions)
			}
			last := writer.waves[len(writer.waves)-1]
			if last.Phase != PhaseFailed || last.Details["targetResolutionError"] == nil {
				t.Fatalf("wave=%+v", last)
			}
		})
	}
}
