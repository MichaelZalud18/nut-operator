package trigger

import (
	"slices"
	"testing"
	"time"
)

func TestEachUPSRequiresItsOwnHold(t *testing.T) {
	start := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	input := Inputs{ObservedAt: start, Triggers: []Trigger{{ID: "battery", Type: TypeOnBattery, For: time.Minute}}, UPSStates: []UPSState{{UPSDevice: "a", OnBattery: true}, {UPSDevice: "b"}}}
	first := Evaluate(input)
	input.Holds = first.Holds
	input.ObservedAt = start.Add(time.Minute)
	input.UPSStates[1].OnBattery = true
	second := Evaluate(input)
	if !slices.Equal(second.SelectedUPSDevices, []string{"a"}) {
		t.Fatalf("pending device selected: %v", second.SelectedUPSDevices)
	}
	if len(second.Holds) != 2 {
		t.Fatal("lost pending device hold")
	}
	input.Holds = second.Holds
	input.ObservedAt = start.Add(2 * time.Minute)
	third := Evaluate(input)
	if !slices.Equal(third.SelectedUPSDevices, []string{"a", "b"}) {
		t.Fatalf("eligible devices missing: %v", third.SelectedUPSDevices)
	}
	// A cleared condition drops its hold; a later recurrence starts fresh.
	input.Holds = third.Holds
	input.UPSStates[0].OnBattery = false
	input.Holds = Evaluate(input).Holds
	input.UPSStates[0].OnBattery = true
	input.ObservedAt = start.Add(3 * time.Minute)
	reset := Evaluate(input)
	if !slices.Equal(reset.SelectedUPSDevices, []string{"b"}) {
		t.Fatalf("reset hold reused: %v", reset.SelectedUPSDevices)
	}
}

func TestImmediateTriggerDoesNotBorrowAnotherTriggersHold(t *testing.T) {
	input := Inputs{ObservedAt: time.Now(), Triggers: []Trigger{
		{ID: "held", Type: TypeOnBattery, For: time.Minute},
		{ID: "immediate", Type: TypeOnBattery, UPSDevices: []string{"b"}},
	}, UPSStates: []UPSState{{UPSDevice: "a", OnBattery: true}, {UPSDevice: "b", OnBattery: true}}}
	got := Evaluate(input)
	if !slices.Equal(got.SelectedUPSDevices, []string{"b"}) {
		t.Fatalf("wrong eligible set: %v", got.SelectedUPSDevices)
	}
}
