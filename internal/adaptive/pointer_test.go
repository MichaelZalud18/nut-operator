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

package adaptive

import (
	"strings"
	"testing"
)

func runtimeOf(seconds int64) *int64 { return &seconds }

func onBattery(seconds int64) PowerObservation {
	return PowerObservation{OnBattery: true, RuntimeSeconds: runtimeOf(seconds), RuntimeTrusted: true}
}

func onMains(seconds int64) PowerObservation {
	return PowerObservation{RuntimeSeconds: runtimeOf(seconds), RuntimeTrusted: true}
}

// The first descent executes the starting tier rather than stepping past it. A flow declared to
// start at tier 5 that began by executing tier 4 would silently skip a tier of workloads.
func TestFirstDescentOccupiesTheStartingTier(t *testing.T) {
	state := PointerState{Tier: 5}

	next, movement := state.Descend(1)

	if next.Tier != 5 || !next.Started {
		t.Fatalf("state = %#v, want tier 5 started", next)
	}
	if movement.Direction != DirectionDescend || movement.To != 5 {
		t.Fatalf("movement = %#v, want a descent to 5", movement)
	}
}

func TestDescentStopsAtTheFloorTier(t *testing.T) {
	state := PointerState{Tier: 2, Deepest: 2, Started: true}

	state, _ = state.Descend(1)
	if state.Tier != 1 {
		t.Fatalf("tier = %d, want 1", state.Tier)
	}

	next, movement := state.Descend(1)
	if next.Tier != 1 || movement.Direction != DirectionHold {
		t.Fatalf("the pointer must not descend past the floor: %#v %#v", next, movement)
	}
}

// EX-27: ascent records that power improved and restores nothing. Deepest must survive it, because
// it is the record of how far the flow actually got.
func TestAscentIsBookkeepingAndKeepsTheDeepestTierReached(t *testing.T) {
	state := PointerState{Tier: 5, Started: true}
	for range 3 {
		state, _ = state.Descend(1)
	}
	if state.Deepest != 2 {
		t.Fatalf("deepest = %d, want 2 after three descents from 5", state.Deepest)
	}

	state, movement := state.Ascend(5)

	if movement.Direction != DirectionAscend || state.Tier != 3 {
		t.Fatalf("expected an ascent to tier 3, got %#v %#v", state, movement)
	}
	if state.Deepest != 2 {
		t.Fatalf("deepest = %d, want it preserved at 2; forgetting it makes a re-descent look new", state.Deepest)
	}
}

// EX-26: re-descending over already-executed tiers is expected and is reported as such, so a
// subscriber can tell a second dip from a first descent without diffing history.
func TestReDescentIsMarkedAsReexecution(t *testing.T) {
	state := PointerState{Tier: 5, Started: true}
	for range 3 {
		state, _ = state.Descend(1)
	}
	state, _ = state.Ascend(5)
	state, _ = state.Ascend(5)

	_, movement := state.Descend(1)

	if !movement.Reexecution {
		t.Fatalf("descending back over an executed tier must be flagged as re-execution, got %#v", movement)
	}
}

func TestFirstPassIntoNewTiersIsNotReexecution(t *testing.T) {
	state := PointerState{Tier: 5, Started: true, Deepest: 5}

	_, movement := state.Descend(1)

	if movement.Reexecution {
		t.Fatalf("a first descent into an unvisited tier is not re-execution, got %#v", movement)
	}
}

// EX-30: abort is halt, not undo. It stops descent and is one-way.
func TestHaltStopsDescentAndAscentAndDoesNotUndo(t *testing.T) {
	state := PointerState{Tier: 3, Deepest: 3, Started: true}.Halt()

	descended, descendMovement := state.Descend(1)
	if descended.Tier != 3 || descendMovement.Direction != DirectionHold {
		t.Fatalf("a halted pointer must not descend, got %#v %#v", descended, descendMovement)
	}

	ascended, ascendMovement := state.Ascend(5)
	if ascended.Tier != 3 || ascendMovement.Direction != DirectionHold {
		t.Fatalf("a halted pointer must not ascend, got %#v %#v", ascended, ascendMovement)
	}
	if !descended.Halted || !ascended.Halted {
		t.Fatal("halt must latch")
	}
}

