//go:build hadron_smoke
// +build hadron_smoke

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

package hadron

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/spectrocloud/peg/pkg/machine/types"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// actuatorGuest is one Hadron guest, booted to a Ready k3s node, with the real node-actuator image
// already imported into its own containerd -- the shared precondition for every actuator smoke
// test below, so each test's own body is only the part that differs: what signal (if any) reaches
// the actuator, and what that must do to the guest.
type actuatorGuest struct {
	machine   types.Machine
	creds     Credentials
	clientset *kubernetes.Clientset
	nodeName  string
	imageRef  string
}

// bootActuatorReadyGuest boots one guest via the same install path TestHadronSingleNodeBoot
// already proved, builds the real production actuator image, and imports it into that guest's own
// containerd -- everything every actuator test needs before it can differ on the one thing it's
// actually testing.
func bootActuatorReadyGuest(ctx context.Context, t *testing.T) actuatorGuest {
	t.Helper()
	imageRef, tarPath := buildActuatorImageTarball(ctx, t)

	m, creds, err := NewSafeMachineContext(ctx, Config{
		Memory:         "4096",
		CPUs:           "2",
		ISO:            hadronISOURL,
		ISOChecksum:    hadronISOChecksum,
		CloudConfig:    func(c Credentials) string { return KairosAutoInstallCloudConfig(c, "/dev/vda") },
		ForwardKubeAPI: true,
	})
	if err != nil {
		t.Fatalf("NewSafeMachine: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("leaving machine state at %s for inspection (stdout/stderr hold the guest console)", m.Config().StateDir)
			if err := SafeStop(m, 30*time.Second); err != nil {
				t.Errorf("stop: %v", err)
			}
			return
		}
		if err := SafeStop(m, 30*time.Second); err != nil {
			t.Errorf("teardown: %v", err)
			return
		}
		if err := m.Clean(); err != nil {
			t.Errorf("removing stopped machine state: %v", err)
		}
	})
	if _, err := m.Create(ctx); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Logf("waiting for SSH on 127.0.0.1:%s as %s", creds.Port, creds.User)
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "SSH", func(ctx context.Context) error {
		_, err := guestCommand(ctx, creds, "true")
		return err
	}, nil)

	t.Log("waiting for a Ready k3s node")
	waitForWithDiagnostics(t, ctx, 10*time.Minute, "k3s readiness", func(ctx context.Context) error {
		out, err := guestCommand(ctx, creds, "sudo k3s kubectl get nodes --request-timeout=20s -o json")
		if err != nil {
			return err
		}
		if !hasReadyNode(out) {
			return fmt.Errorf("no Ready node yet:\n%s", out)
		}
		return nil
	}, nil)

	kubeconfig, err := Kubeconfig(ctx, creds)
	if err != nil {
		t.Fatalf("Kubeconfig: %v", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		t.Fatalf("parsing fetched kubeconfig: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatalf("building client from fetched kubeconfig: %v", err)
	}
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing nodes through the forwarded API port: %v", err)
	}
	if len(nodes.Items) != 1 {
		t.Fatalf("expected exactly one node through the forwarded API, got %d", len(nodes.Items))
	}
	nodeName := nodes.Items[0].Name
	t.Logf("target node: %s", nodeName)

	t.Log("importing the real node-actuator image into the guest's own containerd")
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open image tarball: %v", err)
	}
	defer func() { _ = f.Close() }()
	if out, err := guestCommandStdin(ctx, creds, "sudo k3s ctr -n k8s.io images import -", f); err != nil {
		t.Fatalf("importing actuator image into guest containerd: %v\n%s", err, out)
	}

	return actuatorGuest{machine: m, creds: creds, clientset: clientset, nodeName: nodeName, imageRef: imageRef}
}

