package main

import (
	"log"
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

	status := inspectSignal(path, 2*time.Minute, now.Add(30*time.Second), "node-a")

	if !status.Active {
		t.Fatalf("expected active signal, got reason %q", status.Reason)
	}
	if status.Reason != "SignalAccepted" {
		t.Fatalf("expected SignalAccepted, got %q", status.Reason)
	}
	if status.Key == "" {
		t.Fatal("expected stable signal key")
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

	if status := inspectSignal(writeSignal(t, validPayload), 2*time.Minute, now.Add(3*time.Minute), "node-a"); status.Reason != "SignalStale" {
		t.Fatalf("expected SignalStale, got %q", status.Reason)
	}
	if status := inspectSignal(writeSignal(t, validPayload), 2*time.Minute, now, "node-b"); status.Reason != "SignalWrongNode" {
		t.Fatalf("expected SignalWrongNode, got %q", status.Reason)
	}
	if status := inspectSignal(writeSignal(t, `{`), 2*time.Minute, now, "node-a"); status.Reason != "SignalInvalidJSON" {
		t.Fatalf("expected SignalInvalidJSON, got %q", status.Reason)
	}
}

func TestRunPoweroffExecutesCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "poweroff")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$2\"\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	if err := runPoweroff(script, []string{"requested", marker}); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(data) != "requested" {
		t.Fatalf("expected marker content requested, got %q", string(data))
	}
}

func TestSystemdPoweroffActuatorSkipsDryRunSignal(t *testing.T) {
	status := signalStatus{
		Active: true,
		Payload: shutdownSignal{
			DryRun:       true,
			ExecutionID:  "exec-1",
			NodeName:     "node-a",
			ShutdownFlow: "flow-a",
		},
	}
	config := actuatorConfig{PoweroffCommand: "/path/that/does/not/exist"}

	if err := systemdPoweroffActuator(log.New(os.Stdout, "", 0), config, status); err != nil {
		t.Fatalf("expected dry-run signal to skip poweroff command, got %v", err)
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