// EX-25: the operator descends while power is lost and does not judge whether the outage is durable.
func TestDescentAndAscentConditionsAreInverses(t *testing.T) {
	for name, testCase := range map[string]struct {
		observation PowerObservation
		descend     bool
	}{
		"on battery":                {onBattery(1800), true},
		"low battery":               {PowerObservation{OnBattery: true, LowBattery: true}, true},
		"low battery without mains": {PowerObservation{LowBattery: true}, true},
		"on mains":                  {onMains(1800), false},
		// Unknown runtime must not stop a descent: on this path the optimistic verdict is "stay up".
		"on battery with unknown runtime": {PowerObservation{OnBattery: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ShouldDescend(testCase.observation); got != testCase.descend {
				t.Fatalf("ShouldDescend = %v, want %v", got, testCase.descend)
			}
			if got := ShouldAscend(testCase.observation); got == testCase.descend {
				t.Fatalf("ShouldAscend must be the inverse of ShouldDescend for %s", name)
			}
		})
	}
}

// The pointer must never end up below the floor or above the ceiling no matter how observations
// arrive. Driving it with an alternating sequence is the cheapest way to assert that.
func TestPointerStaysWithinBoundsUnderAlternatingPower(t *testing.T) {
	const floor, ceiling = int32(1), int32(5)
	state := PointerState{Tier: ceiling}

	observations := []PowerObservation{
		onBattery(900), onBattery(400), onMains(1800), onBattery(200),
		onMains(2000), onMains(2000), onBattery(100), onBattery(60),
		onMains(2400), onBattery(30),
	}
	for _, observation := range observations {
		if ShouldDescend(observation) {
			state, _ = state.Descend(floor)
		} else {
			state, _ = state.Ascend(ceiling)
		}
		if state.Tier < floor || state.Tier > ceiling {
			t.Fatalf("tier %d escaped [%d,%d]", state.Tier, floor, ceiling)
		}
		if state.Deepest < floor {
			t.Fatalf("deepest %d escaped the floor", state.Deepest)
		}
	}
}

// EX-28: the log line states the transition and the power observed, and never interprets it.
func TestDescribeStatesFactsWithoutInterpretation(t *testing.T) {
	movement := PointerMovement{From: 4, To: 3, Direction: DirectionDescend}

	line := movement.Describe(PowerObservation{OnBattery: true, LowBattery: true, RuntimeSeconds: runtimeOf(400)})

	for _, want := range []string{"entered tier 3", "OB LB", "runtime 400s"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
	for _, forbidden := range []string{"dip", "flicker", "recovery", "brownout"} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("line %q interprets the sequence; EX-28 forbids it", line)
		}
	}
}

func TestDescribeReportsUnknownRuntimeHonestly(t *testing.T) {
	movement := PointerMovement{From: 4, To: 3, Direction: DirectionDescend}

	line := movement.Describe(PowerObservation{OnBattery: true})

	if !strings.Contains(line, "runtime unknown") {
		t.Fatalf("line %q must say runtime is unknown rather than imply a value", line)
	}
}

// The state is persisted and handed back across executor restarts, so it can arrive with Deepest
// unset. Left unrepaired, the very next descent reports itself as re-execution when it is new work.
func TestNormalizeRepairsAnUnsetDeepestTier(t *testing.T) {
	restored := PointerState{Tier: 5, Started: true}

	_, movement := restored.Descend(1)

	if movement.Reexecution {
		t.Fatalf("a restored state with no recorded depth must not report new work as re-execution: %#v", movement)
	}
}

func TestNormalizeRepairsADeepestAboveTheCurrentTier(t *testing.T) {
	inconsistent := PointerState{Tier: 2, Deepest: 4, Started: true}

	next, _ := inconsistent.Descend(1)

	if next.Deepest != 1 {
		t.Fatalf("deepest = %d, want 1; deepest can never be above the tier reached", next.Deepest)
	}
}
