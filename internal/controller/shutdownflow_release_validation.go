package controller

import (
	"context"
	"fmt"
	"slices"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ValidateNodeReleaseAuthorization rechecks the specific agent, not just the flow's approval.
func (r *ShutdownFlowReconciler) ValidateNodeReleaseAuthorization(ctx context.Context, release executor.NodeRelease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var agent power.NodePowerAgent
	if err := r.reader().Get(ctx, client.ObjectKey{Name: release.NodePowerAgent}, &agent); err != nil {
		return err
	}
	if release.AgentUID == "" || string(agent.UID) != release.AgentUID || agent.Generation != release.AgentGeneration {
		return fmt.Errorf("NodePowerAgent identity or specification changed since release selection")
	}
	if !agent.DeletionTimestamp.IsZero() {
		return fmt.Errorf("NodePowerAgent is being deleted")
	}
	policy := nodePowerAgentActuatorPolicy(&agent)
	switch policy {
	case power.ActuatorPolicySimulate, power.ActuatorPolicyPowerOff, power.ActuatorPolicyTalosShutdown:
	default:
		return fmt.Errorf("NodePowerAgent actuator policy does not permit signal consumption")
	}
	if string(policy) != release.ActuatorPolicy {
		return fmt.Errorf("NodePowerAgent actuator policy changed")
	}
	if mode := nodePowerAgentMode(&agent); mode != power.NodePowerAgentModeDryRun && mode != power.NodePowerAgentModeActuate {
		return fmt.Errorf("NodePowerAgent does not permit signal consumption")
	}
	if err := validateNodePowerAgentRenderSafety(&agent); err != nil {
		return err
	}
	if !slices.Contains(agent.Status.SelectedNodes, release.NodeName) {
		return fmt.Errorf("node is no longer selected by NodePowerAgent")
	}
	return nil
}
