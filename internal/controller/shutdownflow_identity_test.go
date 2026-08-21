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
	"testing"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/util/validation"
)

// A deliberately low-entropy stand-in for a SHA-256 digest. The length is what these tests turn
// on -- 64 hex characters, one over the Kubernetes label-value ceiling -- and a repeating pattern
// carries that without looking like a credential to a secret scanner.
const testEpisodeDigest = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

// F-100: the trigger-episode digest was used as execution_id directly, and execution_id is a uuid
// column in six tables. Every write failed with SQLSTATE 22P02, on every cluster.
func TestShutdownExecutionIdentityIsAUUID(t *testing.T) {
	id := shutdownExecutionIdentity(testEpisodeDigest)

	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("execution identity %q is not a UUID, so every audit write keyed on it fails: %v", id, err)
	}
	if parsed.Version() != 5 {
		t.Errorf("UUID version = %d, want 5 -- v5 is what makes the derivation deterministic", parsed.Version())
	}
}

// Determinism is what the digest was providing and what the derivation has to keep: the same
// trigger episode must land on the same primary key, so re-recording updates the row instead of
// creating a second one.
func TestShutdownExecutionIdentityIsDeterministic(t *testing.T) {
	first := shutdownExecutionIdentity(testEpisodeDigest)
	for range 20 {
		if got := shutdownExecutionIdentity(testEpisodeDigest); got != first {
			t.Fatalf("same episode produced two identities: %q and %q", first, got)
		}
	}

	other := shutdownExecutionIdentity("feedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedfacefeedface")
	if other == first {
		t.Error("two different episodes collapsed onto one execution identity")
	}
}

// The reason this is a UUID rather than widened `text` columns. The identity leaves PostgreSQL and
// is stamped on Kubernetes objects as power.zalud.io/execution, where 63 characters is the ceiling
// -- a 64-character digest is silently truncated there, so the label stops equalling the ID.
func TestShutdownExecutionIdentityFitsInALabelValue(t *testing.T) {
	id := shutdownExecutionIdentity(testEpisodeDigest)

	if errs := validation.IsValidLabelValue(id); len(errs) > 0 {
		t.Fatalf("execution identity %q is not a valid label value: %v", id, errs)
	}
	if len(testEpisodeDigest) <= validation.LabelValueMaxLength {
		t.Fatalf("the digest is %d characters, which no longer exceeds the %d-character label ceiling; "+
			"this test's premise needs rechecking", len(testEpisodeDigest), validation.LabelValueMaxLength)
	}
}

// An empty digest still has to yield a usable primary key. It means the episode key could not be
// computed, which is worth a distinct row rather than a write that fails.
func TestShutdownExecutionIdentityHandlesAnEmptyDigest(t *testing.T) {
	id := shutdownExecutionIdentity("")

	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("empty digest produced %q, which is not a UUID: %v", id, err)
	}
	// Random rather than derived, deliberately: with no episode content there is nothing to derive
	// from, and two unrelated runs must not share a primary key just because both keys were
	// missing.
	if id == shutdownExecutionIdentity("") {
		t.Error("two episodes with no digest collapsed onto one execution identity")
	}
}
