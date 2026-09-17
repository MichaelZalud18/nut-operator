//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

// Preview in a child process so this check neither invokes cluster setup nor
// interferes with the real suite's Ginkgo state or ownership guard.
func TestKindSpecRegistration(t *testing.T) {
	const marker = "SPEC-INVENTORY:"
	if os.Getenv("NUT_OPERATOR_SPEC_PREVIEW_CHILD") == "1" {
		type spec struct {
			Text    string
			Labels  []string
			Ordered bool
			Serial  bool
			State   string
		}
		var inventory []spec
		for _, report := range ginkgo.PreviewSpecs("e2e suite").SpecReports {
			if report.LeafNodeType == types.NodeTypeIt {
				inventory = append(inventory, spec{report.FullText(), report.Labels(), report.IsInOrderedContainer, report.IsSerial, report.State.String()})
			}
		}
		data, err := json.Marshal(inventory)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s%s\n", marker, data)
		return
	}
	t.Run("default", func(t *testing.T) {
		assertKindSpecInventory(t, "false", "72858e7e90a4da80ccf775760dbd29712b6012d16e381fb23f5204533f012f0f")
	})
	t.Run("soak", func(t *testing.T) {
		assertKindSpecInventory(t, "true", "1c10d539bf9fd0f09234de575efc6ef5b58893c18abc2d629e920d5b3ceb6845")
	})
}

func assertKindSpecInventory(t *testing.T, soak, want string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestKindSpecRegistration$", "-ginkgo.seed=1")
	cmd.Env = append(os.Environ(), "NUT_OPERATOR_SPEC_PREVIEW_CHILD=1", "NUT_OPERATOR_E2E_SOAK="+soak,
		"NUT_OPERATOR_E2E_SOAK_CYCLES=10", "NUT_OPERATOR_E2E_SOAK_POD_RESTART_CYCLES=8")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("preview registrations: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if inventory, found := strings.CutPrefix(line, "SPEC-INVENTORY:"); found {
			// Preserve the original 22/24-spec inventory byte for byte while explicitly
			// allowing exactly one TEST-2 addition inside the shared Manager lifecycle.
			var specs []json.RawMessage
			if err := json.Unmarshal([]byte(inventory), &specs); err != nil {
				t.Fatal(err)
			}
			var original []json.RawMessage
			additions := 0
			for _, raw := range specs {
				var spec struct {
					Text            string
					Labels          []string
					Ordered, Serial bool
					State           string
				}
				if err := json.Unmarshal(raw, &spec); err != nil {
					t.Fatal(err)
				}
				if spec.Text == "Manager executes a logical ShutdownFlow from real dummy-ups telemetry through ordered drain and simulated actuation" {
					additions++
					if !spec.Ordered || spec.Serial || spec.State != "passed" || len(spec.Labels) != 1 || spec.Labels[0] != "TEST-2" {
						t.Fatalf("TEST-2 registration changed: %s", raw)
					}
				} else {
					original = append(original, raw)
				}
			}
			count := 22
			if soak == "true" {
				count = 24
			}
			if additions != 1 || len(original) != count {
				t.Fatalf("want original %d specs plus one TEST-2, got %d + %d", count, len(original), additions)
			}
			baseline, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(baseline)); got != want {
				t.Fatalf("spec registration fingerprint = %s, want %s\n%s", got, want, inventory)
			}
			return
		}
	}
	t.Fatalf("preview returned no registration inventory:\n%s", output)
}