// actuatorPodSpec is production's real PowerOff/Actuate security context and environment shape
// (internal/controller/nodepoweragent_render.go's actuatorContainerSecurityContext, minus the
// full DaemonSet/RBAC this milestone deliberately does not build yet -- see
// docs/contributing/audits/hadron-vm-3-actuator-2026-09-13.md for why a bare Pod is enough here).
func actuatorPodSpec(name, imageRef, nodeName string, signalSecretName string) *corev1.Pod {
	falseVal := false
	trueVal := true
	nonRootUID := int64(65532)
	volumes := []corev1.Volume{{
		Name: "power-agent-actuator-state",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}}
	mounts := []corev1.VolumeMount{{Name: "power-agent-actuator-state", MountPath: "/run/actuator"}}
	if signalSecretName != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "power-agent-signals",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: signalSecretName},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{Name: "power-agent-signals", MountPath: "/var/lib/power-agent/signals", ReadOnly: true})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
			HostPID:       true,
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes:       volumes,
			Containers: []corev1.Container{{
				Name:            "actuator",
				Image:           imageRef,
				ImagePullPolicy: corev1.PullNever,
				Env: []corev1.EnvVar{
					{Name: "POWER_AGENT_MODE", Value: "Actuate"},
					{Name: "POWER_ACTUATOR_POLICY", Value: "PowerOff"},
					{Name: "POWER_NODE_NAME", Value: nodeName},
				},
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &trueVal,
					RunAsUser:                &nonRootUID,
					ReadOnlyRootFilesystem:   &trueVal,
					AllowPrivilegeEscalation: &falseVal,
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
						Add:  []corev1.Capability{"SYS_BOOT"},
					},
				},
				VolumeMounts: mounts,
			}},
		},
	}
}

// waitForActuatorLog polls the pod's own log for want, failing the test if the pod ever leaves
// Running/Pending (a crash is never the expected outcome of any scenario below, including the
// rejected-signal ones -- a rejected signal must stop at its own gate, not crash the container).
func waitForActuatorLog(ctx context.Context, t *testing.T, clientset *kubernetes.Clientset, podName, what, want string) string {
	t.Helper()
	var log string
	waitForWithDiagnostics(t, ctx, 2*time.Minute, what, func(ctx context.Context) error {
		current, err := clientset.CoreV1().Pods("default").Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.Status.Phase != corev1.PodRunning && current.Status.Phase != corev1.PodPending {
			return fmt.Errorf("pod left Running/Pending unexpectedly: phase=%s", current.Status.Phase)
		}
		raw, err := clientset.CoreV1().Pods("default").GetLogs(podName, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err != nil {
			return err
		}
		log = string(raw)
		if !strings.Contains(log, want) {
			return fmt.Errorf("no %q yet:\n%s", want, log)
		}
		return nil
	}, func(ctx context.Context) {
		current, err := clientset.CoreV1().Pods("default").Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Logf("diagnostic pod fetch failed: %v", err)
			return
		}
		t.Logf("diagnostic pod status: phase=%s\n%+v", current.Status.Phase, current.Status)
	})
	return log
}

// TestHadronActuatorArmsWithNoSignal is VM-3's first milestone: get the real, shipped
// node-actuator image -- built from this checkout's own images/node-actuator/Dockerfile, not a
// stand-in binary -- running inside a Hadron guest's real k3s/containerd/kubelet stack, and prove
// its one load-bearing precondition holds there: CAP_SYS_BOOT survives to the point the actuator
// checks for it (F-61's own gate, "halt gate=CapabilityPermitted result=pass").
//
// This deliberately does not attempt actuation yet. No signal is ever written, so the actuator's
// own watch loop can only ever observe SignalMissing -- the normal, silent, non-crashing case
// (cmd/node-actuator/main.go's own comment: "SignalMissing is the normal case on every tick and is
// deliberately not logged"). Proving that alone, before wiring any real ShutdownFlow-driven signal,
// isolates exactly one new mechanism (the actuator running for real, under Kubernetes' own kubelet
// and Pod Security Admission, with the exact security context production renders) from everything
// still to come: real signal delivery, real reboot(2), and hypervisor-confirmed evidence that the
// guest powered itself off rather than being killed by the test harness (VM-3's own text, and
// node-agent-operand.md's OD-27 -- the operator has no independent channel to learn what the
// actuator did, so proving it here means watching the guest from outside, not asking it).
//
// F-61 (the capability surviving the switch to UID 65532) is already closed and verified via Kind
// -- a real kubelet and containerd, just not a real kernel underneath them. This test exercises a
// different delivery path (a local containerd image import into k3s's own embedded containerd,
// not a registry pull) against a real VM kernel, which is a legitimate, narrower thing to confirm
// once, not a re-litigation of F-61's own finding.
func TestHadronActuatorArmsWithNoSignal(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootActuatorReadyGuest(ctx, t)

	// POWER_SIGNAL_PATHS is left unset deliberately, so the actuator falls back to its own derived
	// per-node default (main.go's defaultSignalPath) exactly as production does, rather than this
	// test inventing a path of its own -- and no signal volume is mounted at all, so that default
	// path never resolves to anything but SignalMissing.
	pod := actuatorPodSpec("actuator-arms-smoke", guest.imageRef, guest.nodeName, "")
	if _, err := guest.clientset.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating actuator pod: %v", err)
	}
	t.Cleanup(func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer delCancel()
		if err := guest.clientset.CoreV1().Pods("default").Delete(delCtx, pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Logf("deleting actuator pod during cleanup: %v", err)
		}
	})

	t.Log("waiting for the actuator pod to report armed, with no signal ever written")
	log := waitForActuatorLog(ctx, t, guest.clientset, pod.Name, "actuator armed log line", "halt gate=CapabilityPermitted result=pass")
	t.Logf("actuator log:\n%s", log)
	if strings.Contains(log, "refusing to arm PowerOff actuation") {
		t.Fatalf("actuator refused to arm -- CAP_SYS_BOOT did not survive in this environment:\n%s", log)
	}
}

