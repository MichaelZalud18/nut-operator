//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resourcevalidation"
	flowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestLogicalFlowAdmissionAndPlan(t *testing.T) {
	for _, tc := range []struct {
		mode            string
		approved, valid bool
	}{
		{"DryRun", false, true}, {"Enforce", false, false}, {"Enforce", true, true},
	} {
		t.Run(tc.mode+"/approved="+map[bool]string{true: "true", false: "false"}[tc.approved], func(t *testing.T) {
			var flow power.ShutdownFlow
			decodeLogicalFixture(t, logicalFlowManifest("worker-a", tc.mode, tc.approved), &flow)
			_, errs := resourcevalidation.ValidateShutdownFlowFields(&flow)
			if (len(errs) == 0) != tc.valid {
				t.Fatalf("valid=%v, validation errors: %v", tc.valid, errs)
			}
			if !tc.valid && !strings.Contains(errs.ToAggregate().Error(), "metadata.annotations["+flowApproval+"]") {
				t.Fatalf("negative fixture failed for the wrong reason: %v", errs)
			}
			inputs, err := flowadapter.PlannerInputs(&flow)
			if err != nil {
				t.Fatal(err)
			}
			plan, diagnostics, err := planner.Compile(inputs, planner.TelemetryInputs{})
			if err != nil {
				t.Fatalf("compile: %v; %v", err, diagnostics)
			}
			var groups [][]string
			for _, wave := range plan.Waves {
				groups = append(groups, wave.Groups)
			}
			want := [][]string{{"scale"}, {"observe-scale"}, {"drain"}, {"settle-drain"}, {"release"}}
			if !reflect.DeepEqual(groups, want) {
				t.Fatalf("waves = %v, want %v", groups, want)
			}
		})
	}
}

func TestLogicalFlowStackBoundaries(t *testing.T) {
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(logicalFlowStack("worker-a", "worker-b")), 4096)
	var sequence corev1.ConfigMap
	var sequenceList corev1.List
	decodeLogicalFixture(t, logicalFlowSequence(false), &sequenceList)
	if err := json.Unmarshal(sequenceList.Items[0].Raw, &sequence); err != nil {
		t.Fatal(err)
	}
	var ups power.UPSDevice
	var server power.NUTServer
	var agent power.NodePowerAgent
	for _, object := range []any{&ups, &server, &agent} {
		if err := decoder.Decode(object); err != nil {
			t.Fatal(err)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected extra resource: %v, %v", extra, err)
	}
	if !strings.Contains(sequence.Data["sequence.seq"], "ups.status: OL") || strings.Contains(sequence.Data["sequence.seq"], "ups.status: OB") {
		t.Fatal("initial sequence must remain Online until explicitly advanced")
	}
	if ups.Spec.Driver != "dummy-ups" || ups.Spec.Simulation.SequenceConfigMapRef.Name != sequence.Name {
		t.Fatal("UPS bypasses sequence")
	}
	decodeLogicalFixture(t, logicalFlowSequence(true), &sequenceList)
	if err := json.Unmarshal(sequenceList.Items[0].Raw, &sequence); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sequence.Data["sequence.seq"], "ups.status: OB") {
		t.Fatal("outage sequence never transitions")
	}
	if server.Spec.Placement.NodeSelector["kubernetes.io/hostname"] != "worker-b" {
		t.Fatal("NUT can land on the drained worker")
	}
	if server.Spec.Image.Repository != nutServerRepository || server.Spec.Image.Tag != operandImageTag ||
		agent.Spec.Images.Upsmon.Repository != upsmonAgentRepository || agent.Spec.Images.Upsmon.Tag != operandImageTag ||
		agent.Spec.Images.Actuator.Repository != nodeActuatorRepository || agent.Spec.Images.Actuator.Tag != operandImageTag {
		t.Fatal("fixture no longer uses exact suite operand images")
	}
	if agent.Spec.Shutdown.ActuatorPolicy != power.ActuatorPolicySimulate || agent.Spec.Mode != power.NodePowerAgentModeDryRun ||
		agent.Spec.Shutdown.RequireFreshTelemetry == nil || !*agent.Spec.Shutdown.RequireFreshTelemetry {
		t.Fatal("fixture weakened Simulate or telemetry boundary")
	}
}

