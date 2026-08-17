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

package haltwatch

import (
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var signalTime = time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)

func attempt(node string) Attempt {
	return Attempt{
		Node:            node,
		ShutdownFlow:    "rack-a-conservation",
		ExecutionID:     "exec-1",
		SignalWrittenAt: signalTime,
	}
}

func TestNodeStoppedResolvesTheAttempt(t *testing.T) {
	observer := NewObserver()
	observer.SignalWritten(attempt("node-a"))

	result, resolved := observer.NodeStopped("node-a", signalTime.Add(42*time.Second))
	if !resolved {
		t.Fatal("expected a pending attempt to resolve")
	}
	if result.Outcome != OutcomeHalted {
		t.Fatalf("expected Halted, got %q", result.Outcome)
	}
	if result.Duration != 42*time.Second {
		t.Fatalf("expected a 42s reconstruction, got %s", result.Duration)
	}
	if result.ShutdownFlow != "rack-a-conservation" || result.ExecutionID != "exec-1" {
		t.Fatalf("expected the attempt's provenance to survive into the result, got %#v", result)
	}
	if observer.Pending() != 0 {
		t.Fatalf("expected the attempt to be consumed, %d still pending", observer.Pending())
	}
}

// Nodes go NotReady constantly and for reasons this operator had nothing to do with. Counting those
// as halts would report a cluster that verifies its own shutdown path every time a kubelet hiccups.
func TestNodeStoppedIgnoresNodesNobodySignalled(t *testing.T) {
	observer := NewObserver()
	observer.SignalWritten(attempt("node-a"))

	if _, resolved := observer.NodeStopped("node-b", signalTime.Add(time.Minute)); resolved {
		t.Fatal("expected an unsignalled node to resolve nothing")
	}
	if observer.Pending() != 1 {
		t.Fatalf("expected node-a to stay pending, %d pending", observer.Pending())
	}
}

// The same node cannot be halted twice by one signal. Without consuming the attempt, every
// subsequent node update while it sat at NotReady would count another halt.
func TestNodeStoppedResolvesAtMostOnce(t *testing.T) {
	observer := NewObserver()
	observer.SignalWritten(attempt("node-a"))

	if _, resolved := observer.NodeStopped("node-a", signalTime.Add(time.Second)); !resolved {
		t.Fatal("expected the first observation to resolve")
	}
	if _, resolved := observer.NodeStopped("node-a", signalTime.Add(2*time.Second)); resolved {
		t.Fatal("expected the second observation to resolve nothing")
	}
}

// A node that was already unhealthy when it was signalled reports its last heartbeat from before the
// write. Zero is the honest reading of that -- no measurable interval -- where a negative duration
// would say the machine halted before anyone asked it to.
func TestNodeStoppedClampsHeartbeatsThatPrecedeTheSignal(t *testing.T) {
	observer := NewObserver()
	observer.SignalWritten(attempt("node-a"))

	result, resolved := observer.NodeStopped("node-a", signalTime.Add(-30*time.Second))
	if !resolved {
		t.Fatal("expected the attempt to resolve")
	}
	if result.Duration != 0 {
		t.Fatalf("expected a clamped zero duration, got %s", result.Duration)
	}
}

// A re-issued signal is the one the node is responding to. Keeping the first write's timestamp would
// charge the second halt with the time spent waiting on the first.
func TestSignalWrittenRestartsTheClockOnReissue(t *testing.T) {
	observer := NewObserver()
	observer.SignalWritten(attempt("node-a"))

	reissued := attempt("node-a")
	reissued.ExecutionID = "exec-2"
	reissued.SignalWrittenAt = signalTime.Add(5 * time.Minute)
	observer.SignalWritten(reissued)

	result, resolved := observer.NodeStopped("node-a", signalTime.Add(6*time.Minute))
	if !resolved {
		t.Fatal("expected the attempt to resolve")
	}
	if result.ExecutionID != "exec-2" {
		t.Fatalf("expected the re-issued execution, got %q", result.ExecutionID)
	}
	if result.Duration != time.Minute {
		t.Fatalf("expected the clock to restart at the re-issue, got %s", result.Duration)
	}
}

func TestExpireRecordsNodesThatNeverStopped(t *testing.T) {
	observer := NewObserver()
	observer.Deadline = 2 * time.Minute
	observer.SignalWritten(attempt("node-b"))
	observer.SignalWritten(attempt("node-a"))

	if expired := observer.Expire(signalTime.Add(90 * time.Second)); len(expired) != 0 {
		t.Fatalf("expected nothing inside the deadline to expire, got %#v", expired)
	}

	expired := observer.Expire(signalTime.Add(3 * time.Minute))
	if len(expired) != 2 {
		t.Fatalf("expected both attempts to expire, got %d", len(expired))
	}
	if expired[0].Node != "node-a" || expired[1].Node != "node-b" {
		t.Fatalf("expected results sorted by node, got %q then %q", expired[0].Node, expired[1].Node)
	}
	for _, result := range expired {
		if result.Outcome != OutcomeTimedOut {
			t.Fatalf("expected TimedOut for %s, got %q", result.Node, result.Outcome)
		}
	}
	if observer.Pending() != 0 {
		t.Fatalf("expected expiry to consume the attempts, %d pending", observer.Pending())
	}
}

