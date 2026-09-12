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

package inventory

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestStableHashDoesNotPanicOnEncodingFailure(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("encoding failure panicked: %v", recovered)
		}
	}()
	// Current snapshot fields are JSON-safe; exercise the helper's failure contract directly.
	hash, err := stableHash(make(chan int))
	var unsupported *json.UnsupportedTypeError
	if hash != "" || !errors.As(err, &unsupported) {
		t.Fatalf("expected empty hash and wrapped encoding error, got %q, %v", hash, err)
	}
}

func TestStableHashRejectsNonFiniteValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		hash, err := stableHash(value)
		var unsupported *json.UnsupportedValueError
		if hash != "" || !errors.As(err, &unsupported) {
			t.Fatalf("expected empty hash and wrapped encoding error for %v, got %q, %v", value, hash, err)
		}
	}
}

func TestCompilePreservesSnapshotHashEncoding(t *testing.T) {
	topology, diagnostics, err := Compile(Snapshot{})
	if err != nil || len(diagnostics) != 0 {
		t.Fatalf("empty inventory rejected: %v, %#v", err, diagnostics)
	}
	// SHA-256 of the existing JSON encoding, "{}".
	const want = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	if topology.Hash != want {
		t.Fatalf("snapshot hash encoding changed: got %q, want %q", topology.Hash, want)
	}
}

func TestCompileEdgeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     string
		source    string
		duplicate bool
	}{
		{name: "same edge", input: "psu-a", duplicate: true},
		{name: "source does not distinguish edges", input: "psu-a", source: "second-provider", duplicate: true},
		{name: "distinct power input", input: "psu-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, diagnostics, err := Compile(Snapshot{
				Entities: []Entity{
					{ID: "ups", Kind: EntityKindUPSDevice, PowerDomains: []string{"domain"}},
					{ID: "node", Kind: EntityKindNode, CommunicationPathExempt: true},
				},
				Edges: []Edge{
					{From: "ups", To: "node", Relation: EdgeRelationFeeds, Input: "psu-a"},
					{From: "ups", To: "node", Relation: EdgeRelationFeeds, Input: tc.input, SourceID: tc.source},
				},
			})
			if tc.duplicate {
				if !errors.Is(err, ErrRejected) || !hasDiagnosticReason(diagnostics, "DuplicateEdge") {
					t.Fatalf("expected duplicate edge rejection: %v, %#v", err, diagnostics)
				}
			} else if err != nil {
				t.Fatalf("distinct edge rejected: %v, %#v", err, diagnostics)
			}
		})
	}
}
