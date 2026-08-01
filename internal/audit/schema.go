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

// Package audit owns PostgreSQL schema artifacts for durable power events,
// telemetry snapshots, planner compilations, and shutdown decisions.
package audit

import (
	"fmt"
	"strings"
)

const (
	// DefaultSchema is the schema used when storage configuration does not
	// specify a custom PostgreSQL schema name.
	DefaultSchema = "power"

	// CurrentSchemaVersion is the latest bundled migration version.
	CurrentSchemaVersion = 1
)

// Migration is one ordered PostgreSQL migration.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations returns the ordered PostgreSQL migrations for schema.
func Migrations(schema string) ([]Migration, error) {
	quotedSchema, err := quotePostgresIdentifier(defaultSchema(schema))
	if err != nil {
		return nil, err
	}

	return []Migration{
		{
			Version: 1,
			Name:    "initial_power_audit_schema",
			SQL:     initialSchemaSQL(quotedSchema),
		},
	}, nil
}

func defaultSchema(schema string) string {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		return DefaultSchema
	}
	return schema
}

func quotePostgresIdentifier(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("PostgreSQL identifier is empty")
	}
	if strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("PostgreSQL identifier contains NUL")
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`, nil
}

func initialSchemaSQL(schema string) string {
	return fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %[1]s;

CREATE TABLE IF NOT EXISTS %[1]s.audit_schema_migrations (
  version integer PRIMARY KEY,
  name text NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS %[1]s.power_events (
  event_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  event_type text NOT NULL,
  severity text NOT NULL,
  source_kind text NOT NULL,
  source_name text NOT NULL,
  resource_generation bigint,
  correlation_id text,
  message text NOT NULL,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS power_events_observed_at_idx
  ON %[1]s.power_events (observed_at DESC);

CREATE INDEX IF NOT EXISTS power_events_source_idx
  ON %[1]s.power_events (source_kind, source_name, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.ups_telemetry_snapshots (
  snapshot_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  ups_device text NOT NULL,
  nut_server text,
  nut_name text,
  ups_status text,
  battery_charge_percent numeric(5,2),
  runtime_seconds bigint,
  load_percent numeric(5,2),
  variables jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS ups_telemetry_snapshots_device_time_idx
  ON %[1]s.ups_telemetry_snapshots (ups_device, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.capability_profile_matches (
  match_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  ups_device text NOT NULL,
  profile_id text NOT NULL,
  profile_version text NOT NULL,
  profile_source text NOT NULL,
  match_tier text NOT NULL,
  fallback boolean NOT NULL DEFAULT false,
  diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS capability_profile_matches_device_time_idx
  ON %[1]s.capability_profile_matches (ups_device, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.shutdownflow_compilations (
  compilation_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  shutdownflow text NOT NULL,
  resource_generation bigint NOT NULL,
  config_hash text NOT NULL,
  input_hash text,
  accepted boolean NOT NULL,
  diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb,
  compiled_waves jsonb NOT NULL DEFAULT '[]'::jsonb
);

CREATE INDEX IF NOT EXISTS shutdownflow_compilations_flow_time_idx
  ON %[1]s.shutdownflow_compilations (shutdownflow, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.shutdownflow_decisions (
  decision_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  shutdownflow text NOT NULL,
  trigger_type text NOT NULL,
  mode text NOT NULL,
  approved boolean NOT NULL DEFAULT false,
  decision text NOT NULL,
  reason text NOT NULL,
  selected_ups_devices jsonb NOT NULL DEFAULT '[]'::jsonb,
  plan_config_hash text,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS shutdownflow_decisions_flow_time_idx
  ON %[1]s.shutdownflow_decisions (shutdownflow, observed_at DESC);

INSERT INTO %[1]s.audit_schema_migrations (version, name)
VALUES (1, 'initial_power_audit_schema')
ON CONFLICT (version) DO NOTHING;
`, schema)
}
