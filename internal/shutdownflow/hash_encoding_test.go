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

package shutdownflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func TestHashEncodingFailureReturnsError(t *testing.T) {
	// API target fields are JSON-safe; test the encoding failure mechanism directly.
	hash, err := stableHash(make(chan int))
	var unsupported *json.UnsupportedTypeError
	if hash != "" || !errors.As(err, &unsupported) {
		t.Fatalf("expected empty hash and wrapped JSON error, got %q, %v", hash, err)
	}
}

func TestIdentitySetDiscardsPartialHashes(t *testing.T) {
	hashes, err := identitySet([]any{"valid first item", make(chan int)})
	var unsupported *json.UnsupportedTypeError
	if hashes != nil || !errors.As(err, &unsupported) {
		t.Fatalf("expected no partial hashes and wrapped JSON error, got %#v, %v", hashes, err)
	}
}

func TestIdentitySetPreservesOrderingAndEmptySemantics(t *testing.T) {
	for _, values := range [][]string{nil, {}} {
		hashes, err := identitySet(values)
		if hashes != nil || err != nil {
			t.Fatalf("expected nil empty set, got %#v, %v", hashes, err)
		}
	}
	first, err := identitySet([]string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := identitySet([]string{"b", "a"})
	if err != nil || !reflect.DeepEqual(first, second) || len(first) != 2 {
		t.Fatalf("identity ordering changed: %#v, %#v, %v", first, second, err)
	}
}

func TestEmptyTargetHashCompatibility(t *testing.T) {
	target, err := PlannerTarget(power.ShutdownStepTarget{})
	if err != nil {
		t.Fatal(err)
	}
	// SHA-256 of the pre-change canonical empty target JSON, with all seven fields null.
	const want = "33a04b2ca121f10d2e1ea6dcd002398e5f8876b6e5c6462ba812525a67f3468e"
	if target.IdentityHash != want {
		t.Fatalf("canonical target hash changed: got %q, want %q", target.IdentityHash, want)
	}
}
