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
	"fmt"
	"strings"
	"testing"
)

func TestMigrationsRenderInitialPostgreSQLSchema(t *testing.T) {
	migrations, err := Migrations("")
	if err != nil {
		t.Fatalf("Migrations returned error: %v", err)
	}
	if len(migrations) != CurrentSchemaVersion {
		t.Fatalf("expected %d migrations, got %#v", CurrentSchemaVersion, migrations)
	}
	if migrations[len(migrations)-1].Version != CurrentSchemaVersion {
		t.Fatalf("expected current schema version %d, got %d", CurrentSchemaVersion, migrations[len(migrations)-1].Version)
	}
	combinedSQL := combinedMigrationSQL(migrations)
	for _, want := range []string{
		`CREATE SCHEMA IF NOT EXISTS "power";`,
		`CREATE TABLE IF NOT EXISTS "power".power_events`,
		`CREATE TABLE IF NOT EXISTS "power".ups_telemetry_snapshots`,
		`CREATE TABLE IF NOT EXISTS "power".capability_profile_matches`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_compilations`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_decisions`,
		`ALTER TABLE "power".shutdownflow_compilations`,
		`ALTER COLUMN config_hash DROP NOT NULL`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_executions`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_execution_waves`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_execution_groups`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_action_attempts`,
		`CREATE TABLE IF NOT EXISTS "power".node_release_records`,
		`CREATE TABLE IF NOT EXISTS "power".node_signal_handoffs`,
		`CREATE TABLE IF NOT EXISTS "power".executor_resume_states`,
		`CREATE TABLE IF NOT EXISTS "power".capability_profile_verifications`,
		`CREATE INDEX IF NOT EXISTS capability_profile_verifications_device_time_idx`,
		`CREATE INDEX IF NOT EXISTS capability_profile_verifications_drift_time_idx`,
		`ADD COLUMN IF NOT EXISTS dependency_graph jsonb`,
		`ADD COLUMN IF NOT EXISTS startup_waves jsonb`,
		`ADD COLUMN IF NOT EXISTS explanations jsonb`,
		`ADD COLUMN IF NOT EXISTS diagram_exports jsonb`,
	} {
		if !strings.Contains(combinedSQL, want) {
			t.Fatalf("migration SQL missing %q:\n%s", want, combinedSQL)
		}
	}
	if strings.Contains(strings.ToLower(combinedSQL), "sqlite") {
		t.Fatalf("migration SQL must remain PostgreSQL/CNPG-specific:\n%s", combinedSQL)
	}
}

func TestMigrationsQuoteCustomSchema(t *testing.T) {
	migrations, err := Migrations(`power"audit`)
	if err != nil {
		t.Fatalf("Migrations returned error: %v", err)
	}
	combinedSQL := combinedMigrationSQL(migrations)
	if !strings.Contains(combinedSQL, `CREATE SCHEMA IF NOT EXISTS "power""audit";`) ||
		!strings.Contains(combinedSQL, `"power""audit".shutdownflow_executions`) ||
		!strings.Contains(combinedSQL, `"power""audit".capability_profile_verifications`) {
		t.Fatalf("expected custom schema to be quoted safely:\n%s", combinedSQL)
	}
}

func combinedMigrationSQL(migrations []Migration) string {
	var builder strings.Builder
	for _, migration := range migrations {
		builder.WriteString(migration.SQL)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func TestMigrationsRejectEmptyQuotedIdentifier(t *testing.T) {
	if _, err := Migrations("   "); err != nil {
		t.Fatalf("blank schema should default to %q, got %v", DefaultSchema, err)
	}
	_, err := quotePostgresIdentifier("")
	if err == nil {
		t.Fatal("expected empty PostgreSQL identifier to be rejected")
	}
}

func TestEveryRetentionTargetHasAnObservedAtIndex(t *testing.T) {
	migrations, err := Migrations("power")
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	var schema strings.Builder
	for _, migration := range migrations {
		schema.WriteString(migration.SQL)
	}
	sql := schema.String()

	// EnforceRetention deletes with WHERE observed_at < $1. A composite index
	// whose leading column is something else cannot serve that as a range seek,
	// so each retention target needs observed_at indexed on its own.
	targets := append([]string{"ups_telemetry_snapshots"}, eventRetentionTables...)
	for _, table := range targets {
		// Ascending or descending both range-scan for observed_at < $1; what
		// matters is that observed_at leads the index.
		leading := fmt.Sprintf("ON \"power\".%s (observed_at)", table)
		leadingDesc := fmt.Sprintf("ON \"power\".%s (observed_at DESC)", table)
		if !strings.Contains(sql, leading) && !strings.Contains(sql, leadingDesc) {
			t.Errorf("retention deletes from %s by observed_at, but no index leads with that column", table)
		}
	}
}
