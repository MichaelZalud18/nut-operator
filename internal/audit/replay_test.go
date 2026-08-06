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
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// recordingWriter captures what replay hands back to the primary, and can be
// told to fail so a still-unavailable database can be simulated.
type recordingWriter struct {
	err        error
	events     []PowerEvent
	executions []ShutdownFlowExecution
	releases   []NodeReleaseRecord
}

func (w *recordingWriter) RecordPowerEvent(_ context.Context, event PowerEvent) error {
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, event)
	return nil
}
func (w *recordingWriter) RecordTelemetrySnapshot(context.Context, TelemetrySnapshot) error {
	return w.err
}
func (w *recordingWriter) RecordCapabilityProfileMatch(context.Context, CapabilityProfileMatch) error {
	return w.err
}
func (w *recordingWriter) RecordCapabilityProfileVerification(context.Context, CapabilityProfileVerification) error {
	return w.err
}
func (w *recordingWriter) RecordShutdownFlowCompilation(context.Context, ShutdownFlowCompilation) error {
	return w.err
}
func (w *recordingWriter) RecordShutdownFlowDecision(context.Context, ShutdownFlowDecision) error {
	return w.err
}
func (w *recordingWriter) RecordShutdownFlowExecution(_ context.Context, execution ShutdownFlowExecution) error {
	if w.err != nil {
		return w.err
	}
	w.executions = append(w.executions, execution)
	return nil
}
func (w *recordingWriter) RecordShutdownFlowExecutionWave(context.Context, ShutdownFlowExecutionWave) error {
	return w.err
}
func (w *recordingWriter) RecordShutdownFlowExecutionGroup(context.Context, ShutdownFlowExecutionGroup) error {
	return w.err
}
func (w *recordingWriter) RecordShutdownFlowActionAttempt(context.Context, ShutdownFlowActionAttempt) error {
	return w.err
}
func (w *recordingWriter) RecordNodeRelease(_ context.Context, release NodeReleaseRecord) error {
	if w.err != nil {
		return w.err
	}
	w.releases = append(w.releases, release)
	return nil
}
func (w *recordingWriter) RecordNodeSignalHandoff(context.Context, NodeSignalHandoff) error {
	return w.err
}
func (w *recordingWriter) UpsertExecutorResumeState(context.Context, ExecutorResumeState) error {
	return w.err
}

// spoolAndDrain writes records through a spool whose primary is failing, then
// replays the journal into a healthy primary -- the exact sequence a PostgreSQL
// outage during execution produces.
func spoolAndDrain(t *testing.T, write func(Writer) error) (*recordingWriter, ReplayStats, string) {
	t.Helper()
	directory := t.TempDir()

	failing := &recordingWriter{err: errors.New("postgres unavailable")}
	spool, err := NewSpoolWriter(failing, SpoolOptions{Directory: directory, DisableSync: true})
	if err != nil {
		t.Fatalf("NewSpoolWriter: %v", err)
	}
	if err := write(spool); err != nil {
		t.Fatalf("spooled write returned an error: %v", err)
	}

	healthy := &recordingWriter{}
	stats, err := ReplaySpool(context.Background(), healthy, ReplayOptions{Directory: directory})
	if err != nil {
		t.Fatalf("ReplaySpool: %v", err)
	}
	return healthy, stats, filepath.Join(directory, defaultSpoolFileName)
}

func TestReplaySpoolReturnsRecordsToPrimary(t *testing.T) {
	healthy, stats, path := spoolAndDrain(t, func(w Writer) error {
		return w.RecordShutdownFlowExecution(context.Background(), ShutdownFlowExecution{
			ExecutionID:  "exec-1",
			ShutdownFlow: "cluster-power-loss",
		})
	})

	if stats.Replayed != 1 || stats.Read != 1 || stats.Skipped != 0 {
		t.Fatalf("stats = %#v, want one record read and replayed", stats)
	}
	if len(healthy.executions) != 1 || healthy.executions[0].ExecutionID != "exec-1" {
		t.Fatalf("primary did not receive the spooled execution: %#v", healthy.executions)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a fully drained journal must be removed")
	}
}

func TestReplaySpoolIsIdempotent(t *testing.T) {
	directory := t.TempDir()
	failing := &recordingWriter{err: errors.New("postgres unavailable")}
	spool, err := NewSpoolWriter(failing, SpoolOptions{Directory: directory, DisableSync: true})
	if err != nil {
		t.Fatalf("NewSpoolWriter: %v", err)
	}
	release := NodeReleaseRecord{ReleaseID: "rel-1", ExecutionID: "exec-1", NodeName: "node-a"}
	if err := spool.RecordNodeRelease(context.Background(), release); err != nil {
		t.Fatalf("spooled write: %v", err)
	}

	healthy := &recordingWriter{}
	first, err := ReplaySpool(context.Background(), healthy, ReplayOptions{Directory: directory})
	if err != nil {
		t.Fatalf("first ReplaySpool: %v", err)
	}
	second, err := ReplaySpool(context.Background(), healthy, ReplayOptions{Directory: directory})
	if err != nil {
		t.Fatalf("second ReplaySpool: %v", err)
	}

	if first.Replayed != 1 {
		t.Fatalf("first drain = %#v, want one replay", first)
	}
	// The journal is gone after a clean drain, so a second run is a no-op
	// rather than a duplicate write.
	if second.Read != 0 || second.Replayed != 0 {
		t.Fatalf("second drain = %#v, want a no-op", second)
	}
	if len(healthy.releases) != 1 {
		t.Fatalf("replay must not duplicate records, got %d", len(healthy.releases))
	}
}

