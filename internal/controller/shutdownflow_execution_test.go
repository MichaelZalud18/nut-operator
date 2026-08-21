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

package controller

import (
	"reflect"
	"testing"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
)

func releaseNames(releases []executorpkg.NodeRelease) []string {
	names := make([]string, 0, len(releases))
	for _, release := range releases {
		names = append(names, release.NodeName)
	}
	return names
}

// OD-18: a blocked node must never reach the executor as a power-off candidate.
// Publishing the block in status and then powering the node off anyway would be
// worse than not detecting the inversion at all.
func TestWithholdBlockedNodeReleasesDropsBlockedNodes(t *testing.T) {
	releases := []executorpkg.NodeRelease{
		{NodeName: "node-a"},
		{NodeName: "node-b"},
		{NodeName: "node-c"},
	}
	blocked := blockedNodeNames([]powerv1alpha1.BlockedNodeReleaseStatus{
		{NodeName: "node-b"},
	})

	kept := releaseNames(withholdBlockedNodeReleases(releases, blocked))
	if !reflect.DeepEqual(kept, []string{"node-a", "node-c"}) {
		t.Fatalf("expected node-b to be withheld, got %#v", kept)
	}
}

// The unblocked flow must be untouched: blocking is an exception, and every node
// still powering off is the normal path.
func TestWithholdBlockedNodeReleasesKeepsEverythingWhenNothingIsBlocked(t *testing.T) {
	releases := []executorpkg.NodeRelease{
		{NodeName: "node-a"},
		{NodeName: "node-b"},
	}

	kept := releaseNames(withholdBlockedNodeReleases(releases, blockedNodeNames(nil)))
	if !reflect.DeepEqual(kept, []string{"node-a", "node-b"}) {
		t.Fatalf("expected every release to survive, got %#v", kept)
	}
}

// A flow whose every node is blocked still executes. The remaining groups do
// their work; the plan simply powers nothing off, which is the intended failure
// direction rather than an error.
func TestWithholdBlockedNodeReleasesCanEmptyTheReleaseSet(t *testing.T) {
	releases := []executorpkg.NodeRelease{{NodeName: "node-a"}}
	blocked := blockedNodeNames([]powerv1alpha1.BlockedNodeReleaseStatus{
		{NodeName: "node-a"},
	})

	if kept := withholdBlockedNodeReleases(releases, blocked); len(kept) != 0 {
		t.Fatalf("expected no releases to survive, got %#v", releaseNames(kept))
	}
}

// F-106: reason and message are one statement. Deactivation rewrote the reason and left the
// message from the live episode behind, so a flow published TriggerNotEligible beside "eligible
// trigger episode already has execution evidence" -- two claims that cannot both be true.
func TestDeactivateLastExecutionClearsTheEpisodeMessage(t *testing.T) {
	for _, reason := range []string{"AlreadyExecuted", "RehearsalAlreadyExecuted"} {
		status := &powerv1alpha1.ShutdownExecutionStatus{
			TriggerActive: true,
			Reason:        reason,
			Message:       "eligible trigger episode already has execution evidence",
		}
		deactivateLastExecution(&status)

		if status.TriggerActive {
			t.Errorf("%s: trigger episode is still active after deactivation", reason)
		}
		if status.Reason != triggerNotEligibleReason {
			t.Errorf("%s: reason is %q, want %q", reason, status.Reason, triggerNotEligibleReason)
		}
		if status.Message != triggerNotEligibleMessage {
			t.Errorf("%s: message is %q, want %q", reason, status.Message, triggerNotEligibleMessage)
		}
	}
}

// A reason this function does not own must survive untouched: deactivation reports that the
// episode ended, not that whatever else happened during it is no longer true.
func TestDeactivateLastExecutionLeavesUnrelatedReasonsAlone(t *testing.T) {
	status := &powerv1alpha1.ShutdownExecutionStatus{
		TriggerActive: true,
		Reason:        "ExecutionFailed",
		Message:       "wave 2 action attempt exhausted its retries",
	}
	deactivateLastExecution(&status)

	if status.TriggerActive {
		t.Error("trigger episode is still active after deactivation")
	}
	if status.Reason != "ExecutionFailed" || status.Message != "wave 2 action attempt exhausted its retries" {
		t.Errorf("deactivation rewrote an unrelated reason/message pair: %q / %q", status.Reason, status.Message)
	}
}
