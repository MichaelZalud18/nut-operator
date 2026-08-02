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

// Package polling composes read-only NUT variable polling with telemetry
// normalization. It deliberately avoids Kubernetes client usage so controller
// code can adapt API resources without owning protocol details.
package polling

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/nut"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

// VariableClient is the NUT variable transport required by the poller.
type VariableClient interface {
	ListVariables(ctx context.Context, target nut.Target) (map[string]string, error)
}

// Clock returns the current observation time.
type Clock func() time.Time

// Target identifies one UPS exposed by one NUT server endpoint.
type Target struct {
	UPSDevice string
	NUTServer string
	NUTName   string
	Host      string
	Port      int
}

// Options configure a telemetry poller.
type Options struct {
	Client VariableClient
	Clock  Clock
}

// Poller fetches NUT variables and normalizes them into policy-ready facts.
type Poller struct {
	client VariableClient
	clock  Clock
}

// Result is one successful telemetry poll.
type Result struct {
	Target   Target
	Snapshot telemetry.Snapshot
}

// NewPoller returns a telemetry poller with production defaults.
func NewPoller(options Options) *Poller {
	client := options.Client
	if client == nil {
		client = nut.NewClient(nut.ClientOptions{})
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Poller{
		client: client,
		clock:  clock,
	}
}

// Poll reads all exposed variables for target and returns a normalized
// telemetry snapshot. It only issues NUT LIST VAR through the configured client.
func (p *Poller) Poll(ctx context.Context, target Target) (Result, error) {
	target = normalizeTarget(target)
	if err := validateTarget(target); err != nil {
		return Result{Target: target}, err
	}

	variables, err := p.client.ListVariables(ctx, nut.Target{
		Host:    target.Host,
		Port:    target.Port,
		UPSName: target.NUTName,
	})
	if err != nil {
		return Result{Target: target}, fmt.Errorf("poll NUT variables for UPSDevice %q through NUTServer %q: %w", target.UPSDevice, target.NUTServer, err)
	}

	snapshot := telemetry.Normalize(telemetry.Sample{
		UPSDevice:  target.UPSDevice,
		NUTServer:  target.NUTServer,
		NUTName:    target.NUTName,
		ObservedAt: p.now(),
		Variables:  variables,
	})
	return Result{
		Target:   target,
		Snapshot: snapshot,
	}, nil
}

// AuditSnapshot adapts the polling result into the durable audit-store shape.
func (r Result) AuditSnapshot(snapshotID string) audit.TelemetrySnapshot {
	return telemetry.AuditSnapshot(snapshotID, r.Snapshot)
}

func (p *Poller) now() time.Time {
	if p.clock == nil {
		return time.Now().UTC()
	}
	return p.clock().UTC()
}

func normalizeTarget(target Target) Target {
	target.UPSDevice = strings.TrimSpace(target.UPSDevice)
	target.NUTServer = strings.TrimSpace(target.NUTServer)
	target.NUTName = strings.TrimSpace(target.NUTName)
	target.Host = strings.TrimSpace(target.Host)
	if target.Port == 0 {
		target.Port = nut.DefaultPort
	}
	return target
}

func validateTarget(target Target) error {
	switch {
	case target.UPSDevice == "":
		return errors.New("poll target UPS device is required")
	case target.NUTServer == "":
		return errors.New("poll target NUT server is required")
	case target.NUTName == "":
		return errors.New("poll target NUT name is required")
	case target.Host == "":
		return errors.New("poll target host is required")
	case target.Port < 0 || target.Port > 65535:
		return fmt.Errorf("poll target port %d is outside TCP port range", target.Port)
	}
	return nil
}
