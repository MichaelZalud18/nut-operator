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
		if mode != "Actuate" {
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
	signalPath := env("POWER_SIGNAL_PATH", "/run/power-agent/shutdown.json")
	return actuatorConfig{
		Mode:        env("POWER_AGENT_MODE", "DryRun"),
		Policy:      env("POWER_ACTUATOR_POLICY", policyStub),
		NodeName:    env("POWER_NODE_NAME", ""),
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

func signalPaths(value, fallback string) []string {
	if value == "" {
		if fallback == "" {
			return nil
		}
		return []string{fallback}
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ':' || r == '\n' || r == '\t' || r == ' '
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

func watchSignals(logger *log.Logger, config actuatorConfig, actuate actuatorFunc) {
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	seen := map[string]struct{}{}
	// Logged on transition only, so a persistently unwritable state volume does not bury the
	// actuation log under one line per interval.
	stateWriteFailed := false

	for {
		for _, path := range config.SignalPaths {
			status := nodeagent.InspectSignal(path, config.SignalTTL, time.Now().UTC(), config.NodeName)
			if status.Active {
				if _, alreadySeen := seen[status.Key]; !alreadySeen {
					seen[status.Key] = struct{}{}
					if err := actuate(logger, config, status); err != nil {
						logger.Printf("actuator rejected signal path=%s executionID=%s node=%s reason=%s error=%v", status.Path, status.Payload.ExecutionID, status.Payload.NodeName, status.Reason, err)
					}
				}
			} else if status.Reason != "SignalMissing" {
				logger.Printf("ignoring shutdown signal path=%s reason=%s", status.Path, status.Reason)
			}
		}

		// Record the completed pass (F-64). This is the only evidence readiness has that the loop
		// is running at all, so a failure to write it must be visible rather than swallowed -- a
		// silently unwritten state file would read as a stalled loop, which is the correct
		// conclusion but for the wrong reason, and the log line is what separates the two.
		state := actuatorState{
			Timestamp:            time.Now().UTC().Format(time.RFC3339),
			Policy:               config.Policy,
			IntervalSeconds:      config.Interval.Seconds(),
			UnreadableSignalDirs: unreadableSignalDirs(config.SignalPaths),
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

func stubActuator(logger *log.Logger, _ actuatorConfig, status nodeagent.SignalStatus) error {
	logger.Printf("stub actuator accepted shutdown signal executionID=%s node=%s flow=%s dryRun=%t", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow, status.Payload.DryRun)
	return nil
}

func systemdPoweroffActuator(logger *log.Logger, config actuatorConfig, status nodeagent.SignalStatus) error {
	if status.Payload.DryRun {
		logger.Printf("systemd actuator observed dry-run signal executionID=%s node=%s", status.Payload.ExecutionID, status.Payload.NodeName)
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
