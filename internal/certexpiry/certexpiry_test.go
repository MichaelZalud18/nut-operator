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

package certexpiry

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/MichaelZalud18/nut-operator/internal/metrics"
)

// certPEM mints a self-signed certificate expiring at notAfter.
func certPEM(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    notAfter.Add(-24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestEarliestNotAfterReadsASingleCertificate(t *testing.T) {
	want := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second).UTC()

	got, err := EarliestNotAfter(certPEM(t, want))
	if err != nil {
		t.Fatalf("EarliestNotAfter: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("expiry = %s, want %s", got, want)
	}
}

// The bundle case is the reason this returns a minimum rather than the leaf: an intermediate that
// expires first takes the endpoint down on its date, not the leaf's.
//
// Asserted in both orders deliberately. With the soonest certificate last, "keep the last one" and
// "keep the minimum" agree, and a test written only that way passes against an implementation that
// does not compare at all.
func TestEarliestNotAfterReturnsTheSoonestInABundleRegardlessOfOrder(t *testing.T) {
	soon := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second).UTC()
	later := time.Now().Add(365 * 24 * time.Hour).Truncate(time.Second).UTC()
	soonPEM, laterPEM := certPEM(t, soon), certPEM(t, later)

	for name, bundle := range map[string][]byte{
		"soonest first": append(append([]byte(nil), soonPEM...), laterPEM...),
		"soonest last":  append(append([]byte(nil), laterPEM...), soonPEM...),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := EarliestNotAfter(bundle)
			if err != nil {
				t.Fatalf("EarliestNotAfter: %v", err)
			}
			if !got.Equal(soon) {
				t.Fatalf("expiry = %s, want the soonest %s", got, soon)
			}
		})
	}
}

func TestEarliestNotAfterSkipsNonCertificateBlocks(t *testing.T) {
	want := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second).UTC()
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not a certificate")})

	got, err := EarliestNotAfter(append(keyBlock, certPEM(t, want)...))
	if err != nil {
		t.Fatalf("EarliestNotAfter: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("expiry = %s, want %s", got, want)
	}
}

// A file with no certificate must be an error, not a zero time. A zero timestamp published as a
// gauge reads as "expired in 1970", and silently reporting nothing reads as "never expires" -- both
// answer the alert's question wrongly.
func TestEarliestNotAfterRejectsInputWithNoCertificate(t *testing.T) {
	for name, input := range map[string][]byte{
		"empty":       nil,
		"garbage":     []byte("not pem at all"),
		"key only":    pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("key")}),
		"bad der":     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not der")}),
		"empty block": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE"}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EarliestNotAfter(input); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

func TestNewReporterSkipsUnconfiguredPaths(t *testing.T) {
	reporter := NewReporter("", "tls.crt", "", "tls.crt")
	if len(reporter.Targets) != 0 {
		t.Fatalf("targets = %v, want none when no path is configured", reporter.Targets)
	}

	reporter = NewReporter("/webhook", "tls.crt", "", "tls.crt")
	if len(reporter.Targets) != 1 || reporter.Targets[0].Name != "webhook" {
		t.Fatalf("targets = %v, want only the webhook certificate", reporter.Targets)
	}
	if want := filepath.Join("/webhook", "tls.crt"); reporter.Targets[0].Path != want {
		t.Fatalf("path = %s, want %s", reporter.Targets[0].Path, want)
	}

	reporter = NewReporter("/webhook", "tls.crt", "/metrics", "tls.crt")
	if len(reporter.Targets) != 2 {
		t.Fatalf("targets = %v, want both certificates", reporter.Targets)
	}
}

// Start must publish before its first tick. With a 1h interval, waiting for the tick would leave the
// metric absent for an hour after every restart -- long enough for an absence alert to fire on a
// healthy manager.
func TestReporterPublishesImmediatelyOnStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tls.crt")
	want := time.Now().Add(200 * 24 * time.Hour).Truncate(time.Second)
	if err := os.WriteFile(path, certPEM(t, want), 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}

	metrics.CertificateNotAfterTimestampSeconds.Reset()
	reporter := &Reporter{Targets: []Target{{Name: "immediate", Path: path}}, Interval: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Start reports once before it ever consults the context.
	if err := reporter.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := testutil.ToFloat64(metrics.CertificateNotAfterTimestampSeconds.WithLabelValues("immediate"))
	if got != float64(want.Unix()) {
		t.Fatalf("gauge = %v, want %v", got, float64(want.Unix()))
	}
}

// A gauge left at its last good value would answer "expires in eight months" long after the file
// became unreadable. Deleting it makes the unknown visible as an absence plus an error count.
func TestReporterDeletesTheGaugeWhenTheCertificateBecomesUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tls.crt")
	if err := os.WriteFile(path, certPEM(t, time.Now().Add(48*time.Hour)), 0o600); err != nil {
		t.Fatalf("writing certificate: %v", err)
	}

	metrics.CertificateNotAfterTimestampSeconds.Reset()
	metrics.CertificateReadErrorsTotal.Reset()
	reporter := &Reporter{Targets: []Target{{Name: "stale", Path: path}}}

	reporter.report(context.Background())
	if count := testutil.CollectAndCount(metrics.CertificateNotAfterTimestampSeconds); count != 1 {
		t.Fatalf("gauge series = %d, want 1 after a good read", count)
	}

	if err := os.WriteFile(path, []byte("corrupted"), 0o600); err != nil {
		t.Fatalf("corrupting certificate: %v", err)
	}
	reporter.report(context.Background())

	if count := testutil.CollectAndCount(metrics.CertificateNotAfterTimestampSeconds); count != 0 {
		t.Fatalf("gauge series = %d, want 0 once the certificate is unreadable", count)
	}
	if errs := testutil.ToFloat64(metrics.CertificateReadErrorsTotal.WithLabelValues("stale")); errs != 1 {
		t.Fatalf("read errors = %v, want 1", errs)
	}
}

func TestReporterCountsAMissingFileAsAReadError(t *testing.T) {
	metrics.CertificateNotAfterTimestampSeconds.Reset()
	metrics.CertificateReadErrorsTotal.Reset()
	reporter := &Reporter{Targets: []Target{{Name: "absent", Path: filepath.Join(t.TempDir(), "nope.crt")}}}

	reporter.report(context.Background())

	if errs := testutil.ToFloat64(metrics.CertificateReadErrorsTotal.WithLabelValues("absent")); errs != 1 {
		t.Fatalf("read errors = %v, want 1", errs)
	}
	if count := testutil.CollectAndCount(metrics.CertificateNotAfterTimestampSeconds); count != 0 {
		t.Fatalf("gauge series = %d, want 0", count)
	}
}

// Without this the reporter would be gated behind leader election, so a standby replica serving a
// stale mount would publish nothing at all.
func TestReporterRunsWithoutLeaderElection(t *testing.T) {
	if (&Reporter{}).NeedLeaderElection() {
		t.Fatal("the expiry reporter must run on every replica, not only the leader")
	}
}

func TestStartIsANoOpWithNoTargets(t *testing.T) {
	if err := (&Reporter{}).Start(context.Background()); err != nil {
		t.Fatalf("Start with no targets: %v", err)
	}
}
