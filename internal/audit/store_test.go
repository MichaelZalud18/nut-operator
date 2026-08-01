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
	"errors"
	"strings"
	"testing"
	"time"
)

type execCall struct {
	query string
	args  []any
}

type fakeExecutor struct {
	calls []execCall
	err   error
}

func (f *fakeExecutor) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.calls = append(f.calls, execCall{query: query, args: append([]any(nil), args...)})
	if f.err != nil {
		return nil, f.err
	}
	return fakeResult(0), nil
}

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return int64(r), nil }
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

func TestNewSQLStoreRequiresExecutor(t *testing.T) {
	if _, err := NewSQLStore(nil, SQLStoreOptions{}); err == nil {
		t.Fatal("expected nil SQL executor to be rejected")
	}
}

func TestSQLStoreEnsuresSchema(t *testing.T) {
	exec := &fakeExecutor{}
	store, err := NewSQLStore(exec, SQLStoreOptions{Schema: `power"audit`})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}

	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema returned error: %v", err)
	}

	if len(exec.calls) != 1 {
		t.Fatalf("expected one migration call, got %d", len(exec.calls))
	}
	if !strings.Contains(exec.calls[0].query, `CREATE SCHEMA IF NOT EXISTS "power""audit";`) {
		t.Fatalf("migration did not use quoted custom schema:\n%s", exec.calls[0].query)
	}
}

func TestSQLStoreWrapsMigrationErrors(t *testing.T) {
	exec := &fakeExecutor{err: errors.New("database unavailable")}
	store, err := NewSQLStore(exec, SQLStoreOptions{})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}

	err = store.EnsureSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "apply audit migration 1") {
		t.Fatalf("expected wrapped migration error, got %v", err)
	}
}

func TestSQLStoreRecordsAllAuditPayloadTypes(t *testing.T) {
	exec := &fakeExecutor{}
	store, err := NewSQLStore(exec, SQLStoreOptions{})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}
	observedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	generation := int64(42)
	charge := 94.5
	runtimeSeconds := int64(1200)
	load := 38.25

	writes := []struct {
		name  string
		table string
		write func() error
	}{
		{
			name:  "power event",
			table: `"power".power_events`,
			write: func() error {
				return store.RecordPowerEvent(context.Background(), PowerEvent{
					EventID:            "00000000-0000-4000-8000-000000000001",
					ObservedAt:         observedAt,
					EventType:          "PlanCompiled",
					Severity:           "Info",
					SourceKind:         "ShutdownFlow",
					SourceName:         "conserve-power",
					ResourceGeneration: &generation,
					CorrelationID:      "flow-1",
					Message:            "Compiled shutdown flow",
					Details:            map[string]any{"accepted": true},
				})
			},
		},
		{
			name:  "telemetry snapshot",
			table: `"power".ups_telemetry_snapshots`,
			write: func() error {
				return store.RecordTelemetrySnapshot(context.Background(), TelemetrySnapshot{
					SnapshotID:           "00000000-0000-4000-8000-000000000002",
					ObservedAt:           observedAt,
					UPSDevice:            "rack-a-ups",
					NUTServer:            "rack-a",
					NUTName:              "ups",
					UPSStatus:            "OL",
					BatteryChargePercent: &charge,
					RuntimeSeconds:       &runtimeSeconds,
					LoadPercent:          &load,
					Variables:            map[string]string{"ups.status": "OL"},
				})
			},
		},
		{
			name:  "capability match",
			table: `"power".capability_profile_matches`,
			write: func() error {
				return store.RecordCapabilityProfileMatch(context.Background(), CapabilityProfileMatch{
					MatchID:        "00000000-0000-4000-8000-000000000003",
					ObservedAt:     observedAt,
					UPSDevice:      "rack-a-ups",
					ProfileID:      "ubiquiti-unifi-ups-tower",
					ProfileVersion: "0.1.0",
					ProfileSource:  "Bundled",
					MatchTier:      "ModelGlob",
					Diagnostics: []DiagnosticRecord{{
						Severity: "Warning",
						Reason:   "ProbeStale",
						Message:  "Probe data is stale",
					}},
				})
			},
		},
		{
			name:  "shutdown flow compilation",
			table: `"power".shutdownflow_compilations`,
			write: func() error {
				return store.RecordShutdownFlowCompilation(context.Background(), ShutdownFlowCompilation{
					CompilationID:      "00000000-0000-4000-8000-000000000004",
					ObservedAt:         observedAt,
					ShutdownFlow:       "conserve-power",
					ResourceGeneration: 42,
					ConfigHash:         "hash-a",
					InputHash:          "input-a",
					Accepted:           true,
					CompiledWaves:      []map[string]any{{"index": 0}},
				})
			},
		},
		{
			name:  "shutdown flow decision",
			table: `"power".shutdownflow_decisions`,
			write: func() error {
				return store.RecordShutdownFlowDecision(context.Background(), ShutdownFlowDecision{
					DecisionID:         "00000000-0000-4000-8000-000000000005",
					ObservedAt:         observedAt,
					ShutdownFlow:       "conserve-power",
					TriggerType:        "RuntimeBelow",
					Mode:               "DryRun",
					Decision:           "WouldShutdown",
					Reason:             "runtime threshold crossed",
					SelectedUPSDevices: []string{"rack-a-ups"},
					PlanConfigHash:     "hash-a",
					Details:            map[string]any{"waveCount": float64(1)},
				})
			},
		},
	}

	for _, write := range writes {
		if err := write.write(); err != nil {
			t.Fatalf("%s write returned error: %v", write.name, err)
		}
		call := exec.calls[len(exec.calls)-1]
		if !strings.Contains(call.query, write.table) {
			t.Fatalf("%s query did not target %s:\n%s", write.name, write.table, call.query)
		}
		if len(call.args) == 0 {
			t.Fatalf("%s write sent no SQL arguments", write.name)
		}
	}
}

