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

package resolver

import "testing"

// This package's stableHash used to panic on a json.Marshal failure -- the same pattern
// internal/planner's stableHash carried and F-123 fixed there. The panic was already
// "effectively unreachable" through real inputs (ResolveStructural only ever hashes plain
// strings and capability.MatchResult slices), so this tests the mechanism directly with a value
// that can fail, rather than trying to manufacture an unreachable real bundle that does.
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
