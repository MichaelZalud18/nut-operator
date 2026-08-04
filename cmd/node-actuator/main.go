package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

var version = "dev"

const (
	policyDisabled        = "Disabled"
	policyStub            = "Stub"
	policySystemdPoweroff = "SystemdPoweroff"

	poweroffMethodCommand       = "command"
	poweroffMethodRebootSyscall = "reboot-syscall"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			_, _ = fmt.Fprintln(os.Stdout, "Usage: node-actuator [--help|--version]")
			_, _ = fmt.Fprintln(os.Stdout, "Watches POWER_SIGNAL_PATH, validates structured shutdown signals, and performs the configured actuator policy.")
			return
		case "--version", "version":
			_, _ = fmt.Fprintln(os.Stdout, version)
			return
		default:
			_, _ = fmt.Fprintf(os.Stderr, "unknown argument %q\n", os.Args[1])
			os.Exit(64)
		}
	}

	logger := log.New(os.Stdout, "node-actuator ", log.LstdFlags|log.LUTC)

	mode := env("POWER_AGENT_MODE", "DryRun")
	policy := env("POWER_ACTUATOR_POLICY", policyStub)
	config := actuatorConfig{
		Mode:            mode,
		Policy:          policy,
		NodeName:        env("POWER_NODE_NAME", ""),
		SignalPath:      env("POWER_SIGNAL_PATH", "/run/power-agent/shutdown.json"),
		SignalPaths:     signalPaths(env("POWER_SIGNAL_PATHS", ""), env("POWER_SIGNAL_PATH", "/run/power-agent/shutdown.json")),
		SignalTTL:       parseDuration(env("POWER_SIGNAL_TTL", "2m"), 2*time.Minute),
		Interval:        parseDuration(env("POWER_ACTUATOR_INTERVAL", "5s"), 5*time.Second),
		PoweroffMethod:  env("POWER_POWEROFF_METHOD", poweroffMethodRebootSyscall),
		PoweroffCommand: env("POWER_POWEROFF_COMMAND", "/usr/bin/systemctl"),
		PoweroffArgs:    commandArgs(env("POWER_POWEROFF_ARGS", "poweroff")),
	}

	logger.Printf("starting mode=%s policy=%s node=%s signalPaths=%s signalTTL=%s poweroffMethod=%s", config.Mode, config.Policy, config.NodeName, strings.Join(config.SignalPaths, ","), config.SignalTTL, config.PoweroffMethod)

	switch policy {
	case policyDisabled, "":
		block(logger, "disabled policy")
	case policyStub:
		watchSignals(logger, config, stubActuator)
	case policySystemdPoweroff:
		if mode != "Actuate" {
			block(logger, "SystemdPoweroff requires POWER_AGENT_MODE=Actuate")
		}
		watchSignals(logger, config, systemdPoweroffActuator)
	default:
		logger.Printf("unknown actuator policy %q", policy)
		os.Exit(64)
	}
}

type actuatorConfig struct {
	Mode            string
	Policy          string
	NodeName        string
	SignalPath      string
	SignalPaths     []string
	SignalTTL       time.Duration
	Interval        time.Duration
	PoweroffMethod  string
	PoweroffCommand string
	PoweroffArgs    []string
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

func commandArgs(value string) []string {
	return strings.Fields(value)
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
	logger.Printf("systemd actuator executing poweroff method=%s executionID=%s node=%s flow=%s", config.PoweroffMethod, status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow)
	return runPoweroff(config)
}

func runPoweroff(config actuatorConfig) error {
	switch config.PoweroffMethod {
	case "", poweroffMethodRebootSyscall:
		if err := rebootPoweroff(); err != nil {
			return fmt.Errorf("run reboot poweroff syscall: %w", err)
		}
		return nil
	case poweroffMethodCommand:
		return runPoweroffCommand(config.PoweroffCommand, config.PoweroffArgs)
	default:
		return fmt.Errorf("unsupported POWER_POWEROFF_METHOD %q", config.PoweroffMethod)
	}
}

func runPoweroffCommand(command string, args []string) error {
	if command == "" {
		return fmt.Errorf("POWER_POWEROFF_COMMAND is required")
	}
	cmd := exec.Command(command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run poweroff command: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
