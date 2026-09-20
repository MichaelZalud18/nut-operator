// Package workflow checks the common lifetime contract of VM smoke jobs.
package workflow

import (
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

type Step struct {
	Name    string `json:"name"`
	ID      string `json:"id"`
	If      string `json:"if"`
	Run     string `json:"run"`
	Uses    string `json:"uses"`
	Timeout int    `json:"timeout-minutes"`
}

type Job struct {
	Timeout int    `json:"timeout-minutes"`
	Steps   []Step `json:"steps"`
}

// Validate checks every job, including newly added guest workflows. It validates
// declared bounds/order, not the runtime behavior of arbitrary shell commands.
func Validate(data []byte) error {
	var document struct {
		Jobs map[string]Job `json:"jobs"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Jobs) == 0 {
		return fmt.Errorf("VM workflow has no jobs")
	}
	var failures []error
	for name, job := range document.Jobs {
		if err := ValidateJob(job); err != nil {
			failures = append(failures, fmt.Errorf("job %s: %w", name, err))
		}
	}
	return errors.Join(failures...)
}

func ValidateJob(job Job) error {
	var failures []error
	total, cleanup := 0, -1
	for i, step := range job.Steps {
		if step.Timeout <= 0 {
			failures = append(failures, fmt.Errorf("unbounded step %s", step.Name))
		}
		total += step.Timeout
		if step.ID == "cleanup" {
			if cleanup >= 0 {
				failures = append(failures, fmt.Errorf("multiple cleanup IDs"))
			}
			cleanup = i
			if !strings.Contains(step.If, "always()") {
				failures = append(failures, fmt.Errorf("cleanup must run after failure/cancellation"))
			}
		}
		if strings.Contains(step.Uses, "actions/upload-artifact") && cleanup < 0 {
			failures = append(failures, fmt.Errorf("artifact upload precedes process cleanup"))
		}
		if strings.Contains(step.Run, "rm -rf") &&
			(cleanup < 0 || !strings.Contains(step.If, "steps.cleanup.outcome == 'success'")) {
			failures = append(failures, fmt.Errorf("state removal requires successful preceding cleanup"))
		}
	}
	if cleanup < 0 || total >= job.Timeout || job.Timeout <= 0 {
		failures = append(failures, fmt.Errorf("missing cleanup or job-level time margin"))
	}
	return errors.Join(failures...)
}
