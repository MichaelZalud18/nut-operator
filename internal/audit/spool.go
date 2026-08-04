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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const defaultSpoolFileName = "audit-spool.jsonl"

// SpoolOptions configure a local JSONL fallback writer.
type SpoolOptions struct {
	Directory   string
	FileName    string
	Clock       func() time.Time
	DisableSync bool
}

// SpoolStats summarize fallback writes made by one writer instance.
type SpoolStats struct {
	FallbackWrites   int
	LastPrimaryError string
	LastSpoolPath    string
}

// SpoolWriter writes to a primary audit writer first and appends a replayable
// JSONL record locally when the primary writer fails.
type SpoolWriter struct {
	primary     Writer
	directory   string
	fileName    string
	clock       func() time.Time
	disableSync bool

	mu               sync.Mutex
	fallbackWrites   int
	lastPrimaryError string
	lastSpoolPath    string
}

// NewSpoolWriter wraps a primary audit writer with a local fallback journal.
func NewSpoolWriter(primary Writer, options SpoolOptions) (*SpoolWriter, error) {
	if primary == nil {
		return nil, fmt.Errorf("primary audit writer is required")
	}
	directory := strings.TrimSpace(options.Directory)
	if directory == "" {
		return nil, fmt.Errorf("audit spool directory is required")
	}
	if containsControlCharacter(directory) {
		return nil, fmt.Errorf("audit spool directory must not contain control characters")
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("audit spool directory must be absolute")
	}
	fileName := options.FileName
	if fileName == "" {
		fileName = defaultSpoolFileName
	}
	if filepath.Base(fileName) != fileName || containsControlCharacter(fileName) {
		return nil, fmt.Errorf("audit spool file name must be a plain file name")
	}
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &SpoolWriter{
		primary:     primary,
		directory:   filepath.Clean(directory),
		fileName:    fileName,
		clock:       clock,
		disableSync: options.DisableSync,
	}, nil
}

func (w *SpoolWriter) Stats() SpoolStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return SpoolStats{
		FallbackWrites:   w.fallbackWrites,
		LastPrimaryError: w.lastPrimaryError,
		LastSpoolPath:    w.lastSpoolPath,
	}
}

func (w *SpoolWriter) RecordPowerEvent(ctx context.Context, event PowerEvent) error {
	return w.record(ctx, "power_event", event.EventID, event, func() error {
		return w.primary.RecordPowerEvent(ctx, event)
	})
}

func (w *SpoolWriter) RecordTelemetrySnapshot(ctx context.Context, snapshot TelemetrySnapshot) error {
	return w.record(ctx, "ups_telemetry_snapshot", snapshot.SnapshotID, snapshot, func() error {
		return w.primary.RecordTelemetrySnapshot(ctx, snapshot)
	})
}

func (w *SpoolWriter) RecordCapabilityProfileMatch(ctx context.Context, match CapabilityProfileMatch) error {
	return w.record(ctx, "capability_profile_match", match.MatchID, match, func() error {
		return w.primary.RecordCapabilityProfileMatch(ctx, match)
	})
}

func (w *SpoolWriter) RecordCapabilityProfileVerification(ctx context.Context, verification CapabilityProfileVerification) error {
	return w.record(ctx, "capability_profile_verification", verification.VerificationID, verification, func() error {
		return w.primary.RecordCapabilityProfileVerification(ctx, verification)
	})
}

func (w *SpoolWriter) RecordShutdownFlowCompilation(ctx context.Context, compilation ShutdownFlowCompilation) error {
	return w.record(ctx, "shutdownflow_compilation", compilation.CompilationID, compilation, func() error {
		return w.primary.RecordShutdownFlowCompilation(ctx, compilation)
	})
}

func (w *SpoolWriter) RecordShutdownFlowDecision(ctx context.Context, decision ShutdownFlowDecision) error {
	return w.record(ctx, "shutdownflow_decision", decision.DecisionID, decision, func() error {
		return w.primary.RecordShutdownFlowDecision(ctx, decision)
	})
}