func TestLogicalFlowWorkloadBoundaries(t *testing.T) {
	var workloads corev1.List
	decodeLogicalFixture(t, logicalFlowWorkloads("worker-a", "worker-b"), &workloads)
	if len(workloads.Items) != 3 {
		t.Fatalf("workloads = %d", len(workloads.Items))
	}
	var deployment appsv1.Deployment
	if err := json.Unmarshal(workloads.Items[0].Raw, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Namespace == flowOperandNamespace || deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "worker-a" {
		t.Fatal("workload is protected from drain or on wrong node")
	}
	for i, expectedNode := range []string{"worker-a", "worker-b"} {
		var pod corev1.Pod
		if err := json.Unmarshal(workloads.Items[i+1].Raw, &pod); err != nil {
			t.Fatal(err)
		}
		if pod.Spec.NodeSelector["kubernetes.io/hostname"] != expectedNode || pod.Spec.Containers[0].Image != nutServerImage || pod.Namespace == flowOperandNamespace {
			t.Fatalf("wrong workload boundary: %+v", pod)
		}
	}
}

func TestLogicalFlowStorageBoundaries(t *testing.T) {
	var postgres corev1.List
	decodeLogicalFixture(t, logicalFlowPostgres("worker-b"), &postgres)
	var database corev1.Pod
	if err := json.Unmarshal(postgres.Items[0].Raw, &database); err != nil {
		t.Fatal(err)
	}
	if database.Spec.NodeSelector["kubernetes.io/hostname"] != "worker-b" || database.Spec.Containers[0].Image != flowPostgresImage {
		t.Fatal("database lost survivor placement or immutable image")
	}
	storageDecoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(logicalFlowStorage()), 4096)
	var secret corev1.Secret
	var cluster power.PowerManagementCluster
	if err := storageDecoder.Decode(&secret); err != nil {
		t.Fatal(err)
	}
	if err := storageDecoder.Decode(&cluster); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(secret.StringData["dsn"])
	if err != nil || dsn.Host != "test2-postgres."+flowOperandNamespace+".svc:5432" {
		t.Fatalf("bad fixture DSN: %v", err)
	}
	if string(cluster.Spec.Storage.Mode) != "ExternalPostgres" || cluster.Spec.Storage.ExternalPostgres.DSNSecretKeyRef.Name != secret.Name {
		t.Fatal("no real audit store")
	}
}

func decodeLogicalFixture(t *testing.T, manifest string, into any) {
	t.Helper()
	if err := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096).Decode(into); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalFlowStalePodIsolation(t *testing.T) {
	no := false
	ds := appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &no,
		Containers: []corev1.Container{
			{Name: "upsmon", Image: upsmonAgentImage, VolumeMounts: []corev1.VolumeMount{{Name: "credentials"}}},
			{Name: "actuator", Image: nodeActuatorImage, Env: []corev1.EnvVar{{Name: "ACTUATOR_POLICY", Value: "Simulate"}},
				VolumeMounts: []corev1.VolumeMount{{Name: "signals", ReadOnly: true}, {Name: "state"}}},
		},
		Volumes: []corev1.Volume{
			{Name: "signals", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: flowAgent + "-node-signals"}}},
			{Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "nut-credentials"}}},
		},
	}}}}
	before := ds.DeepCopy()
	pod, err := logicalFlowStalePod(ds, "worker-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ds, *before) {
		t.Fatal("mutated production render")
	}
	if len(pod.Spec.Containers) != 1 || !reflect.DeepEqual(pod.Spec.Containers[0], ds.Spec.Template.Spec.Containers[1]) ||
		len(pod.Spec.Volumes) != 2 || pod.Spec.Volumes[0].Secret.SecretName != "test2-stale-signal" || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("negative control changed actuator or credential boundaries: %+v", pod.Spec)
	}
	ds.Spec.Template.Spec.Volumes[0].Secret.SecretName = "unexpected"
	if _, err := logicalFlowStalePod(ds, "worker-a"); err == nil {
		t.Fatal("accepted missing production signal mount")
	}
}

