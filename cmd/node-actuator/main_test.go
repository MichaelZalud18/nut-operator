package main

import (
	"encoding/json"
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
	previousReboot := rebootPoweroff
	// The raise is stubbed alongside the syscall. Unprivileged, the real one fails, and a test that
	// left it in place would be asserting on the capability check rather than on the halt path.
	previousRaise := raiseHaltCapability
	rebootPoweroff = func() error {
		calls++
		return nil
	}
	raiseHaltCapability = func() error { return nil }
	t.Cleanup(func() {
		rebootPoweroff = previousReboot
		raiseHaltCapability = previousRaise
	})
	return &calls
}

func TestRunPoweroffInvokesTheSyscall(t *testing.T) {
	calls := stubRebootPoweroff(t)

	if err := runPoweroff(log.New(io.Discard, "", 0), nodeagent.ShutdownSignal{}); err != nil {
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
	// 7 -> 8 for StatePath (F-64), where the watch loop records that it ran. 8 -> 9 for
	// ShutdownFlow (F-55), the flow an accepted signal must name. 9 -> 8 again when F-75 dropped
	// SignalPath, the singular half of a duplicate pair the actuator never read. None of them
	// carries a poweroff mechanism, which is what this count is guarding: changing it is a
	// deliberate act, not a fix.
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

// Whether this node may halt is the safety property the whole binary hangs on, so it is asserted
// against the syscall itself and in both directions: the mode that forbids actuation must not reach
// it, and the mode that permits it must.
//
// The gate moved from the signal payload to this process's own env (F-56). The old test proved the
// payload's DryRun flag made the difference, which was true and worth nothing — the writer set that
// flag from POWER_AGENT_MODE, so the actuator was reading its own configuration back out of a file,
// and the executor hard-coded it to false on the only path OD-37 leaves standing. An identical
// signal now produces opposite outcomes based on the actuator's env alone, which is the property
// that actually bounds the blast radius.
func TestPowerOffActuatorHonorsItsOwnModeNotTheSignal(t *testing.T) {
	signal := nodeagent.SignalStatus{
		Active: true,
		Payload: nodeagent.ShutdownSignal{
			ExecutionID:  "exec-1",
			NodeName:     "node-a",
			ShutdownFlow: "flow-a",
		},
	}
	logger := log.New(os.Stdout, "", 0)

	for _, mode := range []string{"DryRun", "MonitorOnly", ""} {
		t.Run("mode "+mode+" never powers off", func(t *testing.T) {
			calls := stubRebootPoweroff(t)

			if err := powerOffActuator(logger, actuatorConfig{Mode: mode}, signal); err != nil {
				t.Fatalf("expected the signal to be accepted and declined, got %v", err)
			}
			if *calls != 0 {
				t.Fatalf("mode %q powered the node off (%d syscalls)", mode, *calls)
			}
		})
	}

	t.Run("mode Actuate powers off", func(t *testing.T) {
		calls := stubRebootPoweroff(t)

		if err := powerOffActuator(logger, actuatorConfig{Mode: modeActuate}, signal); err != nil {
			t.Fatalf("expected Actuate mode to power off, got %v", err)
		}
		if *calls != 1 {
			t.Fatalf("expected exactly one poweroff syscall, got %d", *calls)
		}
	})
}

// The signal cannot re-enable actuation that the actuator's own configuration forbids. This is the
// shape F-56 removed: a field in a file that looked like it granted permission.
func TestSignalCannotGrantActuationTheModeForbids(t *testing.T) {
	calls := stubRebootPoweroff(t)
	logger := log.New(os.Stdout, "", 0)

	raw, err := json.Marshal(map[string]any{
		"dryRun":       false,
		"executionID":  "exec-1",
		"nodeName":     "node-a",
		"shutdownFlow": "flow-a",
	})
	if err != nil {
		t.Fatalf("marshal signal: %v", err)
	}
	var payload nodeagent.ShutdownSignal
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal signal: %v", err)
	}

	if err := powerOffActuator(logger, actuatorConfig{Mode: "DryRun"}, nodeagent.SignalStatus{Active: true, Payload: payload}); err != nil {
		t.Fatalf("expected the signal to be declined without error, got %v", err)
	}
	if *calls != 0 {
		t.Fatalf("a dryRun:false field in the signal powered off a DryRun-mode actuator (%d syscalls)", *calls)
	}
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

// writeTestSignal drops a well-formed, in-TTL signal for node-a under the given flow.
func writeTestSignal(t *testing.T, flow string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shutdown.json")
	if err := nodeagent.WriteSignalAtomic(path, nodeagent.ShutdownSignal{
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		NodeName:       "node-a",
		ExecutionID:    "exec-1",
		PlanConfigHash: "plan-hash-1",
		ShutdownFlow:   flow,
		Reason:         "ReleaseApproved",
	}); err != nil {
		t.Fatalf("write signal: %v", err)
	}
	return path
}

// F-55: fields were checked for presence and never for value, so any well-formed signal for this
// node was accepted regardless of which flow issued it.
func TestScanRejectsASignalFromAFlowTheAgentDidNotDeclare(t *testing.T) {
	config := actuatorConfig{
		NodeName:     "node-a",
		SignalTTL:    2 * time.Minute,
		ShutdownFlow: "flow-a",
		SignalPaths:  []string{writeTestSignal(t, "flow-b")},
	}

	actuations := 0
	scanSignalPaths(log.New(io.Discard, "", 0), config, func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error {
		actuations++
		return nil
	}, map[string]struct{}{})

	if actuations != 0 {
		t.Fatalf("a signal from an undeclared flow must not actuate, got %d", actuations)
	}
}

func TestScanAcceptsASignalFromTheDeclaredFlow(t *testing.T) {
	config := actuatorConfig{
		NodeName:     "node-a",
		SignalTTL:    2 * time.Minute,
		ShutdownFlow: "flow-a",
		SignalPaths:  []string{writeTestSignal(t, "flow-a")},
	}

	actuations := 0
	scanSignalPaths(log.New(io.Discard, "", 0), config, func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error {
		actuations++
		return nil
	}, map[string]struct{}{})

	if actuations != 1 {
		t.Fatalf("a signal from the declared flow must actuate once, got %d", actuations)
	}
}

// An agent that declares no flow keeps the operator's existing model: a ShutdownFlow names its
// agents through AgentRefs, and the agent's reference back is optional, so an unset ref is
// permission rather than an omission. Asserted so the check cannot quietly become mandatory.
func TestScanAcceptsAnyFlowWhenTheAgentDeclaresNone(t *testing.T) {
	config := actuatorConfig{
		NodeName:    "node-a",
		SignalTTL:   2 * time.Minute,
		SignalPaths: []string{writeTestSignal(t, "flow-whatever")},
	}

	actuations := 0
	scanSignalPaths(log.New(io.Discard, "", 0), config, func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error {
		actuations++
		return nil
	}, map[string]struct{}{})

	if actuations != 1 {
		t.Fatalf("an agent declaring no flow must accept any flow's signal, got %d", actuations)
	}
}

// F-59: only a future-dated signal counts as clock disagreement.
//
// The executor stamps the signal when it writes it, so a timestamp a whole TTL ahead of this node
// can only mean the clocks disagree by more than the delivery window. A merely stale signal cannot
// carry that meaning -- it ages out whenever nobody collects it or a flow is called off -- so
// counting it would report skew on every cancelled shutdown.
func TestScanReportsClockSkewOnlyForFutureDatedSignals(t *testing.T) {
	write := func(t *testing.T, stamp time.Time) actuatorConfig {
		t.Helper()
		path := filepath.Join(t.TempDir(), "shutdown.json")
		if err := nodeagent.WriteSignalAtomic(path, nodeagent.ShutdownSignal{
			Timestamp:      stamp.Format(time.RFC3339Nano),
			NodeName:       "node-a",
			ExecutionID:    "exec-1",
			PlanConfigHash: "hash-1",
			ShutdownFlow:   "flow-1",
			Reason:         "test",
		}); err != nil {
			t.Fatalf("write signal: %v", err)
		}
		return actuatorConfig{NodeName: "node-a", SignalTTL: time.Minute, SignalPaths: []string{path}}
	}
	noop := func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error { return nil }
	logger := log.New(io.Discard, "", 0)

	t.Run("a signal from the future is clock skew", func(t *testing.T) {
		skewed := scanSignalPaths(logger, write(t, time.Now().UTC().Add(10*time.Minute)), noop, map[string]struct{}{})
		if len(skewed) != 1 {
			t.Fatalf("expected the future-dated signal to be reported as skew, got %v", skewed)
		}
	})

	t.Run("an ordinary stale signal is not", func(t *testing.T) {
		skewed := scanSignalPaths(logger, write(t, time.Now().UTC().Add(-10*time.Minute)), noop, map[string]struct{}{})
		if len(skewed) != 0 {
			t.Fatalf("a stale signal must not be reported as clock skew, got %v", skewed)
		}
	})
}
