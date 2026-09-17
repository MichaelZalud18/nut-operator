//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func startupValidSample() startupObservation {
	p := startupClient("ns6-unit", 0)
	p.UID = "server-uid"
	p.Spec.ShareProcessNamespace = ptr.To(true)
	p.Spec.Containers[0].Name = "upsd"
	supervisor := *p.Spec.Containers[0].DeepCopy()
	supervisor.Name = "driver-supervisor"
	supervisor.Command = []string{"/usr/local/bin/nut-driver-supervisor"}
	p.Spec.Containers = append(p.Spec.Containers, supervisor)
	p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Unix(100, 0))}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{
		{Name: "upsd", ImageID: "sha256:nut", ContainerID: "containerd://upsd", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		{Name: "driver-supervisor", ImageID: "sha256:nut", ContainerID: "containerd://supervisor", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
	}
	return startupObservation{At: time.Unix(100, 0), Pod: p, Driver: "12:1000"}
}

func TestStartupObservation(t *testing.T) {
	for name, mutate := range map[string]func(*startupObservation){
		"container identity missing": func(s *startupObservation) { s.Pod.Status.ContainerStatuses[0].ContainerID = "" },
		"container identity changed": func(s *startupObservation) { s.Pod.Status.ContainerStatuses[0].ContainerID = "replacement" },
		"container waiting": func(s *startupObservation) {
			s.Pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
		},
		"pod replacement":                 func(s *startupObservation) { s.Pod.UID = "replacement" },
		"driver replacement":              func(s *startupObservation) { s.Driver = "13:2000" },
		"driver absent":                   func(s *startupObservation) { s.Driver = "" },
		"probe regression":                func(s *startupObservation) { s.ProbeError = "timeout" },
		"Ready missing":                   func(s *startupObservation) { s.Pod.Status.Conditions = nil },
		"container status missing":        func(s *startupObservation) { s.Pod.Status.ContainerStatuses = nil },
		"unexpected init":                 func(s *startupObservation) { s.Pod.Spec.InitContainers = []corev1.Container{{Name: "unexpected"}} },
		"Ready false":                     func(s *startupObservation) { s.Pod.Status.Conditions[0].Status = corev1.ConditionFalse },
		"Ready recovered between samples": func(s *startupObservation) { s.Pod.Status.Conditions[0].LastTransitionTime = metav1.Now() },
		"container restart":               func(s *startupObservation) { s.Pod.Status.ContainerStatuses[0].RestartCount = 1 },
		"container exit": func(s *startupObservation) {
			s.Pod.Status.ContainerStatuses[0].State.Terminated = &corev1.ContainerStateTerminated{}
		},
		"image change":  func(s *startupObservation) { s.Pod.Status.ContainerStatuses[0].ImageID = "changed" },
		"image missing": func(s *startupObservation) { s.Pod.Status.ContainerStatuses[0].ImageID = "" },
		"token default": func(s *startupObservation) { s.Pod.Spec.AutomountServiceAccountToken = nil },
		"token enabled": func(s *startupObservation) { s.Pod.Spec.AutomountServiceAccountToken = ptr.To(true) },
		"host path": func(s *startupObservation) {
			s.Pod.Spec.Volumes = append(s.Pod.Spec.Volumes, corev1.Volume{VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}})
		},
		"token projected": func(s *startupObservation) {
			s.Pod.Spec.Volumes = append(s.Pod.Spec.Volumes, corev1.Volume{VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}}}}}})
		},
		"host PID":         func(s *startupObservation) { s.Pod.Spec.HostPID = true },
		"shell supervisor": func(s *startupObservation) { s.Pod.Spec.Containers[1].Command = []string{"sh"} },
		"privileged":       func(s *startupObservation) { s.Pod.Spec.Containers[1].SecurityContext.Privileged = ptr.To(true) },
	} {
		t.Run(name, func(t *testing.T) {
			var a startupAcceptance
			if err := a.observe(startupValidSample()); err != nil {
				t.Fatal(err)
			}
			s := startupValidSample()
			mutate(&s)
			if err := a.observe(s); err == nil {
				t.Fatal("accepted regression")
			}
		})
	}
	t.Run("clock starts before readiness", func(t *testing.T) {
		var a startupAcceptance
		s := startupValidSample()
		s.Pod.Status.Conditions = nil
		s.ProbeError = "not yet ready"
		if err := a.observe(s); err != nil {
			t.Fatal(err)
		}
		if !a.FirstDriver.Equal(s.At) || a.StartupProbeErrors != 1 || !a.ReadyTransition.IsZero() {
			t.Fatalf("wrong startup state: %+v", a)
		}
		if err := a.observe(startupValidSample()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStartupCompletion(t *testing.T) {
	var a startupAcceptance
	if err := a.observe(startupValidSample()); err != nil {
		t.Fatal(err)
	}
	log := ""
	for i := 0; i < startupClients; i++ {
		log += fmt.Sprintf("User observer%d@127.0.0.1 logged into UPS [test]\n", i)
	}
	log += "User observer0@127.0.0.2 logged into UPS [test]\n"
	end := a.FirstDriver.Add(startupWindow)
	if err := a.complete(end, "Started driver", log, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                string
		at                  time.Time
		supervisor, clients string
		reconnected         bool
	}{
		{"short", end.Add(-time.Second), "Started driver", log, true},
		{"early exit", end, "Started driver\nDriver exited\nStarted driver", log, true},
		{"missing launch", end, "", log, true},
		{"missing client", end, "Started driver", "", true},
		{"no injection", end, "Started driver", log, false},
		{"no reconnect login", end, "Started driver", strings.TrimSuffix(log, "User observer0@127.0.0.2 logged into UPS [test]\n"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if a.complete(tc.at, tc.supervisor, tc.clients, tc.reconnected) == nil {
				t.Fatal("accepted incomplete evidence")
			}
		})
	}
}

func TestStartupFixture(t *testing.T) {
	var list corev1.List
	if err := json.Unmarshal([]byte(startupResources("ns6-unit")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatal("want device and server")
	}
	var server power.NUTServer
	if err := json.Unmarshal(list.Items[1].Raw, &server); err != nil {
		t.Fatal(err)
	}
	if server.Name != "ns6-unit" || server.Spec.Namespace != "ns6-unit" || server.Spec.Auth.Mode != power.NUTAuthExistingSecret || server.Spec.Auth.ExistingSecretRef.Name != "startup-users" || server.Spec.Image.Repository != nutServerRepository {
		t.Fatalf("wrong rendered fixture inputs: %+v", server.Spec)
	}
	if err := json.Unmarshal([]byte(startupClientsManifest("ns6-unit")), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 17 {
		t.Fatal("want eight pods, eight configs, and server users")
	}
	for i := 0; i < startupClients; i++ {
		p := startupClient("ns6-unit", i)
		if p.Spec.Containers[0].Image != upsmonAgentImage || *p.Spec.AutomountServiceAccountToken || p.Spec.RestartPolicy != corev1.RestartPolicyNever {
			t.Fatal("unsafe client fixture")
		}
		var config corev1.Secret
		if err := json.Unmarshal(list.Items[i*2].Raw, &config); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(config.StringData["upsmon.conf"], "ns6-unit@ns6-unit") || !strings.Contains(config.StringData["upsmon.conf"], "SHUTDOWNCMD /bin/false") {
			t.Fatal("wrong service or unsafe shutdown command")
		}
	}
}
