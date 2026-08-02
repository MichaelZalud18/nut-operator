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

package polling

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nut"
)

func TestPollerPollsAndNormalizesTelemetry(t *testing.T) {
	observedAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	client := &fakeVariableClient{
		variables: map[string]string{
			"ups.status":      "OB LB",
			"battery.charge":  "18.5",
			"battery.runtime": "90",
			"ups.load":        "44",
		},
	}
	poller := NewPoller(Options{
		Client: client,
		Clock:  func() time.Time { return observedAt },
	})

	result, err := poller.Poll(context.Background(), Target{
		UPSDevice: " rack-a ",
		NUTServer: " server-a ",
		NUTName:   " rack-a ",
		Host:      " upsd.power-system.svc.cluster.local ",
	})
	if err != nil {
		t.Fatalf("Poll returned error: %v", err)
	}

	if client.calls != 1 {
		t.Fatalf("expected one NUT client call, got %d", client.calls)
	}
	if client.target.Host != "upsd.power-system.svc.cluster.local" || client.target.Port != nut.DefaultPort || client.target.UPSName != "rack-a" {
		t.Fatalf("unexpected NUT client target: %#v", client.target)
	}
	if result.Target.UPSDevice != "rack-a" || result.Target.NUTServer != "server-a" {
		t.Fatalf("expected poll target to be normalized, got %#v", result.Target)
	}
	if result.Snapshot.Phase != "LowBattery" || !result.Snapshot.OnBattery || !result.Snapshot.LowBattery {
		t.Fatalf("expected low-battery on-battery snapshot, got %#v", result.Snapshot)
	}
	if result.Snapshot.ObservedAt.Location() != time.UTC {
		t.Fatalf("expected observedAt to be UTC, got %s", result.Snapshot.ObservedAt.Location())
	}

	auditSnapshot := result.AuditSnapshot("snapshot-1")
	if auditSnapshot.SnapshotID != "snapshot-1" || auditSnapshot.UPSDevice != "rack-a" || auditSnapshot.NUTServer != "server-a" {
		t.Fatalf("unexpected audit snapshot: %#v", auditSnapshot)
	}
	if auditSnapshot.BatteryChargePercent == nil || *auditSnapshot.BatteryChargePercent != 18.5 {
		t.Fatalf("expected audit battery charge to be preserved, got %#v", auditSnapshot.BatteryChargePercent)
	}
}

func TestPollerReturnsClientErrors(t *testing.T) {
	poller := NewPoller(Options{
		Client: &fakeVariableClient{err: errors.New("dial failed")},
	})

	_, err := poller.Poll(context.Background(), Target{
		UPSDevice: "rack-a",
		NUTServer: "server-a",
		NUTName:   "rack-a",
		Host:      "upsd.power-system.svc.cluster.local",
	})
	if err == nil || !strings.Contains(err.Error(), "dial failed") || !strings.Contains(err.Error(), "UPSDevice \"rack-a\"") {
		t.Fatalf("expected wrapped client error, got %v", err)
	}
}

func TestPollerValidatesTargets(t *testing.T) {
	client := &fakeVariableClient{}
	poller := NewPoller(Options{Client: client})

	tests := []Target{
		{NUTServer: "server-a", NUTName: "rack-a", Host: "upsd.power-system.svc.cluster.local"},
		{UPSDevice: "rack-a", NUTName: "rack-a", Host: "upsd.power-system.svc.cluster.local"},
		{UPSDevice: "rack-a", NUTServer: "server-a", Host: "upsd.power-system.svc.cluster.local"},
		{UPSDevice: "rack-a", NUTServer: "server-a", NUTName: "rack-a"},
		{UPSDevice: "rack-a", NUTServer: "server-a", NUTName: "rack-a", Host: "upsd.power-system.svc.cluster.local", Port: 70000},
	}

	for _, target := range tests {
		if _, err := poller.Poll(context.Background(), target); err == nil {
			t.Fatalf("expected target %#v to be rejected", target)
		}
	}
	if client.calls != 0 {
		t.Fatalf("expected invalid targets not to reach NUT client, got %d calls", client.calls)
	}
}

type fakeVariableClient struct {
	target    nut.Target
	variables map[string]string
	err       error
	calls     int
}

func (c *fakeVariableClient) ListVariables(_ context.Context, target nut.Target) (map[string]string, error) {
	c.calls++
	c.target = target
	if c.err != nil {
		return nil, c.err
	}
	return c.variables, nil
}
