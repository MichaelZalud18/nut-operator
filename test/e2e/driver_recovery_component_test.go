//go:build e2e

package e2e

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRecoveryDriverIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"responsive", "IDENTITY\tups\t123\t456\nups\tdummy-ups\tRUNNING\t123\tRESPONSIVE\t123\tOL\n", true},
		{"not responsive", "IDENTITY\tups\t123\t456\nups\tdummy-ups\tRUNNING\t123\tNOT_RESPONSIVE\t123\tOL\n", false},
		{"wrong socket PID", "IDENTITY\tups\t123\t456\nups\tdummy-ups\tRUNNING\t123\tRESPONSIVE\t999\tOL\n", false},
		{"wrong device", "IDENTITY\tups\t123\t456\nother\tdummy-ups\tRUNNING\t123\tRESPONSIVE\t123\tOL\n", false},
		{"missing identity", "ups\tdummy-ups\tRUNNING\t123\tRESPONSIVE\t123\tOL\n", false},
		{"invalid PID", "IDENTITY\tups\t0\t456\nups\tdummy-ups\tRUNNING\t0\tRESPONSIVE\t0\tOL\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRecoveryDriver(tc.output)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
}

func TestRecoveryPodContinuity(t *testing.T) {
	before := corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "original"}, Status: corev1.PodStatus{
		ContainerStatuses:     []corev1.ContainerStatus{{Name: "upsd", ContainerID: "server", RestartCount: 2, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		InitContainerStatuses: []corev1.ContainerStatus{{Name: "driver-supervisor", ContainerID: "supervisor", RestartCount: 1, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
	}}
	for _, tc := range []struct {
		name   string
		change func(*corev1.Pod)
		valid  bool
	}{
		{"unchanged nonzero baseline", func(*corev1.Pod) {}, true},
		{"replaced pod", func(p *corev1.Pod) { p.UID = "replacement" }, false},
		{"server restart", func(p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount++ }, false},
		{"sidecar restart", func(p *corev1.Pod) { p.Status.InitContainerStatuses[0].RestartCount++ }, false},
		{"sidecar replaced", func(p *corev1.Pod) { p.Status.InitContainerStatuses[0].ContainerID = "new" }, false},
		{"missing sidecar status", func(p *corev1.Pod) { p.Status.InitContainerStatuses = nil }, false},
		{"sidecar no longer running", func(p *corev1.Pod) { p.Status.InitContainerStatuses[0].State = corev1.ContainerState{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := before.DeepCopy()
			tc.change(after)
			if err := recoveryPodUnchanged(before, *after); (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
		})
	}
	for _, regularSidecar := range []bool{false, true} {
		baseline := before.DeepCopy()
		if regularSidecar {
			baseline.Status.ContainerStatuses = append(baseline.Status.ContainerStatuses, baseline.Status.InitContainerStatuses[0])
			baseline.Status.InitContainerStatuses = nil
		}
		if err := recoveryPodUnchanged(*baseline, *baseline); err != nil {
			t.Fatalf("valid baseline (regular sidecar=%v): %v", regularSidecar, err)
		}
		statuses := []*corev1.ContainerStatus{&baseline.Status.ContainerStatuses[0]}
		if regularSidecar {
			statuses = append(statuses, &baseline.Status.ContainerStatuses[1])
		} else {
			statuses = append(statuses, &baseline.Status.InitContainerStatuses[0])
		}
		for _, status := range statuses {
			id := status.ContainerID
			status.ContainerID = ""
			if err := recoveryPodUnchanged(*baseline, *baseline); err == nil {
				t.Fatal("accepted empty baseline container ID")
			}
			status.ContainerID = id
			state := status.State
			status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
			if err := recoveryPodUnchanged(*baseline, *baseline); err == nil {
				t.Fatal("accepted non-running baseline process")
			}
			status.State = state
		}
	}
}

func TestRecoveryDriverReplacement(t *testing.T) {
	original := recoveryDriver{"ups", "123", "456"}
	for _, tc := range []struct {
		name    string
		current recoveryDriver
		want    bool
	}{
		{"original still responsive", original, false},
		{"reused PID", recoveryDriver{"ups", "123", "789"}, false},
		{"other device", recoveryDriver{"other", "124", "789"}, false},
		{"fast replacement already responsive", recoveryDriver{"ups", "124", "789"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoveryDriverReplaced(original, tc.current); got != tc.want {
				t.Fatalf("replacement=%v, want %v", got, tc.want)
			}
		})
	}
}
