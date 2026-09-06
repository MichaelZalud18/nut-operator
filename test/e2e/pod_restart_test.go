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
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

// F-110's other missing half. driverSoakSpecs (soak_test.go) and
// nutserver_driver_supervisor_repeated_restart_test.go both restart the *driver process* many
// times inside one long-lived pod -- that proves the shell supervisor's own bookkeeping, but it
// structurally cannot reach the layer above it: whether the *Pod* the operator renders comes back
// clean when Kubernetes itself, not the supervisor, is the thing recreating it. A node reboot, an
// OOM kill, an eviction, or a rollout all destroy the whole pod, not just a driver inside it -- the
// operator's ReplicaSet-owned PodTemplate has to converge to Ready from nothing every time, with a
// fresh init container run (nut-tls-assemble), fresh mounts, and a fresh driver-supervisor startup,
// not just a supervisor loop noticing a stale digest.
//
// Force-deleted rather than gracefully deleted, on purpose: a graceful delete runs the container's
// preStop/terminationGracePeriod machinery the way a deploy or a kubectl-initiated restart would,
// which this project already exercises implicitly on every fixture teardown. `--grace-period=0
// --force` is the shape a node failure or an OOM kill actually takes -- no graceful shutdown at
// all -- which is the "ugly runtime behavior" this finding names and the shape most likely to
// expose a rendered PodSpec that only converges when Kubernetes gets to say goodbye first.
//
// Opt-in for the same reason as driverSoakSpecs: real pod recreation (image already cached, but a
// full container + init-container + readiness cycle) costs real minutes per cycle, and F-77 gates
// a published `:main` on the default e2e suite stayng fast. Set NUT_OPERATOR_E2E_SOAK=true to run
// it; NUT_OPERATOR_E2E_SOAK_POD_RESTART_CYCLES overrides the cycle count (default 8).
func podRestartSpecs() {
	if os.Getenv("NUT_OPERATOR_E2E_SOAK") != "true" {
		return
	}

	Describe("Pod restart stability", Ordered, func() {
		const (
			namespace  = "power-podrestart-e2e"
			serverName = "podrestart-e2e-nutserver"
			upsName    = "podrestart-e2e-ups"
			// Generous: a force-deleted pod has to reschedule, run nut-tls-assemble again, start
			// both containers, and pass its readiness probe from a cold driver -- more than the
			// in-pod driverRecoveryBudget covers, which only ever waits on a process inside an
			// already-Ready pod.
			podRecoveryBudget = 2 * time.Minute
		)
		cycles := 8
		if raw := os.Getenv("NUT_OPERATOR_E2E_SOAK_POD_RESTART_CYCLES"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				panic(fmt.Sprintf("NUT_OPERATOR_E2E_SOAK_POD_RESTART_CYCLES must be a positive integer, got %q", raw))
			}
			cycles = parsed
		}

		podSelector := "power.zalud.io/nutserver=" + serverName

		// readyPods returns the names of every currently-Ready pod matching the selector. Plural,
		// not singular: the property this spec checks after each cycle is that exactly one exists,
		// which a helper that already assumes one could not catch a regression in.
		readyPods := func() ([]string, error) {
			out, err := utils.Run(exec.Command("kubectl", "-n", namespace, "get", "pods",
				"-l", podSelector,
				"-o", `jsonpath={range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}`))
			if err != nil {
				return nil, err
			}
			var ready []string
			for _, line := range utils.GetNonEmptyLines(out) {
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[1] == "True" {
					ready = append(ready, fields[0])
				}
			}
			return ready, nil
		}

		devicePhase := func() (string, error) {
			out, err := utils.Run(exec.Command("kubectl", "get", "upsdevice", upsName,
				"-o", "jsonpath={.status.phase}"))
			return strings.TrimSpace(out), err
		}

		var serverPod string

		BeforeAll(func() {
			applyDummyUPSFixture(namespace, serverName, upsName, "Pod Restart E2E Dummy UPS")
			serverPod = waitForNUTServerPodReady(namespace, podSelector)
		})

		AfterAll(func() {
			teardownDummyUPSFixture(namespace, serverName, upsName)
		})

		It(fmt.Sprintf("recovers a force-deleted Pod within budget across %d repeated restart cycles", cycles), func() {
			By("confirming telemetry is flowing before anything is broken")
			Eventually(func(g Gomega) {
				phase, err := devicePhase()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(phase).To(Equal("Online"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed(),
				"telemetry never came up, so a restart soak against it would prove nothing")

			var recoveries []time.Duration
			for cycle := 1; cycle <= cycles; cycle++ {
				By(fmt.Sprintf("cycle %d/%d: force-deleting Pod %s", cycle, cycles, serverPod))
				deletedPod := serverPod
				_, err := utils.Run(exec.Command("kubectl", "-n", namespace, "delete", "pod", deletedPod,
					"--grace-period=0", "--force"))
				Expect(err).NotTo(HaveOccurred(), "cycle %d: failed to force-delete %s", cycle, deletedPod)
				deletedAt := time.Now()

				By(fmt.Sprintf("cycle %d/%d: waiting for a replacement Pod to become Ready", cycle, cycles))
				Eventually(func(g Gomega) {
					ready, err := readyPods()
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(ready).To(HaveLen(1),
						"expected exactly one Ready pod matching %s, got %v -- either the replacement "+
							"never came up or a duplicate was left behind", podSelector, ready)
					g.Expect(ready[0]).NotTo(Equal(deletedPod),
						"the \"replacement\" pod is the one that was just force-deleted; ReplicaSet has not acted yet")
					serverPod = ready[0]
				}, podRecoveryBudget, 2*time.Second).Should(Succeed(),
					fmt.Sprintf("cycle %d: no single Ready replacement pod appeared inside the %s budget", cycle, podRecoveryBudget))

				By(fmt.Sprintf("cycle %d/%d: confirming telemetry reconverged on the new Pod", cycle, cycles))
				Eventually(func(g Gomega) {
					phase, err := devicePhase()
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(phase).To(Equal("Online"))
				}, podRecoveryBudget, 2*time.Second).Should(Succeed(),
					fmt.Sprintf("cycle %d: UPSDevice phase did not reconverge to Online after the pod restart", cycle))

				recoveries = append(recoveries, time.Since(deletedAt))
			}

			var total time.Duration
			for i, recovered := range recoveries {
				AddReportEntry(fmt.Sprintf("cycle %d pod recovery", i+1), recovered.String())
				total += recovered
			}
			AddReportEntry("mean pod recovery", (total / time.Duration(len(recoveries))).String())
		})
	})
}
