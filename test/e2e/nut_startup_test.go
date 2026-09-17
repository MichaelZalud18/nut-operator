//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

const startupWindow = 660 * time.Second

func startupRun(ctx context.Context, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--request-timeout=15s"}, args...)...)
	cmd.WaitDelay = time.Second
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func startupReport(name string, value any) {
	data, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())
	_, err = fmt.Fprintf(GinkgoWriter, "NS6 %s %s\n", name, data)
	Expect(err).NotTo(HaveOccurred())
	AddReportEntry("NS6 "+name, string(data))
}

type startupObservation struct {
	At         time.Time
	Pod        corev1.Pod
	Driver     string
	ProbeError string
}

// The first live identity starts a conservative full window, never a Ready wait.
type startupAcceptance struct {
	FirstDriver        time.Time
	Driver, UID        string
	ReadyTransition    time.Time
	ProbeSucceeded     bool
	Images             map[string]string
	ContainerIDs       map[string]string
	StartupProbeErrors int
}

func (a *startupAcceptance) observe(s startupObservation) error {
	p := s.Pod
	if a.UID == "" {
		a.UID = string(p.UID)
	}
	if string(p.UID) != a.UID {
		return fmt.Errorf("NUT pod replaced")
	}
	if err := startupSecurity(p); err != nil {
		return err
	}
	if err := a.observeContainers(p.Status.ContainerStatuses); err != nil {
		return err
	}
	return a.observeReadinessAndDriver(s)
}

func (a *startupAcceptance) observeContainers(statuses []corev1.ContainerStatus) error {
	if len(a.Images) == 2 && len(statuses) != 2 {
		return fmt.Errorf("container status disappeared")
	}
	for _, status := range statuses {
		if err := a.observeContainerIdentity(status); err != nil {
			return err
		}
		if status.Name != "upsd" && status.Name != "driver-supervisor" {
			return fmt.Errorf("unexpected container status %s", status.Name)
		}
		if a.Images[status.Name] != "" && status.ImageID == "" {
			return fmt.Errorf("image identity disappeared")
		}
		if status.RestartCount != 0 || status.LastTerminationState.Terminated != nil || status.State.Terminated != nil {
			return fmt.Errorf("container %s restarted/exited", status.Name)
		}
		if status.ImageID != "" {
			if a.Images == nil {
				a.Images = map[string]string{}
			}
			if old := a.Images[status.Name]; old != "" && old != status.ImageID {
				return fmt.Errorf("image changed")
			}
			a.Images[status.Name] = status.ImageID
		}
	}
	return nil
}

func (a *startupAcceptance) observeContainerIdentity(s corev1.ContainerStatus) error {
	old := a.ContainerIDs[s.Name]
	if old != "" && (s.ContainerID != old || s.State.Running == nil) {
		return fmt.Errorf("container %s identity/running state changed", s.Name)
	}
	if s.State.Running != nil {
		if s.ContainerID == "" || s.ImageID == "" {
			return fmt.Errorf("running container %s missing runtime identity", s.Name)
		}
		if a.ContainerIDs == nil {
			a.ContainerIDs = map[string]string{}
		}
		a.ContainerIDs[s.Name] = s.ContainerID
	}
	return nil
}

func (a *startupAcceptance) observeReadinessAndDriver(s startupObservation) error {
	readyFound := false
	for _, c := range s.Pod.Status.Conditions {
		if c.Type != corev1.PodReady {
			continue
		}
		readyFound = true
		if !a.ReadyTransition.IsZero() && (c.Status != corev1.ConditionTrue || !c.LastTransitionTime.Time.Equal(a.ReadyTransition)) {
			return fmt.Errorf("readiness regressed after first Ready")
		}
		if c.Status == corev1.ConditionTrue {
			a.ReadyTransition = c.LastTransitionTime.Time
		}
	}
	if !readyFound && !a.ReadyTransition.IsZero() {
		return fmt.Errorf("Ready condition disappeared")
	}
	if s.Driver == "" && a.Driver != "" {
		return fmt.Errorf("driver disappeared")
	}
	if s.Driver != "" {
		if a.Driver != "" && a.Driver != s.Driver {
			return fmt.Errorf("driver identity changed")
		}
		if a.FirstDriver.IsZero() {
			a.FirstDriver = s.At
			a.Driver = s.Driver
		}
	}
	if s.ProbeError != "" {
		if a.ProbeSucceeded {
			return fmt.Errorf("readiness helper regressed: %s", s.ProbeError)
		}
		a.StartupProbeErrors++
	} else {
		a.ProbeSucceeded = true
	}
	return nil
}

