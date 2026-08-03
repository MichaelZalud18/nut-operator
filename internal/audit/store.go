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
	"reflect"
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
	EnforceRetention(ctx context.Context, now time.Time) error
	Writer
	Close() error
}

// Writer records durable power-management state. Implementations must not be
// on the critical shutdown decision path; write failures degrade auditability.
type Writer interface {
	RecordPowerEvent(ctx context.Context, event PowerEvent) error
	RecordTelemetrySnapshot(ctx context.Context, snapshot TelemetrySnapshot) error
	RecordCapabilityProfileMatch(ctx context.Context, match CapabilityProfileMatch) error
	RecordCapabilityProfileVerification(ctx context.Context, verification CapabilityProfileVerification) error
	RecordShutdownFlowCompilation(ctx context.Context, compilation ShutdownFlowCompilation) error
	RecordShutdownFlowDecision(ctx context.Context, decision ShutdownFlowDecision) error
	RecordShutdownFlowExecution(ctx context.Context, execution ShutdownFlowExecution) error
	RecordShutdownFlowExecutionWave(ctx context.Context, wave ShutdownFlowExecutionWave) error
	RecordShutdownFlowExecutionGroup(ctx context.Context, group ShutdownFlowExecutionGroup) error
	RecordShutdownFlowActionAttempt(ctx context.Context, attempt ShutdownFlowActionAttempt) error
	RecordNodeRelease(ctx context.Context, release NodeReleaseRecord) error
	RecordNodeSignalHandoff(ctx context.Context, handoff NodeSignalHandoff) error
	UpsertExecutorResumeState(ctx context.Context, state ExecutorResumeState) error
}

// SQLStoreOptions configure a PostgreSQL-backed Store.
type SQLStoreOptions struct {
	Schema    string
	Retention RetentionPolicy
}

