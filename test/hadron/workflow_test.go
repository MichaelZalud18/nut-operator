//go:build hadron

package hadron

import (
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSmokeWorkflowReservesCleanupBudget(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/hadron-vm-boot-smoke.yml")
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
	job := workflow.Jobs["boot"]
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
}
