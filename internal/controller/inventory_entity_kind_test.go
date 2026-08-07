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
	"os"
	"regexp"
	"testing"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// entityKindEnumPattern extracts the CRD-enforced entity kinds straight from the
// kubebuilder marker that generates them.
var entityKindEnumPattern = regexp.MustCompile(`\+kubebuilder:validation:Enum=(.+)\n\s*type PowerInventoryEntityKind`)

// declaredInventoryEntityKinds reads the entity kinds the API actually accepts.
//
// The controller and the admission webhook validate entity kinds independently,
// on purpose: the webhook rejects at admission, the controller re-checks objects
// that predate it or arrive with admission disabled. Sharing one helper across
// the packages would couple the two layers that are deliberately independent.
//
// What that costs is the risk of silent divergence — a fourth entity kind added
// to the API and taught to only one validator. Rather than merge the two, both
// are pinned here and in the webhook's mirror of this test to the same source of
// truth, so a new kind fails a build until every layer knows about it.
func declaredInventoryEntityKinds(t *testing.T, sourcePath string) []string {
	t.Helper()
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read entity kind source: %v", err)
	}
	match := entityKindEnumPattern.FindSubmatch(source)
	if match == nil {
		t.Fatalf("no kubebuilder Enum marker found on PowerInventoryEntityKind in %s", sourcePath)
	}
	kinds := splitEnumValues(string(match[1]))
	if len(kinds) == 0 {
		t.Fatalf("entity kind enum marker is empty in %s", sourcePath)
	}
	return kinds
}

func splitEnumValues(raw string) []string {
	var values []string
	for _, value := range regexp.MustCompile(`;`).Split(raw, -1) {
		trimmed := regexp.MustCompile(`\s+`).ReplaceAllString(value, "")
		if trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func TestControllerAcceptsExactlyTheDeclaredInventoryEntityKinds(t *testing.T) {
	kinds := declaredInventoryEntityKinds(t, "../../api/v1alpha1/powerinventoryedge_types.go")

	for _, kind := range kinds {
		if !isSupportedInventoryEntityKind(powerv1alpha1.PowerInventoryEntityKind(kind)) {
			t.Errorf("entity kind %q is accepted by the CRD schema but rejected by the controller", kind)
		}
	}
	for _, rejected := range []string{"", "Switch", "PDU", "node"} {
		if isSupportedInventoryEntityKind(powerv1alpha1.PowerInventoryEntityKind(rejected)) {
			t.Errorf("entity kind %q is not in the CRD schema but the controller accepts it", rejected)
		}
	}
}
