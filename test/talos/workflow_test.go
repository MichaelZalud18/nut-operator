//go:build talos

package talos

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestSmokeWorkflowsReserveCleanupBudget checks the same safety invariants test/hadron's own
// TestSmokeWorkflowsReserveCleanupBudget checks for every Hadron smoke workflow: every step
// bounded, cleanup unconditional, cleanup precedes artifact upload, and state removal gated on
// cleanup succeeding. Duplicated here rather than imported: the check itself is small,
// self-contained, and has no dependency on either package's own guest adapter -- promoting it into
// one shared location is the same VM-8 extraction adapter.go's own doc comment defers until a
// third guest adapter makes the overlap self-evident. Table-driven so a new smoke workflow (like
// talos-actuator-smoke.yml, added alongside talos-vm-boot-smoke.yml) is checked by construction
// rather than by remembering to copy this test too.
func TestSmokeWorkflowsReserveCleanupBudget(t *testing.T) {
	for _, tc := range []struct {
		file string
		job  string
	}{
		{"../../.github/workflows/talos-vm-boot-smoke.yml", "boot"},
		{"../../.github/workflows/talos-actuator-smoke.yml", "actuator"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatal(err)
			}
			var workflow struct {
				Jobs map[string]struct {
					Timeout int `json:"timeout-minutes"`
					Steps   []struct {
						Name    string `json:"name"`
						ID      string `json:"id"`
						If      string `json:"if"`
						Run     string `json:"run"`
						Uses    string `json:"uses"`
						Timeout int    `json:"timeout-minutes"`
					} `json:"steps"`
				} `json:"jobs"`
			}
			if err := yaml.Unmarshal(data, &workflow); err != nil {
				t.Fatal(err)
			}
			job, ok := workflow.Jobs[tc.job]
			if !ok {
				t.Fatalf("job %q not found in %s", tc.job, tc.file)
			}
			total, cleanupIndex := 0, -1
			for i, step := range job.Steps {
				if step.Timeout <= 0 {
					t.Errorf("unbounded step: %s", step.Name)
				}
				total += step.Timeout
				if step.ID == "cleanup" {
					cleanupIndex = i
					if !strings.Contains(step.If, "always()") {
						t.Error("cleanup must run after cancellation/failure")
					}
				}
				if strings.Contains(step.Uses, "actions/upload-artifact") && cleanupIndex < 0 {
					t.Error("process cleanup must precede artifact upload")
				}
				if strings.Contains(step.Run, "rm -rf") && !strings.Contains(step.If, "steps.cleanup.outcome == 'success'") {
					t.Error("state removal must require successful process cleanup")
				}
			}
			if cleanupIndex < 0 || total >= job.Timeout {
				t.Fatalf("cleanup missing or no job-level margin: steps=%dm job=%dm", total, job.Timeout)
			}
		})
	}
}
