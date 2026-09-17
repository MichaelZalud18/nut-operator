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
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/MichaelZalud18/nut-operator/test/utils"
)

// ENG-1 measures supervisor recovery from before fault injection, including kubectl latency.
const driverRecoveryBudget = 30 * time.Second

type recoveryDriver struct {
	name, pid, startTicks string
}

// The fixture has exactly one dummy driver. Verify the PID names a live dummy-ups
// process and preserve Linux start time so a stale/reused PID cannot identify it.
const recoveryDriverIdentityScript = `set -eu
set -- /run/nut/dummy-ups-*.pid
[ "$#" -eq 1 ] && [ -f "$1" ]
pidfile=$1
name=${pidfile#/run/nut/dummy-ups-}
name=${name%.pid}
pid=$(cat "$pidfile")
case "$pid" in ''|*[!0-9]*) exit 1;; esac
[ "$pid" -gt 1 ]
[ "$(cat "/proc/$pid/comm")" = dummy-ups ]
stat=$(cat "/proc/$pid/stat")
fields=${stat##*) }
state=${fields%% *}
[ "$state" != Z ] && [ "$state" != X ]
ticks=$(printf '%s\n' "$fields" | awk '{print $20}')
`

const recoveryDriverSampleScript = recoveryDriverIdentityScript + `
status=$(NUT_QUIET_INIT_BANNER=true timeout -k 1 5 upsdrvctl -- status "$name")
[ "$(cat "$pidfile")" = "$pid" ]
stat=$(cat "/proc/$pid/stat")
fields=${stat##*) }
[ "${fields%% *}" != Z ] && [ "${fields%% *}" != X ]
[ "$(printf '%s\n' "$fields" | awk '{print $20}')" = "$ticks" ]
printf 'IDENTITY\t%s\t%s\t%s\n%s\n' "$name" "$pid" "$ticks" "$status"
`

// Positional arguments contain the baseline identity, never an interpolated shell command.
const recoveryDriverKillScript = `expected_pid=$1
expected_ticks=$2
` + recoveryDriverIdentityScript + `
[ "$pid" = "$expected_pid" ] && [ "$ticks" = "$expected_ticks" ]
kill -KILL "$pid"
`

func parseRecoveryDriver(output string) (recoveryDriver, error) {
	var driver recoveryDriver
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 4 && fields[0] == "IDENTITY" {
			driver = recoveryDriver{fields[1], fields[2], fields[3]}
		}
	}
	pid, pidErr := strconv.ParseUint(driver.pid, 10, 32)
	ticks, ticksErr := strconv.ParseUint(driver.startTicks, 10, 64)
	if driver.name == "" || pidErr != nil || pid <= 1 || ticksErr != nil || ticks == 0 {
		return recoveryDriver{}, fmt.Errorf("missing or invalid live driver identity: %q", output)
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 7 && strings.TrimSpace(fields[0]) == driver.name &&
			strings.TrimSpace(fields[1]) == "dummy-ups" &&
			strings.TrimSpace(fields[4]) == "RESPONSIVE" &&
			strings.TrimSpace(fields[5]) == driver.pid {
			return driver, nil
		}
	}
	return recoveryDriver{}, fmt.Errorf("no responsive socket matching driver %+v: %q", driver, output)
}

func recoveryPodUnchanged(before, after corev1.Pod) error {
	if before.UID == "" || before.UID != after.UID || after.DeletionTimestamp != nil {
		return fmt.Errorf("recovery replaced or deleted the original pod: %s -> %s", before.UID, after.UID)
	}
	for _, pod := range []corev1.Pod{before, after} {
		for _, group := range [][]corev1.ContainerStatus{pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses} {
			for _, status := range group {
				if status.ContainerID == "" {
					return fmt.Errorf("container %s has no runtime identity", status.Name)
				}
				if (status.Name == "upsd" || status.Name == "driver-supervisor") && status.State.Running == nil {
					return fmt.Errorf("container %s is not Running", status.Name)
				}
			}
		}
	}
	type identity struct {
		ContainerID string
		Restarts    int32
	}
	statuses := func(p corev1.Pod) map[string]identity {
		result := make(map[string]identity)
		for _, s := range p.Status.ContainerStatuses {
			result["container/"+s.Name] = identity{s.ContainerID, s.RestartCount}
		}
		for _, s := range p.Status.InitContainerStatuses {
			result["init/"+s.Name] = identity{s.ContainerID, s.RestartCount}
		}
		return result
	}
	old, current := statuses(before), statuses(after)
	if len(old) == 0 || !reflect.DeepEqual(old, current) {
		return fmt.Errorf("container identities/restart counts changed: before=%v after=%v", old, current)
	}
	if _, ok := old["container/upsd"]; !ok {
		return fmt.Errorf("baseline has no upsd status")
	}
	if _, normal := old["container/driver-supervisor"]; !normal {
		if _, init := old["init/driver-supervisor"]; !init {
			return fmt.Errorf("baseline has no driver-supervisor status")
		}
	}
	return nil
}

