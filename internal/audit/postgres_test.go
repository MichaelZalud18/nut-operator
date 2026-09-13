//go:build postgres

package audit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The tagged suite requires an explicit disposable database; the default suite
// neither opens database connections nor silently skips this acceptance test.
func TestPostgresMigrationsHistoryAndRetention(t *testing.T) {
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
	defer cancel()
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