// SQLStore writes audit records through a PostgreSQL-compatible executor.
type SQLStore struct {
	executor     SQLExecutor
	schema       string
	quotedSchema string
	retention    RetentionPolicy
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
		retention:    options.Retention,
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

// RetentionPolicy controls pruning of durable audit records. A zero duration
// keeps that record family indefinitely.
type RetentionPolicy struct {
	Events    time.Duration
	Telemetry time.Duration
}

func (p RetentionPolicy) validate() error {
	if p.Events < 0 {
		return fmt.Errorf("event retention duration must not be negative")
	}
	if p.Telemetry < 0 {
		return fmt.Errorf("telemetry retention duration must not be negative")
	}
	return nil
}

// EnforceRetention prunes records older than the configured retention windows.
// It intentionally deletes only from root tables; executor child tables are
// removed by ON DELETE CASCADE from shutdownflow_executions.
func (s *SQLStore) EnforceRetention(ctx context.Context, now time.Time) error {
	if err := s.retention.validate(); err != nil {
		return err
	}
	if s.retention.Events == 0 && s.retention.Telemetry == 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if s.retention.Events > 0 {
		cutoff := now.Add(-s.retention.Events)
		for _, table := range eventRetentionTables {
			if err := s.deleteOlderThan(ctx, table, cutoff); err != nil {
				return err
			}
		}
	}
	if s.retention.Telemetry > 0 {
		if err := s.deleteOlderThan(ctx, "ups_telemetry_snapshots", now.Add(-s.retention.Telemetry)); err != nil {
			return err
		}
	}
	return nil
}

var eventRetentionTables = []string{
	"power_events",
	"capability_profile_matches",
	"capability_profile_verifications",
	"shutdownflow_compilations",
	"shutdownflow_decisions",
	"shutdownflow_executions",
}

func (s *SQLStore) deleteOlderThan(ctx context.Context, table string, cutoff time.Time) error {
	_, err := s.executor.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %[1]s.%[2]s
WHERE observed_at < $1`, s.quotedSchema, table), cutoff.UTC())
	if err != nil {
		return fmt.Errorf("enforce audit retention for %s: %w", table, err)
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
func (NoopStore) EnforceRetention(context.Context, time.Time) error {
	return nil
}
func (NoopStore) Close() error { return nil }

func (NoopStore) RecordPowerEvent(context.Context, PowerEvent) error               { return nil }
func (NoopStore) RecordTelemetrySnapshot(context.Context, TelemetrySnapshot) error { return nil }
func (NoopStore) RecordCapabilityProfileMatch(context.Context, CapabilityProfileMatch) error {
	return nil
}
func (NoopStore) RecordCapabilityProfileVerification(context.Context, CapabilityProfileVerification) error {
	return nil
}
func (NoopStore) RecordShutdownFlowCompilation(context.Context, ShutdownFlowCompilation) error {
	return nil
}
func (NoopStore) RecordShutdownFlowDecision(context.Context, ShutdownFlowDecision) error { return nil }
func (NoopStore) RecordShutdownFlowExecution(context.Context, ShutdownFlowExecution) error {
	return nil
}
func (NoopStore) RecordShutdownFlowExecutionWave(context.Context, ShutdownFlowExecutionWave) error {
	return nil
}
func (NoopStore) RecordShutdownFlowExecutionGroup(context.Context, ShutdownFlowExecutionGroup) error {
	return nil
}
func (NoopStore) RecordShutdownFlowActionAttempt(context.Context, ShutdownFlowActionAttempt) error {
	return nil
}
func (NoopStore) RecordNodeRelease(context.Context, NodeReleaseRecord) error { return nil }
func (NoopStore) RecordNodeSignalHandoff(context.Context, NodeSignalHandoff) error {
	return nil
}
func (NoopStore) UpsertExecutorResumeState(context.Context, ExecutorResumeState) error { return nil }

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

// CapabilityProfileVerification records runtime evidence that a matched profile
// was checked against observed NUT variables and provider identity.
type CapabilityProfileVerification struct {
	VerificationID      string
	ObservedAt          time.Time
	UPSDevice           string
	ProfileID           string
	ProfileVersion      string
	ProfileSource       string
	Model               string
	Firmware            string
	NUTDriver           string
	NUTServer           string
	NUTName             string
	Verified            bool
	DriftDetected       bool
	ProbeVariables      map[string]string
	ExpectedVariables   []string
	MissingVariables    []string
	UnexpectedVariables []string
	Diagnostics         []DiagnosticRecord
	Details             map[string]any
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
	DependencyGraph    any
	StartupWaves       any
	Explanations       any
	DiagramExports     any
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

// ShutdownFlowExecution records one executor run for a compiled plan.
type ShutdownFlowExecution struct {
	ExecutionID       string
	ObservedAt        time.Time
	ShutdownFlow      string
	TriggerDecisionID string
	Mode              string
	Phase             string
	Reason            string
	PlanConfigHash    string
	InputHash         string
	StartedAt         *time.Time
	CompletedAt       *time.Time
	DryRun            bool
	Approved          bool
	ApprovalEvidence  map[string]any
	Revalidation      map[string]any
	Details           map[string]any
}

// ShutdownFlowExecutionWave records durable progress for one execution wave.
type ShutdownFlowExecutionWave struct {
	WaveRecordID string
	ExecutionID  string
	ObservedAt   time.Time
	WaveIndex    int32
	Phase        string
	StartedAt    *time.Time
	CompletedAt  *time.Time
	GroupNames   []string
	Details      map[string]any
}

// ShutdownFlowExecutionGroup records durable progress for one group inside a wave.
type ShutdownFlowExecutionGroup struct {
	GroupRecordID   string
	ExecutionID     string
	ObservedAt      time.Time
	WaveIndex       int32
	GroupName       string
	Action          string
	Phase           string
	StartedAt       *time.Time
	CompletedAt     *time.Time
	SelectedTargets any
	Details         map[string]any
}

// ShutdownFlowActionAttempt records one effectful or dry-run executor action.
type ShutdownFlowActionAttempt struct {
	AttemptID       string
	ExecutionID     string
	ObservedAt      time.Time
	WaveIndex       *int32
	GroupName       string
	Action          string
	TargetKind      string
	TargetNamespace string
	TargetName      string
	StartedAt       *time.Time
	CompletedAt     *time.Time
	Outcome         string
	Error           string
	DryRun          bool
	Details         map[string]any
}

// NodeReleaseRecord records the executor-to-node-agent release decision.
type NodeReleaseRecord struct {
	ReleaseID      string
	ExecutionID    string
	ObservedAt     time.Time
	NodeName       string
	NodePowerAgent string
	PlanConfigHash string
	Approved       bool
	Released       bool
	Reason         string
	Clearance      map[string]any
	Details        map[string]any
}

// NodeSignalHandoff records the signal-file handoff to the host actuator boundary.
type NodeSignalHandoff struct {
	HandoffID      string
	ExecutionID    string
	ObservedAt     time.Time
	NodeName       string
	NodePowerAgent string
	SignalPath     string
	SignalPayload  map[string]any
	StaleAfter     *time.Time
	Accepted       bool
	Reason         string
	Details        map[string]any
}

// ExecutorResumeState stores compact executor restart state for one execution.
type ExecutorResumeState struct {
	ExecutionID      string
	ObservedAt       time.Time
	ShutdownFlow     string
	PlanConfigHash   string
	CurrentWaveIndex *int32
	Phase            string
	State            map[string]any
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

func (s *SQLStore) RecordCapabilityProfileVerification(ctx context.Context, verification CapabilityProfileVerification) error {
	if verification.VerificationID == "" || verification.UPSDevice == "" || verification.ProfileID == "" || verification.ProfileVersion == "" || verification.ProfileSource == "" {
		return fmt.Errorf("capability profile verification requires verification ID, UPS device, profile, and source")
	}
	probeVariables, err := jsonObject(verification.ProbeVariables)
	if err != nil {
		return err
	}
	expectedVariables, err := jsonArray(verification.ExpectedVariables)
	if err != nil {
		return err
	}
	missingVariables, err := jsonArray(verification.MissingVariables)
	if err != nil {
		return err
	}
	unexpectedVariables, err := jsonArray(verification.UnexpectedVariables)
	if err != nil {
		return err
	}
	diagnostics, err := jsonArray(verification.Diagnostics)
	if err != nil {
		return err
	}
	details, err := jsonObject(verification.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.capability_profile_verifications
(verification_id, observed_at, ups_device, profile_id, profile_version, profile_source, model, firmware, nut_driver, nut_server, nut_name, verified, drift_detected, probe_variables, expected_variables, missing_variables, unexpected_variables, diagnostics, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15::jsonb, $16::jsonb, $17::jsonb, $18::jsonb, $19::jsonb)`, s.quotedSchema),
		verification.VerificationID,
		observedAt(verification.ObservedAt),
		verification.UPSDevice,
		verification.ProfileID,
		verification.ProfileVersion,
		verification.ProfileSource,
		optionalString(verification.Model),
		optionalString(verification.Firmware),
		optionalString(verification.NUTDriver),
		optionalString(verification.NUTServer),
		optionalString(verification.NUTName),
		verification.Verified,
		verification.DriftDetected,
		probeVariables,
		expectedVariables,
		missingVariables,
		unexpectedVariables,
		diagnostics,
		details,
	)
	if err != nil {
		return fmt.Errorf("record capability profile verification %q: %w", verification.VerificationID, err)
	}
	return nil
}

