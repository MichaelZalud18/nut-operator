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
	"strings"
	"testing"
)

func TestMigrationsRenderInitialPostgreSQLSchema(t *testing.T) {
	migrations, err := Migrations("")
	if err != nil {
		t.Fatalf("Migrations returned error: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected one migration, got %#v", migrations)
	}
	migration := migrations[0]
	if migration.Version != CurrentSchemaVersion {
		t.Fatalf("expected current schema version %d, got %d", CurrentSchemaVersion, migration.Version)
	}
	for _, want := range []string{
		`CREATE SCHEMA IF NOT EXISTS "power";`,
		`CREATE TABLE IF NOT EXISTS "power".power_events`,
		`CREATE TABLE IF NOT EXISTS "power".ups_telemetry_snapshots`,
		`CREATE TABLE IF NOT EXISTS "power".capability_profile_matches`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_compilations`,
		`CREATE TABLE IF NOT EXISTS "power".shutdownflow_decisions`,
	} {
		if !strings.Contains(migration.SQL, want) {
			t.Fatalf("migration SQL missing %q:\n%s", want, migration.SQL)
		}
	}
	if strings.Contains(strings.ToLower(migration.SQL), "sqlite") {
		t.Fatalf("migration SQL must remain PostgreSQL/CNPG-specific:\n%s", migration.SQL)
	}
}

func TestMigrationsQuoteCustomSchema(t *testing.T) {
	migrations, err := Migrations(`power"audit`)
	if err != nil {
		t.Fatalf("Migrations returned error: %v", err)
	}
	if !strings.Contains(migrations[0].SQL, `CREATE SCHEMA IF NOT EXISTS "power""audit";`) {
		t.Fatalf("expected custom schema to be quoted safely:\n%s", migrations[0].SQL)
	}
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
