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
	"encoding/json"
	"fmt"
)

// ResumeReader reads the durable execution restart state for one deterministic
// execution ID (EX-14).
//
// Separate from Writer for the same reason as HistoryReader: failing to read
// audit state must not stop a power response, and a Disabled/no-query store is a
// normal deployment shape during evaluation.
type ResumeReader interface {
	ExecutorResumeState(ctx context.Context, executionID string) (*ExecutorResumeState, error)
	ExecutionGroupProgress(ctx context.Context, executionID string) ([]ExecutionGroupProgress, error)
}

// ExecutorResumeState returns the compact state row for one execution.
func (s *SQLStore) ExecutorResumeState(ctx context.Context, executionID string) (*ExecutorResumeState, error) {
	if executionID == "" {
		return nil, nil
	}
	querier, ok := s.executor.(SQLQueryExecutor)
	if !ok {
		return nil, nil
	}

	rows, err := querier.QueryContext(ctx, fmt.Sprintf(`SELECT execution_id, observed_at, shutdownflow,
       plan_config_hash, current_wave_index, phase, state
  FROM %[1]s.executor_resume_states
 WHERE execution_id = $1`, s.quotedSchema), executionID)
	if err != nil {
		return nil, fmt.Errorf("read executor resume state %q: %w", executionID, err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read executor resume state %q: %w", executionID, err)
		}
		return nil, nil
	}

	state, err := scanExecutorResumeState(rows)
	if err != nil {
		return nil, fmt.Errorf("scan executor resume state %q: %w", executionID, err)
	}
	if rows.Next() {
		return nil, fmt.Errorf("executor resume state %q returned more than one row", executionID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read executor resume state %q: %w", executionID, err)
	}
	return &state, nil
}

func scanExecutorResumeState(rows interface {
	Scan(dest ...any) error
}) (ExecutorResumeState, error) {
	var (
		state       ExecutorResumeState
		waveIndex   sql.NullInt32
		encodedJSON []byte
	)
	if err := rows.Scan(
		&state.ExecutionID,
		&state.ObservedAt,
		&state.ShutdownFlow,
		&state.PlanConfigHash,
		&waveIndex,
		&state.Phase,
		&encodedJSON,
	); err != nil {
		return ExecutorResumeState{}, err
	}
	if waveIndex.Valid {
		index := waveIndex.Int32
		state.CurrentWaveIndex = &index
	}
	if len(encodedJSON) > 0 {
		if err := json.Unmarshal(encodedJSON, &state.State); err != nil {
			return ExecutorResumeState{}, fmt.Errorf("decode state payload: %w", err)
		}
	}
	if state.State == nil {
		state.State = map[string]any{}
	}
	return state, nil
}

// ExecutionGroupProgress returns group records already written for one
// execution, ordered in the same direction the executor walks the plan.
func (s *SQLStore) ExecutionGroupProgress(ctx context.Context, executionID string) ([]ExecutionGroupProgress, error) {
	if executionID == "" {
		return nil, nil
	}
	querier, ok := s.executor.(SQLQueryExecutor)
	if !ok {
		return nil, nil
	}

	rows, err := querier.QueryContext(ctx, fmt.Sprintf(`SELECT wave_index, group_name, action, phase, completed_at
  FROM %[1]s.shutdownflow_execution_groups
 WHERE execution_id = $1
   AND completed_at IS NOT NULL
 ORDER BY wave_index ASC, group_name ASC`, s.quotedSchema), executionID)
	if err != nil {
		return nil, fmt.Errorf("read execution group progress %q: %w", executionID, err)
	}
	defer func() { _ = rows.Close() }()

	var groups []ExecutionGroupProgress
	for rows.Next() {
		var group ExecutionGroupProgress
		if err := rows.Scan(&group.WaveIndex, &group.GroupName, &group.Action, &group.Phase, &group.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan execution group progress %q: %w", executionID, err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read execution group progress %q: %w", executionID, err)
	}
	return groups, nil
}

func (NoopStore) ExecutorResumeState(context.Context, string) (*ExecutorResumeState, error) {
	return nil, nil
}

func (NoopStore) ExecutionGroupProgress(context.Context, string) ([]ExecutionGroupProgress, error) {
	return nil, nil
}