func (s *SQLStore) RecordShutdownFlowCompilation(ctx context.Context, compilation ShutdownFlowCompilation) error {
	if compilation.CompilationID == "" || compilation.ShutdownFlow == "" {
		return fmt.Errorf("shutdown flow compilation requires compilation ID and flow")
	}
	if compilation.Accepted && compilation.ConfigHash == "" {
		return fmt.Errorf("accepted shutdown flow compilation requires config hash")
	}
	diagnostics, err := jsonArray(compilation.Diagnostics)
	if err != nil {
		return err
	}
	waves, err := jsonArrayValue(compilation.CompiledWaves)
	if err != nil {
		return err
	}
	dependencyGraph, err := jsonObject(compilation.DependencyGraph)
	if err != nil {
		return err
	}
	startupWaves, err := jsonArrayValue(compilation.StartupWaves)
	if err != nil {
		return err
	}
	explanations, err := jsonArrayValue(compilation.Explanations)
	if err != nil {
		return err
	}
	diagramExports, err := jsonObject(compilation.DiagramExports)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_compilations
(compilation_id, observed_at, shutdownflow, resource_generation, config_hash, input_hash, accepted, diagnostics, compiled_waves, dependency_graph, startup_waves, explanations, diagram_exports)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb, $13::jsonb)`, s.quotedSchema),
		compilation.CompilationID,
		observedAt(compilation.ObservedAt),
		compilation.ShutdownFlow,
		compilation.ResourceGeneration,
		optionalString(compilation.ConfigHash),
		optionalString(compilation.InputHash),
		compilation.Accepted,
		diagnostics,
		waves,
		dependencyGraph,
		startupWaves,
		explanations,
		diagramExports,
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

func (s *SQLStore) RecordShutdownFlowExecution(ctx context.Context, execution ShutdownFlowExecution) error {
	if execution.ExecutionID == "" || execution.ShutdownFlow == "" || execution.Mode == "" || execution.Phase == "" || execution.PlanConfigHash == "" {
		return fmt.Errorf("shutdown flow execution requires execution ID, flow, mode, phase, and plan config hash")
	}
	approvalEvidence, err := jsonObject(execution.ApprovalEvidence)
	if err != nil {
		return err
	}
	revalidation, err := jsonObject(execution.Revalidation)
	if err != nil {
		return err
	}
	details, err := jsonObject(execution.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_executions
(execution_id, observed_at, shutdownflow, trigger_decision_id, mode, phase, reason, plan_config_hash, input_hash, started_at, completed_at, dry_run, approved, approval_evidence, revalidation, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15::jsonb, $16::jsonb)
ON CONFLICT (execution_id) DO UPDATE SET
  observed_at = EXCLUDED.observed_at,
  shutdownflow = EXCLUDED.shutdownflow,
  trigger_decision_id = EXCLUDED.trigger_decision_id,
  mode = EXCLUDED.mode,
  phase = EXCLUDED.phase,
  reason = EXCLUDED.reason,
  plan_config_hash = EXCLUDED.plan_config_hash,
  input_hash = EXCLUDED.input_hash,
  started_at = EXCLUDED.started_at,
  completed_at = EXCLUDED.completed_at,
  dry_run = EXCLUDED.dry_run,
  approved = EXCLUDED.approved,
  approval_evidence = EXCLUDED.approval_evidence,
  revalidation = EXCLUDED.revalidation,
  details = EXCLUDED.details`, s.quotedSchema),
		execution.ExecutionID,
		observedAt(execution.ObservedAt),
		execution.ShutdownFlow,
		optionalString(execution.TriggerDecisionID),
		execution.Mode,
		execution.Phase,
		optionalString(execution.Reason),
		execution.PlanConfigHash,
		optionalString(execution.InputHash),
		optionalTime(execution.StartedAt),
		optionalTime(execution.CompletedAt),
		execution.DryRun,
		execution.Approved,
		approvalEvidence,
		revalidation,
		details,
	)
	if err != nil {
		return fmt.Errorf("record shutdown flow execution %q: %w", execution.ExecutionID, err)
	}
	return nil
}

