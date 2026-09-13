//go:build postgres

package audit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The tagged suite requires an explicit disposable database; the default suite
// neither opens database connections nor silently skips this acceptance test.
func openTestPostgres(t *testing.T) (context.Context, *sql.DB, *SQLStore) {
	t.Helper()
	dsn := os.Getenv("AUDIT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("AUDIT_TEST_POSTGRES_DSN is required; use bash hack/test-postgres.sh")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	for {
		if err = db.PingContext(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("database readiness: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Quoted identifiers must survive both migrations and production queries.
	schema := fmt.Sprintf("audit_test_%d_\"quoted", time.Now().UnixNano())
	store, err := NewSQLStore(db, SQLStoreOptions{Schema: schema, Retention: RetentionPolicy{Events: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+store.quotedSchema+" CASCADE"); err != nil {
			t.Errorf("clean owned schema: %v", err)
		}
	})
	for range 2 {
		if err := store.EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, db, store
}

func TestPostgresMigrationsHistoryAndRetention(t *testing.T) {
	ctx, db, store := openTestPostgres(t)
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-2 * time.Hour)
	ended := started.Add(30 * time.Second)
	for i, scenario := range []struct {
		hash string
		dry  bool
	}{{"selected", false}, {"other", false}, {"selected", true}} {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		err := store.RecordShutdownFlowExecution(ctx, ShutdownFlowExecution{
			ExecutionID: id, ObservedAt: started, ShutdownFlow: "flow", Mode: "Enforce",
			Phase: "Completed", PlanConfigHash: scenario.hash, DryRun: scenario.dry,
			StartedAt: &started, CompletedAt: &ended,
		})
		if err != nil {
			t.Fatal(err)
		}
		err = store.RecordShutdownFlowExecutionGroup(ctx, ShutdownFlowExecutionGroup{
			GroupRecordID: id, ExecutionID: id, ObservedAt: started,
			GroupName: "workers", Action: "DrainNodes", Phase: "Completed",
			StartedAt: &started, CompletedAt: &ended,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	samples, err := store.GroupDurations(ctx, "flow", "selected", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].Observed != 30*time.Second {
		t.Fatalf("expected one matching effectful duration: %+v", samples)
	}
	if err := store.RecordPowerEvent(ctx, PowerEvent{
		EventID: "00000000-0000-4000-8000-000000000004", ObservedAt: now,
		EventType: "Test", Severity: "Info", SourceKind: "ShutdownFlow", SourceName: "flow",
		Message: "Recent event survives retention",
		Details: map[string]any{"preserved": true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnforceRetention(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"shutdownflow_executions", "shutdownflow_execution_groups"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+store.quotedSchema+"."+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("retention left %d rows in %s", count, table)
		}
	}
	var preserved bool
	if err := db.QueryRowContext(ctx, "SELECT (details->>'preserved')::boolean FROM "+store.quotedSchema+".power_events").Scan(&preserved); err != nil || !preserved {
		t.Fatalf("retention must preserve recent JSON payload: preserved=%v err=%v", preserved, err)
	}
	blocked, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := db.ExecContext(blocked, "SELECT pg_sleep(30)"); err == nil {
		t.Fatal("expected canceled database query")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("database query did not honor caller deadline")
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("connection pool unusable after cancellation: %v", err)
	}
}

func TestPostgresAllRecordsReplay(t *testing.T) {
	ctx, db, store := openTestPostgres(t)
	now := time.Now().UTC().Truncate(time.Second)
	id := "00000000-0000-4000-8000-000000000010"
	dir := t.TempDir()
	// A closed real connection exercises the same write-error boundary as an
	// unavailable backend, without stopping a database another test may share.
	closed, err := sql.Open("pgx", os.Getenv("AUDIT_TEST_POSTGRES_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	unavailable, err := NewSQLStore(closed, SQLStoreOptions{Schema: store.schema})
	if err != nil {
		t.Fatal(err)
	}
	spool, err := NewSpoolWriter(unavailable, SpoolOptions{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	writes := []struct {
		table string
		write func(Writer) error
	}{
		{"power_events", func(w Writer) error {
			return w.RecordPowerEvent(ctx, PowerEvent{EventID: id, ObservedAt: now, EventType: "Test", Severity: "Info", SourceKind: "ShutdownFlow", SourceName: "flow", Message: "test"})
		}},
		{"ups_telemetry_snapshots", func(w Writer) error {
			return w.RecordTelemetrySnapshot(ctx, TelemetrySnapshot{SnapshotID: id, ObservedAt: now, UPSDevice: "ups", NUTServer: "server", NUTName: "ups", Variables: map[string]string{"ups.status": "OL"}})
		}},
		{"capability_profile_matches", func(w Writer) error {
			return w.RecordCapabilityProfileMatch(ctx, CapabilityProfileMatch{MatchID: id, ObservedAt: now, UPSDevice: "ups", ProfileID: "profile", ProfileVersion: "1", ProfileSource: "Bundled", MatchTier: "ModelGlob"})
		}},
		{"capability_profile_verifications", func(w Writer) error {
			return w.RecordCapabilityProfileVerification(ctx, CapabilityProfileVerification{VerificationID: id, ObservedAt: now, UPSDevice: "ups", ProfileID: "profile", ProfileVersion: "1", ProfileSource: "Bundled"})
		}},
		{"shutdownflow_compilations", func(w Writer) error {
			return w.RecordShutdownFlowCompilation(ctx, ShutdownFlowCompilation{CompilationID: id, ObservedAt: now, ShutdownFlow: "flow", ConfigHash: "hash", Accepted: true})
		}},
		{"shutdownflow_decisions", func(w Writer) error {
			return w.RecordShutdownFlowDecision(ctx, ShutdownFlowDecision{DecisionID: id, ObservedAt: now, ShutdownFlow: "flow", TriggerType: "RuntimeBelow", Mode: "Enforce", Decision: "Execute", Reason: "test"})
		}},
		{"shutdownflow_executions", func(w Writer) error {
			return w.RecordShutdownFlowExecution(ctx, ShutdownFlowExecution{ExecutionID: id, ObservedAt: now, ShutdownFlow: "flow", PlanConfigHash: "hash", Mode: "Enforce", Phase: "Completed"})
		}},
		{"shutdownflow_execution_waves", func(w Writer) error {
			return w.RecordShutdownFlowExecutionWave(ctx, ShutdownFlowExecutionWave{WaveRecordID: id, ExecutionID: id, ObservedAt: now, Phase: "Completed", CompletedAt: &now})
		}},
		{"shutdownflow_execution_groups", func(w Writer) error {
			return w.RecordShutdownFlowExecutionGroup(ctx, ShutdownFlowExecutionGroup{GroupRecordID: id, ExecutionID: id, ObservedAt: now, GroupName: "workers", Action: "DrainNodes", Phase: "Completed", CompletedAt: &now})
		}},
		{"shutdownflow_action_attempts", func(w Writer) error {
			return w.RecordShutdownFlowActionAttempt(ctx, ShutdownFlowActionAttempt{AttemptID: id, ExecutionID: id, ObservedAt: now, Action: "DrainNodes", Outcome: "Completed"})
		}},
		{"node_release_records", func(w Writer) error {
			return w.RecordNodeRelease(ctx, NodeReleaseRecord{ReleaseID: id, ExecutionID: id, ObservedAt: now, NodeName: "worker", Reason: "test"})
		}},
		{"node_signal_handoffs", func(w Writer) error {
			return w.RecordNodeSignalHandoff(ctx, NodeSignalHandoff{HandoffID: id, ExecutionID: id, ObservedAt: now, NodeName: "worker", Reason: "test"})
		}},
		{"executor_resume_states", func(w Writer) error {
			return w.UpsertExecutorResumeState(ctx, ExecutorResumeState{ExecutionID: id, ObservedAt: now, ShutdownFlow: "flow", PlanConfigHash: "hash", Phase: "Completed", State: map[string]any{"test": true}})
		}},
	}
	if len(writes) != reflect.TypeFor[Writer]().NumMethod() {
		t.Fatal("extend the PostgreSQL fixtures for the changed Writer interface")
	}
	for _, record := range writes {
		if err := record.write(spool); err != nil {
			t.Fatalf("spool %s: %v", record.table, err)
		}
	}
	if _, err := ReplaySpool(ctx, unavailable, ReplayOptions{Directory: dir}); err == nil {
		t.Fatal("unavailable database must retain journal")
	}
	path := filepath.Join(dir, defaultSpoolFileName)
	journal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		// Reintroduce the same journal to model retry after partial delivery. A
		// missing journal alone cannot establish database-level repeat safety.
		if err := os.WriteFile(path, journal, 0o600); err != nil {
			t.Fatal(err)
		}
		stats, err := ReplaySpool(ctx, store, ReplayOptions{Directory: dir})
		if err != nil {
			t.Fatal(err)
		}
		if stats.Replayed != len(writes) || stats.Skipped != 0 {
			t.Fatalf("replay: %+v", stats)
		}
		for _, record := range writes {
			var count int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+store.quotedSchema+"."+record.table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("%s has %d rows", record.table, count)
			}
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drained journal remains: %v", err)
	}
	state, err := store.ExecutorResumeState(ctx, id)
	if err != nil || state == nil || state.State["test"] != true {
		t.Fatalf("stored state: %+v %v", state, err)
	}
	groups, err := store.ExecutionGroupProgress(ctx, id)
	if err != nil || len(groups) != 1 || groups[0].GroupName != "workers" {
		t.Fatalf("stored groups: %+v %v", groups, err)
	}
	if err := store.UpsertExecutorResumeState(ctx, ExecutorResumeState{ExecutionID: id, ShutdownFlow: "flow", PlanConfigHash: "hash", Phase: "Updated"}); err != nil {
		t.Fatal(err)
	}
	state, err = store.ExecutorResumeState(ctx, id)
	if err != nil || state == nil || state.Phase != "Updated" {
		t.Fatalf("updated state: %+v %v", state, err)
	}
	// Identity conflicts are repeat-safe, but unrelated integrity failures must
	// remain errors rather than being mistaken for already-delivered evidence.
	if err := store.RecordNodeRelease(ctx, NodeReleaseRecord{
		ReleaseID:   "00000000-0000-4000-8000-000000000099",
		ExecutionID: "00000000-0000-4000-8000-000000000098",
		NodeName:    "worker", Reason: "missing parent",
	}); err == nil {
		t.Fatal("missing execution foreign key must remain an error")
	}
}

func TestPostgresLockedWriterFallsBackToSpool(t *testing.T) {
	ctx, db, store := openTestPostgres(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+store.quotedSchema+".power_events IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	bounded, err := NewBoundedStore(store, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spool, err := NewSpoolWriter(bounded, SpoolOptions{Directory: dir, DisableSync: true})
	if err != nil {
		t.Fatal(err)
	}
	event := PowerEvent{EventID: "00000000-0000-4000-8000-000000000099", EventType: "Test", Severity: "Info", SourceKind: "ShutdownFlow", SourceName: "flow", Message: "blocked writer"}
	started := time.Now()
	if err := spool.RecordPowerEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second || spool.Stats().FallbackWrites != 1 {
		t.Fatalf("unbounded or missing fallback: %+v", spool.Stats())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stats, err := ReplaySpool(ctx, store, ReplayOptions{Directory: dir})
	if err != nil || stats.Replayed != 1 {
		t.Fatalf("replay after lock release: %+v %v", stats, err)
	}
}
