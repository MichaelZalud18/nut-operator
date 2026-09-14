package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
)

const defaultTargetResolutionTimeout = 30 * time.Second

func (e Executor) resolveWaveTargets(ctx context.Context, groups map[string]Group, wave Wave, state waveAdaptiveState) (map[string]Group, string, error) {
	resolved := make(map[string]Group, len(wave.Groups))
	for _, name := range wave.Groups {
		group := groups[name]
		if err := ctx.Err(); err != nil {
			return nil, name, err
		}
		if e.ResolveTargets != nil {
			timeout := defaultTargetResolutionTimeout
			if declared := adaptive.ScaleDuration(group.Timeout, state.Budget); declared > 0 && declared < timeout {
				timeout = declared
			}
			resolveCtx, cancel := context.WithTimeout(ctx, timeout)
			targets, err := e.ResolveTargets(resolveCtx, group)
			if err == nil {
				err = resolveCtx.Err()
			}
			cancel()
			if err != nil {
				return nil, name, fmt.Errorf("resolve wave %d group %q targets: %w", wave.Index, name, err)
			}
			group.SelectedTargets = append([]Target(nil), targets...)
		}
		resolved[name] = group
	}
	return resolved, "", nil
}
