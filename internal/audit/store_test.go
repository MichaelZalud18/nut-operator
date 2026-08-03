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

	if len(exec.calls) != 2 {
		t.Fatalf("expected two migration calls, got %d", len(exec.calls))
	}
	if !strings.Contains(exec.calls[0].query, `CREATE SCHEMA IF NOT EXISTS "power""audit";`) {
		t.Fatalf("migration did not use quoted custom schema:\n%s", exec.calls[0].query)
	}
	if !strings.Contains(exec.calls[1].query, `"power""audit".shutdownflow_executions`) {
		t.Fatalf("executor migration did not use quoted custom schema:\n%s", exec.calls[1].query)
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
	startedAt := observedAt.Add(30 * time.Second)
	completedAt := observedAt.Add(90 * time.Second)
	staleAfter := observedAt.Add(5 * time.Minute)
	waveIndex := int32(0)

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
		{
			name:  "shutdown flow execution",
			table: `"power".shutdownflow_executions`,
			write: func() error {
				return store.RecordShutdownFlowExecution(context.Background(), ShutdownFlowExecution{
					ExecutionID:       "00000000-0000-4000-8000-000000000006",
					ObservedAt:        observedAt,
					ShutdownFlow:      "conserve-power",
					TriggerDecisionID: "00000000-0000-4000-8000-000000000005",
					Mode:              "DryRun",
					Phase:             "Running",
					Reason:            "TriggerEligible",
					PlanConfigHash:    "hash-a",
					InputHash:         "input-a",
					StartedAt:         &startedAt,
					DryRun:            true,
					Approved:          false,
					ApprovalEvidence:  map[string]any{"flowApproved": false},
					Revalidation:      map[string]any{"inputHash": "input-a"},
					Details:           map[string]any{"waveCount": float64(1)},
				})
			},
		},
		{
			name:  "shutdown flow execution wave",
			table: `"power".shutdownflow_execution_waves`,
			write: func() error {
				return store.RecordShutdownFlowExecutionWave(context.Background(), ShutdownFlowExecutionWave{
					WaveRecordID: "00000000-0000-4000-8000-000000000007",
					ExecutionID:  "00000000-0000-4000-8000-000000000006",
					ObservedAt:   observedAt,
					WaveIndex:    0,
					Phase:        "Completed",
					StartedAt:    &startedAt,
					CompletedAt:  &completedAt,
					GroupNames:   []string{"applications"},
					Details:      map[string]any{"durationSeconds": float64(60)},
				})
			},
		},
		{
			name:  "shutdown flow execution group",
			table: `"power".shutdownflow_execution_groups`,
			write: func() error {
				return store.RecordShutdownFlowExecutionGroup(context.Background(), ShutdownFlowExecutionGroup{
					GroupRecordID: "00000000-0000-4000-8000-000000000008",
					ExecutionID:   "00000000-0000-4000-8000-000000000006",
					ObservedAt:    observedAt,
					WaveIndex:     0,
					GroupName:     "applications",
					Action:        "ScaleWorkload",
					Phase:         "Completed",
					StartedAt:     &startedAt,
					CompletedAt:   &completedAt,
					SelectedTargets: []map[string]string{{
						"kind":      "Deployment",
						"namespace": "apps",
						"name":      "web",
					}},
					Details: map[string]any{"replicas": float64(0)},
				})
			},
		},
		{
			name:  "shutdown flow action attempt",
			table: `"power".shutdownflow_action_attempts`,
			write: func() error {
				return store.RecordShutdownFlowActionAttempt(context.Background(), ShutdownFlowActionAttempt{
					AttemptID:       "00000000-0000-4000-8000-000000000009",
					ExecutionID:     "00000000-0000-4000-8000-000000000006",
					ObservedAt:      observedAt,
					WaveIndex:       &waveIndex,
					GroupName:       "applications",
					Action:          "ScaleWorkload",
					TargetKind:      "Deployment",
					TargetNamespace: "apps",
					TargetName:      "web",
					StartedAt:       &startedAt,
					CompletedAt:     &completedAt,
					Outcome:         "Simulated",
					DryRun:          true,
					Details:         map[string]any{"replicas": float64(0)},
				})
			},
		},
		{
			name:  "node release",
			table: `"power".node_release_records`,
			write: func() error {
				return store.RecordNodeRelease(context.Background(), NodeReleaseRecord{
					ReleaseID:      "00000000-0000-4000-8000-00000000000a",
					ExecutionID:    "00000000-0000-4000-8000-000000000006",
					ObservedAt:     observedAt,
					NodeName:       "node-a",
					NodePowerAgent: "agent-a",
					PlanConfigHash: "hash-a",
					Approved:       false,
					Released:       true,
					Reason:         "DryRunRelease",
					Clearance:      map[string]any{"workloadsCleared": true},
					Details:        map[string]any{"waveIndex": float64(0)},
				})
			},
		},
		{
			name:  "node signal handoff",
			table: `"power".node_signal_handoffs`,
			write: func() error {
				return store.RecordNodeSignalHandoff(context.Background(), NodeSignalHandoff{
					HandoffID:      "00000000-0000-4000-8000-00000000000b",
					ExecutionID:    "00000000-0000-4000-8000-000000000006",
					ObservedAt:     observedAt,
					NodeName:       "node-a",
					NodePowerAgent: "agent-a",
					SignalPath:     "/run/nut-operator/shutdown.json",
					SignalPayload:  map[string]any{"flow": "conserve-power", "planHash": "hash-a"},
					StaleAfter:     &staleAfter,
					Accepted:       true,
					Reason:         "SignalAccepted",
					Details:        map[string]any{"dryRun": true},
				})
			},
		},
		{
			name:  "executor resume state",
			table: `"power".executor_resume_states`,
			write: func() error {
				return store.UpsertExecutorResumeState(context.Background(), ExecutorResumeState{
					ExecutionID:      "00000000-0000-4000-8000-000000000006",
					ObservedAt:       observedAt,
					ShutdownFlow:     "conserve-power",
					PlanConfigHash:   "hash-a",
					CurrentWaveIndex: &waveIndex,
					Phase:            "Running",
					State:            map[string]any{"completedGroups": []string{"applications"}},
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

func TestSQLStoreUpsertsExecutorProgressRecords(t *testing.T) {
	exec := &fakeExecutor{}
	store, err := NewSQLStore(exec, SQLStoreOptions{})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}

	writes := []struct {
		name  string
		want  string
		write func() error
	}{
		{
			name: "execution",
			want: "ON CONFLICT (execution_id) DO UPDATE SET",
			write: func() error {
				return store.RecordShutdownFlowExecution(context.Background(), ShutdownFlowExecution{
					ExecutionID:    "00000000-0000-4000-8000-000000000006",
					ShutdownFlow:   "conserve-power",
					Mode:           "DryRun",
					Phase:          "Running",
					PlanConfigHash: "hash-a",
				})
			},
		},
		{
			name: "wave",
			want: "ON CONFLICT (execution_id, wave_index) DO UPDATE SET",
			write: func() error {
				return store.RecordShutdownFlowExecutionWave(context.Background(), ShutdownFlowExecutionWave{
					WaveRecordID: "00000000-0000-4000-8000-000000000007",
					ExecutionID:  "00000000-0000-4000-8000-000000000006",
					WaveIndex:    0,
					Phase:        "Running",
				})
			},
		},
		{
			name: "group",
			want: "ON CONFLICT (execution_id, wave_index, group_name) DO UPDATE SET",
			write: func() error {
				return store.RecordShutdownFlowExecutionGroup(context.Background(), ShutdownFlowExecutionGroup{
					GroupRecordID: "00000000-0000-4000-8000-000000000008",
					ExecutionID:   "00000000-0000-4000-8000-000000000006",
					GroupName:     "applications",
					Action:        "ScaleWorkload",
					Phase:         "Running",
				})
			},
		},
	}

	for _, write := range writes {
		if err := write.write(); err != nil {
			t.Fatalf("%s write returned error: %v", write.name, err)
		}
		query := exec.calls[len(exec.calls)-1].query
		if !strings.Contains(query, write.want) {
			t.Fatalf("%s query missing %q:\n%s", write.name, write.want, query)
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

func TestSQLStoreRecordsRejectedCompilationWithoutConfigHash(t *testing.T) {
	exec := &fakeExecutor{}
	store, err := NewSQLStore(exec, SQLStoreOptions{})
	if err != nil {
		t.Fatalf("NewSQLStore returned error: %v", err)
	}

	err = store.RecordShutdownFlowCompilation(context.Background(), ShutdownFlowCompilation{
		CompilationID:      "00000000-0000-4000-8000-000000000004",
		ObservedAt:         time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		ShutdownFlow:       "conserve-power",
		ResourceGeneration: 42,
		Accepted:           false,
		Diagnostics: []DiagnosticRecord{{
			Severity: "Error",
			Reason:   "PlannerRejected",
			Message:  "cycle detected",
		}},
		CompiledWaves: []map[string]any{},
	})
	if err != nil {
		t.Fatalf("RecordShutdownFlowCompilation returned error: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("expected one insert call, got %d", len(exec.calls))
	}
	call := exec.calls[0]
	if call.args[4] != nil {
		t.Fatalf("expected rejected compilation config_hash to be NULL, got %#v", call.args[4])
	}
	if call.args[6] != false {
		t.Fatalf("expected accepted argument to be false, got %#v", call.args[6])
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
		Accepted:      true,
		CompiledWaves: []map[string]any{},
	}); err == nil {
		t.Fatal("expected accepted shutdown flow compilation without config hash to be rejected")
	}
	if err := store.RecordShutdownFlowCompilation(context.Background(), ShutdownFlowCompilation{
		CompilationID: "00000000-0000-4000-8000-000000000004",
		ShutdownFlow:  "conserve-power",
		ConfigHash:    "hash-a",
		CompiledWaves: map[string]any{"not": "an array"},
	}); err == nil {
		t.Fatal("expected non-array compiled waves to be rejected")
	}
	if err := store.RecordShutdownFlowExecution(context.Background(), ShutdownFlowExecution{}); err == nil {
		t.Fatal("expected incomplete shutdown flow execution to be rejected")
	}
	if err := store.RecordShutdownFlowExecutionGroup(context.Background(), ShutdownFlowExecutionGroup{
		GroupRecordID:   "00000000-0000-4000-8000-000000000008",
		ExecutionID:     "00000000-0000-4000-8000-000000000006",
		GroupName:       "applications",
		Action:          "ScaleWorkload",
		Phase:           "Running",
		SelectedTargets: map[string]any{"not": "an array"},
	}); err == nil {
		t.Fatal("expected non-array selected targets to be rejected")
	}
	if err := store.UpsertExecutorResumeState(context.Background(), ExecutorResumeState{}); err == nil {
		t.Fatal("expected incomplete executor resume state to be rejected")
	}
}
