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

// Package haltwatch reconstructs, from the operator's side, whether a node asked to power off
// actually stopped -- and how long it took.
//
// A node that powered off cannot report that it powered off. The actuator times its own sync(2) and
// logs the syscall, but that log lives on a machine which halts immediately afterwards, so whether a
// collector ships it first is a race, and one that loses precisely in the slow-sync case the
// measurement exists to capture. The actuator cannot close that gap itself and must not try: it
// holds no Kubernetes API token by design (OD-37, NA-2) and that stays.
//
// So the record is kept here instead, on the one side of the exchange that is still running
// afterwards. Both ends of the measurement are things the operator can see on its own: it wrote the
// signal, and it watches the Node object stop reporting.
//
// # What the reconstruction is and is not
//
// It is coarser than the actuator's own number. Between the two endpoints sit kubelet's projection
// of the updated Secret, the actuator's poll interval, the flush, the syscall, and the delay before
// the control plane concludes the node is gone. It is an upper bound on the halt, not a measurement
// of the flush.
//
// It is also not proof of a clean shutdown. A node that goes NotReady while a halt is pending is
// recorded as halted, and a network partition at the wrong moment looks identical from here. That is
// the honest limit of an observation made from the other end of the network the node just left.
//
// What it does do is survive. The precise number may or may not arrive; this one is written by a
// process that is still up.
package haltwatch

import (
	"sort"
	"sync"
	"time"
)

// DefaultDeadline is how long a signalled node has to stop reporting before the attempt is recorded
// as having failed.
//
// It is not a timeout on the halt itself -- nothing here can cancel anything. It bounds how long an
// attempt stays unresolved, so a node that simply never halts produces a recorded failure rather than
// silence.
//
// Generous on purpose, because every delay in the chain lands inside it: the actuator's poll
// interval, a bounded sync(2), and then the control plane's own node-monitor grace period before a
// silent kubelet is called NotReady. Too tight and a successful halt is filed as a failure, which is
// the worse error of the two -- a false failure sends someone to investigate a node that did exactly
// what it was told.
const DefaultDeadline = 10 * time.Minute

// Outcome is how a halt attempt resolved.
type Outcome string

const (
	// OutcomeHalted means the node stopped reporting Ready after being signalled.
	OutcomeHalted Outcome = "Halted"
	// OutcomeTimedOut means the node was still reporting Ready when the deadline passed. The
	// signal was delivered and the machine kept running.
	OutcomeTimedOut Outcome = "TimedOut"
)

// Attempt is one node's shutdown signal, recorded at the moment it reached the API server.
type Attempt struct {
	Node            string
	ShutdownFlow    string
	ExecutionID     string
	SignalWrittenAt time.Time
}

// Result is a resolved attempt.
type Result struct {
	Attempt
	Outcome Outcome
	// ObservedAt is when the node was last seen alive, for OutcomeHalted, or when the deadline
	// passed, for OutcomeTimedOut.
	ObservedAt time.Time
	// Duration is ObservedAt minus SignalWrittenAt. Never negative.
	Duration time.Duration
}

// Observer tracks signalled nodes until they stop reporting or run out of time.
//
// It is safe for concurrent use: the signal write happens on an executor goroutine and the node
// observations arrive on a controller's workers.
type Observer struct {
	// Deadline overrides DefaultDeadline. Zero means the default.
	Deadline time.Duration

	mu      sync.Mutex
	pending map[string]Attempt
}

// NewObserver returns an Observer with the default deadline.
func NewObserver() *Observer {
	return &Observer{pending: map[string]Attempt{}}
}

func (o *Observer) deadline() time.Duration {
	if o.Deadline > 0 {
		return o.Deadline
	}
	return DefaultDeadline
}

// SignalWritten records that a shutdown signal for one node reached the API server.
//
// Called after the write succeeds rather than before it. A Secret write the API server rejected is
// not an attempt to halt anything, and counting it as one would report failures against nodes that
// were never asked to stop.
//
// A second signal for a node that already has one pending replaces it and restarts the clock. That
// is a re-issue -- a retried wave, a re-entered execution -- and the node's halt is a response to
// the most recent thing it was told, not the first.
func (o *Observer) SignalWritten(attempt Attempt) {
	if attempt.Node == "" || attempt.SignalWrittenAt.IsZero() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pending == nil {
		o.pending = map[string]Attempt{}
	}
	o.pending[attempt.Node] = attempt
}

// NodeStopped resolves a pending attempt because the node stopped reporting Ready.
//
// observedAt should be the last moment the node was known to be alive, not the moment the control
// plane concluded it was gone -- see LastAliveAt. It is clamped to the signal-write time, so a
// heartbeat that predates the signal reports a zero duration instead of a negative one; that happens
// when a node was already unhealthy when it was signalled, and the honest reading of it is "no
// measurable interval", not "halted before it was asked to".
//
// Returns false when nothing was pending for this node, which is the common case: nodes go NotReady
// for reasons that have nothing to do with this operator, and those are not halt attempts.
func (o *Observer) NodeStopped(node string, observedAt time.Time) (Result, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	attempt, pending := o.pending[node]
	if !pending {
		return Result{}, false
	}
	delete(o.pending, node)
	duration := observedAt.Sub(attempt.SignalWrittenAt)
	if duration < 0 {
		duration = 0
	}
	return Result{
		Attempt:    attempt,
		Outcome:    OutcomeHalted,
		ObservedAt: observedAt,
		Duration:   duration,
	}, true
}

// Expire resolves every attempt whose deadline has passed, as OutcomeTimedOut.
//
// Results come back sorted by node name so a sweep produces a stable order in logs and tests.
func (o *Observer) Expire(now time.Time) []Result {
	o.mu.Lock()
	defer o.mu.Unlock()
	deadline := o.deadline()
	var expired []Result
	for node, attempt := range o.pending {
		if now.Sub(attempt.SignalWrittenAt) <= deadline {
			continue
		}
		delete(o.pending, node)
		expired = append(expired, Result{
			Attempt:    attempt,
			Outcome:    OutcomeTimedOut,
			ObservedAt: now,
			Duration:   now.Sub(attempt.SignalWrittenAt),
		})
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].Node < expired[j].Node })
	return expired
}

// Watching reports whether this node has an unresolved attempt.
//
// It is what lets the Node watch stay quiet: with nothing pending there is nothing any node update
// could resolve, so the predicate can drop the event before it becomes a reconcile.
func (o *Observer) Watching(node string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, pending := o.pending[node]
	return pending
}

// Forget drops any pending attempt for a node, without recording an outcome.
//
// For nodes deleted from the cluster. A node that powered off keeps its Node object -- it goes
// NotReady and stays there -- so an actual deletion means someone removed it, and an attempt to halt
// a machine that is no longer part of the cluster has no outcome worth recording either way.
func (o *Observer) Forget(node string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.pending, node)
}

// Pending returns the number of unresolved attempts.
func (o *Observer) Pending() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}
