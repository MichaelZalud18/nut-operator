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

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

func TestWriteSignalPublishesStructuredPayload(t *testing.T) {
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "shutdown.json")

	payload, reused, err := writeSignal(signalWriterConfig{
		Mode:               "DryRun",
		NodeName:           "node-a",
		PlanConfigHash:     "hash-a",
		Reason:             "upsmon-fsd",
		SelectedUPSDevices: []string{"ups-b", "ups-a", "ups-a"},
		ShutdownFlow:       "flow-a",
		SignalPath:         path,
		SignalTTL:          2 * time.Minute,
	}, now)
	if err != nil {
		t.Fatalf("writeSignal returned error: %v", err)
	}
	if reused {
		t.Fatal("expected new signal")
	}
	if payload.ExecutionID == "" || payload.Timestamp != now.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected payload identity: %#v", payload)
	}
	if got := nodeagent.InspectSignal(path, time.Minute, now, "node-a"); !got.Active {
		t.Fatalf("expected active written signal, got %q", got.Reason)
	}
	if len(payload.SelectedUPSDevices) != 2 || payload.SelectedUPSDevices[0] != "ups-a" || payload.SelectedUPSDevices[1] != "ups-b" {
		t.Fatalf("expected sorted unique UPS devices, got %#v", payload.SelectedUPSDevices)
	}
}

func TestWriteSignalReusesFreshMatchingPayload(t *testing.T) {
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "shutdown.json")
	config := signalWriterConfig{
		Mode:               "Actuate",
		NodeName:           "node-a",
		PlanConfigHash:     "hash-a",
		Reason:             "upsmon-fsd",
		SelectedUPSDevices: []string{"ups-a"},
		ShutdownFlow:       "flow-a",
		SignalPath:         path,
		SignalTTL:          2 * time.Minute,
	}
	first, reused, err := writeSignal(config, now)
	if err != nil {
		t.Fatalf("first writeSignal returned error: %v", err)
	}
	if reused {
		t.Fatal("expected first write to create a signal")
	}
	second, reused, err := writeSignal(config, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("second writeSignal returned error: %v", err)
	}
	if !reused {
		t.Fatal("expected second write to reuse fresh signal")
	}
	if second.ExecutionID != first.ExecutionID || second.Timestamp != first.Timestamp {
		t.Fatalf("expected stable reused signal, first=%#v second=%#v", first, second)
	}
}

func TestWriteSignalRejectsWrongNodeSignal(t *testing.T) {
	now := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "shutdown.json")
	if err := nodeagent.WriteSignalAtomic(path, nodeagent.ShutdownSignal{
		ExecutionID:    "exec-a",
		NodeName:       "node-b",
		PlanConfigHash: "hash-a",
		Reason:         "upsmon-fsd",
		ShutdownFlow:   "flow-a",
		Timestamp:      now.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("seed signal returned error: %v", err)
	}

	_, _, err := writeSignal(signalWriterConfig{
		Mode:           "Actuate",
		NodeName:       "node-a",
		PlanConfigHash: "hash-a",
		Reason:         "upsmon-fsd",
		ShutdownFlow:   "flow-a",
		SignalPath:     path,
		SignalTTL:      2 * time.Minute,
	}, now)
	if err == nil {
		t.Fatal("expected wrong-node signal to be rejected")
	}
}
