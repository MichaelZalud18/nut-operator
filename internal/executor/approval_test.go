/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

// twoWaveApprovalInput builds a two-wave enforce Input, each wave releasing one distinct,
// otherwise-eligible node, for testing what ApprovalChecker does to a wave that would otherwise
// actuate. Approved is true at the top level throughout -- these tests are entirely about
// ApprovalChecker overriding that snapshot, not about Input.Approved itself.
func twoWaveApprovalInput() Input {
	release := func(node string) NodeRelease {
		return NodeRelease{
			NodeName:       node,
			NodePowerAgent: "rack-a-agents",
			AgentReady:     true,
			Cleared:        true,
			TelemetryFresh: true,
		}
	}
	return Input{
		ExecutionID:    "execution-approval",
		ShutdownFlow:   "conserve-power",
		Mode:           ModeEnforce,
		Approved:       true,
		PlanConfigHash: "plan-hash-approval",
		Waves: []Wave{
			{Index: 0, Groups: []string{"node-a"}},
			{Index: 1, Groups: []string{"node-b"}},
		},
		Groups: []Group{
			{Name: "node-a", Action: ActionAgentShutdown, NodeReleases: []NodeRelease{release("node-a")}},
			{Name: "node-b", Action: ActionAgentShutdown, NodeReleases: []NodeRelease{release("node-b")}},
		},
	}
}

func releaseFor(records []audit.NodeReleaseRecord, node string) audit.NodeReleaseRecord {
	for _, r := range records {
		if r.NodeName == node {
			return r
		}
	}
	return audit.NodeReleaseRecord{}
}

func handoffFor(records []audit.NodeSignalHandoff, node string) audit.NodeSignalHandoff {
	for _, r := range records {
		if r.NodeName == node {
			return r
		}
	}
	return audit.NodeSignalHandoff{}
}

// constantApprovalChecker returns approvals[wave call count], clamped to the last entry once
// exhausted -- one entry per wave in call order, since ApprovalChecker is called at most once per
// wave boundary.
func constantApprovalChecker(approvals ...bool) ApprovalChecker {
	call := 0
	return func(context.Context) (bool, error) {
		i := call
		if i >= len(approvals) {
			i = len(approvals) - 1
		}
		call++
		return approvals[i], nil
	}
}

func TestExecutorRecheckApprovalBeforeEachWave(t *testing.T) {
	writer := &fakeAuditWriter{}
	executor := Executor{
		Writer:          writer,
		Clock:           func() time.Time { return time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC) },
		NewID:           sequenceIDs(),
		Runner:          fakeActionRunner{outcome: ActionOutcome{Outcome: OutcomeSucceeded}},
		ApprovalChecker: constantApprovalChecker(true, false),
	}

	result, err := executor.Execute(context.Background(), twoWaveApprovalInput())
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted {
		t.Fatalf("expected completed result, got %#v", result)
	}

	if a := releaseFor(writer.nodeReleases, "node-a"); !a.Released {
		t.Errorf("wave 0 (approved): expected node-a released, got %#v", a)
	}
	if b := releaseFor(writer.nodeReleases, "node-b"); b.Released {
		t.Errorf("wave 1 (revoked): expected node-b NOT released, got %#v", b)
	}
	if h := handoffFor(writer.nodeSignalHandoffs, "node-a"); !h.Accepted {
		t.Errorf("wave 0 (approved): expected node-a handoff accepted, got %#v", h)
	}
	if h := handoffFor(writer.nodeSignalHandoffs, "node-b"); h.Accepted {
		t.Errorf("wave 1 (revoked): expected node-b handoff NOT accepted -- an already-rendered actuator is not current authorization, got %#v", h)
	}
}

func TestExecutorApprovalRevocationIsSticky(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := twoWaveApprovalInput()
	input.Waves = append(input.Waves, Wave{Index: 2, Groups: []string{"node-c"}})
	input.Groups = append(input.Groups, Group{
		Name:   "node-c",
		Action: ActionAgentShutdown,
		NodeReleases: []NodeRelease{{
			NodeName: "node-c", NodePowerAgent: "rack-a-agents",
			AgentReady: true, Cleared: true, TelemetryFresh: true,
		}},
	})

	executor := Executor{
		Writer: writer,
		Clock:  func() time.Time { return time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC) },
		NewID:  sequenceIDs(),
		Runner: fakeActionRunner{outcome: ActionOutcome{Outcome: OutcomeSucceeded}},
		// Revoked at wave 1, then reports approved again at wave 2 -- must not resume.
		ApprovalChecker: constantApprovalChecker(true, false, true),
	}

	if _, err := executor.Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if a := releaseFor(writer.nodeReleases, "node-a"); !a.Released {
		t.Errorf("wave 0 (approved): expected node-a released, got %#v", a)
	}
	if b := releaseFor(writer.nodeReleases, "node-b"); b.Released {
		t.Errorf("wave 1 (revoked): expected node-b NOT released, got %#v", b)
	}
	if c := releaseFor(writer.nodeReleases, "node-c"); c.Released {
		t.Errorf("wave 2 (checker reports approved again): expected node-c still NOT released -- revocation is sticky for the rest of the execution, got %#v", c)
	}
}

func TestExecutorTreatsApprovalCheckerErrorAsRevoked(t *testing.T) {
	writer := &fakeAuditWriter{}
	calls := 0
	executor := Executor{
		Writer: writer,
		Clock:  func() time.Time { return time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC) },
		NewID:  sequenceIDs(),
		Runner: fakeActionRunner{outcome: ActionOutcome{Outcome: OutcomeSucceeded}},
		ApprovalChecker: func(context.Context) (bool, error) {
			calls++
			if calls == 1 {
				return true, nil
			}
			return false, errors.New("api server unreachable")
		},
	}

	result, err := executor.Execute(context.Background(), twoWaveApprovalInput())
	// PL-32: a failed read must degrade gracefully to dry-run, the same as an explicit
	// revocation -- not abort the whole execution, and never assume the good case.
	if err != nil {
		t.Fatalf("Execute returned error for a checker read failure, want graceful degradation: %v", err)
	}
	if result.Phase != PhaseCompleted {
		t.Fatalf("expected completed result even with a checker read failure, got %#v", result)
	}
	if a := releaseFor(writer.nodeReleases, "node-a"); !a.Released {
		t.Errorf("wave 0 (approved): expected node-a released, got %#v", a)
	}
	if b := releaseFor(writer.nodeReleases, "node-b"); b.Released {
		t.Errorf("wave 1 (checker error): expected node-b NOT released, got %#v", b)
	}
}

func TestExecutorSkipsApprovalCheckWhenAlreadyDryRun(t *testing.T) {
	writer := &fakeAuditWriter{}
	called := false
	input := twoWaveApprovalInput()
	input.Mode = ModeDryRun
	executor := Executor{
		Writer: writer,
		Clock:  func() time.Time { return time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC) },
		NewID:  sequenceIDs(),
		Runner: fakeActionRunner{outcome: ActionOutcome{Outcome: OutcomeSucceeded}},
		ApprovalChecker: func(context.Context) (bool, error) {
			called = true
			return true, nil
		},
	}

	if _, err := executor.Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if called {
		t.Error("ApprovalChecker was called for a flow that was already dry-run; nothing was at risk of becoming effectful")
	}
}
