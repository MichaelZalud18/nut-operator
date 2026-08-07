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

package v1alpha1

import (
	"os"
	"regexp"
	"strings"
	"testing"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// This mirrors the same assertion in internal/controller. Both layers validate
// entity kinds independently by design; pinning each to the CRD's own enum
// marker is what keeps them from drifting apart silently. See the comment on
// declaredInventoryEntityKinds there for the full reasoning.
var webhookEntityKindEnumPattern = regexp.MustCompile(`\+kubebuilder:validation:Enum=(.+)\n\s*type PowerInventoryEntityKind`)

func TestWebhookAcceptsExactlyTheDeclaredInventoryEntityKinds(t *testing.T) {
	source, err := os.ReadFile("../../../api/v1alpha1/powerinventoryedge_types.go")
	if err != nil {
		t.Fatalf("read entity kind source: %v", err)
	}
	match := webhookEntityKindEnumPattern.FindSubmatch(source)
	if match == nil {
		t.Fatal("no kubebuilder Enum marker found on PowerInventoryEntityKind")
	}

	var kinds []string
	for _, value := range strings.Split(string(match[1]), ";") {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			kinds = append(kinds, trimmed)
		}
	}
	if len(kinds) == 0 {
		t.Fatal("entity kind enum marker is empty")
	}

	for _, kind := range kinds {
		if !isSupportedInventoryEntityKind(powerv1alpha1.PowerInventoryEntityKind(kind)) {
			t.Errorf("entity kind %q is accepted by the CRD schema but rejected by the webhook", kind)
		}
	}
	for _, rejected := range []string{"", "Switch", "PDU", "node"} {
		if isSupportedInventoryEntityKind(powerv1alpha1.PowerInventoryEntityKind(rejected)) {
			t.Errorf("entity kind %q is not in the CRD schema but the webhook accepts it", rejected)
		}
	}

	// supportedInventoryEntityKinds backs user-facing error messages; a kind the
	// webhook accepts but never names would produce a misleading rejection.
	listed := strings.Join(supportedInventoryEntityKinds(), ";")
	for _, kind := range kinds {
		if !strings.Contains(listed, kind) {
			t.Errorf("entity kind %q is accepted but missing from supportedInventoryEntityKinds", kind)
		}
	}
}