func (s *SQLStore) RecordShutdownFlowExecutionWave(ctx context.Context, wave ShutdownFlowExecutionWave) error {
	if wave.WaveRecordID == "" || wave.ExecutionID == "" || wave.Phase == "" {
		return fmt.Errorf("shutdown flow execution wave requires wave record ID, execution ID, and phase")
	}
	groupNames, err := jsonArray(wave.GroupNames)
	if err != nil {
		return err
	}
	details, err := jsonObject(wave.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_execution_waves
(wave_record_id, execution_id, observed_at, wave_index, phase, started_at, completed_at, group_names, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb)
ON CONFLICT (execution_id, wave_index) DO UPDATE SET
  observed_at = EXCLUDED.observed_at,
  phase = EXCLUDED.phase,
  started_at = EXCLUDED.started_at,
  completed_at = EXCLUDED.completed_at,
  group_names = EXCLUDED.group_names,
  details = EXCLUDED.details`, s.quotedSchema),
		wave.WaveRecordID,
		wave.ExecutionID,
		observedAt(wave.ObservedAt),
		wave.WaveIndex,
		wave.Phase,
		optionalTime(wave.StartedAt),
		optionalTime(wave.CompletedAt),
		groupNames,
		details,
	)
	if err != nil {
		return fmt.Errorf("record shutdown flow execution wave %q: %w", wave.WaveRecordID, err)
	}
	return nil
}

func (s *SQLStore) RecordShutdownFlowExecutionGroup(ctx context.Context, group ShutdownFlowExecutionGroup) error {
	if group.GroupRecordID == "" || group.ExecutionID == "" || group.GroupName == "" || group.Action == "" || group.Phase == "" {
		return fmt.Errorf("shutdown flow execution group requires group record ID, execution ID, group, action, and phase")
	}
	selectedTargets, err := jsonArrayValue(group.SelectedTargets)
	if err != nil {
		return err
	}
	details, err := jsonObject(group.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_execution_groups
(group_record_id, execution_id, observed_at, wave_index, group_name, action, phase, started_at, completed_at, selected_targets, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb)
ON CONFLICT (execution_id, wave_index, group_name) DO UPDATE SET
  observed_at = EXCLUDED.observed_at,
  action = EXCLUDED.action,
  phase = EXCLUDED.phase,
  started_at = EXCLUDED.started_at,
  completed_at = EXCLUDED.completed_at,
  selected_targets = EXCLUDED.selected_targets,
  details = EXCLUDED.details`, s.quotedSchema),
		group.GroupRecordID,
		group.ExecutionID,
		observedAt(group.ObservedAt),
		group.WaveIndex,
		group.GroupName,
		group.Action,
		group.Phase,
		optionalTime(group.StartedAt),
		optionalTime(group.CompletedAt),
		selectedTargets,
		details,
	)
	if err != nil {
		return fmt.Errorf("record shutdown flow execution group %q: %w", group.GroupRecordID, err)
	}
	return nil
}