func (a startupAcceptance) complete(now time.Time, supervisor, upsd string, reconnected bool) error {
	if a.FirstDriver.IsZero() || now.Sub(a.FirstDriver) < startupWindow {
		return fmt.Errorf("incomplete 660-second driver observation")
	}
	if a.ReadyTransition.IsZero() || !a.ProbeSucceeded || a.Images["upsd"] == "" || a.Images["driver-supervisor"] == "" {
		return fmt.Errorf("missing readiness/image evidence")
	}
	if a.ContainerIDs["upsd"] == "" || a.ContainerIDs["driver-supervisor"] == "" {
		return fmt.Errorf("missing Running container identity evidence")
	}
	if strings.Count(supervisor, "Started driver") != 1 || strings.Contains(supervisor, "Driver exited") {
		return fmt.Errorf("unexpected driver launch/exit history")
	}
	for i := 0; i < startupClients; i++ {
		want := 1
		if i == 0 {
			want = 2
		}
		if startupLogins(upsd, i) < want {
			return fmt.Errorf("observer%d missing authenticated login/reconnect", i)
		}
	}
	if !reconnected {
		return fmt.Errorf("missing intentional reconnect")
	}
	return nil
}

func startupLogins(log string, index int) int {
	n := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, fmt.Sprintf("observer%d@", index)) && strings.Contains(line, "logged into UPS") {
			n++
		}
	}
	return n
}

func startupSecurity(p corev1.Pod) error {
	s := p.Spec
	if s.AutomountServiceAccountToken == nil || *s.AutomountServiceAccountToken {
		return fmt.Errorf("service account token automount not disabled")
	}
	for _, v := range s.Volumes {
		if v.HostPath != nil {
			return fmt.Errorf("hostPath volume present")
		}
		if v.Projected != nil {
			for _, source := range v.Projected.Sources {
				if source.ServiceAccountToken != nil {
					return fmt.Errorf("service account token projection present")
				}
			}
		}
	}
	if s.HostPID || s.HostIPC || s.HostNetwork || s.ShareProcessNamespace == nil || !*s.ShareProcessNamespace || len(s.Containers) != 2 || len(s.InitContainers) != 0 || len(s.EphemeralContainers) != 0 {
		return fmt.Errorf("shared PID/sidecar isolation changed")
	}
	if s.SecurityContext == nil || s.SecurityContext.RunAsNonRoot == nil || !*s.SecurityContext.RunAsNonRoot || s.SecurityContext.RunAsUser == nil || *s.SecurityContext.RunAsUser != 65532 || s.SecurityContext.SeccompProfile == nil || s.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		return fmt.Errorf("pod security changed")
	}
	return startupContainerSecurity(s.Containers)
}

func startupContainerSecurity(containers []corev1.Container) error {
	names := map[string]bool{}
	for _, c := range containers {
		names[c.Name] = true
		v := c.SecurityContext
		if v == nil || v.ReadOnlyRootFilesystem == nil || !*v.ReadOnlyRootFilesystem || v.AllowPrivilegeEscalation == nil || *v.AllowPrivilegeEscalation || (v.Privileged != nil && *v.Privileged) || v.Capabilities == nil || len(v.Capabilities.Add) != 0 || !reflect.DeepEqual(v.Capabilities.Drop, []corev1.Capability{"ALL"}) {
			return fmt.Errorf("container %s security changed", c.Name)
		}
		if c.Name == "driver-supervisor" && !reflect.DeepEqual(c.Command, []string{"/usr/local/bin/nut-driver-supervisor"}) {
			return fmt.Errorf("not the Go supervisor")
		}
	}
	if !names["upsd"] || !names["driver-supervisor"] {
		return fmt.Errorf("sidecar topology changed")
	}
	return nil
}

