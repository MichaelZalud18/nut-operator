//go:build hadron

package hadron

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// Every workflow that boots a real guest and tears it down shares the same safety invariants:
// every step bounded, cleanup unconditional, cleanup precedes artifact upload, and state removal
// gated on cleanup succeeding. Table-driven so a new smoke workflow (like
// hadron-cluster-link-smoke.yml, hadron-actuator-smoke.yml, hadron-operator-smoke.yml,
// hadron-ups-stack-smoke.yml, hadron-outage-flow-smoke.yml, or
// hadron-actuator-daemonset-smoke.yml, added alongside hadron-vm-boot-smoke.yml) is checked by
// construction rather than by remembering to copy this test too.
func TestSmokeWorkflowsReserveCleanupBudget(t *testing.T) {
	for _, tc := range []struct {
		file string
		job  string
	}{
		{"../../.github/workflows/hadron-vm-boot-smoke.yml", "boot"},
		{"../../.github/workflows/hadron-cluster-link-smoke.yml", "cluster-link"},
		{"../../.github/workflows/hadron-actuator-smoke.yml", "actuator-arms"},
		{"../../.github/workflows/hadron-operator-smoke.yml", "operator-deploys"},
		{"../../.github/workflows/hadron-ups-stack-smoke.yml", "ups-stack-deploys"},
		{"../../.github/workflows/hadron-outage-flow-smoke.yml", "outage-flow-produces-signal"},
		{"../../.github/workflows/hadron-actuator-daemonset-smoke.yml", "actuator-daemonset"},
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
