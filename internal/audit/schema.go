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
// telemetry snapshots, planner compilations, shutdown decisions, and executor
// state.
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
	CurrentSchemaVersion = 4
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
		{
			Version: 2,
			Name:    "executor_state_schema",
			SQL:     executorStateSchemaSQL(quotedSchema),
		},
		{
			Version: 3,
			Name:    "capability_profile_verification_schema",
			SQL:     capabilityProfileVerificationSchemaSQL(quotedSchema),
		},
		{
			Version: 4,
			Name:    "planner_artifact_schema",
			SQL:     plannerArtifactSchemaSQL(quotedSchema),
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

func plannerArtifactSchemaSQL(schema string) string {
	return fmt.Sprintf(`ALTER TABLE %[1]s.shutdownflow_compilations
  ADD COLUMN IF NOT EXISTS dependency_graph jsonb NOT NULL DEFAULT '{"vertices":[],"edges":[]}'::jsonb,
  ADD COLUMN IF NOT EXISTS startup_waves jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS explanations jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS diagram_exports jsonb NOT NULL DEFAULT '{}'::jsonb;

INSERT INTO %[1]s.audit_schema_migrations (version, name)
VALUES (4, 'planner_artifact_schema')
ON CONFLICT (version) DO NOTHING;
`, schema)
}

func executorStateSchemaSQL(schema string) string {
	return fmt.Sprintf(`ALTER TABLE %[1]s.shutdownflow_compilations
  ALTER COLUMN config_hash DROP NOT NULL;

CREATE TABLE IF NOT EXISTS %[1]s.shutdownflow_executions (
  execution_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  shutdownflow text NOT NULL,
  trigger_decision_id uuid,
  mode text NOT NULL,
  phase text NOT NULL,
  reason text,
  plan_config_hash text NOT NULL,
  input_hash text,
  started_at timestamptz,
  completed_at timestamptz,
  dry_run boolean NOT NULL DEFAULT true,
  approved boolean NOT NULL DEFAULT false,
  approval_evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
  revalidation jsonb NOT NULL DEFAULT '{}'::jsonb,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS shutdownflow_executions_flow_time_idx
  ON %[1]s.shutdownflow_executions (shutdownflow, observed_at DESC);

CREATE INDEX IF NOT EXISTS shutdownflow_executions_plan_hash_idx
  ON %[1]s.shutdownflow_executions (plan_config_hash);

CREATE TABLE IF NOT EXISTS %[1]s.shutdownflow_execution_waves (
  wave_record_id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES %[1]s.shutdownflow_executions (execution_id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  wave_index integer NOT NULL,
  phase text NOT NULL,
  started_at timestamptz,
  completed_at timestamptz,
  group_names jsonb NOT NULL DEFAULT '[]'::jsonb,
  details jsonb NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (execution_id, wave_index)
);

CREATE INDEX IF NOT EXISTS shutdownflow_execution_waves_execution_idx
  ON %[1]s.shutdownflow_execution_waves (execution_id, wave_index);

CREATE TABLE IF NOT EXISTS %[1]s.shutdownflow_execution_groups (
  group_record_id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES %[1]s.shutdownflow_executions (execution_id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  wave_index integer NOT NULL,
  group_name text NOT NULL,
  action text NOT NULL,
  phase text NOT NULL,
  started_at timestamptz,
  completed_at timestamptz,
  selected_targets jsonb NOT NULL DEFAULT '[]'::jsonb,
  details jsonb NOT NULL DEFAULT '{}'::jsonb,
  UNIQUE (execution_id, wave_index, group_name)
);

CREATE INDEX IF NOT EXISTS shutdownflow_execution_groups_execution_idx
  ON %[1]s.shutdownflow_execution_groups (execution_id, wave_index, group_name);

CREATE TABLE IF NOT EXISTS %[1]s.shutdownflow_action_attempts (
  attempt_id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES %[1]s.shutdownflow_executions (execution_id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  wave_index integer,
  group_name text,
  action text NOT NULL,
  target_kind text,
  target_namespace text,
  target_name text,
  started_at timestamptz,
  completed_at timestamptz,
  outcome text NOT NULL,
  error text,
  dry_run boolean NOT NULL DEFAULT true,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS shutdownflow_action_attempts_execution_idx
  ON %[1]s.shutdownflow_action_attempts (execution_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.node_release_records (
  release_id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES %[1]s.shutdownflow_executions (execution_id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  node_name text NOT NULL,
  node_power_agent text,
  plan_config_hash text,
  approved boolean NOT NULL DEFAULT false,
  released boolean NOT NULL DEFAULT false,
  reason text NOT NULL,
  clearance jsonb NOT NULL DEFAULT '{}'::jsonb,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS node_release_records_execution_idx
  ON %[1]s.node_release_records (execution_id, observed_at DESC);

CREATE INDEX IF NOT EXISTS node_release_records_node_time_idx
  ON %[1]s.node_release_records (node_name, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.node_signal_handoffs (
  handoff_id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES %[1]s.shutdownflow_executions (execution_id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  node_name text NOT NULL,
  node_power_agent text,
  signal_path text,
  signal_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  stale_after timestamptz,
  accepted boolean NOT NULL DEFAULT false,
  reason text NOT NULL,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS node_signal_handoffs_execution_idx
  ON %[1]s.node_signal_handoffs (execution_id, observed_at DESC);

CREATE INDEX IF NOT EXISTS node_signal_handoffs_node_time_idx
  ON %[1]s.node_signal_handoffs (node_name, observed_at DESC);

CREATE TABLE IF NOT EXISTS %[1]s.executor_resume_states (
  execution_id uuid PRIMARY KEY REFERENCES %[1]s.shutdownflow_executions (execution_id) ON DELETE CASCADE,
  observed_at timestamptz NOT NULL DEFAULT now(),
  shutdownflow text NOT NULL,
  plan_config_hash text NOT NULL,
  current_wave_index integer,
  phase text NOT NULL,
  state jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS executor_resume_states_flow_idx
  ON %[1]s.executor_resume_states (shutdownflow, updated_at DESC);

INSERT INTO %[1]s.audit_schema_migrations (version, name)
VALUES (2, 'executor_state_schema')
ON CONFLICT (version) DO NOTHING;
`, schema)
}

func capabilityProfileVerificationSchemaSQL(schema string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s.capability_profile_verifications (
  verification_id uuid PRIMARY KEY,
  observed_at timestamptz NOT NULL DEFAULT now(),
  ups_device text NOT NULL,
  profile_id text NOT NULL,
  profile_version text NOT NULL,
  profile_source text NOT NULL,
  model text,
  firmware text,
  nut_driver text,
  nut_server text,
  nut_name text,
  verified boolean NOT NULL DEFAULT false,
  drift_detected boolean NOT NULL DEFAULT false,
  probe_variables jsonb NOT NULL DEFAULT '{}'::jsonb,
  expected_variables jsonb NOT NULL DEFAULT '[]'::jsonb,
  missing_variables jsonb NOT NULL DEFAULT '[]'::jsonb,
  unexpected_variables jsonb NOT NULL DEFAULT '[]'::jsonb,
  diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb,
  details jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS capability_profile_verifications_device_time_idx
  ON %[1]s.capability_profile_verifications (ups_device, observed_at DESC);

CREATE INDEX IF NOT EXISTS capability_profile_verifications_profile_time_idx
  ON %[1]s.capability_profile_verifications (profile_id, profile_version, observed_at DESC);

CREATE INDEX IF NOT EXISTS capability_profile_verifications_drift_time_idx
  ON %[1]s.capability_profile_verifications (observed_at DESC)
  WHERE drift_detected;

INSERT INTO %[1]s.audit_schema_migrations (version, name)
VALUES (3, 'capability_profile_verification_schema')
ON CONFLICT (version) DO NOTHING;
`, schema)
}