func TestSQLStoreRecordsJSONObjects(t *testing.T) {
	exec := &fakeExecutor{}
	store, err := NewSQLStore(exec, SQLStoreOptions{})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}

	err = store.RecordPowerEvent(context.Background(), PowerEvent{
		EventID:    "00000000-0000-4000-8000-000000000001",
		EventType:  "PlanCompiled",
		Severity:   "Info",
		SourceKind: "ShutdownFlow",
		SourceName: "conserve-power",
		Message:    "Compiled shutdown flow",
		Details:    map[string]any{"accepted": true},
	})
	if err != nil {
		t.Fatalf("RecordPowerEvent returned error: %v", err)
	}

	rawJSON, ok := exec.calls[0].args[9].(string)
	if !ok {
		t.Fatalf("expected details argument to be JSON string, got %#v", exec.calls[0].args[9])
	}
	var details map[string]bool
	if err := json.Unmarshal([]byte(rawJSON), &details); err != nil {
		t.Fatalf("details were not valid JSON: %v", err)
	}
	if !details["accepted"] {
		t.Fatalf("expected accepted detail to round trip, got %#v", details)
	}
}

func TestSQLStoreRejectsIncompleteRecords(t *testing.T) {
	store, err := NewSQLStore(&fakeExecutor{}, SQLStoreOptions{})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}

	if err := store.RecordPowerEvent(context.Background(), PowerEvent{}); err == nil {
		t.Fatal("expected incomplete power event to be rejected")
	}
	if err := store.RecordShutdownFlowCompilation(context.Background(), ShutdownFlowCompilation{
		CompilationID: "00000000-0000-4000-8000-000000000004",
		ShutdownFlow:  "conserve-power",
		ConfigHash:    "hash-a",
		CompiledWaves: map[string]any{"not": "an array"},
	}); err == nil {
		t.Fatal("expected non-array compiled waves to be rejected")
	}
}
