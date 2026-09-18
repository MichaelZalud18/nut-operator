//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
)

type logicalFlowCleanup struct {
	target     string
	manifests  []string
	namespaces []string
	originals  []appsv1.Deployment
}

func (c *logicalFlowCleanup) run(run func(*exec.Cmd) (string, error)) error {
	if err := c.removeFixtures(run); err != nil {
		return fmt.Errorf("TEST-2 fixture cleanup failed; retaining reservation and shared placement until cluster teardown: %w", err)
	}
	// Only release scheduling restrictions after every fixture deletion and absence
	// check succeeded. Original selectors may require the formerly drained worker.
	_, uncordonErr := logicalFlowRunBounded(run, time.Minute, "", "uncordon", c.target)
	_, taintErr := logicalFlowRunBounded(run, time.Minute, "", "taint", "node", c.target, flowDrainTaint+":NoSchedule-")
	cleanupErr := errors.Join(uncordonErr, taintErr)
	for _, deployment := range c.originals {
		var restore any
		if original, present := deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"]; present {
			restore = original
		}
		cleanupErr = errors.Join(cleanupErr, logicalFlowPatchPlacement(run, deployment, restore))
		cleanupErr = errors.Join(cleanupErr, logicalFlowDeploymentReady(run, deployment))
	}
	return cleanupErr
}

func (c *logicalFlowCleanup) removeFixtures(run func(*exec.Cmd) (string, error)) error {
	type target struct {
		args     []string
		manifest string
		label    string
	}
	var targets []target
	// Reverse creation order: flow, operands, storage, then their namespaces.
	for i := len(c.manifests) - 1; i >= 0; i-- {
		targets = append(targets, target{args: []string{"-f", "-"}, manifest: c.manifests[i], label: fmt.Sprintf("manifest[%d]", i)})
	}
	for i := len(c.namespaces) - 1; i >= 0; i-- {
		targets = append(targets, target{args: []string{"namespace", c.namespaces[i]}, label: "namespace/" + c.namespaces[i]})
	}
	var cleanupErr error
	for _, resource := range targets {
		// Agent pods allow 60s to terminate; foreground GC needs time beyond that.
		args := append([]string{"delete", "--ignore-not-found=true", "--wait=true", "--timeout=2m", "--cascade=foreground"}, resource.args...)
		_, err := logicalFlowRunBounded(run, 2*time.Minute, resource.manifest, args...)
		cleanupErr = errors.Join(cleanupErr, logicalFlowCleanupReportError("delete "+resource.label, err))
	}
	// Check again after all deletion attempts, including namespace contents and
	// resources a still-running reconciler could have recreated during teardown.
	for _, resource := range targets {
		args := append([]string{"get", "--ignore-not-found=true", "-o", "name"}, resource.args...)
		out, err := logicalFlowRunBounded(run, time.Minute, resource.manifest, args...)
		cleanupErr = errors.Join(cleanupErr, logicalFlowCleanupReportError("get "+resource.label, err))
		if err == nil && strings.TrimSpace(out) != "" {
			cleanupErr = errors.Join(cleanupErr, logicalFlowCleanupReportError("get "+resource.label,
				fmt.Errorf("TEST-2 resources remain: %s", out)))
		}
	}
	return cleanupErr
}

func logicalFlowCleanupReportError(operation string, err error) error {
	if err == nil {
		return nil
	}
	err = fmt.Errorf("%s: %w", operation, err)
	// Ginkgo hides secondary cleanup failures at -v. These delete/get commands
	// return resource names and errors, never object bodies; do not log stdin.
	message := err.Error()
	if len(message) > 8192 {
		message = message[:8192] + " [truncated]"
	}
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "TEST-2 cleanup failed: %s\n", message)
	return err
}

func logicalFlowRunBounded(run func(*exec.Cmd) (string, error), timeout time.Duration, manifest string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--request-timeout=15s"}, args...)...)
	cmd.WaitDelay = time.Second
	if manifest != "" {
		cmd.Stdin = strings.NewReader(manifest)
	}
	return run(cmd)
}
