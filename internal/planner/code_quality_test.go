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

package planner

import (
	"strings"
	"testing"
	"time"
)

// This file covers the mechanical findings from the 2026-08-25 planner-code-quality sweep
// (F-118, F-119, F-121, F-122, F-123 -- docs/contributing/audits/planner-code-quality.md).
// F-120's fix lives in internal/shutdownflow, the adapter package the finding assigned it to.

// F-123: stableHash used to panic on a json.Marshal failure. The panic was already
// "effectively unreachable" through real planner inputs -- every field stableHash is ever asked
// to encode is a plain string, slice, or int pointer, none of which can fail to marshal -- so this
// tests the mechanism directly with a value that can, rather than trying to manufacture an
// unreachable real Trigger/Group/Plan that fails the same way.
func TestStableHashReturnsErrorRatherThanPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("stableHash panicked instead of returning an error: %v", r)
		}
	}()

	_, err := stableHash(make(chan int))
	if err == nil {
		t.Fatal("expected an error hashing an unencodable value, got nil")
	}
}

// F-121: a step id or group name repeated three or more times used to emit one diagnostic for
// every occurrence after the first -- two diagnostics for three occurrences, three for four, and
// so on. The duplication is correctly detected either way; what this guards is that the *output*
// says "this identifier is duplicated" exactly once, not once per repeat.
func TestValidateStructuralInputsReportsEachDuplicateIdentifierOnce(t *testing.T) {
	input := StructuralInputs{
		Steps: []Step{
			{ID: "shutdown-db", Action: "Wait"},
			{ID: "shutdown-db", Action: "Wait"},
			{ID: "shutdown-db", Action: "Wait"},
		},
		Groups: []Group{
			{Name: "databases", Action: "ScaleWorkload"},
			{Name: "databases", Action: "ScaleWorkload"},
			{Name: "databases", Action: "ScaleWorkload"},
		},
	}

	diagnostics := validateStructuralInputs(input)

	duplicateStepDiagnostics := countDiagnosticsWithReason(diagnostics, "DuplicateStepID")
	if duplicateStepDiagnostics != 1 {
		t.Fatalf("three occurrences of one step id produced %d DuplicateStepID diagnostics, want exactly 1: %+v",
			duplicateStepDiagnostics, diagnostics)
	}
	duplicateGroupDiagnostics := countDiagnosticsWithReason(diagnostics, "DuplicateGroupName")
	if duplicateGroupDiagnostics != 1 {
		t.Fatalf("three occurrences of one group name produced %d DuplicateGroupName diagnostics, want exactly 1: %+v",
			duplicateGroupDiagnostics, diagnostics)
	}
}

func countDiagnosticsWithReason(diagnostics []Diagnostic, reason string) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Reason == reason {
			count++
		}
	}
	return count
}

// F-118: sortTriggers used to recompute stableHash(triggers[i]) and stableHash(triggers[j]) on
// every comparison sort.SliceStable made -- O(n log n) reencodes for n distinct answers. This
// does not measure the call count directly (stableHash has no seam to intercept), but it does
// confirm the rewritten decorate-sort-copy shape still produces the deterministic, hash-ascending
// order the original inline-hashing comparator promised, across triggers whose only difference is
// in a field that changes their JSON encoding.
func TestSortTriggersOrdersDeterministicallyByContentHash(t *testing.T) {
	seconds := func(v int64) *int64 { return &v }
	triggers := []Trigger{
		{Type: "RuntimeBelow", RuntimeBelowSeconds: seconds(300)},
		{Type: "ChargeBelow", ChargeBelowPercent: int32Ptr(20)},
		{Type: "RuntimeBelow", RuntimeBelowSeconds: seconds(60)},
	}
	// Compute the same content hashes independently to know the expected order without
	// depending on sortTriggers' own internals to also produce it.
	expectedHashes := make([]string, len(triggers))
	for i, trigger := range triggers {
		hash, err := stableHash(trigger)
		if err != nil {
			t.Fatalf("stableHash(triggers[%d]) returned error: %v", i, err)
		}
		expectedHashes[i] = hash
	}

	if err := sortTriggers(triggers); err != nil {
		t.Fatalf("sortTriggers returned error: %v", err)
	}

	for i := 1; i < len(triggers); i++ {
		hash, err := stableHash(triggers[i])
		if err != nil {
			t.Fatalf("stableHash(triggers[%d]) returned error: %v", i, err)
		}
		previous, err := stableHash(triggers[i-1])
		if err != nil {
			t.Fatalf("stableHash(triggers[%d]) returned error: %v", i-1, err)
		}
		if previous > hash {
			t.Fatalf("triggers not sorted by ascending content hash at index %d: %q > %q", i, previous, hash)
		}
	}

	// Every hash from before the sort must still be present after it -- the decorate-sort-copy
	// rewrite must permute triggers, not drop or duplicate one.
	for _, expected := range expectedHashes {
		found := false
		for _, trigger := range triggers {
			hash, err := stableHash(trigger)
			if err != nil {
				t.Fatalf("stableHash returned error: %v", err)
			}
			if hash == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("hash %q present before sortTriggers is missing afterward; a trigger was dropped", expected)
		}
	}
}

func int32Ptr(v int32) *int32 { return &v }

// F-122: Duration.MarshalJSON moved from compiler.go to types.go, beside the type it serializes.
// The move is code organization only, so this locks in the behavior at its new location rather
// than re-testing what compiler_test.go's hash-stability assertions already cover indirectly.
func TestDurationMarshalsAsHumanReadableString(t *testing.T) {
	encoded, err := stableHash(Duration{Duration: 90 * time.Minute}) // 1h30m0s, in time.Duration nanoseconds
	if err != nil {
		t.Fatalf("stableHash(Duration) returned error: %v", err)
	}
	if encoded == "" {
		t.Fatal("expected a non-empty hash")
	}

	raw, err := Duration{Duration: 90 * time.Minute}.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	if !strings.Contains(string(raw), "1h30m0s") {
		t.Fatalf("expected the human-readable duration string in the encoding, got %s", raw)
	}
}
