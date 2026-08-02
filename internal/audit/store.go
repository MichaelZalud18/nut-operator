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
	"fmt"
	"time"
)

// SQLExecutor is the narrow database boundary required by the audit writer.
// database/sql.DB, database/sql.Tx, and pgx stdlib connections can satisfy this.
type SQLExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Store owns schema readiness and durable audit writes.
type Store interface {
	EnsureSchema(ctx context.Context) error
	Writer
	Close() error
}

// Writer records durable power-management state. Implementations must not be
// on the critical shutdown decision path; write failures degrade auditability.
type Writer interface {
	RecordPowerEvent(ctx context.Context, event PowerEvent) error
	RecordTelemetrySnapshot(ctx context.Context, snapshot TelemetrySnapshot) error
	RecordCapabilityProfileMatch(ctx context.Context, match CapabilityProfileMatch) error
	RecordShutdownFlowCompilation(ctx context.Context, compilation ShutdownFlowCompilation) error
	RecordShutdownFlowDecision(ctx context.Context, decision ShutdownFlowDecision) error
}

// SQLStoreOptions configure a PostgreSQL-backed Store.
type SQLStoreOptions struct {
	Schema string
}

// SQLStore writes audit records through a PostgreSQL-compatible executor.
type SQLStore struct {
	executor     SQLExecutor
	schema       string
	quotedSchema string
}

// NewSQLStore creates a PostgreSQL-shaped audit store without taking ownership
// of the underlying connection lifecycle.
func NewSQLStore(executor SQLExecutor, options SQLStoreOptions) (*SQLStore, error) {
	if executor == nil {
		return nil, fmt.Errorf("audit SQL executor is required")
	}
	schema := defaultSchema(options.Schema)
	quotedSchema, err := quotePostgresIdentifier(schema)
	if err != nil {
		return nil, err
	}
	return &SQLStore{
		executor:     executor,
		schema:       schema,
		quotedSchema: quotedSchema,
	}, nil
}

// EnsureSchema applies all bundled idempotent PostgreSQL migrations.
func (s *SQLStore) EnsureSchema(ctx context.Context) error {
	migrations, err := Migrations(s.schema)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if _, err := s.executor.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply audit migration %d %q: %w", migration.Version, migration.Name, err)
		}
	}
	return nil
}

// Close releases store-owned resources. SQLStore does not own its executor, so
// this is intentionally a no-op.
func (s *SQLStore) Close() error {
	return nil
}

// NoopStore is used when durable storage is explicitly disabled for development
// or tests.
type NoopStore struct{}

func (NoopStore) EnsureSchema(context.Context) error { return nil }
func (NoopStore) Close() error                       { return nil }

func (NoopStore) RecordPowerEvent(context.Context, PowerEvent) error               { return nil }
func (NoopStore) RecordTelemetrySnapshot(context.Context, TelemetrySnapshot) error { return nil }
func (NoopStore) RecordCapabilityProfileMatch(context.Context, CapabilityProfileMatch) error {
	return nil
}
func (NoopStore) RecordShutdownFlowCompilation(context.Context, ShutdownFlowCompilation) error {
	return nil
}
func (NoopStore) RecordShutdownFlowDecision(context.Context, ShutdownFlowDecision) error { return nil }

// PowerEvent records a controller or executor decision/event.
type PowerEvent struct {
	EventID            string
	ObservedAt         time.Time
	EventType          string
	Severity           string
	SourceKind         string
	SourceName         string
	ResourceGeneration *int64
	CorrelationID      string
	Message            string
	Details            map[string]any
}

// TelemetrySnapshot records one observed UPS telemetry sample.
type TelemetrySnapshot struct {
	SnapshotID           string
	ObservedAt           time.Time
	UPSDevice            string
	NUTServer            string
	NUTName              string
	UPSStatus            string
	BatteryChargePercent *float64
	RuntimeSeconds       *int64
	LoadPercent          *float64
	Variables            map[string]string
}

// CapabilityProfileMatch records the profile chosen for one UPS device.
type CapabilityProfileMatch struct {
	MatchID        string
	ObservedAt     time.Time
	UPSDevice      string
	ProfileID      string
	ProfileVersion string
	ProfileSource  string
	MatchTier      string
	Fallback       bool
	Diagnostics    []DiagnosticRecord
}

// ShutdownFlowCompilation records a planner compilation.
type ShutdownFlowCompilation struct {
	CompilationID      string
	ObservedAt         time.Time
	ShutdownFlow       string
	ResourceGeneration int64
	ConfigHash         string
	InputHash          string
	Accepted           bool
	Diagnostics        []DiagnosticRecord
	CompiledWaves      any
}

// ShutdownFlowDecision records a dry-run or enforce decision for a trigger.
type ShutdownFlowDecision struct {
	DecisionID         string
	ObservedAt         time.Time
	ShutdownFlow       string
	TriggerType        string
	Mode               string
	Approved           bool
	Decision           string
	Reason             string
	SelectedUPSDevices []string
	PlanConfigHash     string
	Details            map[string]any
}

// DiagnosticRecord is the durable, package-local diagnostic shape.
type DiagnosticRecord struct {
	Severity string `json:"severity"`
	Source   string `json:"source,omitempty"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

func (s *SQLStore) RecordPowerEvent(ctx context.Context, event PowerEvent) error {
	if event.EventID == "" {
		return fmt.Errorf("power event requires event ID")
	}
	if event.EventType == "" || event.Severity == "" || event.SourceKind == "" || event.SourceName == "" || event.Message == "" {
		return fmt.Errorf("power event requires event type, severity, source, and message")
	}
	details, err := jsonObject(event.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.power_events
(event_id, observed_at, event_type, severity, source_kind, source_name, resource_generation, correlation_id, message, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`, s.quotedSchema),
		event.EventID,
		observedAt(event.ObservedAt),
		event.EventType,
		event.Severity,
		event.SourceKind,
		event.SourceName,
		optionalInt64(event.ResourceGeneration),
		optionalString(event.CorrelationID),
		event.Message,
		details,
	)
	if err != nil {
		return fmt.Errorf("record power event %q: %w", event.EventID, err)
	}
	return nil
}