func TestReplaySpoolKeepsJournalWhenARecordIsNotUnderstood(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, defaultSpoolFileName)
	journal := `{"kind":"power_event","key":"evt-1","payload":{"eventID":"evt-1"}}` + "\n" +
		`{"kind":"invented_future_record","key":"x","payload":{}}` + "\n"
	if err := os.WriteFile(path, []byte(journal), 0o600); err != nil {
		t.Fatalf("seed journal: %v", err)
	}

	healthy := &recordingWriter{}
	stats, err := ReplaySpool(context.Background(), healthy, ReplayOptions{Directory: directory})
	if err != nil {
		t.Fatalf("ReplaySpool: %v", err)
	}

	if stats.Replayed != 1 || stats.Skipped != 1 {
		t.Fatalf("stats = %#v, want one replayed and one skipped", stats)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a journal holding unreplayed evidence must not be removed")
	}
}

func TestReplaySpoolLeavesJournalWhenPrimaryStillFails(t *testing.T) {
	directory := t.TempDir()
	failing := &recordingWriter{err: errors.New("postgres unavailable")}
	spool, err := NewSpoolWriter(failing, SpoolOptions{Directory: directory, DisableSync: true})
	if err != nil {
		t.Fatalf("NewSpoolWriter: %v", err)
	}
	if err := spool.RecordPowerEvent(context.Background(), PowerEvent{EventID: "evt-1"}); err != nil {
		t.Fatalf("spooled write: %v", err)
	}

	stillFailing := &recordingWriter{err: errors.New("postgres still unavailable")}
	if _, err := ReplaySpool(context.Background(), stillFailing, ReplayOptions{Directory: directory}); err == nil {
		t.Fatal("a failing primary must surface as a replay error")
	}
	if _, err := os.Stat(filepath.Join(directory, defaultSpoolFileName)); err != nil {
		t.Fatal("the journal must survive a failed drain")
	}
}

func TestReplaySpoolWithNoJournalIsNotAnError(t *testing.T) {
	stats, err := ReplaySpool(context.Background(), &recordingWriter{}, ReplayOptions{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("a missing journal is the normal case, got %v", err)
	}
	if stats.Read != 0 {
		t.Fatalf("stats = %#v, want an empty drain", stats)
	}
}

// TestReplayCoversEveryWriterMethod drives one spooled write through every
// method on the Writer interface and asserts replay understands all of them.
//
// This exists because the two sides nearly drifted: the spool wrote
// node_release_record while replay matched on node_release, so that record kind
// would have been silently skipped forever. A hand-maintained list of kinds
// would have the same failure mode, so the coverage is derived from real writes
// instead.
func TestReplayCoversEveryWriterMethod(t *testing.T) {
	ctx := context.Background()
	writes := map[string]func(Writer) error{
		"PowerEvent":             func(w Writer) error { return w.RecordPowerEvent(ctx, PowerEvent{EventID: "a"}) },
		"TelemetrySnapshot":      func(w Writer) error { return w.RecordTelemetrySnapshot(ctx, TelemetrySnapshot{SnapshotID: "a"}) },
		"CapabilityProfileMatch": func(w Writer) error { return w.RecordCapabilityProfileMatch(ctx, CapabilityProfileMatch{MatchID: "a"}) },
		"CapabilityProfileVerification": func(w Writer) error {
			return w.RecordCapabilityProfileVerification(ctx, CapabilityProfileVerification{VerificationID: "a"})
		},
		"ShutdownFlowCompilation": func(w Writer) error {
			return w.RecordShutdownFlowCompilation(ctx, ShutdownFlowCompilation{CompilationID: "a"})
		},
		"ShutdownFlowDecision": func(w Writer) error { return w.RecordShutdownFlowDecision(ctx, ShutdownFlowDecision{DecisionID: "a"}) },
		"ShutdownFlowExecution": func(w Writer) error {
			return w.RecordShutdownFlowExecution(ctx, ShutdownFlowExecution{ExecutionID: "a"})
		},
		"ShutdownFlowExecutionWave": func(w Writer) error {
			return w.RecordShutdownFlowExecutionWave(ctx, ShutdownFlowExecutionWave{ExecutionID: "a"})
		},
		"ShutdownFlowExecutionGroup": func(w Writer) error {
			return w.RecordShutdownFlowExecutionGroup(ctx, ShutdownFlowExecutionGroup{ExecutionID: "a"})
		},
		"ShutdownFlowActionAttempt": func(w Writer) error {
			return w.RecordShutdownFlowActionAttempt(ctx, ShutdownFlowActionAttempt{AttemptID: "a"})
		},
		"NodeRelease":         func(w Writer) error { return w.RecordNodeRelease(ctx, NodeReleaseRecord{ReleaseID: "a"}) },
		"NodeSignalHandoff":   func(w Writer) error { return w.RecordNodeSignalHandoff(ctx, NodeSignalHandoff{HandoffID: "a"}) },
		"ExecutorResumeState": func(w Writer) error { return w.UpsertExecutorResumeState(ctx, ExecutorResumeState{ExecutionID: "a"}) },
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			failing := &recordingWriter{err: errors.New("postgres unavailable")}
			spool, err := NewSpoolWriter(failing, SpoolOptions{Directory: directory, DisableSync: true})
			if err != nil {
				t.Fatalf("NewSpoolWriter: %v", err)
			}
			if err := write(spool); err != nil {
				t.Fatalf("spooled write: %v", err)
			}

			stats, err := ReplaySpool(ctx, &recordingWriter{}, ReplayOptions{Directory: directory})
			if err != nil {
				t.Fatalf("ReplaySpool: %v", err)
			}
			if stats.Read != 1 {
				t.Fatalf("stats = %#v, want exactly one spooled record", stats)
			}
			if stats.Replayed != 1 {
				t.Fatalf("replay does not understand the record kind this write produces: %#v", stats)
			}
		})
	}
}
