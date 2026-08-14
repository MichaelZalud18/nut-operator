package main

import (
	"log"
	"os"
	"reflect"
	"testing"

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
