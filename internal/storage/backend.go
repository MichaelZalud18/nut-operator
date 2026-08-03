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

// Package storage resolves Kubernetes storage configuration into durable audit
// backend contracts. It does not import a database driver or assume CNPG is the
// only PostgreSQL provider.
package storage

import (
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

// BackendKind is the implementation family used by the audit store.
type BackendKind string

const (
	BackendDisabled   BackendKind = "Disabled"
	BackendPostgreSQL BackendKind = "PostgreSQL"
)

// Backend is the resolved storage contract used by controllers and future
// database connection wiring.
type Backend struct {
	Kind             BackendKind
	Source           powerv1alpha1.PowerStorageMode
	Schema           string
	Retention        audit.RetentionPolicy
	ExternalPostgres *ExternalPostgresBackend
	CNPG             *CNPGBackend
}

// ExternalPostgresBackend points at an existing PostgreSQL DSN Secret.
type ExternalPostgresBackend struct {
	DSNSecretKeyRef powerv1alpha1.SecretKeyReference
	RequireTLS      bool
}

// CNPGBackend records the CloudNativePG Cluster identity and application DB.
type CNPGBackend struct {
	ClusterRef             powerv1alpha1.NamespacedNameReference
	Database               string
	AppCredentialSecretRef *powerv1alpha1.NamespacedNameReference
}

// Resolve applies API defaults and validates the selected storage backend.
func Resolve(spec powerv1alpha1.PowerStorageSpec) (Backend, error) {
	mode := EffectiveMode(spec)
	retention, err := resolveRetention(spec.Retention)
	if err != nil {
		return Backend{}, err
	}
	switch mode {
	case powerv1alpha1.PowerStorageDisabled:
		return Backend{
			Kind:      BackendDisabled,
			Source:    powerv1alpha1.PowerStorageDisabled,
			Schema:    audit.DefaultSchema,
			Retention: retention,
		}, nil
	case powerv1alpha1.PowerStorageExternalPostgres:
		if spec.ExternalPostgres == nil {
			return Backend{}, fmt.Errorf("ExternalPostgres storage requires spec.storage.externalPostgres")
		}
		return Backend{
			Kind:      BackendPostgreSQL,
			Source:    powerv1alpha1.PowerStorageExternalPostgres,
			Schema:    defaultSchema(spec.ExternalPostgres.Schema),
			Retention: retention,
			ExternalPostgres: &ExternalPostgresBackend{
				DSNSecretKeyRef: spec.ExternalPostgres.DSNSecretKeyRef,
				RequireTLS:      spec.ExternalPostgres.RequireTLS == nil || *spec.ExternalPostgres.RequireTLS,
			},
		}, nil
	case powerv1alpha1.PowerStorageCNPG:
		if spec.CNPG == nil {
			return Backend{}, fmt.Errorf("CNPG storage requires spec.storage.cnpg")
		}
		return Backend{
			Kind:      BackendPostgreSQL,
			Source:    powerv1alpha1.PowerStorageCNPG,
			Schema:    defaultSchema(spec.CNPG.Schema),
			Retention: retention,
			CNPG: &CNPGBackend{
				ClusterRef:             spec.CNPG.ClusterRef,
				Database:               defaultDatabase(spec.CNPG.Database),
				AppCredentialSecretRef: spec.CNPG.AppCredentialSecretRef,
			},
		}, nil
	default:
		return Backend{}, fmt.Errorf("unsupported storage mode %q", spec.Mode)
	}
}

// EffectiveMode returns the API-defaulted storage mode.
func EffectiveMode(spec powerv1alpha1.PowerStorageSpec) powerv1alpha1.PowerStorageMode {
	if spec.Mode != "" {
		return spec.Mode
	}
	return powerv1alpha1.PowerStorageCNPG
}

// NewAuditStore creates the audit Store for a resolved backend and caller-owned
// SQL executor. PostgreSQL connection opening remains outside this package.
func NewAuditStore(backend Backend, executor audit.SQLExecutor) (audit.Store, error) {
	switch backend.Kind {
	case BackendDisabled:
		return audit.NoopStore{}, nil
	case BackendPostgreSQL:
		return audit.NewSQLStore(executor, audit.SQLStoreOptions{
			Schema:    backend.Schema,
			Retention: backend.Retention,
		})
	default:
		return nil, fmt.Errorf("unsupported resolved backend kind %q", backend.Kind)
	}
}

func resolveRetention(spec *powerv1alpha1.AuditRetentionSpec) (audit.RetentionPolicy, error) {
	if spec == nil {
		return audit.RetentionPolicy{}, nil
	}
	policy := audit.RetentionPolicy{}
	if spec.Events != nil {
		if spec.Events.Duration < 0 {
			return audit.RetentionPolicy{}, fmt.Errorf("spec.storage.retention.events must not be negative")
		}
		policy.Events = spec.Events.Duration
	}
	if spec.Telemetry != nil {
		if spec.Telemetry.Duration < 0 {
			return audit.RetentionPolicy{}, fmt.Errorf("spec.storage.retention.telemetry must not be negative")
		}
		policy.Telemetry = spec.Telemetry.Duration
	}
	return policy, nil
}

func defaultSchema(schema string) string {
	if schema == "" {
		return audit.DefaultSchema
	}
	return schema
}

func defaultDatabase(database string) string {
	if database == "" {
		return "power"
	}
	return database
}
