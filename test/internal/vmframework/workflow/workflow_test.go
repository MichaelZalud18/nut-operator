package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func validJob() Job {
	return Job{Timeout: 10, Steps: []Step{
		{Name: "test", Timeout: 5},
		{ID: "cleanup", If: "always()", Timeout: 1},
		{Uses: "actions/upload-artifact@pinned", Timeout: 1},
		{Run: "rm -rf owned-state", If: "steps.cleanup.outcome == 'success'", Timeout: 1},
	}}
}

func TestLifecycleMutationsAreRejected(t *testing.T) {
	if err := ValidateJob(validJob()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Job){
		"unbounded":           func(j *Job) { j.Steps[0].Timeout = 0 },
		"no-margin":           func(j *Job) { j.Timeout = 8 },
		"conditional-cleanup": func(j *Job) { j.Steps[1].If = "success()" },
		"upload-first":        func(j *Job) { j.Steps[0].Uses = "actions/upload-artifact@pinned" },
		"unguarded-delete":    func(j *Job) { j.Steps[3].If = "always()" },
		"missing-cleanup":     func(j *Job) { j.Steps[1].ID = "other" },
		"duplicate-cleanup":   func(j *Job) { j.Steps[2].ID = "cleanup" },
	} {
		t.Run(name, func(t *testing.T) {
			job := validJob()
			mutate(&job)
			if err := ValidateJob(job); err == nil {
				t.Fatal("unsafe job accepted")
			}
		})
	}
}

func TestAllCheckedInVMSmokeWorkflows(t *testing.T) {
	for _, guest := range []string{"hadron", "talos"} {
		paths, err := filepath.Glob("../../../../.github/workflows/" + guest + "*smoke.yml")
		if err != nil || len(paths) == 0 {
			t.Fatalf("missing %s workflows: %v", guest, err)
		}
		for _, path := range paths {
			t.Run(filepath.Base(path), func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := Validate(data); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMalformedAndEmptyWorkflows(t *testing.T) {
	for _, data := range []string{"[", "jobs: {}", "jobs: {boot: {}}"} {
		if err := Validate([]byte(data)); err == nil {
			t.Fatal("invalid workflow accepted")
		}
	}
}
