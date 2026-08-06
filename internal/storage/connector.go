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
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	DriverName          = "pgx"
	CNPGFQDNURIKey      = "fqdn-uri"
	CNPGURIKey          = "uri"
	defaultPingTimeout  = 5 * time.Second
	defaultMaxOpenConns = 4
	defaultMaxIdleConns = 2

	// A pooled connection is retired after this long even when healthy.
	//
	// This matters specifically because the recommended backing store is CNPG.
	// A CNPG failover or switchover moves the primary to a different pod, and
	// the rw Service follows it -- but an already-established TCP connection
	// does not. Without a lifetime cap, idle pooled connections can stay bound
	// to a demoted instance and fail writes long after the cluster recovered.
	// A bounded lifetime forces periodic re-resolution through the Service.
	defaultConnMaxLifetime = 30 * time.Minute
	// Idle connections are dropped sooner, so a pool that goes quiet after an
	// event does not hold sessions open against the database for half an hour.
	defaultConnMaxIdleTime = 5 * time.Minute
)

var sslModePattern = regexp.MustCompile(`(?:^|\s)sslmode\s*=\s*'?([^'\s]+)'?`)

// AuditStoreConnector opens the audit store described by a PowerManagementCluster.
type AuditStoreConnector interface {
	OpenAuditStore(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster) (audit.Store, error)
}

// Database is the subset of database/sql.DB required by the audit connector.
type Database interface {
	audit.SQLExecutor
	PingContext(ctx context.Context) error
	Close() error
}

// SQLOpener opens a PostgreSQL database handle from a DSN.
type SQLOpener func(driverName, dsn string) (Database, error)

// ConnectorOptions configure Kubernetes-backed audit store construction.
type ConnectorOptions struct {
	Open            SQLOpener
	PingTimeout     time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// KubernetesConnector resolves storage Secrets with the Kubernetes API and
// opens PostgreSQL-backed audit stores with caller-owned connection settings.
type KubernetesConnector struct {
	client client.Client
	open   SQLOpener
	opts   ConnectorOptions
}

// NewKubernetesConnector creates the production storage connector.
func NewKubernetesConnector(k8sClient client.Client, opts ConnectorOptions) *KubernetesConnector {
	open := opts.Open
	if open == nil {
		open = defaultSQLOpen
	}
	return &KubernetesConnector{
		client: k8sClient,
		open:   open,
		opts:   opts,
	}
}

// OpenAuditStore resolves storage, pings PostgreSQL when configured, applies
// migrations, and returns an audit store that owns the opened database handle.
func (c *KubernetesConnector) OpenAuditStore(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster) (audit.Store, error) {
	if cluster == nil {
		return nil, fmt.Errorf("PowerManagementCluster is required")
	}
	backend, err := Resolve(cluster.Spec.Storage)
	if err != nil {
		return nil, err
	}
	if backend.Kind == BackendDisabled {
		return audit.NoopStore{}, nil
	}
	dsn, err := c.ResolveDSN(ctx, backend)
	if err != nil {
		return nil, err
	}
	db, err := c.open(DriverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open audit PostgreSQL connection: %w", err)
	}
	configureSQLDB(db, c.opts)
	if err := ping(ctx, db, c.opts.PingTimeout); err != nil {
		closeErr := db.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("audit PostgreSQL ping failed and close failed: %w", errors.Join(err, closeErr))
		}
		return nil, fmt.Errorf("audit PostgreSQL ping failed: %w", err)
	}
	store, err := NewAuditStore(backend, db)
	if err != nil {
		closeErr := db.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("create audit store failed and close failed: %w", errors.Join(err, closeErr))
		}
		return nil, err
	}
	owned := &ownedStore{
		Store:  store,
		closer: db,
	}
	if err := owned.EnsureSchema(ctx); err != nil {
		closeErr := owned.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("ensure audit schema failed and close failed: %w", errors.Join(err, closeErr))
		}
		return nil, err
	}
	return owned, nil
}

type ownedStore struct {
	audit.Store
	closer interface {
		Close() error
	}
}

func (s *ownedStore) Close() error {
	storeErr := s.Store.Close()
	closeErr := s.closer.Close()
	if storeErr != nil || closeErr != nil {
		return errors.Join(storeErr, closeErr)
	}
	return nil
}

