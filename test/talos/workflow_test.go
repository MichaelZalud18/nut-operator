//go:build talos

package talos

import (
	"os"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/MichaelZalud18/nut-operator/test/internal/vmframework/workflow"
)

// TestSmokeWorkflowsReserveCleanupBudget applies the shared lifetime contract
// to every job while requiring each Talos workflow's expected entry point.
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
			var document struct {
				Jobs map[string]workflow.Job `json:"jobs"`
			}
			if err := yaml.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
			if _, ok := document.Jobs[tc.job]; !ok {
				t.Fatalf("job %q not found in %s", tc.job, tc.file)
			}
			for name, job := range document.Jobs {
				if err := workflow.ValidateJob(job); err != nil {
					t.Errorf("job %s: %v", name, err)
				}
			}
		})
	}
}