func (s *SQLStore) RecordTelemetrySnapshot(ctx context.Context, snapshot TelemetrySnapshot) error {
	if snapshot.SnapshotID == "" || snapshot.UPSDevice == "" {
		return fmt.Errorf("telemetry snapshot requires snapshot ID and UPS device")
	}
	variables, err := jsonObject(snapshot.Variables)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.ups_telemetry_snapshots
(snapshot_id, observed_at, ups_device, nut_server, nut_name, ups_status, battery_charge_percent, runtime_seconds, load_percent, variables)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`, s.quotedSchema),
		snapshot.SnapshotID,
		observedAt(snapshot.ObservedAt),
		snapshot.UPSDevice,
		optionalString(snapshot.NUTServer),
		optionalString(snapshot.NUTName),
		optionalString(snapshot.UPSStatus),
		optionalFloat64(snapshot.BatteryChargePercent),
		optionalInt64(snapshot.RuntimeSeconds),
		optionalFloat64(snapshot.LoadPercent),
		variables,
	)
	if err != nil {
		return fmt.Errorf("record telemetry snapshot %q: %w", snapshot.SnapshotID, err)
	}
	return nil
}

func (s *SQLStore) RecordCapabilityProfileMatch(ctx context.Context, match CapabilityProfileMatch) error {
	if match.MatchID == "" || match.UPSDevice == "" || match.ProfileID == "" || match.ProfileVersion == "" || match.ProfileSource == "" || match.MatchTier == "" {
		return fmt.Errorf("capability profile match requires match ID, UPS device, profile, source, and tier")
	}
	diagnostics, err := jsonArray(match.Diagnostics)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.capability_profile_matches
(match_id, observed_at, ups_device, profile_id, profile_version, profile_source, match_tier, fallback, diagnostics)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb)`, s.quotedSchema),
		match.MatchID,
		observedAt(match.ObservedAt),
		match.UPSDevice,
		match.ProfileID,
		match.ProfileVersion,
		match.ProfileSource,
		match.MatchTier,
		match.Fallback,
		diagnostics,
	)
	if err != nil {
		return fmt.Errorf("record capability profile match %q: %w", match.MatchID, err)
	}
	return nil
}

func (s *SQLStore) RecordShutdownFlowCompilation(ctx context.Context, compilation ShutdownFlowCompilation) error {
	if compilation.CompilationID == "" || compilation.ShutdownFlow == "" || compilation.ConfigHash == "" {
		return fmt.Errorf("shutdown flow compilation requires compilation ID, flow, and config hash")
	}
	diagnostics, err := jsonArray(compilation.Diagnostics)
	if err != nil {
		return err
	}
	waves, err := jsonArrayValue(compilation.CompiledWaves)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_compilations
(compilation_id, observed_at, shutdownflow, resource_generation, config_hash, input_hash, accepted, diagnostics, compiled_waves)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb)`, s.quotedSchema),
		compilation.CompilationID,
		observedAt(compilation.ObservedAt),
		compilation.ShutdownFlow,
		compilation.ResourceGeneration,
		compilation.ConfigHash,
		optionalString(compilation.InputHash),
		compilation.Accepted,
		diagnostics,
		waves,
	)
	if err != nil {
		return fmt.Errorf("record shutdown flow compilation %q: %w", compilation.CompilationID, err)
	}
	return nil
}

func (s *SQLStore) RecordShutdownFlowDecision(ctx context.Context, decision ShutdownFlowDecision) error {
	if decision.DecisionID == "" || decision.ShutdownFlow == "" || decision.TriggerType == "" || decision.Mode == "" || decision.Decision == "" || decision.Reason == "" {
		return fmt.Errorf("shutdown flow decision requires decision ID, flow, trigger, mode, decision, and reason")
	}
	selectedUPSDevices, err := jsonArray(decision.SelectedUPSDevices)
	if err != nil {
		return err
	}
	details, err := jsonObject(decision.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_decisions
(decision_id, observed_at, shutdownflow, trigger_type, mode, approved, decision, reason, selected_ups_devices, plan_config_hash, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11::jsonb)`, s.quotedSchema),
		decision.DecisionID,
		observedAt(decision.ObservedAt),
		decision.ShutdownFlow,
		decision.TriggerType,
		decision.Mode,
		decision.Approved,
		decision.Decision,
		decision.Reason,
		selectedUPSDevices,
		optionalString(decision.PlanConfigHash),
		details,
	)
	if err != nil {
		return fmt.Errorf("record shutdown flow decision %q: %w", decision.DecisionID, err)
	}
	return nil
}

func observedAt(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func jsonObject(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal JSON object: %w", err)
	}
	if len(encoded) == 0 || encoded[0] != '{' {
		return "", fmt.Errorf("expected JSON object payload")
	}
	return string(encoded), nil
}

func jsonArray[T any](value []T) (string, error) {
	if value == nil {
		return "[]", nil
	}
	return jsonArrayValue(value)
}

func jsonArrayValue(value any) (string, error) {
	if value == nil {
		return "[]", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal JSON array: %w", err)
	}
	if len(encoded) == 0 || encoded[0] != '[' {
		return "", fmt.Errorf("expected JSON array payload")
	}
	return string(encoded), nil
}
