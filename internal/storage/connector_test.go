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
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

type connectorFakeDB struct {
	execCalls int
	pingErr   error
	closeErr  error
	closed    bool
}

func (db *connectorFakeDB) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	db.execCalls++
	return connectorFakeResult(0), nil
}

func (db *connectorFakeDB) PingContext(context.Context) error {
	return db.pingErr
}

func (db *connectorFakeDB) Close() error {
	db.closed = true
	return db.closeErr
}

type connectorFakeResult int64

func (r connectorFakeResult) LastInsertId() (int64, error) {
	return int64(r), nil
}

func (r connectorFakeResult) RowsAffected() (int64, error) {
	return int64(r), nil
}

func TestKubernetesConnectorResolvesExternalPostgresDSN(t *testing.T) {
	connector := newTestConnector(t, []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "postgres-dsn"},
			Data: map[string][]byte{
				"dsn": []byte("postgres://power:example@postgres.example.net:5432/power?sslmode=verify-full"),
			},
		},
	}, nil)
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageExternalPostgres,
		ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
			DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
				Namespace: "power-system",
				Name:      "postgres-dsn",
				Key:       "dsn",
			},
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	dsn, err := connector.ResolveDSN(context.Background(), backend)
	if err != nil {
		t.Fatalf("ResolveDSN returned error: %v", err)
	}
	if !strings.Contains(dsn, "sslmode=verify-full") {
		t.Fatalf("expected secure DSN, got %q", dsn)
	}
}

func TestKubernetesConnectorRejectsExternalPostgresDSNWithoutTLS(t *testing.T) {
	connector := newTestConnector(t, []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "postgres-dsn"},
			Data: map[string][]byte{
				"dsn": []byte("postgres://power:example@postgres.example.net:5432/power?sslmode=disable"),
			},
		},
	}, nil)
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageExternalPostgres,
		ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
			DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
				Namespace: "power-system",
				Name:      "postgres-dsn",
				Key:       "dsn",
			},
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	_, err = connector.ResolveDSN(context.Background(), backend)
	if err == nil || !strings.Contains(err.Error(), "insecure sslmode") {
		t.Fatalf("expected insecure sslmode rejection, got %v", err)
	}
}

func TestKubernetesConnectorResolvesCNPGFQDNURI(t *testing.T) {
	connector := newTestConnector(t, []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "power-audit-app"},
			Data: map[string][]byte{
				CNPGURIKey:     []byte("postgres://power:example@power-audit-rw:5432/power"),
				CNPGFQDNURIKey: []byte("postgres://power:example@power-audit-rw.power-system.svc:5432/power"),
			},
		},
	}, nil)
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageCNPG,
		CNPG: &powerv1alpha1.CNPGStorageSpec{
			ClusterRef: powerv1alpha1.NamespacedNameReference{
				Namespace: "power-system",
				Name:      "power-audit",
			},
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	dsn, err := connector.ResolveDSN(context.Background(), backend)
	if err != nil {
		t.Fatalf("ResolveDSN returned error: %v", err)
	}
	if !strings.Contains(dsn, "power-audit-rw.power-system.svc") {
		t.Fatalf("expected FQDN URI, got %q", dsn)
	}
}

func TestKubernetesConnectorUsesExplicitCNPGCredentialSecret(t *testing.T) {
	connector := newTestConnector(t, []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "data", Name: "custom-power-app"},
			Data: map[string][]byte{
				CNPGURIKey: []byte("postgres://power:example@custom-rw:5432/power"),
			},
		},
	}, nil)
	backend, err := Resolve(powerv1alpha1.PowerStorageSpec{
		Mode: powerv1alpha1.PowerStorageCNPG,
		CNPG: &powerv1alpha1.CNPGStorageSpec{
			ClusterRef: powerv1alpha1.NamespacedNameReference{
				Namespace: "data",
				Name:      "power-audit",
			},
			AppCredentialSecretRef: &powerv1alpha1.NamespacedNameReference{
				Namespace: "data",
				Name:      "custom-power-app",
			},
		},
	})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	dsn, err := connector.ResolveDSN(context.Background(), backend)
	if err != nil {
		t.Fatalf("ResolveDSN returned error: %v", err)
	}
	if !strings.Contains(dsn, "custom-rw") {
		t.Fatalf("expected explicit app credential URI, got %q", dsn)
	}
}

func TestKubernetesConnectorOpensPingsMigratesAndOwnsStore(t *testing.T) {
	db := &connectorFakeDB{}
	var openedDSN string
	connector := newTestConnector(t, []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "postgres-dsn"},
			Data: map[string][]byte{
				"dsn": []byte("user=power host=postgres.example.net port=5432 dbname=power sslmode=require"),
			},
		},
	}, func(_ string, dsn string) (Database, error) {
		openedDSN = dsn
		return db, nil
	})
	cluster := &powerv1alpha1.PowerManagementCluster{
		Spec: powerv1alpha1.PowerManagementClusterSpec{
			Storage: powerv1alpha1.PowerStorageSpec{
				Mode: powerv1alpha1.PowerStorageExternalPostgres,
				ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
					DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
						Namespace: "power-system",
						Name:      "postgres-dsn",
						Key:       "dsn",
					},
					Schema: "power",
				},
			},
		},
	}

	store, err := connector.OpenAuditStore(context.Background(), cluster)
	if err != nil {
		t.Fatalf("OpenAuditStore returned error: %v", err)
	}
	if !strings.Contains(openedDSN, "sslmode=require") {
		t.Fatalf("expected opener to receive DSN, got %q", openedDSN)
	}
	if db.execCalls != audit.CurrentSchemaVersion {
		t.Fatalf("expected %d migrations to be applied, got %d calls", audit.CurrentSchemaVersion, db.execCalls)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !db.closed {
		t.Fatal("expected returned store to close the database handle")
	}
}

func TestKubernetesConnectorClosesDBAfterPingFailure(t *testing.T) {
	db := &connectorFakeDB{pingErr: errors.New("database unavailable")}
	connector := newTestConnector(t, []runtime.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "postgres-dsn"},
			Data: map[string][]byte{
				"dsn": []byte("postgres://power:example@postgres.example.net:5432/power?sslmode=require"),
			},
		},
	}, func(string, string) (Database, error) {
		return db, nil
	})
	cluster := &powerv1alpha1.PowerManagementCluster{
		Spec: powerv1alpha1.PowerManagementClusterSpec{
			Storage: powerv1alpha1.PowerStorageSpec{
				Mode: powerv1alpha1.PowerStorageExternalPostgres,
				ExternalPostgres: &powerv1alpha1.ExternalPostgresStorageSpec{
					DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
						Namespace: "power-system",
						Name:      "postgres-dsn",
						Key:       "dsn",
					},
				},
			},
		},
	}

	_, err := connector.OpenAuditStore(context.Background(), cluster)
	if err == nil || !strings.Contains(err.Error(), "ping failed") {
		t.Fatalf("expected ping failure, got %v", err)
	}
	if !db.closed {
		t.Fatal("expected ping failure to close the database handle")
	}
}

func newTestConnector(t *testing.T, objects []runtime.Object, opener SQLOpener) *KubernetesConnector {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objects) > 0 {
		builder.WithRuntimeObjects(objects...)
	}
	return NewKubernetesConnector(builder.Build(), ConnectorOptions{Open: opener})
}
