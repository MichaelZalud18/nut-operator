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

package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GroupDurationSample is what one previous execution of one group actually took.
type GroupDurationSample struct {
	// GroupName and WaveIndex identify the work. Both are needed: a group can be
	// compiled into a different wave when the graph changes around it.
	GroupName string
	WaveIndex int32

	// Observed is the wall-clock duration between the group starting and completing.
	Observed time.Duration

	// Phase is the phase the group ended in. Kept so a caller can decide whether a
	// timed-out or aborted run belongs in an estimate; this package does not decide
	// that for them.
	Phase string

	// Rehearsal is true when the execution was deliberately requested to build
	// history before an outage. The caller decides whether those samples belong in
	// the current estimate.
	Rehearsal bool

	// CompletedAt orders the samples, newest first, so a caller can weight recent
	// runs or cut off old ones without a second query.
	CompletedAt time.Time
}

// HistoryReader reads back what previous executions actually took (EX-32).
//
// Separate from Writer because it is a different capability with a different
// failure posture. A write failure degrades auditability and the shutdown
// continues (EX-20); a read failure degrades an estimate and the shutdown
// continues too — but the read happens at planning time, off the outage path
// entirely, which is why it may do real work per call.
type HistoryReader interface {
	// GroupDurations returns samples for one flow, restricted to executions whose
	// plan config hash matches. Newest first, capped at limit.
	GroupDurations(ctx context.Context, flow, planConfigHash string, limit int) ([]GroupDurationSample, error)
}

// SQLQueryExecutor is the read half of the SQL surface.
//
// Separate from SQLExecutor rather than added to it, because every existing
// implementation — including the test fakes — satisfies write-only. A store whose
// executor cannot query degrades to "no history", which is the same state a fresh
// cluster is in and which callers already handle.
type SQLQueryExecutor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// GroupDurations implements HistoryReader against PostgreSQL.
//
// Scoped to a plan config hash on purpose. A group is identified by name, and a
// name outlives the work it refers to: retarget a group at a different selector
// and "databases" now means something else entirely, while every historical row
// still carries that name. Comparing across a plan change would quietly average
// two different pieces of work and present the result as evidence. The hash is
// already recorded on every execution, so the check costs nothing.
//
// Only groups that both started and completed contribute. A row missing either
// timestamp is a run that was interrupted — the executor restarted, the node went
// down — and the elapsed time it implies is not how long the work takes.
//
// Dry-run executions are excluded. They skip effects, so their durations measure
// the executor's own overhead rather than the work, and folding them in would drag
// every estimate toward zero exactly where the estimate is meant to be
// conservative.
func (s *SQLStore) GroupDurations(ctx context.Context, flow, planConfigHash string, limit int) ([]GroupDurationSample, error) {
	if flow == "" || planConfigHash == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultHistorySampleLimit
	}
	querier, ok := s.executor.(SQLQueryExecutor)
	if !ok {
		return nil, nil
	}

	rows, err := querier.QueryContext(ctx, fmt.Sprintf(`SELECT g.group_name, g.wave_index, g.phase,
       g.completed_at,
       COALESCE((e.details->>'rehearsal')::boolean, false) AS rehearsal,
       EXTRACT(EPOCH FROM (g.completed_at - g.started_at)) AS elapsed_seconds
  FROM %[1]s.shutdownflow_execution_groups g
  JOIN %[1]s.shutdownflow_executions e ON e.execution_id = g.execution_id
 WHERE e.shutdownflow = $1
   AND e.plan_config_hash = $2
   AND e.dry_run = false
   AND g.started_at IS NOT NULL
   AND g.completed_at IS NOT NULL
   AND g.completed_at >= g.started_at
 ORDER BY g.completed_at DESC
 LIMIT $3`, s.quotedSchema), flow, planConfigHash, limit)
	if err != nil {
		return nil, fmt.Errorf("read group durations for shutdown flow %q: %w", flow, err)
	}
	defer func() { _ = rows.Close() }()

	var samples []GroupDurationSample
	for rows.Next() {
		var (
			sample  GroupDurationSample
			seconds float64
		)
		if err := rows.Scan(&sample.GroupName, &sample.WaveIndex, &sample.Phase, &sample.CompletedAt, &sample.Rehearsal, &seconds); err != nil {
			return nil, fmt.Errorf("scan group duration for shutdown flow %q: %w", flow, err)
		}
		sample.Observed = time.Duration(seconds * float64(time.Second))
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read group durations for shutdown flow %q: %w", flow, err)
	}
	return samples, nil
}

// defaultHistorySampleLimit caps an unbounded request.
//
// An upper bound on rows, not a statement about how many samples make a good estimate.
// The caller decides that; this only stops one query from reading a year of
// executions into memory.
const defaultHistorySampleLimit = 500

// GroupDurations on NoopStore returns nothing, which callers must already handle:
// storage is optional (Disabled mode), so "no history" is a normal state and never
// an error.
func (NoopStore) GroupDurations(context.Context, string, string, int) ([]GroupDurationSample, error) {
	return nil, nil
}