// ResolveDSN reads the Kubernetes Secret containing the backend DSN.
func (c *KubernetesConnector) ResolveDSN(ctx context.Context, backend Backend) (string, error) {
	if c.client == nil {
		return "", fmt.Errorf("kubernetes client is required to resolve storage Secrets")
	}
	switch backend.Source {
	case powerv1alpha1.PowerStorageExternalPostgres:
		if backend.ExternalPostgres == nil {
			return "", fmt.Errorf("external PostgreSQL backend is not configured")
		}
		dsn, err := c.readSecretKey(ctx, backend.ExternalPostgres.DSNSecretKeyRef)
		if err != nil {
			return "", err
		}
		if backend.ExternalPostgres.RequireTLS {
			if err := requireSecurePostgresDSN(dsn); err != nil {
				return "", err
			}
		}
		return dsn, nil
	case powerv1alpha1.PowerStorageCNPG:
		if backend.CNPG == nil {
			return "", fmt.Errorf("CNPG backend is not configured")
		}
		return c.readCNPGApplicationURI(ctx, *backend.CNPG)
	default:
		return "", fmt.Errorf("storage backend %q does not expose a PostgreSQL DSN", backend.Source)
	}
}

func (c *KubernetesConnector) readCNPGApplicationURI(ctx context.Context, backend CNPGBackend) (string, error) {
	ref := cnpgApplicationSecretRef(backend)
	secret, err := c.readSecret(ctx, ref.Namespace, ref.Name)
	if err != nil {
		return "", err
	}
	if value := strings.TrimSpace(string(secret.Data[CNPGFQDNURIKey])); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(string(secret.Data[CNPGURIKey])); value != "" {
		return value, nil
	}
	return "", fmt.Errorf("storage Secret %s/%s must contain %q or %q", ref.Namespace, ref.Name, CNPGFQDNURIKey, CNPGURIKey)
}

func (c *KubernetesConnector) readSecretKey(ctx context.Context, ref powerv1alpha1.SecretKeyReference) (string, error) {
	secret, err := c.readSecret(ctx, ref.Namespace, ref.Name)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(secret.Data[ref.Key]))
	if value == "" {
		return "", fmt.Errorf("storage Secret %s/%s key %q is empty or missing", ref.Namespace, ref.Name, ref.Key)
	}
	return value, nil
}

func (c *KubernetesConnector) readSecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	var secret corev1.Secret
	if err := c.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &secret); err != nil {
		return nil, fmt.Errorf("read storage Secret %s/%s: %w", namespace, name, err)
	}
	return &secret, nil
}

func cnpgApplicationSecretRef(backend CNPGBackend) powerv1alpha1.NamespacedNameReference {
	if backend.AppCredentialSecretRef != nil {
		return *backend.AppCredentialSecretRef
	}
	return powerv1alpha1.NamespacedNameReference{
		Namespace: backend.ClusterRef.Namespace,
		Name:      backend.ClusterRef.Name + "-app",
	}
}

func requireSecurePostgresDSN(dsn string) error {
	sslmode, ok := postgresSSLMode(dsn)
	if !ok {
		return fmt.Errorf("external PostgreSQL DSN must set sslmode=require, verify-ca, or verify-full")
	}
	switch strings.ToLower(sslmode) {
	case "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("external PostgreSQL DSN uses insecure sslmode %q", sslmode)
	}
}

func postgresSSLMode(dsn string) (string, bool) {
	trimmed := strings.TrimSpace(dsn)
	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", false
		}
		value := parsed.Query().Get("sslmode")
		return value, value != ""
	}
	match := sslModePattern.FindStringSubmatch(trimmed)
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func defaultSQLOpen(driverName, dsn string) (Database, error) {
	return sql.Open(driverName, dsn)
}

func configureSQLDB(db Database, opts ConnectorOptions) {
	sqlDB, ok := db.(*sql.DB)
	if !ok {
		return
	}
	maxOpen := opts.MaxOpenConns
	if maxOpen == 0 {
		maxOpen = defaultMaxOpenConns
	}
	maxIdle := opts.MaxIdleConns
	if maxIdle == 0 {
		maxIdle = defaultMaxIdleConns
	}
	maxLifetime := opts.ConnMaxLifetime
	if maxLifetime == 0 {
		maxLifetime = defaultConnMaxLifetime
	}
	maxIdleTime := opts.ConnMaxIdleTime
	if maxIdleTime == 0 {
		maxIdleTime = defaultConnMaxIdleTime
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(maxLifetime)
	sqlDB.SetConnMaxIdleTime(maxIdleTime)
}

func ping(ctx context.Context, db Database, timeout time.Duration) error {
	if timeout == 0 {
		timeout = defaultPingTimeout
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}
