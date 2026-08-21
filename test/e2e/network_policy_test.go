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
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

// This spec asserts a property of the cluster, not of the operator (F-109).
//
// kindnet, which `kind create cluster` installs by default, does not implement NetworkPolicy. Under
// it every policy in this repository applies cleanly, is reported as created, and drops nothing.
// That is not a hypothetical: it is how F-98 -- a shipped bundle containing no egress rules at all
// -- passed every CI run before failing on the first cluster that enforced them.
//
// A suite that cannot fail a policy assertion must not be relied on to make one, so this fails the
// run when the cluster underneath it does not enforce. It uses a policy of its own rather than one
// of the operator's on purpose: when this fails the fault is the environment, and asserting through
// the operator's policies would make an environment failure read as a product bug.
//
// The restore step at the end is what makes the middle step mean anything. A connection that fails
// after a policy is applied has only been shown to fail; showing it succeeds again once the policy
// is removed is what attributes the failure to the policy rather than to a slow pod or a flake.
var _ = Describe("NetworkPolicy enforcement", Ordered, func() {
	const (
		namespace = "netpol-guard-e2e"
		podName   = "netpol-guard-client"
	)

	// Reached by address, never by name. A default-deny egress policy blocks DNS along with
	// everything else, so a name-based probe would fail at resolution on the blocked leg and prove
	// only that DNS was unavailable -- true under a working policy and equally true under a broken
	// cluster. An address isolates the thing being measured to the connection itself.
	var apiServerIP string

	// probe reports whether the pod can open a TCP connection to the API server's ClusterIP.
	//
	// The exit status is the whole result: busybox nc -z returns 0 on connect and non-zero
	// otherwise, and a blocked connection ends at the -w timeout rather than refusing, which is why
	// the timeout is short enough that a blocked probe stays quick.
	probe := func() error {
		cmd := exec.Command("kubectl", "-n", namespace, "exec", podName, "--",
			"nc", "-z", "-w", "3", apiServerIP, "443")
		_, err := utils.Run(cmd)
		return err
	}

	BeforeAll(func() {
		By("creating the guard namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the guard namespace")

		By("reading the API server ClusterIP")
		cmd = exec.Command("kubectl", "get", "svc", "kubernetes", "-n", "default",
			"-o", "jsonpath={.spec.clusterIP}")
		apiServerIP, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		Expect(apiServerIP).NotTo(BeEmpty(), "the kubernetes Service must have a ClusterIP")

		// nutServerImage rather than a public one: BeforeSuite has already loaded it onto every
		// node, so this adds no pull and cannot fail on a registry that is rate limiting. All the
		// spec needs from it is a shell and busybox nc.
		By("starting a client pod from an image the suite has already loaded")
		manifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app: netpol-guard
spec:
  containers:
    - name: client
      image: %s
      imagePullPolicy: IfNotPresent
      command: ["sleep", "600"]
`, podName, namespace, nutServerImage)

		applyCmd := exec.Command("kubectl", "apply", "-f", "-")
		applyCmd.Stdin = strings.NewReader(manifest)
		_, err = utils.Run(applyCmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create the guard pod")

		By("waiting for the client pod to be Ready")
		cmd = exec.Command("kubectl", "-n", namespace, "wait", "--for=condition=Ready",
			"pod", podName, "--timeout=2m")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "The guard pod never became Ready")
	})

	AfterAll(func() {
		By("removing the guard namespace")
		cmd := exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found=true", "--wait=false")
		_, _ = utils.Run(cmd)
	})

	It("permits egress before any policy selects the pod", func() {
		Eventually(probe, 2*time.Minute, 5*time.Second).Should(Succeed(),
			"the client could not reach the API server before any policy existed, so nothing this spec "+
				"measures afterwards would mean anything")
	})

	It("blocks egress once a deny-all policy selects the pod, and restores it when the policy is removed", func() {
		By("applying a default-deny egress policy")
		manifest := fmt.Sprintf(`
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: netpol-guard-deny-egress
  namespace: %s
spec:
  podSelector:
    matchLabels:
      app: netpol-guard
  policyTypes:
    - Egress
`, namespace)

		applyCmd := exec.Command("kubectl", "apply", "-f", "-")
		applyCmd.Stdin = strings.NewReader(manifest)
		_, err := utils.Run(applyCmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to apply the deny-egress policy")

		By("confirming the connection is refused while the policy stands")
		Eventually(probe, 2*time.Minute, 5*time.Second).ShouldNot(Succeed(),
			"a default-deny egress policy did not stop the pod reaching the API server. The cluster "+
				"this suite is running on does not enforce NetworkPolicy, so no policy assertion "+
				"anywhere in this repository is meaningful on it. Check that the CNI installed by "+
				"`make setup-test-e2e` came up (F-109).")

		By("removing the policy")
		cmd := exec.Command("kubectl", "-n", namespace, "delete", "networkpolicy",
			"netpol-guard-deny-egress", "--ignore-not-found=true")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		By("confirming the connection works again, which is what attributes the block to the policy")
		Eventually(probe, 2*time.Minute, 5*time.Second).Should(Succeed(),
			"egress did not recover after the policy was deleted, so the earlier failure cannot be "+
				"attributed to the policy")
	})
})
