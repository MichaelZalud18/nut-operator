//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	flowOperandNamespace = "test2-operands"
	flowWorkNamespace    = "test2-workloads"
	flowAgent            = "test2-agent"
	flowName             = "test2-flow"
	flowApproval         = "power.zalud.io/approved-for-enforce"
	flowDrainTaint       = "power.zalud.io/test2-drain"
	// Same immutable PostgreSQL fixture as the existing outage acceptance test.
	flowPostgresImage = "docker.io/library/postgres:16-alpine@sha256:cf78e76683b9ca8c5733cbbdce6c9262b45b6767934dd0a95e671f9a0fc20685"
)

func logicalFlowSequence(outage bool) string {
	sequence := "device.mfr: nut-operator\ndevice.model: test2\nups.mfr: nut-operator\nups.model: test2\nups.status: OL\nbattery.charge: 100\nbattery.runtime: 3600\nups.load: 10\n\n"
	if outage {
		sequence += "TIMER 5\n\nups.status: OB\nbattery.charge: 40\nbattery.runtime: 1800\nups.load: 10\n\nTIMER 900\n"
	} else {
		// Loop Online indefinitely until readiness and the ineligible flow are observed.
		sequence += "TIMER 900\n"
	}
	return logicalFlowJSONList(corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "test2-sequence", Namespace: flowOperandNamespace},
		Data:       map[string]string{"sequence.seq": sequence}})
}

func logicalFlowStack(node, survivor string) string {
	return fmt.Sprintf(`
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: test2-ups
spec:
  displayName: TEST-2 logical shutdown
  driver: dummy-ups
  powerDomains: [test2-domain]
  simulation:
    sequenceConfigMapRef:
      namespace: %[1]s
      name: test2-sequence
  telemetry:
    pollInterval: 2s
    alertPollInterval: 2s
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: test2-server
spec:
  namespace: %[1]s
  deviceRefs: [{name: test2-ups}]
  placement:
    nodeSelector:
      kubernetes.io/hostname: %[7]s
  image:
    repository: %[3]s
    tag: %[6]q
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
---
apiVersion: power.zalud.io/v1alpha1
kind: NodePowerAgent
metadata:
  name: test2-agent
spec:
  namespace: %[1]s
  nutServerRefs: [{name: test2-server}]
  nodeSelector:
    matchLabels:
      kubernetes.io/hostname: %[2]s
  mode: DryRun
  images:
    upsmon:
      repository: %[4]s
      tag: %[6]q
      pullPolicy: IfNotPresent
    actuator:
      repository: %[5]s
      tag: %[6]q
      pullPolicy: IfNotPresent
  shutdown:
    actuatorPolicy: Simulate
    signalTTL: 2m
    requireFreshTelemetry: true
`, flowOperandNamespace, node, nutServerRepository, upsmonAgentRepository, nodeActuatorRepository, operandImageTag, survivor)
}

func logicalFlowManifest(node, mode string, approved bool) string {
	annotation := ""
	if approved {
		annotation = fmt.Sprintf("  annotations:\n    %s: \"true\"\n", flowApproval)
	}
	return fmt.Sprintf(`apiVersion: power.zalud.io/v1alpha1
kind: ShutdownFlow
metadata:
  name: test2-flow
%[1]sspec:
  managementClusterRef: {name: test2-cluster}
  mode: %[2]s
  triggers:
    - type: OnBattery
      powerDomains: [test2-domain]
      for: 1s
  groups:
    - name: scale
      action: ScaleWorkload
      shutdownTier: 2
      target:
        workloadRefs:
          - {apiVersion: apps/v1, kind: Deployment, namespace: test2-workloads, name: test2-scale}
      params: {replicas: "0"}
      timeout: 1m
    - name: observe-scale
      action: Wait
      shutdownTier: 2
      after: [scale]
      params: {duration: 20s}
      timeout: 1m
    - name: drain
      action: DrainNodes
      shutdownTier: 2
      after: [observe-scale]
      target:
        nodeSelector:
          matchLabels:
            kubernetes.io/hostname: %[3]s
      timeout: 1m
    - name: settle-drain
      action: Wait
      shutdownTier: 2
      after: [drain]
      params: {duration: 20s}
      timeout: 1m
    - name: release
      action: AgentShutdown
      shutdownTier: 1
      after: [settle-drain]
      target:
        agentRefs: [{name: test2-agent}]
      timeout: 2m
  safety:
    requireManualApproval: true
    approvalAnnotation: %[4]s
    allowUnidentifiedDevices: true
`, annotation, mode, node, flowApproval)
}