// TestHadronActuatorRejectsInvalidSignals is VM-3's second milestone: real signal delivery,
// scoped to the negative cases VM-3's own text names explicitly -- "negative cases must leave the
// guest running" -- before ever attempting the positive one, which is the only path that can
// actually halt the guest.
//
// Each case below is rejected by internal/nodeagent.InspectSignal before cmd/node-actuator/main.go
// ever calls its actuatorFunc (powerOffActuator, the only thing that can call reboot(2)) --
// confirmed by reading the gate order, not assumed: watchSignals checks gateSignalAccepted and
// gateFlowBinding first, and only an accepted, correctly-bound signal ever reaches
// gateModeAuthorized inside powerOffActuator itself. So proving the guest survives each of these
// is a direct, load-bearing consequence of that gate ordering holding on a real kernel, not a
// coincidence of nothing having gone wrong yet.
func TestHadronActuatorRejectsInvalidSignals(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootActuatorReadyGuest(ctx, t)

	cases := []struct {
		name       string
		wantReason string
		signal     func(nodeName string) nodeagent.ShutdownSignal
	}{
		{
			name:       "wrong-node",
			wantReason: "SignalWrongNode",
			signal: func(nodeName string) nodeagent.ShutdownSignal {
				return nodeagent.ShutdownSignal{
					ExecutionID:    "exec-wrong-node",
					NodeName:       nodeName + "-not-this-one",
					PlanConfigHash: "test-hash",
					ShutdownFlow:   "test-flow",
					Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
				}
			},
		},
		{
			name:       "stale",
			wantReason: "SignalStale",
			signal: func(nodeName string) nodeagent.ShutdownSignal {
				return nodeagent.ShutdownSignal{
					ExecutionID:    "exec-stale",
					NodeName:       nodeName,
					PlanConfigHash: "test-hash",
					ShutdownFlow:   "test-flow",
					// Default POWER_SIGNAL_TTL is 2m (main.go's loadActuatorConfig); ten minutes
					// old is unambiguously past it without depending on how close to that boundary
					// the test happens to run.
					Timestamp: time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano),
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			secretName := "actuator-signal-" + tc.name
			payload, err := json.Marshal(tc.signal(guest.nodeName))
			if err != nil {
				t.Fatalf("encode signal: %v", err)
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
				Data: map[string][]byte{
					guest.nodeName + ".json":        payload,
					nodeagent.DeliveryChannelMarker: []byte(""),
				},
			}
			if _, err := guest.clientset.CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{}); err != nil {
				t.Fatalf("creating signal secret: %v", err)
			}
			t.Cleanup(func() {
				delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer delCancel()
				if err := guest.clientset.CoreV1().Secrets("default").Delete(delCtx, secretName, metav1.DeleteOptions{}); err != nil {
					t.Logf("deleting signal secret during cleanup: %v", err)
				}
			})

			pod := actuatorPodSpec("actuator-signal-"+tc.name, guest.imageRef, guest.nodeName, secretName)
			if _, err := guest.clientset.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
				t.Fatalf("creating actuator pod: %v", err)
			}
			t.Cleanup(func() {
				delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer delCancel()
				if err := guest.clientset.CoreV1().Pods("default").Delete(delCtx, pod.Name, metav1.DeleteOptions{}); err != nil {
					t.Logf("deleting actuator pod during cleanup: %v", err)
				}
			})

			log := waitForActuatorLog(ctx, t, guest.clientset, pod.Name, "signal rejection log line",
				"halt gate=SignalAccepted result=fail detail=\""+tc.wantReason)
			t.Logf("actuator log:\n%s", log)
			if strings.Contains(log, "halt gate=ModeAuthorized") || strings.Contains(log, "halt gate=SyscallIssued") {
				t.Fatalf("actuator reached actuation gates on a signal that should have been rejected at SignalAccepted:\n%s", log)
			}

			t.Log("confirming the guest is still reachable -- the rejected signal must not have halted it")
			if _, err := guestCommand(ctx, guest.creds, "true"); err != nil {
				t.Fatalf("guest unreachable after a signal that should have been rejected, not actuated: %v", err)
			}
		})
	}
}

