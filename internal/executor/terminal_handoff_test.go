package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTerminalHandoffWaitsForOverlappedFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	called := false
	e := Executor{Runner: waveTargetRunner(func(ctx context.Context, action Action) (ActionOutcome, error) {
		if action.Group.Name == "earlier" {
			select {
			case <-time.After(10 * time.Millisecond):
				return ActionOutcome{}, errors.New("earlier action failed")
			case <-ctx.Done():
				return ActionOutcome{}, ctx.Err()
			}
		}
		called = true
		return ActionOutcome{Outcome: OutcomeSucceeded}, nil
	})}
	hi, lo := int32(2), int32(1)
	_, err := e.Execute(ctx, Input{ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Approved: true, TierOverrunPolicy: TierOverrunPolicyOverlap,
		Waves:  []Wave{{Index: 0, ShutdownTier: &hi, Duration: time.Millisecond, Groups: []string{"earlier"}}, {Index: 1, ShutdownTier: &lo, Groups: []string{"halt"}}},
		Groups: []Group{{Name: "earlier", Action: "Notify"}, {Name: "halt", Action: ActionAgentShutdown, NodeReleases: []NodeRelease{{NodeName: "a", AgentReady: true, TelemetryFresh: true, Cleared: true}}}},
	})
	if err == nil || called {
		t.Fatalf("terminal called=%v error=%v", called, err)
	}
}

func TestTerminalHandoffAuthorityComesFromPosition(t *testing.T) {
	var flags []bool
	e := Executor{Runner: waveTargetRunner(func(_ context.Context, action Action) (ActionOutcome, error) {
		flags = append(flags, action.Group.NodeReleases[0].TerminalHandoff)
		return ActionOutcome{Outcome: OutcomeSucceeded}, nil
	})}
	release := NodeRelease{NodeName: "a", AgentReady: true, TelemetryFresh: true, Cleared: true, TerminalHandoff: true}
	_, err := e.Execute(context.Background(), Input{ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Approved: true,
		Waves:  []Wave{{Index: 0, Groups: []string{"early"}}, {Index: 0, Groups: []string{"final"}}},
		Groups: []Group{{Name: "early", Action: ActionAgentShutdown, NodeReleases: []NodeRelease{release}}, {Name: "final", Action: ActionAgentShutdown, NodeReleases: []NodeRelease{release}}},
	})
	if err != nil || len(flags) != 2 || flags[0] || !flags[1] {
		t.Fatalf("flags=%v err=%v", flags, err)
	}
}