func logicalFlowStorage() string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: test2-dsn
  namespace: %[1]s
stringData:
  dsn: postgres://test2:test2-fixture@test2-postgres.%[1]s.svc:5432/test2?sslmode=disable
---
apiVersion: power.zalud.io/v1alpha1
kind: PowerManagementCluster
metadata:
  name: test2-cluster
spec:
  storage:
    mode: ExternalPostgres
    externalPostgres:
      dsnSecretKeyRef:
        namespace: %[1]s
        name: test2-dsn
        key: dsn
      requireTLS: false
`, flowOperandNamespace)
}

// Workloads reuse an already-loaded suite image for its shell, never a floating utility image.
func logicalFlowPod(name, node string) corev1.Pod {
	no := false
	grace := int64(1)
	return corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: flowWorkNamespace, Labels: map[string]string{"app": name}},
		Spec: corev1.PodSpec{
			NodeSelector:                 map[string]string{"kubernetes.io/hostname": node},
			Tolerations:                  []corev1.Toleration{{Key: flowDrainTaint, Operator: corev1.TolerationOpEqual, Value: "true", Effect: corev1.TaintEffectNoSchedule}},
			AutomountServiceAccountToken: &no, TerminationGracePeriodSeconds: &grace,
			Containers: []corev1.Container{{Name: "workload", Image: nutServerImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-c", "exec sleep 3600"}}},
		},
	}
}

func logicalFlowWorkloads(target, other string) string {
	pod := logicalFlowPod("test2-scale", target)
	replicas := int32(1)
	deployment := appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: pod.ObjectMeta,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: pod.Labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: pod.Labels}, Spec: pod.Spec}},
	}
	return logicalFlowJSONList(deployment, logicalFlowPod("test2-drain", target), logicalFlowPod("test2-untouched", other))
}

func logicalFlowPostgres(survivor string) string {
	pod := corev1.Pod{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "test2-postgres", Namespace: flowOperandNamespace, Labels: map[string]string{"app": "test2-postgres"}},
		Spec: corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/hostname": survivor}, Containers: []corev1.Container{{Name: "postgres", Image: flowPostgresImage,
			Env:            []corev1.EnvVar{{Name: "POSTGRES_USER", Value: "test2"}, {Name: "POSTGRES_PASSWORD", Value: "test2-fixture"}, {Name: "POSTGRES_DB", Value: "test2"}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"pg_isready", "-U", "test2"}}}},
		}}},
	}
	service := corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: pod.ObjectMeta,
		Spec: corev1.ServiceSpec{Selector: pod.Labels, Ports: []corev1.ServicePort{{Port: 5432}}},
	}
	return logicalFlowJSONList(pod, service)
}

func logicalFlowJSONList(objects ...any) string {
	data, err := json.Marshal(struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Items      []any  `json:"items"`
	}{"v1", "List", objects})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// Keep the production render for the negative actuator control, but project a separate,
// test-owned expired signal. This never writes to the positive operator-owned Secret.
func logicalFlowStalePod(ds appsv1.DaemonSet, node string) (corev1.Pod, error) {
	pod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: "test2-stale-actuator", Namespace: flowOperandNamespace}, Spec: *ds.Spec.Template.Spec.DeepCopy()}
	pod.Spec.NodeName = node
	pod.Spec.InitContainers = nil
	pod.Spec.Containers = nil
	for _, container := range ds.Spec.Template.Spec.Containers {
		if container.Name == "actuator" {
			pod.Spec.Containers = append(pod.Spec.Containers, container)
		}
	}
	if len(pod.Spec.Containers) != 1 {
		return pod, fmt.Errorf("render has no unique actuator")
	}
	needed := map[string]bool{}
	for _, mount := range pod.Spec.Containers[0].VolumeMounts {
		needed[mount.Name] = true
	}
	pod.Spec.Volumes = nil
	replaced := false
	for _, volume := range ds.Spec.Template.Spec.Volumes {
		if !needed[volume.Name] {
			continue
		}
		volume = *volume.DeepCopy()
		if volume.Secret != nil && volume.Secret.SecretName == flowAgent+"-node-signals" {
			volume.Secret.SecretName = "test2-stale-signal"
			replaced = true
		}
		pod.Spec.Volumes = append(pod.Spec.Volumes, volume)
	}
	if !replaced {
		return pod, fmt.Errorf("render has no operator signal Secret volume")
	}
	return pod, nil
}
