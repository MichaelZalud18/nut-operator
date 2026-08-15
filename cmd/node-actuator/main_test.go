package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// stubRebootPoweroff swaps the syscall for a recorder, returning a pointer to the call count. The
// syscall is the single irreversible act in this binary, so tests assert on whether it was reached
// rather than on any observable side effect.
func stubRebootPoweroff(t *testing.T) *int {
	t.Helper()
	calls := 0
	previous := rebootPoweroff
	rebootPoweroff = func() error {
		calls++
		return nil
	}
	t.Cleanup(func() { rebootPoweroff = previous })
	return &calls
}

func TestRunPoweroffInvokesTheSyscall(t *testing.T) {
	calls := stubRebootPoweroff(t)

	if err := runPoweroff(); err != nil {
		t.Fatalf("runPoweroff returned error: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("expected exactly one poweroff syscall, got %d", *calls)
	}
}

// F-36: the actuator once accepted POWER_POWEROFF_COMMAND, an arbitrary executable run as root on
// the host, which no CRD field could reach. It was removed rather than exposed. Nothing should
// reintroduce a configurable poweroff mechanism without revisiting that decision.
func TestPoweroffTakesNoConfiguration(t *testing.T) {
	config := actuatorConfig{}
	// Bumped from 7 to 8 for StatePath (F-64), which is where the watch loop records that it ran.
	// It carries no poweroff mechanism: readiness reads it and the syscall path never consults it.
	// The count is the point of the test, so raising it is a deliberate act, not a fix.
	if reflect.TypeOf(config).NumField() != 8 {
		t.Fatalf("actuatorConfig gained or lost a field; confirm no poweroff mechanism became configurable: %+v", config)
	}
	for _, field := range []string{"PoweroffMethod", "PoweroffCommand", "PoweroffArgs"} {
		if _, present := reflect.TypeOf(config).FieldByName(field); present {
			t.Fatalf("actuatorConfig.%s is back; the poweroff mechanism must stay fixed to the syscall", field)
		}
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

// Dry-run is the safety property this whole binary hangs on, so it is asserted against the syscall
// itself: a dry-run signal must not reach it, and an identical non-dry-run signal must. Testing
// both directions is what proves the DryRun flag is the thing making the difference, rather than
// some unrelated guard happening to stop execution.
func TestSystemdPoweroffActuatorHonorsDryRun(t *testing.T) {
	signal := func(dryRun bool) nodeagent.SignalStatus {
		return nodeagent.SignalStatus{
			Active: true,
			Payload: nodeagent.ShutdownSignal{
				DryRun:       dryRun,
				ExecutionID:  "exec-1",
				NodeName:     "node-a",
				ShutdownFlow: "flow-a",
			},
		}
	}
	logger := log.New(os.Stdout, "", 0)

	t.Run("dry-run signal never powers off", func(t *testing.T) {
		calls := stubRebootPoweroff(t)

		if err := systemdPoweroffActuator(logger, actuatorConfig{}, signal(true)); err != nil {
			t.Fatalf("expected dry-run signal to be accepted, got %v", err)
		}
		if *calls != 0 {
			t.Fatalf("dry-run signal powered the node off (%d syscalls)", *calls)
		}
	})

	t.Run("live signal powers off", func(t *testing.T) {
		calls := stubRebootPoweroff(t)

		if err := systemdPoweroffActuator(logger, actuatorConfig{}, signal(false)); err != nil {
			t.Fatalf("expected live signal to power off, got %v", err)
		}
		if *calls != 1 {
			t.Fatalf("expected exactly one poweroff syscall, got %d", *calls)
		}
	})
}

// F-57/OD-37. The old default was the local tmpfs path the upsmon container writes, so a failure to
// inject POWER_SIGNAL_PATH did not disarm the actuator -- it repointed it at the one path the
// operator declines to give authority.
func TestDefaultSignalPathIsTheProjectedSecretNotTheSharedTmpfs(t *testing.T) {
	path := defaultSignalPath("node-a")

	if strings.Contains(path, "/run/power-agent") {
		t.Fatalf("the actuator must never default to the shared tmpfs path, got %q", path)
	}
	if path != "/var/lib/power-agent/signals/node-a.json" {
		t.Fatalf("expected the node's projected signal path, got %q", path)
	}
}

// Defaults fail safe: with no node name there is no per-node path to build, and guessing one would
// be worse than watching nothing.
func TestDefaultSignalPathIsEmptyWithoutANodeName(t *testing.T) {
	if path := defaultSignalPath(""); path != "" {
		t.Fatalf("expected no default path without a node name, got %q", path)
	}
}

// F-88: two paths describing one shutdown episode.
//
// SignalKey is built from the payload, not the path, so the two files below hash to different keys
// and `seen` cannot connect them. Before the pass stopped at the first live signal, one episode
// delivered through two sources actuated twice -- on a real node, that is a second reboot(2) issued
// against a host already going down.
func TestScanActuatesOnceWhenTwoPathsCarryTheSameEpisode(t *testing.T) {
	directory := t.TempDir()
	now := time.Now().UTC()

	write := func(name, executionID string) string {
		path := filepath.Join(directory, name)
		payload := nodeagent.ShutdownSignal{
			Timestamp:      now.Format(time.RFC3339),
			NodeName:       "node-a",
			ExecutionID:    executionID,
			PlanConfigHash: "hash-1",
			ShutdownFlow:   "flow-1",
			Reason:         "test",
		}
		if err := nodeagent.WriteSignalAtomic(path, payload); err != nil {
			t.Fatalf("write signal %s: %v", name, err)
		}
		return path
	}

	config := actuatorConfig{
		NodeName:  "node-a",
		SignalTTL: 2 * time.Minute,
		SignalPaths: []string{
			write("local.json", "upsmon-node-a-123"),
			write("projected.json", "exec-1"),
		},
	}

	actuations := 0
	logger := log.New(io.Discard, "", 0)
	scanSignalPaths(logger, config, func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error {
		actuations++
		return nil
	}, map[string]struct{}{})

	if actuations != 1 {
		t.Fatalf("one shutdown episode must actuate once, got %d", actuations)
	}
}

// The dedupe F-58 added still has to hold across passes, which the early return must not skip.
func TestScanDoesNotReactuateASignalAlreadySeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shutdown.json")
	payload := nodeagent.ShutdownSignal{
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		NodeName:       "node-a",
		ExecutionID:    "exec-1",
		PlanConfigHash: "hash-1",
		ShutdownFlow:   "flow-1",
		Reason:         "test",
	}
	if err := nodeagent.WriteSignalAtomic(path, payload); err != nil {
		t.Fatalf("write signal: %v", err)
	}

	config := actuatorConfig{NodeName: "node-a", SignalTTL: 2 * time.Minute, SignalPaths: []string{path}}
	seen := map[string]struct{}{}
	actuations := 0
	actuate := func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error {
		actuations++
		return nil
	}
	logger := log.New(io.Discard, "", 0)

	scanSignalPaths(logger, config, actuate, seen)
	scanSignalPaths(logger, config, actuate, seen)

	if actuations != 1 {
		t.Fatalf("a signal already actuated must not fire again on the next pass, got %d", actuations)
	}
}
