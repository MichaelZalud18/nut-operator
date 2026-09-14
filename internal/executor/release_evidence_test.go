package executor

import (
	"context"
	"testing"
	"time"
)

func TestReleaseEvidenceRefreshRejectsIdentityChanges(t *testing.T) {
	for _, field := range []string{"node", "agent", "uid", "generation", "policy", "path", "namespace", "secret", "key"} {
		t.Run(field, func(t *testing.T) {
			selected := NodeRelease{NodeName: "node", NodePowerAgent: "agent", AgentUID: "uid", AgentGeneration: 1, ActuatorPolicy: "Simulate", SignalPath: "path", SignalSecretNamespace: "namespace", SignalSecretName: "secret", SignalSecretKey: "key"}
			e := Executor{RefreshNodeRelease: func(_ context.Context, release NodeRelease) (NodeRelease, error) {
				switch field {
				case "node":
					release.NodeName += "changed"
				case "agent":
					release.NodePowerAgent += "changed"
				case "uid":
					release.AgentUID += "changed"
				case "generation":
					release.AgentGeneration++
				case "policy":
					release.ActuatorPolicy += "changed"
				case "path":
					release.SignalPath += "changed"
				case "namespace":
					release.SignalSecretNamespace += "changed"
				case "secret":
					release.SignalSecretName += "changed"
				case "key":
					release.SignalSecretKey += "changed"
				}
				return release, nil
			}}
			if _, err := e.refreshReleaseEvidence(context.Background(), Group{NodeReleases: []NodeRelease{selected}}); err == nil {
				t.Fatal("changed identity accepted")
			}
		})
	}
}

func TestReleaseEvidenceRefreshHonorsGroupTimeout(t *testing.T) {
	writer := &fakeAuditWriter{}
	runner := &recordingActionRunner{}
	e := Executor{Writer: writer, Runner: runner, RefreshNodeRelease: func(ctx context.Context, release NodeRelease) (NodeRelease, error) {
		<-ctx.Done()
		return release, ctx.Err()
	}}
	result, err := e.Execute(context.Background(), Input{
		ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Approved: true,
		Waves:  []Wave{{Groups: []string{"release"}}},
		Groups: []Group{{Name: "release", Action: ActionAgentShutdown, Timeout: 10 * time.Millisecond, NodeReleases: []NodeRelease{{NodeName: "node"}}}},
	})
	if err == nil || result.Phase != PhaseAborted {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if len(writer.actionAttempts) != 1 || writer.actionAttempts[0].Outcome != OutcomeTimedOut {
		t.Fatalf("attempts=%+v", writer.actionAttempts)
	}
}
