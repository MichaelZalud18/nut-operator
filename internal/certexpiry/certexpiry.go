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

// Package certexpiry publishes the expiry of the manager's mounted serving certificates as a
// metric.
//
// It exists for the no-cert-manager install path (docs/installation/README.md). That path is the recommended
// one precisely because a static Secret has nothing to reconcile while the cluster is losing power
// -- but the cost of having no issuer is that nothing renews the certificate either. Rotation is a
// deliberate `hack/webhook-cert.sh` re-run, and until now nothing anywhere reported that the
// deadline was approaching. A 1-year certificate makes that a once-a-year surprise, which is worse
// than a frequent one: nobody has the procedure in mind when it fires.
//
// The certificate is read from disk rather than from the Secret through the API. That is the file
// controller-runtime's certwatcher actually serves, so it stays correct if the mount is stale or the
// Secret is edited without the pod seeing it -- and reading it needs no RBAC and no cache.
package certexpiry

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/MichaelZalud18/nut-operator/internal/metrics"
)

// DefaultInterval is how often the mounted certificates are re-read.
//
// Deliberately slow. The value moves once per rotation, and the metric exists to give weeks of
// warning rather than to detect a change quickly.
const DefaultInterval = time.Hour

// EarliestNotAfter returns the soonest expiry among every certificate in a PEM file.
//
// The minimum rather than the leaf: if a bundle carries an intermediate or CA that expires first,
// the endpoint stops working on that date regardless of how long the leaf has left. With the single
// certificate hack/webhook-cert.sh writes, this is the leaf.
//
// Non-certificate PEM blocks are skipped rather than rejected, so a file that also carries a key
// still reports. A file with no certificate at all is an error: reporting nothing would be
// indistinguishable from a certificate that never expires.
func EarliestNotAfter(pemData []byte) (time.Time, error) {
	var earliest time.Time
	rest := pemData
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		parsed, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, fmt.Errorf("parsing certificate: %w", err)
		}
		if earliest.IsZero() || parsed.NotAfter.Before(earliest) {
			earliest = parsed.NotAfter
		}
	}
	if earliest.IsZero() {
		return time.Time{}, fmt.Errorf("no CERTIFICATE block found")
	}
	return earliest, nil
}

// Target names one certificate file to report on.
type Target struct {
	// Name is the metric label value, e.g. "webhook".
	Name string
	// Path is the certificate file on disk.
	Path string
}

// Reporter re-reads its targets on an interval and publishes their expiry.
//
// It implements manager.Runnable. It deliberately does not require leader election: each replica
// mounts its own copy of the Secret, and a replica whose mount has gone stale is exactly the
// condition worth seeing separately from its peers.
type Reporter struct {
	Targets  []Target
	Interval time.Duration
}

// NewReporter builds a Reporter for the certificate paths the manager was configured with. Empty
// paths are skipped, so a build with no webhook certificate configured simply reports nothing.
func NewReporter(webhookCertPath, webhookCertName, metricsCertPath, metricsCertName string) *Reporter {
	reporter := &Reporter{Interval: DefaultInterval}
	if webhookCertPath != "" {
		reporter.Targets = append(reporter.Targets, Target{
			Name: "webhook",
			Path: filepath.Join(webhookCertPath, webhookCertName),
		})
	}
	if metricsCertPath != "" {
		reporter.Targets = append(reporter.Targets, Target{
			Name: "metrics",
			Path: filepath.Join(metricsCertPath, metricsCertName),
		})
	}
	return reporter
}

// NeedLeaderElection reports false so every replica publishes its own mount's expiry.
func (r *Reporter) NeedLeaderElection() bool { return false }

// Start reports once immediately, then on the interval until the context is cancelled.
func (r *Reporter) Start(ctx context.Context) error {
	if len(r.Targets) == 0 {
		return nil
	}
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	r.report(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.report(ctx)
		}
	}
}

// report refreshes every target once.
//
// A target that cannot be read has its gauge deleted rather than left at its last value. A stale
// timestamp that still reads as "expires in eight months" is worse than no timestamp: it answers the
// alert's question wrongly. The error counter is what stays visible.
func (r *Reporter) report(ctx context.Context) {
	logger := log.FromContext(ctx).WithName("certexpiry")
	for _, target := range r.Targets {
		notAfter, err := readNotAfter(target.Path)
		if err != nil {
			metrics.CertificateNotAfterTimestampSeconds.DeleteLabelValues(target.Name)
			metrics.CertificateReadErrorsTotal.WithLabelValues(target.Name).Inc()
			logger.Error(err, "Failed to read serving certificate expiry",
				"certificate", target.Name, "path", target.Path)
			continue
		}
		metrics.CertificateNotAfterTimestampSeconds.
			WithLabelValues(target.Name).
			Set(float64(notAfter.Unix()))
	}
}

func readNotAfter(path string) (time.Time, error) {
	pemData, err := os.ReadFile(path) // #nosec G304 -- path comes from the manager's own flags.
	if err != nil {
		return time.Time{}, fmt.Errorf("reading %s: %w", path, err)
	}
	return EarliestNotAfter(pemData)
}
