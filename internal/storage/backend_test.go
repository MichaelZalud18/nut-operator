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

package storage

import (
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

func TestResolveDisabledStorage(t *testing.T) {
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{Mode: powerv1alpha1.PowerStorageDisabled})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if backend.Kind != BackendDisabled {
		t.Fatalf("expected disabled backend, got %q", backend.Kind)
	}

	store, err := NewAuditStore(backend, nil)
	if err != nil {
		t.Fatalf("NewAuditStore returned error: %v", err)
	}
	if _, ok := store.(audit.NoopStore); !ok {
		t.Fatalf("expected audit.NoopStore, got %T", store)
	}
}

func TestResolveExternalPostgresStorage(t *testing.T) {
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageExternalPostgres,
		ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
			DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
				Namespace: "power-system",
				Name:      "power-postgres",
				Key:       "dsn",
			},
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if backend.Kind != BackendPostgreSQL {
		t.Fatalf("expected PostgreSQL backend, got %q", backend.Kind)
	}
	if backend.Source != powerv1alpha1.PowerStorageExternalPostgres {
		t.Fatalf("expected ExternalPostgres source, got %q", backend.Source)
	}
	if backend.Schema != audit.DefaultSchema {
		t.Fatalf("expected default schema, got %q", backend.Schema)
	}
	if backend.ExternalPostgres == nil || !backend.ExternalPostgres.RequireTLS {
		t.Fatalf("expected TLS-required external PostgreSQL backend, got %#v", backend.ExternalPostgres)
	}
}

func TestResolveAuditSpool(t *testing.T) {
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageExternalPostgres,
		ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
			DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
				Namespace: "power-system",
				Name:      "power-postgres",
				Key:       "dsn",
			},
		},
		AuditSpool: powerv1alpha1.AuditSpoolSpec{
			Enabled: true,
			Path:    "/var/lib/nut-operator/audit-spool/../audit-spool",
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !backend.AuditSpool.Enabled || backend.AuditSpool.Path != "/var/lib/nut-operator/audit-spool" {
		t.Fatalf("expected clean enabled audit spool, got %#v", backend.AuditSpool)
	}
}

func TestResolveCNPGStorage(t *testing.T) {
	eventRetention := metav1.Duration{Duration: 90 * 24 * time.Hour}
	telemetryRetention := metav1.Duration{Duration: 14 * 24 * time.Hour}
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageCNPG,
		CNPG: &powerv1alpha1.CNPGStorageSpec{
			ClusterRef: powerv1alpha1.NamespacedNameReference{
				Namespace: "power-system",
				Name:      "power-audit",
			},
			Schema: "audit",
		},
		Retention: &powerv1alpha1.AuditRetentionSpec{
			Events:    &eventRetention,
			Telemetry: &telemetryRetention,
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if backend.Kind != BackendPostgreSQL {
		t.Fatalf("expected PostgreSQL backend, got %q", backend.Kind)
	}
	if backend.Source != powerv1alpha1.PowerStorageCNPG {
		t.Fatalf("expected CNPG source, got %q", backend.Source)
	}
	if backend.Schema != "audit" {
		t.Fatalf("expected custom schema, got %q", backend.Schema)
	}
	if backend.CNPG == nil || backend.CNPG.Database != "power" {
		t.Fatalf("expected CNPG backend with default database, got %#v", backend.CNPG)
	}
	if backend.Retention.Events != 90*24*time.Hour || backend.Retention.Telemetry != 14*24*time.Hour {
		t.Fatalf("expected retention policy to resolve, got %#v", backend.Retention)
	}
}

func TestResolveRejectsIncompleteStorage(t *testing.T) {
	if _, err := Resolve(powerv1alpha1.PowerStorageSpec{Mode: powerv1alpha1.PowerStorageCNPG}); err == nil {
		t.Fatal("expected CNPG without config to be rejected")
	}
	if _, err := Resolve(powerv1alpha1.PowerStorageSpec{Mode: powerv1alpha1.PowerStorageExternalPostgres}); err == nil {
		t.Fatal("expected ExternalPostgres without config to be rejected")
	}
	if _, err := Resolve(powerv1alpha1.PowerStorageSpec{Mode: powerv1alpha1.PowerStorageMode("SQLite")}); err == nil {
		t.Fatal("expected unsupported storage mode to be rejected")
	}
	negative := metav1.Duration{Duration: -time.Minute}
	if _, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageDisabled,
		Retention: &powerv1alpha1.AuditRetentionSpec{
			Events: &negative,
		},
	}); err == nil {
		t.Fatal("expected negative retention to be rejected")
	}
	if _, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageDisabled,
		AuditSpool: powerv1alpha1.AuditSpoolSpec{
			Enabled: true,
			Path:    "/var/lib/nut-operator/audit-spool",
		},
	}); err == nil {
		t.Fatal("expected audit spool with disabled storage to be rejected")
	}
	if _, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageExternalPostgres,
		ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
			DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
				Namespace: "power-system",
				Name:      "power-postgres",
				Key:       "dsn",
			},
		},
		AuditSpool: powerv1alpha1.AuditSpoolSpec{
			Enabled: true,
			Path:    "relative/path",
		},
	}); err == nil {
		t.Fatal("expected relative audit spool path to be rejected")
	}
}

// spoolSpec builds an ExternalPostgres storage spec with the audit spool
// enabled, so the cap tests below differ only in the field under test.
func spoolSpec(maxSize *resource.Quantity) powerv1alpha1.PowerStorageSpec {
	return powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageExternalPostgres,
		ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
			DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
				Namespace: "power-system",
				Name:      "power-postgres",
				Key:       "dsn",
			},
		},
		AuditSpool: powerv1alpha1.AuditSpoolSpec{
			Enabled: true,
			Path:    "/var/lib/nut-operator/audit-spool",
			MaxSize: maxSize,
		},
	}
}

func TestResolveAuditSpoolDefaultsItsCap(t *testing.T) {
	backend, err := Resolve(spoolSpec(nil))
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if backend.AuditSpool.MaxBytes != DefaultAuditSpoolMaxBytes {
		t.Fatalf("expected the default spool cap, got %d", backend.AuditSpool.MaxBytes)
	}
}

func TestResolveAuditSpoolAcceptsAQuantity(t *testing.T) {
	backend, err := Resolve(spoolSpec(resource.NewQuantity(256<<20, resource.BinarySI)))
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if backend.AuditSpool.MaxBytes != 256<<20 {
		t.Fatalf("expected a 256Mi spool cap, got %d", backend.AuditSpool.MaxBytes)
	}
}

// A cap below one record drops every write, which is a disabled spool wearing an
// enabled spool's configuration. Reject it while the user can still see the
// error, not during the outage it was meant to survive.
func TestResolveAuditSpoolRejectsACapSmallerThanARecord(t *testing.T) {
	_, err := Resolve(spoolSpec(resource.NewQuantity(4096, resource.BinarySI)))
	if err == nil || !strings.Contains(err.Error(), "maxSize") {
		t.Fatalf("expected an undersized spool cap rejection, got %v", err)
	}
}