func nutStartupSpecs() {
	It(startupSpecName, Label("NS-6"), func(ctx SpecContext) {
		if os.Getenv("NUT_OPERATOR_E2E_STARTUP") != "true" {
			Skip("requires NUT_OPERATOR_E2E_STARTUP=true; NS-6 acceptance was not performed")
		}
		name := fmt.Sprintf("ns6-%d", time.Now().UnixNano())
		var acceptance startupAcceptance
		var serverPod string
		DeferCleanup(func() {
			// Evidence commands cannot prevent attempting every owned-resource deletion.
			for _, args := range [][]string{{"-n", name, "get", "pods", "-o", "json"}, {"-n", name, "get", "events", "-o", "json"}, {"-n", name, "logs", "-l", "ns6-client=true", "--all-containers=true", "--prefix=true", "--tail=-1", "--max-log-requests=8"}} {
				out, err := startupRun(context.Background(), "", args...)
				startupReport("final evidence", map[string]any{"args": args, "output": out, "error": fmt.Sprint(err)})
			}
			if serverPod != "" {
				for _, container := range []string{"upsd", "driver-supervisor"} {
					out, err := startupRun(context.Background(), "", "-n", name, "logs", serverPod, "-c", container, "--timestamps=true")
					startupReport(container+" full launch history", map[string]any{"output": out, "error": fmt.Sprint(err)})
				}
			}
			var failures []string
			for _, resource := range []string{"nutserver", "upsdevice", "namespace"} {
				out, err := startupRun(context.Background(), "", "delete", resource, name, "--ignore-not-found=true", "--wait=true", "--timeout=20s")
				if err != nil {
					failures = append(failures, resource+": "+out+err.Error())
				}
			}
			startupReport("cleanup", failures)
			Expect(failures).To(BeEmpty())
		})
		getPods := func(ns, selector string) []corev1.Pod {
			out, err := startupRun(ctx, "", "-n", ns, "get", "pods", "-l", selector, "-o", "json")
			Expect(err).NotTo(HaveOccurred(), out)
			var list corev1.PodList
			Expect(json.Unmarshal([]byte(out), &list)).To(Succeed())
			return list.Items
		}
		apply := func(manifest string) {
			out, err := startupRun(ctx, manifest, "apply", "-f", "-")
			Expect(err).NotTo(HaveOccurred(), out)
		}
		manager := getPods(namespace, "control-plane=controller-manager")
		Expect(manager).To(HaveLen(1))
		Expect(manager[0].Status.ContainerStatuses).NotTo(BeEmpty())
		startupReport("manager baseline", manager)
		out, err := startupRun(ctx, "", "create", "namespace", name)
		Expect(err).NotTo(HaveOccurred(), out)
		apply(startupClientsManifest(name))
		Eventually(func() bool {
			pods := getPods(name, "ns6-client=true")
			if len(pods) != startupClients {
				return false
			}
			for _, p := range pods {
				if p.Status.Phase != corev1.PodRunning {
					return false
				}
			}
			return true
		}, 2*time.Minute, 2*time.Second).Should(BeTrue())
		clientUIDs := map[string]string{}
		for _, p := range getPods(name, "ns6-client=true") {
			clientUIDs[p.Name] = string(p.UID)
		}
		startupReport("launch inputs", map[string]any{"namespace": name, "clients": startupClients, "window_seconds": 660, "manager_image": managerImage, "nut_image": nutServerImage, "monitor_image": upsmonAgentImage})
		launched := time.Now()
		apply(startupResources(name))
		reconnected := false
		reconnectLogins := 0
		oldClientUID := clientUIDs["observer-0"]
		for {
			finished := false
			Expect(time.Since(launched)).To(BeNumerically("<", 15*time.Minute), "startup never reached acceptance")
			pods := getPods(name, "power.zalud.io/nutserver="+name)
			Expect(len(pods)).To(BeNumerically("<=", 1))
			if len(pods) == 1 {
				p := pods[0]
				serverPod = p.Name
				probe, probeErr := startupRun(ctx, "", "-n", name, "exec", p.Name, "-c", "upsd", "--", "/usr/local/bin/nut-driver-ready")
				identity, identityErr := startupRun(ctx, "", "-n", name, "exec", p.Name, "-c", "upsd", "--", "sh", "-ec", recoveryDriverIdentityScript+`printf '%s:%s\n' "$pid" "$ticks"`)
				if identityErr != nil {
					identity = ""
				}
				sample := startupObservation{At: time.Now(), Pod: p, Driver: strings.TrimSpace(identity)}
				if probeErr != nil {
					sample.ProbeError = fmt.Sprintf("%v: %s", probeErr, probe)
				}
				startupReport("sample", sample)
				Expect(acceptance.observe(sample)).To(Succeed())
				if !acceptance.FirstDriver.IsZero() {
					upsd, logErr := startupRun(ctx, "", "-n", name, "logs", p.Name, "-c", "upsd")
					Expect(logErr).NotTo(HaveOccurred(), upsd)
					authenticated := true
					for i := 0; i < startupClients; i++ {
						authenticated = authenticated && startupLogins(upsd, i) > 0
					}
					if time.Since(acceptance.FirstDriver) > 90*time.Second {
						Expect(authenticated).To(BeTrue(), "all monitors must participate in the startup window")
					}
					if !reconnected && authenticated && time.Since(acceptance.FirstDriver) >= 30*time.Second {
						startupReport("pre-delete logins", startupLogins(upsd, 0))
						out, err := startupRun(ctx, "", "-n", name, "delete", "pod", "observer-0", "--wait=true", "--timeout=20s")
						Expect(err).NotTo(HaveOccurred(), out)
						settledLog, err := startupRun(ctx, "", "-n", name, "logs", p.Name, "-c", "upsd")
						Expect(err).NotTo(HaveOccurred(), settledLog)
						reconnectLogins = startupLogins(settledLog, 0)
						startupReport("intentional monitor restart", map[string]any{"old_uid": clientUIDs["observer-0"], "at": time.Now()})
						apply(logicalFlowJSONList(startupClient(name, 0)))
						delete(clientUIDs, "observer-0")
						reconnected = true
					}
					if time.Since(acceptance.FirstDriver) >= startupWindow {
						finished = true
					}
				}
			} else {
				Expect(acceptance.UID).To(BeEmpty(), "NUT pod disappeared")
			}
			clients := getPods(name, "ns6-client=true")
			startupCheckClients(clients, clientUIDs, oldClientUID, finished)
			currentManager := getPods(namespace, "control-plane=controller-manager")
			Expect(currentManager).To(HaveLen(1))
			Expect(currentManager[0].UID).To(Equal(manager[0].UID))
			Expect(currentManager[0].Status.ContainerStatuses).To(HaveLen(len(manager[0].Status.ContainerStatuses)))
			for _, s := range currentManager[0].Status.ContainerStatuses {
				var baseline *corev1.ContainerStatus
				for i := range manager[0].Status.ContainerStatuses {
					if manager[0].Status.ContainerStatuses[i].Name == s.Name {
						baseline = &manager[0].Status.ContainerStatuses[i]
					}
				}
				Expect(baseline).NotTo(BeNil())
				Expect(s.RestartCount).To(Equal(baseline.RestartCount))
				Expect(s.ImageID).NotTo(BeEmpty())
				Expect(s.ImageID).To(Equal(baseline.ImageID))
				Expect(s.State.Running).NotTo(BeNil())
			}
			if finished {
				supervisor, err := startupRun(ctx, "", "-n", name, "logs", serverPod, "-c", "driver-supervisor")
				Expect(err).NotTo(HaveOccurred(), supervisor)
				upsd, err := startupRun(ctx, "", "-n", name, "logs", serverPod, "-c", "upsd")
				Expect(err).NotTo(HaveOccurred(), upsd)
				Expect(startupLogins(upsd, 0)).To(BeNumerically(">", reconnectLogins), "new Running monitor must authenticate after old client deletion")
				Expect(acceptance.complete(time.Now(), supervisor, upsd, reconnected)).To(Succeed())
				startupReport("manager final", currentManager)
				startupReport("acceptance", acceptance)
				break
			}
			select {
			case <-ctx.Done():
				Fail("NS6 observation canceled")
			case <-time.After(5 * time.Second):
			}
		}
	}, SpecTimeout(20*time.Minute))
}

func startupCheckClients(clients []corev1.Pod, clientUIDs map[string]string, oldClientUID string, finished bool) {
	startupReport("monitors", clients)
	Expect(clients).To(HaveLen(startupClients))
	for _, p := range clients {
		if old := clientUIDs[p.Name]; old != "" {
			Expect(string(p.UID)).To(Equal(old), "unexpected monitor replacement")
		} else {
			clientUIDs[p.Name] = string(p.UID)
			startupReport("replacement monitor", p)
		}
		Expect(p.Status.Phase).NotTo(BeElementOf(corev1.PodFailed, corev1.PodSucceeded))
		if finished {
			Expect(p.Status.Phase).To(Equal(corev1.PodRunning))
			Expect(p.Status.ContainerStatuses).To(HaveLen(1))
			if p.Name == "observer-0" {
				Expect(string(p.UID)).NotTo(Equal(oldClientUID))
			}
		}
		for _, s := range p.Status.ContainerStatuses {
			Expect(s.RestartCount).To(BeZero())
			Expect(s.State.Terminated).To(BeNil())
			if finished {
				Expect(s.ImageID).NotTo(BeEmpty())
				Expect(s.State.Running).NotTo(BeNil())
			}
		}
	}
}
