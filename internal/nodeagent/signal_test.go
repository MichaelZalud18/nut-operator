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

package nodeagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectSignalAcceptsFreshStructuredSignal(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	path := writeSignal(t, `{
		"dryRun": true,
		"executionID": "exec-1",
		"nodeName": "node-a",
		"planConfigHash": "hash-1",
		"reason": "testing",
		"selectedUPSDevices": ["ups-a"],
		"shutdownFlow": "flow-a",
		"timestamp": "`+now.Format(time.RFC3339Nano)+`"
	}`)

	status := InspectSignal(path, 2*time.Minute, now.Add(30*time.Second), "node-a")

	if !status.Active {
		t.Fatalf("expected active signal, got reason %q", status.Reason)
	}
	if status.Reason != "SignalAccepted" {
		t.Fatalf("expected SignalAccepted, got %q", status.Reason)
	}
	if status.Key == "" {
		t.Fatal("expected stable signal key")
	}
	if status.Path != path {
		t.Fatalf("expected status path %q, got %q", path, status.Path)
	}
	if status.Payload.ExecutionID != "exec-1" {
		t.Fatalf("expected payload executionID exec-1, got %q", status.Payload.ExecutionID)
	}
}

func TestInspectSignalRejectsStaleWrongNodeAndInvalidJSON(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	validPayload := `{
		"dryRun": false,
		"executionID": "exec-1",
		"nodeName": "node-a",
		"planConfigHash": "hash-1",
		"shutdownFlow": "flow-a",
		"timestamp": "` + now.Format(time.RFC3339Nano) + `"
	}`

	if status := InspectSignal(writeSignal(t, validPayload), 2*time.Minute, now.Add(3*time.Minute), "node-a"); status.Reason != "SignalStale" {
		t.Fatalf("expected SignalStale, got %q", status.Reason)
	}
	if status := InspectSignal(writeSignal(t, validPayload), 2*time.Minute, now, "node-b"); status.Reason != "SignalWrongNode" {
		t.Fatalf("expected SignalWrongNode, got %q", status.Reason)
	}
	if status := InspectSignal(writeSignal(t, `{`), 2*time.Minute, now, "node-a"); status.Reason != "SignalInvalidJSON" {
		t.Fatalf("expected SignalInvalidJSON, got %q", status.Reason)
	}
}

func TestWriteSignalAtomicPublishesStructuredJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "shutdown.json")
	payload := ShutdownSignal{
		ExecutionID:        "exec-1",
		NodeName:           "node-a",
		PlanConfigHash:     "hash-1",
		Reason:             "testing",
		SelectedUPSDevices: []string{"ups-a"},
		ShutdownFlow:       "flow-a",
		Timestamp:          time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}

	if err := WriteSignalAtomic(path, payload); err != nil {
		t.Fatalf("WriteSignalAtomic returned error: %v", err)
	}
	status := InspectSignal(path, time.Minute, time.Date(2026, 8, 2, 12, 0, 30, 0, time.UTC), "node-a")
	if !status.Active {
		t.Fatalf("expected written signal to be active, got %q", status.Reason)
	}
	if len(status.Payload.SelectedUPSDevices) != 1 || status.Payload.SelectedUPSDevices[0] != "ups-a" {
		t.Fatalf("unexpected selected UPS devices: %#v", status.Payload.SelectedUPSDevices)
	}
}

func writeSignal(t *testing.T, payload string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shutdown.json")
	if err := os.WriteFile(path, []byte(payload), 0644); err != nil {
		t.Fatalf("write signal: %v", err)
	}
	return path
}
