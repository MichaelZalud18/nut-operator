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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

// The no-cert-manager install is the recommended one (docs/installation/README.md) and was the only install path
// with no CI coverage: the main suite deploys via config/default, so every run exercised the
// cert-manager path and none exercised this one. The gap mattered more than an ordinary coverage
// hole, because the two paths differ in exactly the mechanism that makes admission work at all --
// who puts a certificate in the Secret and a caBundle on the webhook configurations. A break here is
// invisible until a CR write is rejected with an x509 error.
//
// The four steps below are the ones docs/installation/README.md tells a user to run, in that order, asserted
// rather than described.
//
// This container is deliberately self-contained: it creates everything it needs and deletes all of
// it again, including the namespace. Ginkgo randomizes top-level containers, and both install paths
// target the same fixed namespace, so neither may depend on running before or after the other.
var _ = Describe("BYO-cert install", Ordered, Serial, func() {
	const byoCertBundle = "dist/install-byo-cert.yaml"

	// installBundlePath resolves the bundle against the repository root. utils.Run chdirs to the
	// project root, so a relative path is not stable across calls.
	installBundlePath := func() string {
		root, err := utils.GetProjectDir()
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to resolve the project directory")
		return filepath.Join(root, byoCertBundle)
	}

	BeforeAll(func() {
		By("regenerating the byo-cert install bundle so the test cannot pass against a stale one")
		cmd := exec.Command("make", "build-installer-byo-cert")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to build the byo-cert installer bundle")

		By("applying dist/install-byo-cert.yaml")
		cmd = exec.Command("kubectl", "apply", "--server-side", "-f", installBundlePath())
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the byo-cert install bundle")

		// One patch, not two. Image and pull policy applied separately produce two rollouts, which
		// leaves a Terminating pod alongside the new one and makes every later `kubectl ... -l
		// control-plane=controller-manager` read ambiguous.
		By("pointing the manager at the image loaded into Kind")
		cmd = exec.Command("kubectl", "patch", "deployment", "nut-operator-controller-manager",
			"-n", namespace, "--type=json", "-p", fmt.Sprintf(
				`[{"op":"replace","path":"/spec/template/spec/containers/0/image","value":%q},`+
					`{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]`,
				managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to patch the manager image and pull policy for Kind")
	})

	AfterAll(func() {
		By("deleting the byo-cert install")
		cmd := exec.Command("kubectl", "delete", "-f", installBundlePath(), "--ignore-not-found=true")
		_, _ = utils.Run(cmd)

		By("removing the namespace so the other install path finds a clean cluster")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found=true")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		if !CurrentSpecReport().Failed() {
			return
		}
		for _, args := range [][]string{
			{"get", "pods", "-n", namespace, "-o", "wide"},
			{"describe", "deployment", "nut-operator-controller-manager", "-n", namespace},
			{"get", "secret", "webhook-server-cert", "-n", namespace},
			{"get", "events", "-n", namespace, "--sort-by=.lastTimestamp"},
		} {
			out, err := utils.Run(exec.Command("kubectl", args...))
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "kubectl %v:\n%s\n", args, out)
			}
		}
	})

	// Asserted before provisioning, because it is the whole premise of this install path: the bundle
	// carries no Issuer, no Certificate, and no inject-ca-from annotation, so nothing in the cluster
	// will ever fill these in on its own. If a cert-manager object leaked back into the overlay, the
	// remaining specs would still pass while silently testing the wrong path.
	It("ships no cert-manager objects and no caBundle", func() {
		By("confirming the bundle declares no cert-manager resources")
		bundle, err := os.ReadFile(installBundlePath())
		Expect(err).NotTo(HaveOccurred(), "Failed to read the byo-cert bundle")
		Expect(string(bundle)).NotTo(ContainSubstring("cert-manager.io/inject-ca-from"),
			"the byo-cert overlay must not carry cert-manager CA injection annotations")
		Expect(string(bundle)).NotTo(ContainSubstring("kind: Certificate"),
			"the byo-cert overlay must not ship cert-manager Certificate objects")
		Expect(string(bundle)).NotTo(ContainSubstring("kind: Issuer"),
			"the byo-cert overlay must not ship a cert-manager Issuer")

		By("confirming the webhook configurations start with an empty caBundle")
		cmd := exec.Command("kubectl", "get", "validatingwebhookconfiguration",
			"nut-operator-validating-webhook-configuration",
			"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}")
		out, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to read the validating webhook configuration")
		Expect(out).To(BeEmpty(), "caBundle should be empty until hack/webhook-cert.sh runs")
	})

	It("provisions the serving certificate with hack/webhook-cert.sh", func() {
		By("running hack/webhook-cert.sh")
		cmd := exec.Command("hack/webhook-cert.sh")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "hack/webhook-cert.sh failed")

		By("confirming the serving certificate Secret exists with both keys")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "secret", "webhook-server-cert",
				"-n", namespace, "-o", "jsonpath={.data.tls\\.crt} {.data.tls\\.key}"))
			g.Expect(err).NotTo(HaveOccurred())
			parts := strings.Fields(out)
			g.Expect(parts).To(HaveLen(2), "expected both tls.crt and tls.key to be populated")
		}).Should(Succeed())

		By("confirming caBundle was injected into both webhook configurations")
		for _, kind := range []string{"mutatingwebhookconfiguration", "validatingwebhookconfiguration"} {
			name := "nut-operator-mutating-webhook-configuration"
			if kind == "validatingwebhookconfiguration" {
				name = "nut-operator-validating-webhook-configuration"
			}
			out, err := utils.Run(exec.Command("kubectl", "get", kind, name,
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}"))
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("Failed to read %s", kind))
			Expect(out).NotTo(BeEmpty(), fmt.Sprintf("%s still has an empty caBundle", kind))
		}
	})

	It("brings the manager to ready", func() {
		// rollout status before the Available condition: Available is satisfied while a superseded
		// pod is still terminating, and the later specs read the manager by label selector.
		By("waiting for the rollout to complete")
		cmd := exec.Command("kubectl", "rollout", "status", "deployment/nut-operator-controller-manager",
			"-n", namespace, "--timeout=5m")
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "The controller-manager rollout never completed")

		By("waiting for the controller-manager deployment to become available")
		cmd = exec.Command("kubectl", "wait", "deployment/nut-operator-controller-manager",
			"-n", namespace, "--for=condition=Available", "--timeout=5m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "The controller-manager never became available")

		By("waiting for exactly one manager pod to remain")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "pods", "-n", namespace,
				"-l", "control-plane=controller-manager",
				"--field-selector=status.phase=Running", "-o", "jsonpath={.items[*].metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(strings.Fields(out)).To(HaveLen(1), "expected a settled single-pod rollout")
		}).Should(Succeed())

		// Running is not serving. status.phase=Running is true as soon as the container process
		// starts, while the webhook listener binds later, so the next spec could dial a port nothing
		// was on yet and get connection refused.
		// The inner kubectl wait blocks for its own timeout, so a bare Eventually around it gets
		// exactly one attempt: Gomega's default budget is already spent by the time the first poll
		// returns. Given an explicit budget it can actually retry, which matters because the pod
		// being waited on may be replacing one that failed to mount.
		By("waiting for the manager pod to report Ready")
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "wait", "pods", "-n", namespace,
				"-l", "control-plane=controller-manager",
				"--for=condition=Ready", "--timeout=1m"))
			g.Expect(err).NotTo(HaveOccurred(), out)
		}).WithTimeout(5 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
	})

	// The payoff. A rejection proves the whole chain end to end: the API server dialed the webhook,
	// trusted the certificate against the caBundle the script injected, reached the manager, and got
	// a decision back. Any break in provisioning surfaces here as an x509 error instead, which is
	// why the assertion checks for the validation message rather than merely for failure.
	It("serves admission, so an invalid resource is rejected", func() {
		By("attempting to create a NUTServer in a reserved namespace")
		manifest := `apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: byo-cert-admission-probe
spec:
  namespace: kube-system
`
		// Retried, because readiness and reachability are not the same instant: the API server
		// caches endpoints, and the first dial after a rollout can land on a connection refused
		// even once the pod reports Ready. This observably flaked twice on `main`, always as
		// `failed calling webhook ... connection refused` rather than as a wrong answer.
		//
		// The assertions inside are unchanged and deliberately so. Retrying widens the window in
		// which the webhook may answer; it does not accept a different answer. A certificate the
		// API server will not trust fails here exactly as before, because an x509 error never
		// becomes the validation message no matter how long it is retried.
		Eventually(func(g Gomega) {
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(manifest)
			out, err := utils.Run(cmd)

			g.Expect(err).To(HaveOccurred(), "the webhook should have rejected a reserved operand namespace")
			g.Expect(out).To(ContainSubstring("spec.namespace"),
				"expected the webhook's own validation message, not a TLS or connectivity error")
			g.Expect(out).To(ContainSubstring("reserved"),
				"expected the webhook's own validation message, not a TLS or connectivity error")
		}).WithTimeout(2 * time.Minute).WithPolling(5 * time.Second).Should(Succeed())
	})

	// Rotation is the documented procedure for this install path, and its whole design claim is that
	// re-running the script reuses the CA so caBundle stays valid and the manager reloads the new
	// certificate through its certwatcher without restarting. All three are asserted, because a
	// rotation that silently required a restart would only be discovered during an outage.
	It("rotates the serving certificate without changing caBundle or restarting the manager", func() {
		readCABundle := func() string {
			out, err := utils.Run(exec.Command("kubectl", "get", "validatingwebhookconfiguration",
				"nut-operator-validating-webhook-configuration",
				"-o", "jsonpath={.webhooks[0].clientConfig.caBundle}"))
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			return out
		}
		readServingCert := func() string {
			out, err := utils.Run(exec.Command("kubectl", "get", "secret", "webhook-server-cert",
				"-n", namespace, "-o", "jsonpath={.data.tls\\.crt}"))
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			return out
		}
		// Identity plus restart count of one specific pod, not a joined string across every pod
		// matching the selector. The joined form reports "0 0" instead of "0" the moment a
		// superseded pod is still terminating, which fails for a reason that has nothing to do with
		// whether the manager restarted.
		readManagerPod := func() (name, restarts string) {
			out, err := utils.Run(exec.Command("kubectl", "get", "pods", "-n", namespace,
				"-l", "control-plane=controller-manager", "--field-selector=status.phase=Running",
				"-o", "jsonpath={.items[0].metadata.name} {.items[0].status.containerStatuses[0].restartCount}"))
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			fields := strings.Fields(out)
			ExpectWithOffset(1, fields).To(HaveLen(2), "expected exactly one running manager pod")
			return fields[0], fields[1]
		}

		bundleBefore, certBefore := readCABundle(), readServingCert()
		podBefore, restartsBefore := readManagerPod()

		By("re-running hack/webhook-cert.sh")
		_, err := utils.Run(exec.Command("hack/webhook-cert.sh"))
		Expect(err).NotTo(HaveOccurred(), "rotation run of hack/webhook-cert.sh failed")

		Expect(readServingCert()).NotTo(Equal(certBefore), "the serving certificate should have been reissued")
		Expect(readCABundle()).To(Equal(bundleBefore), "the CA is reused, so caBundle must not change on rotation")

		podAfter, restartsAfter := readManagerPod()
		Expect(podAfter).To(Equal(podBefore), "the manager pod must not be replaced by a rotation")
		Expect(restartsAfter).To(Equal(restartsBefore), "the manager must reload the certificate without restarting")

		By("confirming admission still works after rotation")
		manifest := `apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: byo-cert-rotation-probe
spec:
  namespace: kube-system
`
		cmd := exec.Command("kubectl", "apply", "-f", "-")
		cmd.Stdin = strings.NewReader(manifest)
		out, err := utils.Run(cmd)
		Expect(err).To(HaveOccurred(), "the webhook should still reject after rotation")
		Expect(out).To(ContainSubstring("reserved"))
	})

	// The expiry reporter's parsing is unit-tested; what only a real deployment can prove is that
	// --webhook-cert-path plus the projected Secret actually resolve to a readable file inside the
	// container. A wrong path there is invisible in unit tests and silent in production: the metric
	// is simply absent, which is the same shape as F-40.
	It("reads the mounted serving certificate for the expiry metric", func() {
		By("resolving the running manager pod")
		var pod string
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "get", "pods", "-n", namespace,
				"-l", "control-plane=controller-manager",
				"--field-selector=status.phase=Running", "-o", "jsonpath={.items[0].metadata.name}"))
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(out).NotTo(BeEmpty())
			pod = strings.TrimSpace(out)
		}).Should(Succeed())

		By("confirming the manager logged no certificate read failure")
		out, err := utils.Run(exec.Command("kubectl", "logs", pod, "-n", namespace, "-c", "manager", "--tail=-1"))
		Expect(err).NotTo(HaveOccurred(), "Failed to read manager logs")
		Expect(out).NotTo(ContainSubstring("Failed to read serving certificate expiry"),
			"the expiry reporter could not read the mounted certificate")
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
})