func TestLogicalFlowAuditEvidence(t *testing.T) {
	start := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	rows := []logicalFlowAttempt{
		{Action: "ScaleWorkload", Kind: "Deployment", Namespace: flowWorkNamespace, Name: "test2-scale", Outcome: "Succeeded", Started: start, Completed: start.Add(time.Second)},
		{Action: "DrainNodes", Kind: "Node", Name: "worker-a", Outcome: "Succeeded", Started: start.Add(20 * time.Second), Completed: start.Add(21 * time.Second)},
		{Action: "AgentShutdown", Outcome: "Succeeded", Started: start.Add(40 * time.Second), Completed: start.Add(41 * time.Second)},
	}
	rows[0].Details.SelectedTargets, rows[0].Details.Changed = 1, 1
	rows[1].Details.SelectedTargets, rows[1].Details.EvictedPods = 1, 1
	for _, tc := range []struct {
		name   string
		mutate func([]logicalFlowAttempt)
		valid  bool
	}{
		{"ordered targeted effects", func([]logicalFlowAttempt) {}, true},
		{"dry run", func(r []logicalFlowAttempt) { r[1].DryRun = true }, false},
		{"wrong workload", func(r []logicalFlowAttempt) { r[0].Name = "other" }, false},
		{"wrong node", func(r []logicalFlowAttempt) { r[1].Name = "worker-b" }, false},
		{"drain before scale completed", func(r []logicalFlowAttempt) { r[1].Started = start }, false},
		{"no actual eviction", func(r []logicalFlowAttempt) { r[1].Details.EvictedPods = 0 }, false},
		{"blocked release", func(r []logicalFlowAttempt) { r[2].Outcome = "Blocked" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyRows := append([]logicalFlowAttempt(nil), rows...)
			tc.mutate(copyRows)
			data, err := json.Marshal(copyRows)
			if err != nil {
				t.Fatal(err)
			}
			if err := logicalFlowAuditEvidence(data, "worker-a"); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestLogicalFlowDrainPlacement(t *testing.T) {
	nodes := []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "worker-a"}}, {ObjectMeta: metav1.ObjectMeta{Name: "worker-b"}}}
	manager := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "manager", Namespace: namespace, Labels: map[string]string{"control-plane": "controller-manager"}}, Spec: corev1.PodSpec{NodeName: "worker-a"}}
	target, survivor, err := logicalFlowWorkers(nodes, []corev1.Pod{manager})
	if err != nil || target != "worker-b" || survivor != "worker-a" {
		t.Fatalf("placement = %s/%s: %v", target, survivor, err)
	}
	essential := manager.DeepCopy()
	essential.Name, essential.Namespace, essential.Spec.NodeName = "cert-manager", "cert-manager", "worker-b"
	if target, _, err := logicalFlowWorkers(nodes, []corev1.Pod{manager, *essential}); err != nil || target != "worker-b" {
		t.Fatal("shared Deployments must be relocated rather than relying on an empty worker")
	}
	daemon := essential.DeepCopy()
	daemon.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: "cni"}}
	if got := logicalFlowDrainBlockers("worker-b", []corev1.Pod{*daemon}); len(got) != 0 {
		t.Fatalf("DaemonSet should survive real drain: %v", got)
	}
	if got := logicalFlowDrainBlockers("worker-b", []corev1.Pod{*essential}); len(got) != 1 {
		t.Fatal("guard did not catch late placement")
	}
	if _, _, err := logicalFlowWorkers(nodes, nil); err == nil {
		t.Fatal("selected drain worker without locating manager")
	}
}

func TestLogicalFlowReservationTolerations(t *testing.T) {
	for _, tc := range []struct {
		name        string
		tolerations []corev1.Toleration
		want        bool
	}{
		{"production Exists", []corev1.Toleration{{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}, {Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}}, true},
		{"fixture workload", logicalFlowPod("work", "worker-a").Spec.Tolerations, true},
		{"absent", nil, false},
		{"wrong effect", []corev1.Toleration{{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logicalFlowToleratesReservation(tc.tolerations); got != tc.want {
				t.Fatalf("tolerates = %v, want %v", got, tc.want)
			}
		})
	}
}