func (s *SQLStore) RecordShutdownFlowActionAttempt(ctx context.Context, attempt ShutdownFlowActionAttempt) error {
	if attempt.AttemptID == "" || attempt.ExecutionID == "" || attempt.Action == "" || attempt.Outcome == "" {
		return fmt.Errorf("shutdown flow action attempt requires attempt ID, execution ID, action, and outcome")
	}
	details, err := jsonObject(attempt.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.shutdownflow_action_attempts
(attempt_id, execution_id, observed_at, wave_index, group_name, action, target_kind, target_namespace, target_name, started_at, completed_at, outcome, error, dry_run, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb)`, s.quotedSchema),
		attempt.AttemptID,
		attempt.ExecutionID,
		observedAt(attempt.ObservedAt),
		optionalInt32(attempt.WaveIndex),
		optionalString(attempt.GroupName),
		attempt.Action,
		optionalString(attempt.TargetKind),
		optionalString(attempt.TargetNamespace),
		optionalString(attempt.TargetName),
		optionalTime(attempt.StartedAt),
		optionalTime(attempt.CompletedAt),
		attempt.Outcome,
		optionalString(attempt.Error),
		attempt.DryRun,
		details,
	)
	if err != nil {
		return fmt.Errorf("record shutdown flow action attempt %q: %w", attempt.AttemptID, err)
	}
	return nil
}

func (s *SQLStore) RecordNodeRelease(ctx context.Context, release NodeReleaseRecord) error {
	if release.ReleaseID == "" || release.ExecutionID == "" || release.NodeName == "" || release.Reason == "" {
		return fmt.Errorf("node release record requires release ID, execution ID, node name, and reason")
	}
	clearance, err := jsonObject(release.Clearance)
	if err != nil {
		return err
	}
	details, err := jsonObject(release.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.node_release_records
(release_id, execution_id, observed_at, node_name, node_power_agent, plan_config_hash, approved, released, reason, clearance, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11::jsonb)`, s.quotedSchema),
		release.ReleaseID,
		release.ExecutionID,
		observedAt(release.ObservedAt),
		release.NodeName,
		optionalString(release.NodePowerAgent),
		optionalString(release.PlanConfigHash),
		release.Approved,
		release.Released,
		release.Reason,
		clearance,
		details,
	)
	if err != nil {
		return fmt.Errorf("record node release %q: %w", release.ReleaseID, err)
	}
	return nil
}