// A halt that already resolved must not later expire as a failure. Expire walking a map it does not
// consume, or an observation that forgot to delete, would count one attempt as both.
func TestExpireSkipsAlreadyResolvedAttempts(t *testing.T) {
	observer := NewObserver()
	observer.Deadline = time.Minute
	observer.SignalWritten(attempt("node-a"))

	if _, resolved := observer.NodeStopped("node-a", signalTime.Add(10*time.Second)); !resolved {
		t.Fatal("expected the attempt to resolve")
	}
	if expired := observer.Expire(signalTime.Add(time.Hour)); len(expired) != 0 {
		t.Fatalf("expected a resolved attempt not to expire, got %#v", expired)
	}
}

func TestForgetDropsTheAttemptWithoutAnOutcome(t *testing.T) {
	observer := NewObserver()
	observer.Deadline = time.Minute
	observer.SignalWritten(attempt("node-a"))
	observer.Forget("node-a")

	if observer.Watching("node-a") {
		t.Fatal("expected the attempt to be gone")
	}
	if expired := observer.Expire(signalTime.Add(time.Hour)); len(expired) != 0 {
		t.Fatalf("expected a forgotten attempt to produce no outcome, got %#v", expired)
	}
}

// Watching is what lets the Node watch drop events before they become reconciles, so it has to be
// false in the steady state -- otherwise the filter admits everything and the cluster-wide watch
// costs what it was written to avoid.
func TestWatchingIsFalseWithNothingInFlight(t *testing.T) {
	observer := NewObserver()
	if observer.Watching("node-a") {
		t.Fatal("expected no node to be watched before any signal is written")
	}
	observer.SignalWritten(attempt("node-a"))
	if !observer.Watching("node-a") {
		t.Fatal("expected a signalled node to be watched")
	}
	if observer.Watching("node-b") {
		t.Fatal("expected an unsignalled node not to be watched")
	}
}

// The write happens on an executor goroutine and the observations arrive on controller workers.
// Run with -race; without the mutex this is a map written and read concurrently.
func TestObserverIsSafeForConcurrentUse(t *testing.T) {
	observer := NewObserver()
	var waitGroup sync.WaitGroup
	for i := range 32 {
		waitGroup.Add(3)
		node := string(rune('a' + i%26))
		go func() { defer waitGroup.Done(); observer.SignalWritten(attempt(node)) }()
		go func() { defer waitGroup.Done(); observer.NodeStopped(node, signalTime.Add(time.Second)) }()
		go func() { defer waitGroup.Done(); observer.Expire(signalTime.Add(time.Hour)) }()
	}
	waitGroup.Wait()
}

func nodeWithReady(status corev1.ConditionStatus, heartbeat, transition time.Time) *corev1.Node {
	return &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{
					Type:               corev1.NodeReady,
					Status:             status,
					LastHeartbeatTime:  metav1.NewTime(heartbeat),
					LastTransitionTime: metav1.NewTime(transition),
				},
			},
		},
	}
}

func TestReporting(t *testing.T) {
	ready := nodeWithReady(corev1.ConditionTrue, signalTime, signalTime)
	if !Reporting(ready) {
		t.Fatal("expected a Ready node to be reporting")
	}
	// Unknown is what the control plane writes when a kubelet stops posting, which is exactly the
	// state a halted node lands in. False (a node reporting itself unhealthy) is also not reporting.
	for _, status := range []corev1.ConditionStatus{corev1.ConditionUnknown, corev1.ConditionFalse} {
		if Reporting(nodeWithReady(status, signalTime, signalTime)) {
			t.Fatalf("expected Ready=%s not to count as reporting", status)
		}
	}
	if Reporting(&corev1.Node{}) {
		t.Fatal("expected a node with no Ready condition not to be reporting")
	}
	if Reporting(nil) {
		t.Fatal("expected a nil node not to be reporting")
	}
}

// The transition is written after the node-monitor grace period, so trusting it alone inflates every
// halt by that lag. The heartbeat is the last moment kubelet is known to have posted.
func TestLastAliveAtPrefersTheEarlierOfHeartbeatAndTransition(t *testing.T) {
	heartbeat := signalTime.Add(20 * time.Second)
	transition := signalTime.Add(60 * time.Second)

	if got := LastAliveAt(nodeWithReady(corev1.ConditionUnknown, heartbeat, transition)); !got.Equal(heartbeat) {
		t.Fatalf("expected the heartbeat %s, got %s", heartbeat, got)
	}
	// If a control plane rewrites the heartbeat on the transition, the transition is the earlier
	// value and nothing is lost. Taking the minimum is what makes the estimate correct either way,
	// without this code having to know which behaviour it is running against.
	if got := LastAliveAt(nodeWithReady(corev1.ConditionUnknown, transition, transition)); !got.Equal(transition) {
		t.Fatalf("expected the transition %s, got %s", transition, got)
	}
	if got := LastAliveAt(nodeWithReady(corev1.ConditionUnknown, time.Time{}, transition)); !got.Equal(transition) {
		t.Fatalf("expected the transition when no heartbeat is set, got %s", got)
	}
	if got := LastAliveAt(nodeWithReady(corev1.ConditionUnknown, heartbeat, time.Time{})); !got.Equal(heartbeat) {
		t.Fatalf("expected the heartbeat when no transition is set, got %s", got)
	}
	if got := LastAliveAt(&corev1.Node{}); !got.IsZero() {
		t.Fatalf("expected the zero time with no Ready condition, got %s", got)
	}
}
