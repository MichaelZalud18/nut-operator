package controller

import (
	"context"
	"fmt"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ShutdownFlowReconciler) waveTargetResolver(flow *power.ShutdownFlow, bundle resolver.StructuralBundle) func(context.Context, executor.Group) ([]executor.Target, error) {
	compiled := flowForCompiledExecution(flow)
	targets := map[string]power.ShutdownStepTarget{}
	for _, group := range compiled.Spec.Groups {
		targets[group.Name] = group.Target
	}
	for _, step := range compiled.Spec.Steps {
		targets[step.ID] = step.Target
	}
	allowedNodes := map[string]map[string]bool{}
	for _, membership := range shutdownflow.PlannerGroupNodes(compiled, bundle) {
		nodes := map[string]bool{}
		for _, name := range membership.Acts {
			nodes[name] = true
		}
		for _, name := range membership.Releases {
			nodes[name] = true
		}
		allowedNodes[membership.Group] = nodes
	}
	return func(ctx context.Context, group executor.Group) ([]executor.Target, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target, found := targets[group.Name]
		if !found {
			return nil, fmt.Errorf("group %q is outside the compiled execution", group.Name)
		}
		// Node selectors also describe scope/communication membership on workload
		// actions, even though those actions return workload objects rather than Nodes.
		nodes, err := r.nodeTargets(ctx, target)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if !allowedNodes[group.Name][node.Name] {
				return nil, fmt.Errorf("group %q newly selects node %q outside compiled membership; recompile required", group.Name, node.Name)
			}
		}
		selected := nodes
		if group.Action != string(power.ShutdownStepCordonNodes) && group.Action != string(power.ShutdownStepDrainNodes) {
			selected, err = r.executorTargetsForAction(ctx, power.ShutdownStepType(group.Action), target)
			if err != nil {
				return nil, err
			}
		}
		if group.Action == executor.ActionAgentShutdown {
			for _, ref := range target.AgentRefs {
				var agent power.NodePowerAgent
				if err := r.reader().Get(ctx, client.ObjectKey{Name: ref.Name}, &agent); err != nil {
					return nil, err
				}
				for _, node := range agent.Status.SelectedNodes {
					if !allowedNodes[group.Name][node] {
						return nil, fmt.Errorf("agent %q newly selects node %q outside compiled membership; recompile required", agent.Name, node)
					}
				}
			}
		}
		return selected, nil
	}
}
