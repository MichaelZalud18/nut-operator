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
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/MichaelZalud18/nut-operator/internal/haltwatch"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
)

var haltSignalTime = time.Date(2026, 8, 17, 4, 0, 0, 0, time.UTC)

func haltScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return scheme
}

func haltTestNode(name string, status corev1.ConditionStatus, heartbeat time.Time) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             status,
				LastHeartbeatTime:  metav1.NewTime(heartbeat),
				LastTransitionTime: metav1.NewTime(heartbeat.Add(40 * time.Second)),
			}},
		},
	}
}

func reconcileHalt(t *testing.T, reconciler *NodeHaltReconciler, name string) {
	t.Helper()
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name},
	}); err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
}

// The whole point of the component: a node that was signalled and then went away leaves a durable
// record on the operator, because it cannot leave one on itself.
func TestNodeHaltPublishesTheReconstructionWhenASignalledNodeStops(t *testing.T) {
	node := haltTestNode("halt-observed", corev1.ConditionUnknown, haltSignalTime.Add(25*time.Second))
	observer := haltwatch.NewObserver()
	observer.SignalWritten(haltwatch.Attempt{
		Node:            "halt-observed",
		ShutdownFlow:    "halt-observed-flow",
		ExecutionID:     "exec-1",
		SignalWrittenAt: haltSignalTime,
	})
	reconciler := &NodeHaltReconciler{
		Client:   fake.NewClientBuilder().WithScheme(haltScheme(t)).WithObjects(node).Build(),
		Observer: observer,
		Clock:    func() time.Time { return haltSignalTime.Add(time.Hour) },
	}

	before := testutil.ToFloat64(metrics.HaltAttemptsTotal.WithLabelValues("halt-observed-flow", "Halted"))
	reconcileHalt(t, reconciler, "halt-observed")

	if got := testutil.ToFloat64(metrics.HaltAttemptsTotal.WithLabelValues("halt-observed-flow", "Halted")); got != before+1 {
		t.Fatalf("expected one Halted attempt to be counted, went from %v to %v", before, got)
	}
	// The heartbeat, not the transition. The transition is 40s later in the fixture precisely
	// because that lag is what the reconstruction has to avoid charging to the node.
	if got := testutil.ToFloat64(metrics.HaltDurationSeconds.WithLabelValues("halt-observed")); got != 25 {
		t.Fatalf("expected a 25s reconstruction taken from the last heartbeat, got %v", got)
	}
	want := float64(haltSignalTime.Add(25 * time.Second).Unix())
	if got := testutil.ToFloat64(metrics.HaltLastVerifiedTimestampSeconds.WithLabelValues("halt-observed")); got != want {
		t.Fatalf("expected last-verified %v, got %v", want, got)
	}
}

// A node that was told to stop and kept running is the failure this exists to surface. It must be
// counted, and it must not touch the per-node evidence -- "when was this node last asked to halt" is
// a different question from "when was this node last proven to halt", and the second one is the one
// somebody trusts months later.
func TestNodeHaltCountsTheTimeoutWithoutClaimingVerification(t *testing.T) {
	observer := haltwatch.NewObserver()
	observer.Deadline = time.Minute
	observer.SignalWritten(haltwatch.Attempt{
		Node:            "halt-timeout",
		ShutdownFlow:    "halt-timeout-flow",
		ExecutionID:     "exec-1",
		SignalWrittenAt: haltSignalTime,
	})
	expired := observer.Expire(haltSignalTime.Add(2 * time.Minute))
	if len(expired) != 1 {
		t.Fatalf("expected the attempt to expire, got %d results", len(expired))
	}
	publishHaltResult(expired[0])

	if got := testutil.ToFloat64(metrics.HaltAttemptsTotal.WithLabelValues("halt-timeout-flow", "TimedOut")); got != 1 {
		t.Fatalf("expected one TimedOut attempt, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.HaltLastVerifiedTimestampSeconds.WithLabelValues("halt-timeout")); got != 0 {
		t.Fatalf("expected a failed attempt to leave no last-verified timestamp, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.HaltDurationSeconds.WithLabelValues("halt-timeout")); got != 0 {
		t.Fatalf("expected a failed attempt to publish no duration, got %v", got)
	}
}

// A node still reporting Ready has not halted, whatever else changed on the object. Without this the
// first label or taint edit after a signal would be recorded as a successful shutdown.
func TestNodeHaltIgnoresNodesStillReportingReady(t *testing.T) {
	node := haltTestNode("halt-still-ready", corev1.ConditionTrue, haltSignalTime.Add(time.Second))
	observer := haltwatch.NewObserver()
	observer.SignalWritten(haltwatch.Attempt{
		Node:            "halt-still-ready",
		ShutdownFlow:    "halt-still-ready-flow",
		ExecutionID:     "exec-1",
		SignalWrittenAt: haltSignalTime,
	})
	reconciler := &NodeHaltReconciler{
		Client:   fake.NewClientBuilder().WithScheme(haltScheme(t)).WithObjects(node).Build(),
		Observer: observer,
	}

	reconcileHalt(t, reconciler, "halt-still-ready")

	if got := testutil.ToFloat64(metrics.HaltAttemptsTotal.WithLabelValues("halt-still-ready-flow", "Halted")); got != 0 {
		t.Fatalf("expected no halt to be recorded for a Ready node, got %v", got)
	}
	if !observer.Watching("halt-still-ready") {
		t.Fatal("expected the attempt to stay pending while the node is still up")
	}
}

// A powered-off node keeps its Node object at NotReady. An object that is actually gone is a machine
// removed from the cluster, and leaving its series behind would accumulate one per rebuild.
func TestNodeHaltDropsSeriesForDeletedNodes(t *testing.T) {
	observer := haltwatch.NewObserver()
	observer.SignalWritten(haltwatch.Attempt{
		Node:            "halt-deleted",
		ShutdownFlow:    "halt-deleted-flow",
		ExecutionID:     "exec-1",
		SignalWrittenAt: haltSignalTime,
	})
	metrics.HaltLastVerifiedTimestampSeconds.WithLabelValues("halt-deleted").Set(1)
	metrics.HaltDurationSeconds.WithLabelValues("halt-deleted").Set(1)

	reconciler := &NodeHaltReconciler{
		Client:   fake.NewClientBuilder().WithScheme(haltScheme(t)).Build(),
		Observer: observer,
	}
	reconcileHalt(t, reconciler, "halt-deleted")

	if observer.Watching("halt-deleted") {
		t.Fatal("expected a deleted node's attempt to be forgotten rather than resolved")
	}
	if got := testutil.ToFloat64(metrics.HaltLastVerifiedTimestampSeconds.WithLabelValues("halt-deleted")); got != 0 {
		t.Fatalf("expected the last-verified series to be dropped, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.HaltAttemptsTotal.WithLabelValues("halt-deleted-flow", "Halted")); got != 0 {
		t.Fatalf("expected a deleted node not to count as halted, got %v", got)
	}
}
