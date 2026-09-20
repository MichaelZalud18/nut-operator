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
	for _, startup := range []string{"false", "true"} {
		t.Run("startup="+startup, func(t *testing.T) {
			t.Setenv("NUT_OPERATOR_E2E_STARTUP", startup)
			t.Run("default", func(t *testing.T) {
				assertKindSpecInventory(t, "false", "808e7a8447bea5da2e837bd970e70138b5fbaf0942f17c6b2c8f10c3fe57994b")
			})
			t.Run("soak", func(t *testing.T) {
				assertKindSpecInventory(t, "true", "64eaba2b3b66be1af8b8a330d6ed890fa7f326fc23c65919de51d2f884001849")
			})
		})
	}
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
			// NS-6 stays registered when disabled so runtime reports an explicit skip.
			// ENG-1 intentionally combines the two recovery assertions into one
			// timed spec, leaving 21/23 baseline specs. TEST-2, NS-6 and MOD-4 add one each.
			// Adding MOD-4's top-level container changes seeded container ordering;
			// fingerprints retain every original spec and its ordered/serial attributes.
			var specs []json.RawMessage
			if err := json.Unmarshal([]byte(inventory), &specs); err != nil {
				t.Fatal(err)
			}
			var original []json.RawMessage
			additions := 0
			startupAdditions := 0
			recoveryAdditions := 0
			modularAdditions := 0
			safetyAdditions := 0
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
				if strings.HasPrefix(spec.Text, "Manager EX-34 checks execution safety after a hook barrier: ") {
					safetyAdditions++
					if !validKindRegistration(spec.Ordered, spec.Serial, spec.State, spec.Labels, "EX-34") {
						t.Fatalf("EX-34 registration changed: %s", raw)
					}
				} else if spec.Text == "NUT-only "+nutOnlySpecName {
					modularAdditions++
					if !spec.Ordered || !spec.Serial || spec.State != "passed" || strings.Join(spec.Labels, ",") != "Serial,MOD-4" {
						t.Fatalf("MOD-4 registration changed: %s", raw)
					}
				} else if spec.Text == "Manager executes a logical ShutdownFlow from real dummy-ups telemetry through ordered drain and simulated actuation" {
					additions++
					if !validKindRegistration(spec.Ordered, spec.Serial, spec.State, spec.Labels, "TEST-2") {
						t.Fatalf("TEST-2 registration changed: %s", raw)
					}
				} else if spec.Text == "Manager "+startupSpecName {
					startupAdditions++
					if !validKindRegistration(spec.Ordered, spec.Serial, spec.State, spec.Labels, "NS-6") {
						t.Fatalf("NS-6 registration changed: %s", raw)
					}
				} else if strings.HasSuffix(spec.Text, "replaces a killed driver within 30 seconds without restarting the pod or sidecar") {
					recoveryAdditions++
					if !validKindRegistration(spec.Ordered, spec.Serial, spec.State, spec.Labels, "") {
						t.Fatalf("ENG-1 recovery registration changed: %s", raw)
					}
					original = append(original, raw)
				} else {
					original = append(original, raw)
				}
			}
			count := 21
			if soak == "true" {
				count = 23
			}
			if modularAdditions != 1 {
				t.Fatalf("want MOD-4=1, got %d", modularAdditions)
			}
			if safetyAdditions != 3 {
				t.Fatalf("want EX-34=3, got %d", safetyAdditions)
			}
			if additions != 1 || startupAdditions != 1 || recoveryAdditions != 1 || len(original) != count {
				t.Fatalf("want normalized original %d, TEST-2=1, NS-6=1, ENG-1=1; got %d, %d, %d, %d", count, len(original), additions, startupAdditions, recoveryAdditions)
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

func validKindRegistration(ordered, serial bool, state string, labels []string, label string) bool {
	if !ordered || serial || state != "passed" {
		return false
	}
	if label == "" {
		return len(labels) == 0
	}
	return len(labels) == 1 && labels[0] == label
}