// TestHadronActuatorHaltsOnAcceptedSignal is VM-3's third milestone and its actual high-severity
// core: a real, accepted signal, a real reboot(2), and hypervisor-confirmed evidence that the
// guest halted itself rather than being stopped by this test harness.
//
// The evidence problem is exactly what VM-3's own text and node-agent-operand.md's OD-27 warn
// about: neither the operator nor this test has a channel inside the guest to ask what happened,
// and once reboot(2) fires the guest -- including its own k3s API server -- disappears atomically.
//
// A first version of this test assumed halt gate=SignalAccepted result=pass was safe to require,
// reasoning that it happens early enough (well before the sync/reboot race) not to be a race
// itself. A live run disproved that: the guest halted correctly, confirmed independently below,
// but the streamed log came back completely empty -- the whole path a log line has to travel
// (container stdout -> containerd's own log file -> kubelet -> this guest's own k3s API server)
// is itself slow enough, relative to how fast a small idle guest reaches reboot(2) once a valid
// signal is already waiting for it, that no line reliably survives the trip. gateSyscallIssued's
// own comment already said as much for anything after it ("no further output is expected from
// this container"); the live run showed the same is true even this early. So nothing captured
// from the pod's own log is a pass condition here -- it is logged when present, for its
// diagnostic value, and its complete absence is expected, not a failure.
//
// The one thing this test actually requires is entirely outside that pipe: the QEMU process PEG
// is driving exits on its own, discovered by polling its PID directly and never calling
// SafeStop/SafeTeardown ourselves first -- hypervisor-visible proof of a real halt that does not
// depend on the guest's own Kubernetes API surviving long enough to say anything about it. Nothing
// else in this single-purpose guest holds CAP_SYS_BOOT or has a reason to call reboot(2), and the
// two prior tests in this file prove the same guest shape does not exit on its own without a valid
// signal -- so a self-driven exit here is not a coincidence.
func TestHadronActuatorHaltsOnAcceptedSignal(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	guest := bootActuatorReadyGuest(ctx, t)

	secretName := "actuator-signal-accepted"
	payload, err := json.Marshal(nodeagent.ShutdownSignal{
		ExecutionID:    "exec-accepted",
		NodeName:       guest.nodeName,
		PlanConfigHash: "test-hash",
		ShutdownFlow:   "test-flow",
		Timestamp:      time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("encode signal: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
		Data: map[string][]byte{
			guest.nodeName + ".json":        payload,
			nodeagent.DeliveryChannelMarker: []byte(""),
		},
	}
	// Created before the pod, matching production's own ordering (the operator writes the Secret,
	// then the DaemonSet's actuator starts and finds it already there) -- watchSignals' own first
	// pass runs before its first tick, so a pod that starts with a valid signal already mounted
	// begins actuating on its very first read, not on some later poll.
	if _, err := guest.clientset.CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating signal secret: %v", err)
	}
	t.Cleanup(func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer delCancel()
		if err := guest.clientset.CoreV1().Secrets("default").Delete(delCtx, secretName, metav1.DeleteOptions{}); err != nil {
			t.Logf("deleting signal secret during cleanup (guest is likely already gone): %v", err)
		}
	})

	pod := actuatorPodSpec("actuator-signal-accepted", guest.imageRef, guest.nodeName, secretName)

	// Started before the pod exists and read for as long as the connection lasts: polling logs
	// after the fact cannot see a line the guest never survives long enough to be asked about
	// again.
	logCtx, logCancel := context.WithCancel(ctx)
	defer logCancel()
	logLines := make(chan string, 256)
	go streamActuatorLog(logCtx, guest.clientset, pod.Name, logLines)

	if _, err := guest.clientset.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating actuator pod: %v", err)
	}
	t.Cleanup(func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer delCancel()
		if err := guest.clientset.CoreV1().Pods("default").Delete(delCtx, pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Logf("deleting actuator pod during cleanup (guest is likely already gone): %v", err)
		}
	})

	t.Log("waiting for the guest's own QEMU process to exit on its own -- this test never stops it itself")
	process, err := machineProcess(guest.machine)
	if err != nil {
		t.Fatalf("getting machine process handle: %v", err)
	}
	defer func() { _ = process.Release() }()
	haltCtx, haltCancel := context.WithTimeout(ctx, 3*time.Minute)
	defer haltCancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	exited := false
	for !exited {
		select {
		case <-haltCtx.Done():
			t.Fatalf("guest process never exited on its own within the budget -- the actuator armed but the guest did not actually halt (a container outside the host PID namespace can call reboot(2) successfully and leave the machine running)")
		case <-ticker.C:
			if err := process.Signal(syscall.Signal(0)); err != nil {
				exited = true
			}
		}
	}
	t.Log("confirmed: the guest's own QEMU process exited on its own, without this test stopping it")

	// A short grace period for the streaming goroutine to push whatever it already received into
	// the buffered channel before this drains it -- the goroutine and this exit check race by
	// construction, and this is not the race the test is trying to measure.
	time.Sleep(500 * time.Millisecond)
	logCancel()
	var captured []string
