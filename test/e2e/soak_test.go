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

// F-110's optional half. driverRecoverySpecs (driver_recovery_test.go) proves one driver-outage
// cycle recovers inside DEADTIME. What a single cycle cannot show is a bug that only compounds --
// a stray fake-driver-supervisor state file, a slow accumulation in the exit-file bookkeeping, a
// PID reused across cycles in a way the digest tracking mishandles -- because the component-level
// repeated-restart coverage (nutserver_driver_supervisor_repeated_restart_test.go) already proves
// that shape against the real script under fake binaries. This is the same claim against the real
// operand image and a real kubelet, which is the one thing the component test structurally cannot
// reach: whether a real container's PID 1, real cgroups, and a real kubelet's own restart
// accounting stay stable across many outage cycles, not just the shell script's own bookkeeping.
//
// Deliberately not part of the default e2e run. It is many multiples of driverRecoverySpecs'
// runtime by construction (repeating the same wait/kill/wait budget N times), and F-77 already
// gates a published `:main` on the default e2e suite passing -- adding an opt-in-only cost to
// that critical path for a soak signal is the wrong trade. Set NUT_OPERATOR_E2E_SOAK=true to run
// it locally or in a dedicated, non-blocking CI job; NUT_OPERATOR_E2E_SOAK_CYCLES overrides the
// cycle count (default 10).
func driverSoakSpecs() {
	if os.Getenv("NUT_OPERATOR_E2E_SOAK") != "true" {
		return
	}

	Describe("Driver recovery soak", Ordered, func() {
		const (
			namespace  = "power-soak-e2e"
			serverName = "soak-e2e-nutserver"
			upsName    = "soak-e2e-ups"
		)
		cycles := 10
		if raw := os.Getenv("NUT_OPERATOR_E2E_SOAK_CYCLES"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				panic(fmt.Sprintf("NUT_OPERATOR_E2E_SOAK_CYCLES must be a positive integer, got %q", raw))
			}
			cycles = parsed
		}

		var serverPod string
		podSelector := "power.zalud.io/nutserver=" + serverName

		driverState := func() (string, error) {
			return nutDriverState(namespace, serverPod)
		}

		BeforeAll(func() {
			applyDummyUPSFixture(namespace, serverName, upsName, "Driver Soak E2E Dummy UPS")
			serverPod = waitForNUTServerPodReady(namespace, podSelector)
		})

		AfterAll(func() {
			teardownDummyUPSFixture(namespace, serverName, upsName)
		})

		It(fmt.Sprintf("recovers a killed driver within budget across %d repeated outage cycles", cycles), func() {
			Eventually(func(g Gomega) {
				state, err := driverState()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(state).To(ContainSubstring("RESPONSIVE"))
				g.Expect(state).NotTo(ContainSubstring("NOT_RESPONSIVE"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed(),
				"the driver never came up, so a soak run against it would prove nothing")

			var recoveries []time.Duration
			for cycle := 1; cycle <= cycles; cycle++ {
				By(fmt.Sprintf("cycle %d/%d: killing the driver through its own PID file", cycle, cycles))
				_, err := utils.Run(exec.Command("kubectl", "-n", namespace, "exec", serverPod, "-c", "upsd",
					"--", "sh", "-c", "set -e; pid=$(cat /run/nut/dummy-ups-*.pid); kill -9 \"$pid\""))
				Expect(err).NotTo(HaveOccurred(), "cycle %d: failed to kill the driver", cycle)
				killedAt := time.Now()

				Eventually(func(g Gomega) {
					state, stateErr := driverState()
					g.Expect(stateErr).NotTo(HaveOccurred())
					g.Expect(state).To(ContainSubstring("NOT_RESPONSIVE"))
				}, 20*time.Second, time.Second).Should(Succeed(),
					"cycle %d: the driver still answered after being killed", cycle)

				Eventually(func(g Gomega) {
					state, stateErr := driverState()
					g.Expect(stateErr).NotTo(HaveOccurred())
					g.Expect(state).To(ContainSubstring("RESPONSIVE"))
					g.Expect(state).NotTo(ContainSubstring("NOT_RESPONSIVE"))
				}, driverRecoveryBudget, time.Second).Should(Succeed(),
					fmt.Sprintf("cycle %d: the driver did not come back inside the %s budget", cycle, driverRecoveryBudget))

				recoveries = append(recoveries, time.Since(killedAt))
			}

			var total time.Duration
			for i, recovered := range recoveries {
				AddReportEntry(fmt.Sprintf("cycle %d recovery", i+1), recovered.String())
				total += recovered
			}
			AddReportEntry("mean recovery", (total / time.Duration(len(recoveries))).String())

			By("confirming the pod itself was never restarted across any cycle")
			out, err := utils.Run(exec.Command("kubectl", "-n", namespace, "get", "pod", serverPod,
				"-o", "jsonpath={.status.containerStatuses[*].restartCount}"))
			Expect(err).NotTo(HaveOccurred())
			for _, count := range strings.Fields(out) {
				Expect(count).To(Equal("0"),
					"a container restarted during the soak run, which drops every upsmon session on every cycle after it")
			}
		})
	})
}
