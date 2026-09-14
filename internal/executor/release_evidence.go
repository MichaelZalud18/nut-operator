package executor

import (
	"context"
	"fmt"
)

func (e Executor) refreshReleaseEvidence(ctx context.Context, group Group) (Group, error) {
	if e.RefreshNodeRelease == nil {
		return group, nil
	}
	// Do not mutate the input slice shared with other waves or audit consumers.
	releases := make([]NodeRelease, len(group.NodeReleases))
	for i, selected := range group.NodeReleases {
		if err := ctx.Err(); err != nil {
			return group, err
		}
		fresh, err := e.RefreshNodeRelease(ctx, selected)
		if err != nil {
			return group, fmt.Errorf("refresh node %q evidence: %w", selected.NodeName, err)
		}
		if fresh.NodeName != selected.NodeName || fresh.NodePowerAgent != selected.NodePowerAgent ||
			fresh.AgentUID != selected.AgentUID || fresh.AgentGeneration != selected.AgentGeneration ||
			fresh.ActuatorPolicy != selected.ActuatorPolicy || fresh.SignalPath != selected.SignalPath ||
			fresh.SignalSecretNamespace != selected.SignalSecretNamespace || fresh.SignalSecretName != selected.SignalSecretName ||
			fresh.SignalSecretKey != selected.SignalSecretKey {
			return group, fmt.Errorf("node %q release identity changed during evidence refresh", selected.NodeName)
		}
		releases[i] = fresh
	}
	group.NodeReleases = releases
	return group, nil
}
