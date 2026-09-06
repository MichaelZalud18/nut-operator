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

package polling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nut"
)

// F-110: long-duration coverage does not require a test that runs for a long time -- it
// requires the thing under test to behave correctly across many cycles of a long-running
// process, which a fake Clock can compress into milliseconds. Options.Clock is the poller's
// only source of "now", so driving it by hand rather than letting time.Now() run proves the
// poller's own claim: ObservedAt reflects the clock passed to it, not wall time, which is what
// makes every timestamp in this operator's audit trail attributable to a poll cycle rather
// than to whenever the test happened to run.

// stepClock is a hand-advanced Clock: each call to Poll reads whatever it was last set to, and
// the test advances it before the next call. This is deliberately not a ticking clock -- the
// property under test is that the poller reads the clock at call time, not that it schedules
// anything, which is the controller's job (RequeueAfter), not the poller's.
type stepClock struct {
	now time.Time
}

func (c *stepClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func (c *stepClock) Clock() time.Time {
	return c.now
}

// TestPollerFakeClockSoakSurvivesRepeatedCycles simulates a multi-hour polling run (500 cycles
// at the operator's typical 15s telemetry cadence, ~2h4m of simulated time) in well under a
// second of real test time. One in seven cycles fails, modeling a NUT endpoint that is
// intermittently unreachable across a long run rather than cleanly up or down.
//
// Two things are asserted per cycle, and both are the kind of bug that only a multi-cycle run
// would catch: the emitted ObservedAt matches the fake clock's value at that specific cycle
// (not the clock's value at some other cycle, and not real wall time), and a failed cycle
// leaves no residue that corrupts the next successful one -- the poller holds no mutable
// cross-call state, and this is what proves it rather than assumes it.
func TestPollerFakeClockSoakSurvivesRepeatedCycles(t *testing.T) {
	const (
		cycles       = 500
		cadence      = 15 * time.Second
		failEveryNth = 7
	)

	clock := &stepClock{now: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)}
	client := &soakVariableClient{}
	poller := NewPoller(Options{
		Client: client,
		Clock:  clock.Clock,
	})

	target := Target{
		UPSDevice: "rack-a",
		NUTServer: "server-a",
		NUTName:   "rack-a",
		Host:      "upsd.power-system.svc.cluster.local",
	}

	started := time.Now()
	var successes, failures int
	for cycle := 0; cycle < cycles; cycle++ {
		clock.advance(cadence)
		expectedObservedAt := clock.now

		client.failNext = cycle%failEveryNth == 0

		result, err := poller.Poll(context.Background(), target)
		if client.failNext {
			failures++
			if err == nil {
				t.Fatalf("cycle %d: expected the simulated endpoint failure to surface", cycle)
			}
			continue
		}

		successes++
		if err != nil {
			t.Fatalf("cycle %d: expected success following a failed cycle to recover cleanly, got %v", cycle, err)
		}
		if !result.Snapshot.ObservedAt.Equal(expectedObservedAt) {
			t.Fatalf("cycle %d: expected ObservedAt %s (the fake clock's value for this cycle), got %s",
				cycle, expectedObservedAt, result.Snapshot.ObservedAt)
		}
	}
	elapsed := time.Since(started)

	if successes == 0 || failures == 0 {
		t.Fatalf("fixture did not exercise both outcomes: %d successes, %d failures", successes, failures)
	}
	simulatedDuration := time.Duration(cycles) * cadence
	if elapsed > 5*time.Second {
		t.Fatalf("simulating %s of polling took %s of real time -- this test exists to compress that ratio, not spend it", simulatedDuration, elapsed)
	}
}

// soakVariableClient alternates between answering and failing under the caller's control, and
// carries no state that would let a bug hide behind a fixed pass/fail pattern.
type soakVariableClient struct {
	failNext bool
}

func (c *soakVariableClient) ListVariables(_ context.Context, _ nut.Target) (map[string]string, error) {
	if c.failNext {
		return nil, errors.New("simulated NUT endpoint unreachable")
	}
	return map[string]string{
		"ups.status":      "OL",
		"battery.charge":  "97",
		"battery.runtime": "3600",
		"ups.load":        "22",
	}, nil
}
