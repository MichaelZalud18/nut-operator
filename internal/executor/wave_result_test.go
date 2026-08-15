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

import "testing"

// Degradation is first-wins, and nothing asserted it until now.
//
// Found while extracting absorbWave out of Execute: inverting the rule to last-wins left the whole
// suite green, so the property was load-bearing and undefended at once. It matters because the
// reason and message are what an operator reads to learn why an execution stopped being clean. The
// first wave to degrade holds the cause; a later one overwrites it with whatever the cause went on
// to produce, which is a symptom.
func TestDegradationKeepsTheFirstReasonNotTheLast(t *testing.T) {
	var result Result
	result.absorbWave(waveExecutionResult{
		Degraded:        true,
		DegradedReason:  "HookUnreachable",
		DegradedMessage: "the endpoint refused the connection",
	})
	result.absorbWave(waveExecutionResult{
		Degraded:        true,
		DegradedReason:  "GroupTimedOut",
		DegradedMessage: "the group did not clear in time",
	})

	if !result.Degraded {
		t.Fatal("a degraded wave did not mark the execution degraded")
	}
	if result.DegradedReason != "HookUnreachable" {
		t.Errorf("degraded reason is %q; a later wave overwrote the cause with a symptom", result.DegradedReason)
	}
	if result.DegradedMessage != "the endpoint refused the connection" {
		t.Errorf("degraded message is %q; it should still describe the first failure", result.DegradedMessage)
	}
}

func TestAbsorbWaveAccumulatesCountsAndOverruns(t *testing.T) {
	var result Result
	overrun := TierOverrun{WaveIndex: 1, Policy: "Wait"}
	result.absorbWave(waveExecutionResult{Groups: 2, ActionAttempts: 5, NodeReleases: 1})
	result.absorbWave(waveExecutionResult{Groups: 3, ActionAttempts: 4, NodeReleases: 2, TierOverrun: &overrun})

	if result.Groups != 5 || result.ActionAttempts != 9 || result.NodeReleases != 3 {
		t.Errorf("counts did not accumulate across waves: groups=%d attempts=%d releases=%d",
			result.Groups, result.ActionAttempts, result.NodeReleases)
	}
	if len(result.TierOverruns) != 1 {
		t.Fatalf("expected the one recorded overrun to survive, got %d", len(result.TierOverruns))
	}
	// A wave that did not overrun must not contribute a zero-valued entry, or every clean execution
	// would publish overruns that never happened.
	if result.TierOverruns[0].WaveIndex != 1 || result.TierOverruns[0].Policy != "Wait" {
		t.Errorf("recorded overrun is %+v, not the one the wave reported", result.TierOverruns[0])
	}
}

// drainPending must consume every channel even after one wave has already failed. The waves are
// already running; returning early would leak the goroutines still trying to send.
func TestDrainPendingConsumesEveryWaveAndKeepsTheFirstError(t *testing.T) {
	makeChan := func(run waveExecutionResult) <-chan waveExecutionResult {
		ch := make(chan waveExecutionResult, 1)
		ch <- run
		close(ch)
		return ch
	}
	first := errorWave("group-a", "first failure")
	second := errorWave("group-b", "second failure")
	pending := []<-chan waveExecutionResult{
		makeChan(first),
		makeChan(second),
		makeChan(waveExecutionResult{Groups: 1}),
	}

	var result Result
	err, failedGroup, _ := drainPending(pending, &result)
	if err == nil || err.Error() != "first failure" {
		t.Errorf("drainPending reported %v; the first error should win", err)
	}
	if failedGroup != "group-a" {
		t.Errorf("failed group is %q, not the first one to fail", failedGroup)
	}
	if result.Groups != 1 {
		t.Errorf("groups=%d; a wave after the failure was not drained into the result", result.Groups)
	}
}

func errorWave(group, message string) waveExecutionResult {
	return waveExecutionResult{Err: constError(message), FailedGroup: group}
}

type constError string

func (e constError) Error() string { return string(e) }