collect:
	for {
		select {
		case line := <-logLines:
			captured = append(captured, line)
		default:
			break collect
		}
	}
	log := strings.Join(captured, "\n")
	if log == "" {
		t.Log("no actuator log captured before the guest disconnected -- expected (see this test's own doc comment), not a failure")
	} else {
		t.Logf("captured actuator log (best-effort, up to the guest's own disconnect):\n%s", log)
	}
}

// streamActuatorLog follows podName's log for as long as the connection lasts, sending each line
// to lines. Meant to be started before the pod even exists and run in its own goroutine: a dying
// guest can end this stream at any moment, including mid-line, which is expected here and not
// itself a failure -- the caller decides what an incomplete capture means. Each retry's own error
// is also sent to lines (prefixed), so a run that never manages to open the stream at all says why
// instead of just coming back empty.
func streamActuatorLog(ctx context.Context, clientset *kubernetes.Clientset, podName string, lines chan<- string) {
	send := func(line string) {
		select {
		case lines <- line:
		case <-ctx.Done():
		}
	}
	// The pod does not exist yet when this starts (it is launched before pod creation, to not miss
	// the earliest lines) -- retry until the stream opens or the context ends.
	var stream io.ReadCloser
	for stream == nil {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s, err := clientset.CoreV1().Pods("default").GetLogs(podName, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
		if err != nil {
			send(fmt.Sprintf("[stream retry] %v", err))
			time.Sleep(500 * time.Millisecond)
			continue
		}
		stream = s
	}
	defer func() { _ = stream.Close() }()
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		select {
		case lines <- scanner.Text():
		case <-ctx.Done():
			return
		}
	}
}

// buildActuatorImageTarball builds the real node-actuator image from this checkout's own
// production Dockerfile and saves it to a local docker-save tarball, so the guest imports the
// exact artifact this repository would ship -- not a stand-in binary or a hand-rolled security
// context that could quietly diverge from images.yml's real build.
func buildActuatorImageTarball(ctx context.Context, t *testing.T) (imageRef, tarPath string) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	repoRoot := strings.TrimSpace(string(out))
	tag := fmt.Sprintf("nut-operator-hadron-actuator-test:%d", time.Now().UnixNano())
	build := exec.CommandContext(ctx, "docker", "build",
		"-f", filepath.Join(repoRoot, "images", "node-actuator", "Dockerfile"),
		"-t", tag, repoRoot)
	if buildOut, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build node-actuator: %v\n%s", err, buildOut)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })

	tarPath = filepath.Join(t.TempDir(), "node-actuator.tar")
	save := exec.CommandContext(ctx, "docker", "save", "-o", tarPath, tag)
	if saveOut, err := save.CombinedOutput(); err != nil {
		t.Fatalf("docker save node-actuator: %v\n%s", err, saveOut)
	}
	// docker normalizes an unqualified local tag to this form on build (confirmed against this
	// exact Dockerfile's own build output), and that is the reference docker save/ctr import carry
	// through -- not the bare tag the build command was given.
	return "docker.io/library/" + tag, tarPath
}
