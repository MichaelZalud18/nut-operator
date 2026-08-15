package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

var version = "dev"

// Powering the node off is a single, fixed operation: the reboot(2) syscall with
// LINUX_REBOOT_CMD_POWER_OFF. There is deliberately no way to configure a different mechanism.
//
// An earlier build also accepted an arbitrary command (POWER_POWEROFF_COMMAND, defaulting to
// systemctl poweroff) but nothing could ever select it, so it was removed rather than exposed
// (F-36). Exposing it would have meant a CRD field that runs an operator-chosen executable as root
// on every node, and it would have widened the container's privileges: the syscall needs
// CAP_SYS_BOOT alone, which is what F-13 argued for, while shelling out to systemctl needs host
// PID/dbus access. Fewer privileges and no configurable command is the better trade for the one
// operation whose blast radius is the whole machine.
const (
	policyDisabled        = "Disabled"
	policyStub            = "Stub"
	policySystemdPoweroff = "SystemdPoweroff"

	// modeActuate is the only agent mode under which a node may be halted. It is read from this
	// process's environment and never from a signal file (F-56).
	modeActuate = "Actuate"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			_, _ = fmt.Fprintln(os.Stdout, "Usage: node-actuator [--help|--version|--ready]")
			_, _ = fmt.Fprintln(os.Stdout, "Watches POWER_SIGNAL_PATH, validates structured shutdown signals, and performs the configured actuator policy.")
			return
		case "--version", "version":
			_, _ = fmt.Fprintln(os.Stdout, version)
			return
		case "--ready", "ready":
			// F-64: this is what the readiness probe runs, and unlike --version it can fail.
			//
			// It deliberately re-reads the environment rather than sharing state with the running
			// process: an exec probe is a separate process with the same environment, so the only
			// thing it can observe about the loop is what the loop wrote down.
			if err := checkActuatorReady(loadActuatorConfig(), time.Now().UTC()); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "not ready: %v\n", err)
				os.Exit(1)
			}
			_, _ = fmt.Fprintln(os.Stdout, "ready")
			return
		default:
			_, _ = fmt.Fprintf(os.Stderr, "unknown argument %q\n", os.Args[1])
			os.Exit(64)
		}
	}

	logger := log.New(os.Stdout, "node-actuator ", log.LstdFlags|log.LUTC)

	config := loadActuatorConfig()
	mode := config.Mode
	policy := config.Policy

	logger.Printf("starting mode=%s policy=%s node=%s signalPaths=%s signalTTL=%s poweroff=reboot-syscall(POWER_OFF)", config.Mode, config.Policy, config.NodeName, strings.Join(config.SignalPaths, ","), config.SignalTTL)

	switch policy {
	case policyDisabled, "":
		block(logger, "disabled policy")
	case policyStub:
		watchSignals(logger, config, stubActuator)
	case policySystemdPoweroff:
		if mode != modeActuate {
			block(logger, "SystemdPoweroff requires POWER_AGENT_MODE=Actuate")
		}
		// F-61: prove the capability is held now, not during a power event.
		//
		// This is the configuration that claims it can halt a node, so it is the one place worth
		// refusing to start over. Every way of losing CAP_SYS_BOOT is silent, and the code that
		// would have noticed runs once, on a node under load, at the end of a UPS runtime.
		if err := verifySysBootAvailable(); err != nil {
			logger.Printf("refusing to arm SystemdPoweroff actuation: %v", err)
			os.Exit(78)
		}
		logger.Printf("CAP_SYS_BOOT held in the permitted set; actuation armed")
		watchSignals(logger, config, systemdPoweroffActuator)
	default:
		logger.Printf("unknown actuator policy %q", policy)
		os.Exit(64)
	}
}

type actuatorConfig struct {
	Mode        string
	Policy      string
	NodeName    string
	SignalPath  string
	SignalPaths []string
	SignalTTL   time.Duration
	Interval    time.Duration
	// StatePath is where the watch loop records each completed pass, and the only thing the
	// readiness probe can observe about it (F-64).
	StatePath string
}

// loadActuatorConfig reads the actuator's configuration from the environment.
//
// Both the running process and the `--ready` probe go through here, so a probe cannot end up
// judging the loop against a different idea of what was configured.
func loadActuatorConfig() actuatorConfig {
	nodeName := env("POWER_NODE_NAME", "")
	signalPath := env("POWER_SIGNAL_PATH", defaultSignalPath(nodeName))
	return actuatorConfig{
		Mode:        env("POWER_AGENT_MODE", "DryRun"),
		Policy:      env("POWER_ACTUATOR_POLICY", policyStub),
		NodeName:    nodeName,
		SignalPath:  signalPath,
		SignalPaths: signalPaths(env("POWER_SIGNAL_PATHS", ""), signalPath),
		SignalTTL:   parseDuration(env("POWER_SIGNAL_TTL", "2m"), 2*time.Minute),
		Interval:    parseDuration(env("POWER_ACTUATOR_INTERVAL", "5s"), 5*time.Second),
		StatePath:   env("POWER_ACTUATOR_STATE_PATH", "/run/actuator/state.json"),
	}
}

type actuatorFunc func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func parseDuration(value string, fallback time.Duration) time.Duration {
	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration
	}
	if seconds, convErr := strconv.ParseInt(value, 10, 64); convErr == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

// defaultSignalPath is where the operator's projected Secret lands, derived rather than fixed.
//
// The old default was the local tmpfs path the upsmon container writes, which meant a failure to
// inject POWER_SIGNAL_PATH did not disarm the actuator -- it repointed it at the one path OD-37
// declines to give authority. Defaults fail safe here: with no node name there is nothing to build
// a per-node path from, so the actuator watches nothing rather than guessing.
func defaultSignalPath(nodeName string) string {
	if nodeName == "" {
		return ""
	}
	return "/var/lib/power-agent/signals/" + nodeName + ".json"
}

