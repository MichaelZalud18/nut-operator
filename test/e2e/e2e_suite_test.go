//go:build e2e
// +build e2e

/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

var (
	// managerImage is the manager image to be built and loaded for testing.
	managerImage = "example.com/nut-operator:v0.0.1"
	// operandImageTag is the tag used for all project-owned operand images built and loaded for
	// testing. Needed by the NodePowerAgent signal-handoff suite, which runs a real
	// dummy-ups-backed NUTServer and NodePowerAgent DaemonSet end-to-end.
	operandImageTag = "v0.0.1"
	// nutServerRepository, upsmonAgentRepository, and nodeActuatorRepository are the repositories
	// (without tag) for the operand images above.
	nutServerRepository    = "example.com/nut-server"
	upsmonAgentRepository  = "example.com/upsmon-agent"
	nodeActuatorRepository = "example.com/node-actuator"
	nutServerImage         = nutServerRepository + ":" + operandImageTag
	upsmonAgentImage       = upsmonAgentRepository + ":" + operandImageTag
	nodeActuatorImage      = nodeActuatorRepository + ":" + operandImageTag
	// snmpsimFixtureRepository/Image is a test-only fixture (a simulated SNMP UPS), not a real
	// operand -- needed by the snmp-ups driver-conformance suite, which runs the real snmp-ups
	// driver against it inside a real NUTServer pod on Kind.
	snmpsimFixtureRepository = "example.com/snmpsim-fixture"
	snmpsimFixtureImage      = snmpsimFixtureRepository + ":" + operandImageTag
	// shouldCleanupCertManager tracks whether CertManager was installed by this suite.
	shouldCleanupCertManager = false
)

// managerKustomizationBackup holds config/manager/kustomization.yaml as it was before the suite ran.
//
// `make deploy IMG=...` edits that tracked file in place — that is how the kubebuilder scaffold
// points the manifests at an image — and nothing in the deploy/undeploy pair puts it back, so a
// suite run used to leave the repository holding example.com/nut-operator:v0.0.1 as the operator's
// published image. Anyone committing after a local e2e run would ship that silently.
var managerKustomizationBackup []byte

// managerKustomizationPath resolves the file absolutely, every time it is needed.
//
// A relative path does not work here: utils.Run calls os.Chdir to the project root, so the process
// working directory moves out from under the suite the first time it shells out to make. A path
// that resolves correctly in BeforeSuite therefore points somewhere else entirely by AfterSuite.
// GetProjectDir normalizes either location back to the repository root.
func managerKustomizationPath() string {
	root, err := utils.GetProjectDir()
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to resolve the project directory")
	return filepath.Join(root, "config", "manager", "kustomization.yaml")
}

// TestE2E runs the e2e test suite to validate the solution in an isolated environment.
// The default setup requires Kind and CertManager.
//
// To enable kubectl kuberc (use custom kubectl configurations), set: KUBECTL_KUBERC=true
// By default, kuberc is disabled to ensure consistent test behavior across different environments.
// To skip CertManager installation, set: CERT_MANAGER_INSTALL_SKIP=true
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting nut-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	By("recording config/manager/kustomization.yaml so the suite can restore it")
	contents, err := os.ReadFile(managerKustomizationPath())
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to read the manager kustomization")
	managerKustomizationBackup = contents

	By("building the manager image")
	cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", managerImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager image")

	// TODO(user): If you want to change the e2e test vendor from Kind,
	// ensure the image is built and available, then remove the following block.
	By("loading the manager image on Kind")
	err = utils.LoadImageToKindClusterWithName(managerImage)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager image into Kind")

	By("building the operand images")
	cmd = exec.Command("make", "docker-build-nut-server",
		fmt.Sprintf("NUT_SERVER_IMG=%s", nutServerImage), fmt.Sprintf("NUT_SERVER_SHA_IMG=%s", nutServerImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the nut-server operand image")
	cmd = exec.Command("make", "docker-build-upsmon-agent",
		fmt.Sprintf("UPSMON_AGENT_IMG=%s", upsmonAgentImage), fmt.Sprintf("UPSMON_AGENT_SHA_IMG=%s", upsmonAgentImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the upsmon-agent operand image")
	cmd = exec.Command("make", "docker-build-node-actuator",
		fmt.Sprintf("NODE_ACTUATOR_IMG=%s", nodeActuatorImage), fmt.Sprintf("NODE_ACTUATOR_SHA_IMG=%s", nodeActuatorImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the node-actuator operand image")
	cmd = exec.Command("make", "docker-build-snmpsim-fixture",
		fmt.Sprintf("SNMPSIM_FIXTURE_IMG=%s", snmpsimFixtureImage), fmt.Sprintf("SNMPSIM_FIXTURE_SHA_IMG=%s", snmpsimFixtureImage))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the snmpsim fixture image")

	By("loading the operand images on Kind")
	for _, image := range []string{nutServerImage, upsmonAgentImage, nodeActuatorImage, snmpsimFixtureImage} {
		err = utils.LoadImageToKindClusterWithName(image)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to load %s into Kind", image))
	}

	configureKubectlKubeRC()
	setupCertManager()
})

var _ = AfterSuite(func() {
	teardownCertManager()
	restoreManagerKustomization()
})

// restoreManagerKustomization undoes the in-place image edit `make deploy` performs, so a suite run
// leaves the working tree as it found it. Restoring from a recorded copy rather than shelling out
// to git keeps this working on a dirty tree and outside a checkout.
func restoreManagerKustomization() {
	if len(managerKustomizationBackup) == 0 {
		return
	}
	By("restoring config/manager/kustomization.yaml")
	err := os.WriteFile(managerKustomizationPath(), managerKustomizationBackup, 0o644)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to restore the manager kustomization")
}

// Disable kubectl kuberc by default for test isolation.
// This prevents local kubectl configurations from affecting test behavior.
// To enable kuberc, set: KUBECTL_KUBERC=true
func configureKubectlKubeRC() {
	if os.Getenv("KUBECTL_KUBERC") != "true" {
		By("disabling kubectl kuberc for test isolation")
		err := os.Setenv("KUBECTL_KUBERC", "false")
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to disable kubectl kuberc")
		_, _ = fmt.Fprintf(GinkgoWriter,
			"kubectl kuberc disabled for consistent test behavior (override with KUBECTL_KUBERC=true)\n")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "kubectl kuberc enabled (KUBECTL_KUBERC=true)\n")
	}
}

// setupCertManager installs CertManager if needed for webhook tests.
// Skips installation if CERT_MANAGER_INSTALL_SKIP=true or if already present.
func setupCertManager() {
	if os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true" {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager installation (CERT_MANAGER_INSTALL_SKIP=true)\n")
		return
	}

	By("checking if CertManager is already installed")
	if utils.IsCertManagerCRDsInstalled() {
		_, _ = fmt.Fprintf(GinkgoWriter, "CertManager is already installed. Skipping installation.\n")
		return
	}

	// Mark for cleanup before installation to handle interruptions and partial installs.
	shouldCleanupCertManager = true

	By("installing CertManager")
	Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
}

// teardownCertManager uninstalls CertManager if it was installed by setupCertManager.
// This ensures we only remove what we installed.
func teardownCertManager() {
	if !shouldCleanupCertManager {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping CertManager cleanup (not installed by this suite)\n")
		return
	}

	By("uninstalling CertManager")
	utils.UninstallCertManager()
}