func (w *SpoolWriter) RecordShutdownFlowExecution(ctx context.Context, execution ShutdownFlowExecution) error {
	return w.record(ctx, "shutdownflow_execution", execution.ExecutionID, execution, func() error {
		return w.primary.RecordShutdownFlowExecution(ctx, execution)
	})
}

func (w *SpoolWriter) RecordShutdownFlowExecutionWave(ctx context.Context, wave ShutdownFlowExecutionWave) error {
	key := fmt.Sprintf("%s/%d", wave.ExecutionID, wave.WaveIndex)
	return w.record(ctx, "shutdownflow_execution_wave", key, wave, func() error {
		return w.primary.RecordShutdownFlowExecutionWave(ctx, wave)
	})
}

func (w *SpoolWriter) RecordShutdownFlowExecutionGroup(ctx context.Context, group ShutdownFlowExecutionGroup) error {
	key := fmt.Sprintf("%s/%d/%s", group.ExecutionID, group.WaveIndex, group.GroupName)
	return w.record(ctx, "shutdownflow_execution_group", key, group, func() error {
		return w.primary.RecordShutdownFlowExecutionGroup(ctx, group)
	})
}

func (w *SpoolWriter) RecordShutdownFlowActionAttempt(ctx context.Context, attempt ShutdownFlowActionAttempt) error {
	return w.record(ctx, "shutdownflow_action_attempt", attempt.AttemptID, attempt, func() error {
		return w.primary.RecordShutdownFlowActionAttempt(ctx, attempt)
	})
}

func (w *SpoolWriter) RecordNodeRelease(ctx context.Context, release NodeReleaseRecord) error {
	return w.record(ctx, "node_release_record", release.ReleaseID, release, func() error {
		return w.primary.RecordNodeRelease(ctx, release)
	})
}

func (w *SpoolWriter) RecordNodeSignalHandoff(ctx context.Context, handoff NodeSignalHandoff) error {
	return w.record(ctx, "node_signal_handoff", handoff.HandoffID, handoff, func() error {
		return w.primary.RecordNodeSignalHandoff(ctx, handoff)
	})
}

func (w *SpoolWriter) UpsertExecutorResumeState(ctx context.Context, state ExecutorResumeState) error {
	return w.record(ctx, "executor_resume_state", state.ExecutionID, state, func() error {
		return w.primary.UpsertExecutorResumeState(ctx, state)
	})
}

func (w *SpoolWriter) record(ctx context.Context, kind, key string, payload any, primaryWrite func() error) error {
	primaryErr := primaryWrite()
	if primaryErr == nil {
		return nil
	}
	if err := w.append(ctx, kind, key, primaryErr, payload); err != nil {
		return errors.Join(primaryErr, err)
	}
	return nil
}

type spoolRecord struct {
	Kind         string    `json:"kind"`
	Key          string    `json:"key,omitempty"`
	SpooledAt    time.Time `json:"spooledAt"`
	PrimaryError string    `json:"primaryError"`
	Payload      any       `json:"payload"`
}

func (w *SpoolWriter) append(ctx context.Context, kind, key string, primaryErr error, payload any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := os.MkdirAll(w.directory, 0o700); err != nil {
		return fmt.Errorf("create audit spool directory: %w", err)
	}
	path := filepath.Join(w.directory, w.fileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit spool file: %w", err)
	}
	record := spoolRecord{
		Kind:         kind,
		Key:          key,
		SpooledAt:    w.clock().UTC(),
		PrimaryError: primaryErr.Error(),
		Payload:      payload,
	}
	encodeErr := json.NewEncoder(file).Encode(record)
	var syncErr error
	if encodeErr == nil && !w.disableSync {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	if encodeErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("append audit spool record: %w", errors.Join(encodeErr, syncErr, closeErr))
	}

	w.fallbackWrites++
	w.lastPrimaryError = primaryErr.Error()
	w.lastSpoolPath = path
	return nil
}

func containsControlCharacter(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	})
}
