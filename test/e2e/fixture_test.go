//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"os/exec"
	"strings"

	"github.com/MichaelZalud18/nut-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
)

// Each retry needs a fresh command and stdin reader.
func applyFixtureManifest(manifest string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	return err
}

// Register before the first apply: a multi-document apply can create some resources
// and then fail admission on a later one. Namespace-only cleanup uses an empty manifest.
func deferFixtureCleanup(manifest, namespace, description string) {
	DeferCleanup(func() {
		By(description)
		cleanupFixture(utils.Run, manifest, namespace)
	})
}

func cleanupFixture(run func(*exec.Cmd) (string, error), manifest, namespace string) {
	if manifest != "" {
		cmd := exec.Command("kubectl", "delete", "-f", "-", "--ignore-not-found", "--wait=false")
		cmd.Stdin = strings.NewReader(manifest)
		_, _ = run(cmd)
	}
	// Always attempt namespace cleanup, even if deleting a partial fixture failed.
	if namespace != "" {
		_, _ = run(exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found", "--wait=false"))
	}
}
