package controller

import (
	"context"
	"fmt"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func nodeReleaseTelemetryRecent(device *power.UPSDevice, now time.Time) bool {
	polled := device.Status.LastPollTime
	if polled == nil || polled.IsZero() || polled.After(now) {
		return false
	}
	age := now.Sub(polled.Time)
	if threshold := device.Spec.Thresholds.StaleAfter; threshold != nil {
		return threshold.Duration > 0 && age < threshold.Duration
	}
	degraded := device.Status.Phase != power.UPSDevicePhaseOnline
	// Division avoids overflow for unusually large configured poll intervals.
	return age/telemetryPollInterval(device, degraded) < 3
}

func (r *ShutdownFlowReconciler) refreshNodeReleaseEvidence(ctx context.Context, selected executor.NodeRelease) (executor.NodeRelease, error) {
	releases, err := r.nodeReleasesForTarget(ctx, power.ShutdownStepTarget{
		AgentRefs: []power.ObjectNameReference{{Name: selected.NodePowerAgent}},
	})
	if err != nil {
		return executor.NodeRelease{}, err
	}
	for _, release := range releases {
		if release.NodeName == selected.NodeName {
			return release, nil
		}
	}
	return executor.NodeRelease{}, fmt.Errorf("node %q is no longer selected by agent %q", selected.NodeName, selected.NodePowerAgent)
}

// ValidateNodeRelease checks live safety evidence, then authorization, at the write boundary.
func (r *ShutdownFlowReconciler) ValidateNodeRelease(ctx context.Context, release executor.NodeRelease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var agent power.NodePowerAgent
	if err := r.reader().Get(ctx, client.ObjectKey{Name: release.NodePowerAgent}, &agent); err != nil {
		return err
	}
	if agent.Status.ObservedGeneration != agent.Generation {
		return fmt.Errorf("agent status has not observed the current specification")
	}
	cluster, err := r.getNodePowerAgentManagementCluster(ctx, &agent)
	if err != nil {
		return err
	}
	namespace := nodePowerAgentNamespace(&agent, cluster)
	if release.SignalSecretNamespace != namespace || release.SignalSecretName != nodePowerAgentSignalSecretName(&agent) || release.SignalSecretKey != nodePowerAgentSignalKey(release.NodeName) {
		return fmt.Errorf("node signal destination no longer matches the agent")
	}
	status, found := nodePowerAgentStatusByNode(agent.Status.NodeStatuses)[release.NodeName]
	if !found || !status.Ready || status.PodName == "" {
		return fmt.Errorf("agent has no ready pod for node %q", release.NodeName)
	}
	var pod corev1.Pod
	if err := r.reader().Get(ctx, client.ObjectKey{Namespace: namespace, Name: status.PodName}, &pod); err != nil {
		return err
	}
	if pod.Spec.NodeName != release.NodeName || pod.Status.Phase != corev1.PodRunning || !podIsReady(pod) {
		return fmt.Errorf("agent pod is not currently ready on node %q", release.NodeName)
	}
	if !actuatorPodMatchesAgent(pod, &agent) {
		return fmt.Errorf("agent pod actuator configuration does not match current policy")
	}
	fresh, reason, err := r.nodePowerAgentTelemetryFreshness(ctx, &agent)
	if err != nil {
		return err
	}
	if !fresh {
		return fmt.Errorf("agent telemetry is not fresh: %s", reason)
	}
	protected, err := r.clearanceExemptNamespaces(ctx)
	if err != nil {
		return err
	}
	cleared, reason, workloads, err := r.nodeClearance(ctx, release.NodeName, protected)
	if err != nil {
		return err
	}
	if !cleared {
		return fmt.Errorf("node clearance refused: %s (%v)", reason, workloads)
	}
	if err := r.validateControlPlaneRelease(ctx, release); err != nil {
		return err
	}
	return r.ValidateNodeReleaseAuthorization(ctx, release)
}

func actuatorPodMatchesAgent(pod corev1.Pod, agent *power.NodePowerAgent) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name != "actuator" {
			continue
		}
		values := map[string]string{}
		for _, env := range container.Env {
			if env.Name != "POWER_AGENT_MODE" && env.Name != "POWER_ACTUATOR_POLICY" {
				continue
			}
			if _, duplicate := values[env.Name]; duplicate || env.ValueFrom != nil {
				return false
			}
			values[env.Name] = env.Value
		}
		return values["POWER_AGENT_MODE"] == string(nodePowerAgentMode(agent)) && values["POWER_ACTUATOR_POLICY"] == string(nodePowerAgentActuatorPolicy(agent))
	}
	return false
}