func recoveryDriverReplaced(original, current recoveryDriver) bool {
	return current.name == original.name && current.pid != original.pid
}

func recoveryKubectl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.WaitDelay = 100 * time.Millisecond
	return utils.Run(cmd)
}

// Registered by the Manager suite, which owns operator installation.
func driverRecoverySpecs() {
	Describe("Driver recovery under failure", Ordered, func() {
		const (
			namespace  = "power-recovery-e2e"
			serverName = "recovery-e2e-nutserver"
			upsName    = "recovery-e2e-ups"
		)
		var serverPod string
		BeforeAll(func() {
			applyDummyUPSFixture(namespace, serverName, upsName, "Driver Recovery E2E Dummy UPS")
			serverPod = waitForNUTServerPodReady(namespace, "power.zalud.io/nutserver="+serverName)
		})
		AfterAll(func() { teardownDummyUPSFixture(namespace, serverName, upsName) })

		It("replaces a killed driver within 30 seconds without restarting the pod or sidecar", func(ctx SpecContext) {
			sample := func(ctx context.Context) (recoveryDriver, error) {
				out, err := recoveryKubectl(ctx, "-n", namespace, "exec", serverPod, "-c", "upsd",
					"--", "sh", "-c", recoveryDriverSampleScript)
				if err != nil {
					return recoveryDriver{}, err
				}
				return parseRecoveryDriver(out)
			}
			pod := func(ctx context.Context) (corev1.Pod, error) {
				var p corev1.Pod
				out, err := recoveryKubectl(ctx, "-n", namespace, "get", "pod", serverPod, "-o", "json")
				if err != nil {
					return p, err
				}
				err = json.Unmarshal([]byte(out), &p)
				return p, err
			}

			By("recording a responsive driver process and the pod/container baseline")
			var original recoveryDriver
			Eventually(func(g Gomega) {
				var err error
				original, err = sample(ctx)
				g.Expect(err).NotTo(HaveOccurred())
			}, 3*time.Minute, time.Second).Should(Succeed())
			before, err := pod(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(recoveryPodUnchanged(before, before)).To(Succeed())
			Expect(before.Spec.ShareProcessNamespace).NotTo(BeNil())
			Expect(*before.Spec.ShareProcessNamespace).To(BeTrue())

			By("killing only the verified original process and timing the whole recovery")
			started := time.Now()
			recoveryCtx, cancel := context.WithDeadline(ctx, started.Add(driverRecoveryBudget))
			defer cancel()
			_, err = recoveryKubectl(recoveryCtx, "-n", namespace, "exec", serverPod, "-c", "upsd",
				"--", "sh", "-c", recoveryDriverKillScript, "driver-recovery", original.pid, original.startTicks)
			Expect(err).NotTo(HaveOccurred(), "original driver identity changed or fault injection failed")

			var replacement recoveryDriver
			var lastErr error
			for recoveryCtx.Err() == nil {
				replacement, lastErr = sample(recoveryCtx)
				if lastErr == nil && recoveryDriverReplaced(original, replacement) {
					after, getErr := pod(recoveryCtx)
					lastErr = getErr
					if getErr == nil {
						Expect(recoveryPodUnchanged(before, after)).To(Succeed())
						break
					}
				} else if lastErr == nil {
					lastErr = fmt.Errorf("still observing original driver or another device: %+v", replacement)
				}
				select {
				case <-recoveryCtx.Done():
				case <-time.After(time.Second):
				}
			}
			Expect(recoveryCtx.Err()).NotTo(HaveOccurred(), "recovery did not complete in one budget: %v", lastErr)
			Expect(lastErr).NotTo(HaveOccurred())
			elapsed := time.Since(started)
			Expect(elapsed).To(BeNumerically("<", driverRecoveryBudget))
			AddReportEntry("driver recovery", fmt.Sprintf("old=%+v new=%+v podUID=%s elapsed=%s",
				original, replacement, before.UID, elapsed))
		})
	})
}
