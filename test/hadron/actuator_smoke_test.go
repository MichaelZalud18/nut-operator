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
	"context"
	"fmt"
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
)

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

	// PowerOff/Actuate is production's real policy/mode combination -- Simulate or DryRun would
	// never reach the CAP_SYS_BOOT check this test exists to exercise. POWER_SIGNAL_PATHS is left
	// unset deliberately, so the actuator falls back to its own derived per-node default
	// (main.go's defaultSignalPath) exactly as production does, rather than this test inventing a
	// path of its own -- and that default path is never created here, so it never resolves to
	// anything but SignalMissing.
	falseVal := false
	trueVal := true
	nonRootUID := int64(65532)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "actuator-arms-smoke", Namespace: "default"},
		Spec: corev1.PodSpec{
			HostPID:       true,
			RestartPolicy: corev1.RestartPolicyNever,
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
			}},
		},
	}
	if _, err := clientset.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating actuator pod: %v", err)
	}
	t.Cleanup(func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer delCancel()
		if err := clientset.CoreV1().Pods("default").Delete(delCtx, pod.Name, metav1.DeleteOptions{}); err != nil {
			t.Logf("deleting actuator pod during cleanup: %v", err)
		}
	})

	t.Log("waiting for the actuator pod to report armed, with no signal ever written")
	var log string
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "actuator armed log line", func(ctx context.Context) error {
		current, err := clientset.CoreV1().Pods("default").Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.Status.Phase != corev1.PodRunning && current.Status.Phase != corev1.PodPending {
			return fmt.Errorf("pod left Running/Pending unexpectedly: phase=%s", current.Status.Phase)
		}
		req := clientset.CoreV1().Pods("default").GetLogs(pod.Name, &corev1.PodLogOptions{})
		raw, err := req.DoRaw(ctx)
		if err != nil {
			return err
		}
		log = string(raw)
		if !strings.Contains(log, "halt gate=CapabilityPermitted result=pass") {
			return fmt.Errorf("no armed gate line yet:\n%s", log)
		}
		return nil
	}, func(ctx context.Context) {
		current, err := clientset.CoreV1().Pods("default").Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			t.Logf("diagnostic pod fetch failed: %v", err)
			return
		}
		t.Logf("diagnostic pod status: phase=%s\n%+v", current.Status.Phase, current.Status)
	})
	t.Logf("actuator log:\n%s", log)
	if strings.Contains(log, "refusing to arm PowerOff actuation") {
		t.Fatalf("actuator refused to arm -- CAP_SYS_BOOT did not survive in this environment:\n%s", log)
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
