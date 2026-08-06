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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ReplayStats summarize one drain of the spool journal.
type ReplayStats struct {
	// Read is how many journal lines were parsed.
	Read int
	// Replayed is how many records the primary writer accepted.
	Replayed int
	// Skipped is how many lines were unparseable or of an unknown kind. They
	// are retained in the journal rather than dropped.
	Skipped int
}

// ReplayOptions configure a spool drain.
type ReplayOptions struct {
	// Directory holds the spool journal. Defaults to the writer's directory.
	Directory string
	// FileName is the journal file. Defaults to the writer's file name.
	FileName string
}

// ReplaySpool drains a spool journal back into a primary audit writer.
//
// This is the other half of OD-6. A spool that captures records while
// PostgreSQL is unavailable but never returns them does not preserve the audit
// trail; it only loses it more slowly.
//
// Replay is safe to run repeatedly. Every record the spool writes carries the
// same identity the primary writer uses, and every primary insert is an upsert
// on that identity, so re-applying a record is a no-op rather than a duplicate.
// That is what lets the journal be removed only after a fully successful drain:
// a partial drain leaves the file in place and the next attempt re-applies what
// already landed.
//
// A record the replayer cannot parse or does not recognize is counted as
// skipped and left in the journal. Discarding evidence because this build does
// not understand it would defeat the point of spooling it.
func ReplaySpool(ctx context.Context, primary Writer, options ReplayOptions) (ReplayStats, error) {
	var stats ReplayStats
	if primary == nil {
		return stats, errors.New("audit spool replay requires a primary writer")
	}

	if options.Directory == "" {
		return stats, errors.New("audit spool replay requires a spool directory")
	}
	fileName := options.FileName
	if fileName == "" {
		fileName = defaultSpoolFileName
	}
	path := filepath.Join(options.Directory, fileName)

	file, err := os.Open(path) // #nosec G304 -- operator-configured spool path
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, fmt.Errorf("open audit spool journal: %w", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	// Compiled plans and dependency graphs are spooled as JSONB payloads and
	// comfortably exceed bufio's default 64KiB line cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var replayErr error
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		stats.Read++

		var record spoolReplayRecord
		if err := json.Unmarshal(line, &record); err != nil {
			stats.Skipped++
			continue
		}
		applied, err := replayRecord(ctx, primary, record)
		switch {
		case err != nil:
			replayErr = errors.Join(replayErr, fmt.Errorf("replay %s %q: %w", record.Kind, record.Key, err))
		case applied:
			stats.Replayed++
		default:
			stats.Skipped++
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, fmt.Errorf("read audit spool journal: %w", err)
	}
	if replayErr != nil {
		return stats, replayErr
	}

	// Only remove the journal once every record landed. Anything less and the
	// file stays, because a spool that deletes evidence it failed to deliver is
	// worse than one that retries.
	if stats.Skipped == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return stats, fmt.Errorf("remove drained audit spool journal: %w", err)
		}
	}
	return stats, nil
}

// spoolReplayRecord mirrors spoolRecord with a deferred payload, so one unknown
// or malformed payload cannot fail the whole drain.
type spoolReplayRecord struct {
	Kind    string          `json:"kind"`
	Key     string          `json:"key,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// replayRecord re-applies one spooled record. It returns false when the record
// kind is not recognized, which the caller counts as skipped.
func replayRecord(ctx context.Context, primary Writer, record spoolReplayRecord) (bool, error) {
	switch record.Kind {
	case "power_event":
		return decodeAndApply(record, primary.RecordPowerEvent, ctx)
	case "ups_telemetry_snapshot":
		return decodeAndApply(record, primary.RecordTelemetrySnapshot, ctx)
	case "capability_profile_match":
		return decodeAndApply(record, primary.RecordCapabilityProfileMatch, ctx)
	case "capability_profile_verification":
		return decodeAndApply(record, primary.RecordCapabilityProfileVerification, ctx)
	case "shutdownflow_compilation":
		return decodeAndApply(record, primary.RecordShutdownFlowCompilation, ctx)
	case "shutdownflow_decision":
		return decodeAndApply(record, primary.RecordShutdownFlowDecision, ctx)
	case "shutdownflow_execution":
		return decodeAndApply(record, primary.RecordShutdownFlowExecution, ctx)
	case "shutdownflow_execution_wave":
		return decodeAndApply(record, primary.RecordShutdownFlowExecutionWave, ctx)
	case "shutdownflow_execution_group":
		return decodeAndApply(record, primary.RecordShutdownFlowExecutionGroup, ctx)
	case "shutdownflow_action_attempt":
		return decodeAndApply(record, primary.RecordShutdownFlowActionAttempt, ctx)
	case "node_release_record":
		return decodeAndApply(record, primary.RecordNodeRelease, ctx)
	case "node_signal_handoff":
		return decodeAndApply(record, primary.RecordNodeSignalHandoff, ctx)
	case "executor_resume_state":
		return decodeAndApply(record, primary.UpsertExecutorResumeState, ctx)
	default:
		return false, nil
	}
}

func decodeAndApply[T any](record spoolReplayRecord, apply func(context.Context, T) error, ctx context.Context) (bool, error) {
	var payload T
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return false, nil
	}
	if err := apply(ctx, payload); err != nil {
		return false, err
	}
	return true, nil
}
