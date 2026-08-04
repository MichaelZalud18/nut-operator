package main

import (
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

func TestRunPoweroffExecutesCommandMethod(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "poweroff")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$2\"\n"), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	if err := runPoweroff(actuatorConfig{
		PoweroffMethod:  poweroffMethodCommand,
		PoweroffCommand: script,
		PoweroffArgs:    []string{"requested", marker},
	}); err != nil {
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

func TestRunPoweroffUsesSyscallMethod(t *testing.T) {
	called := false
	previous := rebootPoweroff
	rebootPoweroff = func() error {
		called = true
		return nil
	}
	t.Cleanup(func() {
		rebootPoweroff = previous
	})

	if err := runPoweroff(actuatorConfig{PoweroffMethod: poweroffMethodRebootSyscall}); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}
	if !called {
		t.Fatal("expected reboot poweroff syscall path")
	}
}

func TestSignalPathsParsesUniquePaths(t *testing.T) {
	paths := signalPaths("/run/power-agent/shutdown.json,/var/lib/power-agent/signals/node-a.json /run/power-agent/shutdown.json", "")
	if len(paths) != 2 {
		t.Fatalf("expected two unique signal paths, got %#v", paths)
	}
	if paths[0] != "/run/power-agent/shutdown.json" || paths[1] != "/var/lib/power-agent/signals/node-a.json" {
		t.Fatalf("unexpected signal paths: %#v", paths)
	}
}

func TestSystemdPoweroffActuatorSkipsDryRunSignal(t *testing.T) {
	status := nodeagent.SignalStatus{
		Active: true,
		Payload: nodeagent.ShutdownSignal{
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