func signalPaths(value, fallback string) []string {
	if value == "" {
		if fallback == "" {
			return nil
		}
		return []string{fallback}
	}
	// Not ':' (F-75). A colon is a legal character in a path, and splitting on it fragmented any
	// such path into pieces that do not exist -- each failing as SignalMissing, the one reason
	// watchSignals deliberately never logs. The failure was silent by construction.
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	if len(out) == 0 && fallback != "" {
		return []string{fallback}
	}
	return out
}

func block(logger *log.Logger, reason string) {
	logger.Printf("blocking forever: %s", reason)
	select {}
}

// scanSignalPaths runs one pass over the configured signal paths, actuating at most once.
//
// Split out of watchSignals so the at-most-once property is reachable by a test: the loop around it
// never returns, and a rule nothing can exercise is a rule that quietly stops holding.
//
// `seen` is updated in place, which is what carries the actuated keys into the state file the caller
// writes at the end of each pass (F-58).
func scanSignalPaths(logger *log.Logger, config actuatorConfig, actuate actuatorFunc, seen map[string]struct{}) {
	for _, path := range config.SignalPaths {
		status := nodeagent.InspectSignal(path, config.SignalTTL, time.Now().UTC(), config.NodeName)
		if !status.Active {
			// SignalMissing is the normal case on every tick and is deliberately not logged.
			if status.Reason != "SignalMissing" {
				logger.Printf("ignoring shutdown signal path=%s reason=%s", status.Path, status.Reason)
			}
			continue
		}
		if _, alreadySeen := seen[status.Key]; !alreadySeen {
			seen[status.Key] = struct{}{}
			if err := actuate(logger, config, status); err != nil {
				logger.Printf("actuator rejected signal path=%s executionID=%s node=%s reason=%s error=%v", status.Path, status.Payload.ExecutionID, status.Payload.NodeName, status.Reason, err)
			}
		}
		// One live signal ends the pass (F-88). SignalKey is built from the payload, so two paths
		// describing one episode produce two keys and `seen` does not connect them -- continuing
		// past an active signal is how one shutdown became two actuations. OD-37 removes the second
		// source, and this removes the shape that let it matter.
		return
	}
}

func watchSignals(logger *log.Logger, config actuatorConfig, actuate actuatorFunc) {
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	// Seeded from the state file so a container restart cannot re-actuate (F-58). `seen` is
	// in-memory and the emptyDir holding the signal is pod-scoped, so an actuator that restarted
	// while a still-fresh signal sat on disk used to act on it a second time. The same volume that
	// makes readiness able to fail carries the keys across the restart.
	seen := map[string]struct{}{}
	if previous, err := readActuatorState(config.StatePath); err == nil {
		for _, key := range previous.ActuatedKeys {
			seen[key] = struct{}{}
		}
		if len(previous.ActuatedKeys) > 0 {
			logger.Printf("restored %d already-actuated signal key(s) from %s", len(previous.ActuatedKeys), config.StatePath)
		}
	}
	// Logged on transition only, so a persistently unwritable state volume does not bury the
	// actuation log under one line per interval.
	stateWriteFailed := false

	for {
		scanSignalPaths(logger, config, actuate, seen)

		// Record the completed pass (F-64). This is the only evidence readiness has that the loop
		// is running at all, so a failure to write it must be visible rather than swallowed -- a
		// silently unwritten state file would read as a stalled loop, which is the correct
		// conclusion but for the wrong reason, and the log line is what separates the two.
		state := actuatorState{
			Timestamp:             time.Now().UTC().Format(time.RFC3339),
			Policy:                config.Policy,
			IntervalSeconds:       config.Interval.Seconds(),
			UnreadableSignalDirs:  unreadableSignalDirs(config.SignalPaths),
			UnprojectedSignalDirs: unprojectedSignalDirs(config.SignalPaths),
			ActuatedKeys:          actuatedKeys(seen),
		}
		if err := writeActuatorState(config.StatePath, state); err != nil {
			if !stateWriteFailed {
				stateWriteFailed = true
				logger.Printf("cannot record watch-loop state at %s, so readiness will fail: %v", config.StatePath, err)
			}
		} else if stateWriteFailed {
			stateWriteFailed = false
			logger.Printf("recording watch-loop state at %s again", config.StatePath)
		}

		<-ticker.C
	}
}

func stubActuator(logger *log.Logger, config actuatorConfig, status nodeagent.SignalStatus) error {
	logger.Printf("stub actuator accepted shutdown signal executionID=%s node=%s flow=%s mode=%s", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow, config.Mode)
	return nil
}

func systemdPoweroffActuator(logger *log.Logger, config actuatorConfig, status nodeagent.SignalStatus) error {
	// Read from this process's own env, never from the signal (F-56). main() already refuses to
	// start this policy outside Actuate, so reaching here in another mode should be impossible --
	// which is exactly why it is worth checking on the one operation that cannot be undone.
	if config.Mode != modeActuate {
		logger.Printf("refusing to power off in mode=%s: only %s may halt a node, executionID=%s node=%s",
			config.Mode, modeActuate, status.Payload.ExecutionID, status.Payload.NodeName)
		return nil
	}
	logger.Printf("systemd actuator executing poweroff executionID=%s node=%s flow=%s", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow)
	return runPoweroff()
}

// runPoweroff powers the machine off. Despite the syscall's name, reboot(2) with
// LINUX_REBOOT_CMD_POWER_OFF halts and cuts power; it does not restart.
func runPoweroff() error {
	if err := rebootPoweroff(); err != nil {
		return fmt.Errorf("run reboot poweroff syscall: %w", err)
	}
	return nil
}
