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

// The first spec in this suite that breaks something on purpose (F-110).
//
// Every other spec asserts convergence on a healthy cluster; the closest any came to a failure was
// checking that the manager's restart count did *not* change. For an operator whose entire purpose
// is acting during failure, the untested half was the half that matters, and it showed: F-97 and
// F-105 are both defects in what happens after something dies, and both were found by running the
// thing for hours rather than by any gate.
//
// What is asserted here is a bound, not just recovery. The driver dying is survivable; the driver
// staying dead longer than upsmon's DEADTIME is not, because every agent then concludes "too few
// UPS(es) are healthy" and runs SHUTDOWNCMD. That is a timing relationship between two components
// that never appear in the same test: a watchdog that recovers eventually passes a liveness check
// and still shuts down the cluster.
//
// This is not F-105, which the 2026-08-24 correction in operator-maturity-benchmarks.md separated
// out. F-105 is an orphaned secondary reaching HOSTSYNC on low battery with the driver perfectly
// healthy, and no recovery budget touches it. The path bounded here is the other one -- silence long
// enough to expire DEADTIME -- which is the one a driver outage can actually cause.
//
// driverRecoveryBudget is deliberately below the smallest DEADTIME the operator renders (45s
// default, settable per agent). Detection is one to two watchdog intervals, the confirmation adds
// its delay, and `upsdrvctl start` takes a second or two. If this ever needs raising to pass, the
// thing to fix is the watchdog, not the number.
const driverRecoveryBudget = 30 * time.Second

// driverRecoverySpecs is called from inside the Manager container rather than registered at the top
// level, because it needs the operator installed and only that container owns installing
// it. Registered standalone, these specs ran against whatever the Manager and BYO-cert
// containers had last left behind, which was sometimes no CRDs at all.
func driverRecoverySpecs() {
	Describe("Driver recovery under failure", Ordered, func() {
		const (
			namespace  = "power-recovery-e2e"
			serverName = "recovery-e2e-nutserver"
			upsName    = "recovery-e2e-ups"
		)

		var serverPod string
		podSelector := "power.zalud.io/nutserver=" + serverName

		// driverState returns the `upsdrvctl status` row for the device, which carries both whether a
		// process is running and whether it answers. Read as one string because the two have to agree:
		// a stale PID file leaves PF_PID populated while RUNNING and S_PID report N/A, which is exactly
		// how F-97 was misread in the first place.
		driverState := func() (string, error) {
			out, err := utils.Run(exec.Command("kubectl", "-n", namespace, "exec", serverPod, "-c", "upsd",
				"--", "sh", "-c", "upsdrvctl status 2>/dev/null | grep -v S_RESPONSIVE"))
			return strings.TrimSpace(out), err
		}

		BeforeAll(func() {
			applyDummyUPSFixture(namespace, serverName, upsName, "Driver Recovery E2E Dummy UPS")
			serverPod = waitForNUTServerPodReady(namespace, podSelector)
		})

		AfterAll(func() {
			teardownDummyUPSFixture(namespace, serverName, upsName)
		})

		It("brings a killed driver back well inside DEADTIME", func() {
			By("confirming the driver answers before anything is broken")
			Eventually(func(g Gomega) {
				state, err := driverState()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(state).To(ContainSubstring("RESPONSIVE"))
				g.Expect(state).NotTo(ContainSubstring("NOT_RESPONSIVE"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed(),
				"the driver never came up, so killing it would prove nothing")

			By("killing the driver through its own PID file")
			// Through the PID file rather than by name: it is what upsdrvctl itself reads, so this
			// reproduces the state a real exit leaves behind -- a dead process and a stale file -- and
			// not merely an absent process.
			//
			// Globbed rather than spelled out, because the file is named for the section in ups.conf and
			// not for the UPSDevice. There is one device in this fixture, so the glob is unambiguous, and
			// a wrong guess at the name would kill nothing and pass the spec for the wrong reason.
			_, err := utils.Run(exec.Command("kubectl", "-n", namespace, "exec", serverPod, "-c", "upsd",
				"--", "sh", "-c", "set -e; pid=$(cat /run/nut/dummy-ups-*.pid); kill -9 \"$pid\""))
			Expect(err).NotTo(HaveOccurred(), "Failed to kill the driver")

			// The clock starts here and not after the check below. What upsmon experiences is silence
			// from the moment the driver dies, so any window measured from later than this understates
			// the outage by however long the confirmation took.
			killedAt := time.Now()

			By("confirming it is actually gone, so the recovery below is a recovery")
			Eventually(func(g Gomega) {
				state, stateErr := driverState()
				g.Expect(stateErr).NotTo(HaveOccurred())
				g.Expect(state).To(ContainSubstring("NOT_RESPONSIVE"))
			}, 20*time.Second, time.Second).Should(Succeed(),
				"the driver still answered after being killed, so this spec is not testing what it claims")

			By(fmt.Sprintf("confirming the watchdog restores it within %s", driverRecoveryBudget))
			Eventually(func(g Gomega) {
				state, stateErr := driverState()
				g.Expect(stateErr).NotTo(HaveOccurred())
				g.Expect(state).To(ContainSubstring("RESPONSIVE"))
				g.Expect(state).NotTo(ContainSubstring("NOT_RESPONSIVE"))
			}, driverRecoveryBudget, time.Second).Should(Succeed(),
				"the driver did not come back inside the budget. Every upsmon is accumulating silence "+
					"toward its DEADTIME for this whole window, and on expiry each one runs SHUTDOWNCMD "+
					"-- so a recovery slower than DEADTIME turns one driver exit into a cluster-wide "+
					"shutdown signal (F-97)")

			recovered := time.Since(killedAt)
			AddReportEntry("driver recovery", recovered.String())
			Expect(recovered).To(BeNumerically("<", driverRecoveryBudget))
		})

		It("keeps the pod itself running rather than restarting it", func() {
			// The recovery has to happen inside the pod. Restarting the container would drop every
			// upsmon session and NUT's login accounting with it, which is the damage F-15 and F-16
			// exist to prevent -- a "recovery" that costs every client its connection is the outage.
			out, err := utils.Run(exec.Command("kubectl", "-n", namespace, "get", "pod", serverPod,
				"-o", "jsonpath={.status.containerStatuses[*].restartCount}"))
			Expect(err).NotTo(HaveOccurred())
			for _, count := range strings.Fields(out) {
				Expect(count).To(Equal("0"),
					"a container restarted during driver recovery, which drops every upsmon session")
			}
		})
	})
}
