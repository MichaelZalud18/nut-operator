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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingAuditWriter struct {
	err error
}

func (w failingAuditWriter) RecordPowerEvent(context.Context, PowerEvent) error {
	return w.err
}
func (w failingAuditWriter) RecordTelemetrySnapshot(context.Context, TelemetrySnapshot) error {
	return w.err
}
func (w failingAuditWriter) RecordCapabilityProfileMatch(context.Context, CapabilityProfileMatch) error {
	return w.err
}
func (w failingAuditWriter) RecordCapabilityProfileVerification(context.Context, CapabilityProfileVerification) error {
	return w.err
}
func (w failingAuditWriter) RecordShutdownFlowCompilation(context.Context, ShutdownFlowCompilation) error {
	return w.err
}
func (w failingAuditWriter) RecordShutdownFlowDecision(context.Context, ShutdownFlowDecision) error {
	return w.err
}
func (w failingAuditWriter) RecordShutdownFlowExecution(context.Context, ShutdownFlowExecution) error {
	return w.err
}
func (w failingAuditWriter) RecordShutdownFlowExecutionWave(context.Context, ShutdownFlowExecutionWave) error {
	return w.err
}
func (w failingAuditWriter) RecordShutdownFlowExecutionGroup(context.Context, ShutdownFlowExecutionGroup) error {
	return w.err
}
func (w failingAuditWriter) RecordShutdownFlowActionAttempt(context.Context, ShutdownFlowActionAttempt) error {
	return w.err
}
func (w failingAuditWriter) RecordNodeRelease(context.Context, NodeReleaseRecord) error {
	return w.err
}
func (w failingAuditWriter) RecordNodeSignalHandoff(context.Context, NodeSignalHandoff) error {
	return w.err
}
func (w failingAuditWriter) UpsertExecutorResumeState(context.Context, ExecutorResumeState) error {
	return w.err
}

func TestSpoolWriterRecordsFallbackJSONL(t *testing.T) {
	fixed := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	writer, err := NewSpoolWriter(failingAuditWriter{err: errors.New("postgres unavailable")}, SpoolOptions{
		Directory:   t.TempDir(),
		Clock:       func() time.Time { return fixed },
		DisableSync: true,
	})
	if err != nil {
		t.Fatalf("NewSpoolWriter returned error: %v", err)
	}

	err = writer.RecordShutdownFlowExecution(context.Background(), ShutdownFlowExecution{
		ExecutionID:    "execution-a",
		ObservedAt:     fixed,
		ShutdownFlow:   "conserve-power",
		Mode:           "DryRun",
		Phase:          "Running",
		PlanConfigHash: "plan-hash-a",
		InputHash:      "input-hash-a",
	})
	if err != nil {
		t.Fatalf("RecordShutdownFlowExecution returned error: %v", err)
	}

	stats := writer.Stats()
	if stats.FallbackWrites != 1 {
		t.Fatalf("expected one fallback write, got %#v", stats)
	}
	if !strings.HasSuffix(stats.LastSpoolPath, defaultSpoolFileName) {
		t.Fatalf("expected default spool file path, got %q", stats.LastSpoolPath)
	}

	content, err := os.ReadFile(stats.LastSpoolPath)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var record struct {
		Kind         string          `json:"kind"`
		Key          string          `json:"key"`
		SpooledAt    time.Time       `json:"spooledAt"`
		PrimaryError string          `json:"primaryError"`
		Payload      json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatalf("Unmarshal spool record returned error: %v", err)
	}
	if record.Kind != "shutdownflow_execution" || record.Key != "execution-a" || record.PrimaryError != "postgres unavailable" {
		t.Fatalf("unexpected spool metadata: %#v", record)
	}
	if !record.SpooledAt.Equal(fixed) {
		t.Fatalf("expected fixed spool time, got %s", record.SpooledAt)
	}
	var payload map[string]any
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal payload returned error: %v", err)
	}
	if payload["ExecutionID"] != "execution-a" || payload["PlanConfigHash"] != "plan-hash-a" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	info, err := os.Stat(stats.LastSpoolPath)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected spool file mode 0600, got %s", info.Mode().Perm())
	}
}

func TestSpoolWriterDoesNotCreateFileWhenPrimarySucceeds(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewSpoolWriter(NoopStore{}, SpoolOptions{Directory: dir, DisableSync: true})
	if err != nil {
		t.Fatalf("NewSpoolWriter returned error: %v", err)
	}

	if err := writer.RecordPowerEvent(context.Background(), PowerEvent{
		EventID:    "event-a",
		EventType:  "StorageReady",
		Severity:   "Info",
		SourceKind: "PowerManagementCluster",
		SourceName: "production",
		Message:    "storage ready",
	}); err != nil {
		t.Fatalf("RecordPowerEvent returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, defaultSpoolFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected no spool file, got err=%v", err)
	}
}

func TestSpoolWriterRejectsRelativeDirectory(t *testing.T) {
	_, err := NewSpoolWriter(NoopStore{}, SpoolOptions{Directory: "relative"})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("expected absolute directory rejection, got %v", err)
	}
}