func (s *SQLStore) RecordNodeSignalHandoff(ctx context.Context, handoff NodeSignalHandoff) error {
	if handoff.HandoffID == "" || handoff.ExecutionID == "" || handoff.NodeName == "" || handoff.Reason == "" {
		return fmt.Errorf("node signal handoff requires handoff ID, execution ID, node name, and reason")
	}
	signalPayload, err := jsonObject(handoff.SignalPayload)
	if err != nil {
		return err
	}
	details, err := jsonObject(handoff.Details)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.node_signal_handoffs
(handoff_id, execution_id, observed_at, node_name, node_power_agent, signal_path, signal_payload, stale_after, accepted, reason, details)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11::jsonb)`, s.quotedSchema),
		handoff.HandoffID,
		handoff.ExecutionID,
		observedAt(handoff.ObservedAt),
		handoff.NodeName,
		optionalString(handoff.NodePowerAgent),
		optionalString(handoff.SignalPath),
		signalPayload,
		optionalTime(handoff.StaleAfter),
		handoff.Accepted,
		handoff.Reason,
		details,
	)
	if err != nil {
		return fmt.Errorf("record node signal handoff %q: %w", handoff.HandoffID, err)
	}
	return nil
}

func (s *SQLStore) UpsertExecutorResumeState(ctx context.Context, state ExecutorResumeState) error {
	if state.ExecutionID == "" || state.ShutdownFlow == "" || state.PlanConfigHash == "" || state.Phase == "" {
		return fmt.Errorf("executor resume state requires execution ID, flow, plan config hash, and phase")
	}
	encodedState, err := jsonObject(state.State)
	if err != nil {
		return err
	}
	_, err = s.executor.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %[1]s.executor_resume_states
(execution_id, observed_at, shutdownflow, plan_config_hash, current_wave_index, phase, state, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, now())
ON CONFLICT (execution_id) DO UPDATE SET
  observed_at = EXCLUDED.observed_at,
  shutdownflow = EXCLUDED.shutdownflow,
  plan_config_hash = EXCLUDED.plan_config_hash,
  current_wave_index = EXCLUDED.current_wave_index,
  phase = EXCLUDED.phase,
  state = EXCLUDED.state,
  updated_at = now()`, s.quotedSchema),
		state.ExecutionID,
		observedAt(state.ObservedAt),
		state.ShutdownFlow,
		state.PlanConfigHash,
		optionalInt32(state.CurrentWaveIndex),
		state.Phase,
		encodedState,
	)
	if err != nil {
		return fmt.Errorf("upsert executor resume state %q: %w", state.ExecutionID, err)
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

func optionalInt32(value *int32) any {
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

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func jsonObject(value any) (string, error) {
	if isNilJSONValue(value) {
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
	if isNilJSONValue(value) {
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

func isNilJSONValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
