package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

const (
	policyDisabled        = "Disabled"
	policyStub            = "Stub"
	policySystemdPoweroff = "SystemdPoweroff"
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
		SignalTTL:       parseDuration(env("POWER_SIGNAL_TTL", "2m"), 2*time.Minute),
		Interval:        parseDuration(env("POWER_ACTUATOR_INTERVAL", "5s"), 5*time.Second),
		PoweroffCommand: env("POWER_POWEROFF_COMMAND", "/usr/bin/systemctl"),
		PoweroffArgs:    commandArgs(env("POWER_POWEROFF_ARGS", "poweroff")),
	}

	logger.Printf("starting mode=%s policy=%s node=%s signalPath=%s signalTTL=%s", config.Mode, config.Policy, config.NodeName, config.SignalPath, config.SignalTTL)

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
	SignalTTL       time.Duration
	Interval        time.Duration
	PoweroffCommand string
	PoweroffArgs    []string
}

type shutdownSignal struct {
	DryRun             bool     `json:"dryRun"`
	ExecutionID        string   `json:"executionID"`
	NodeName           string   `json:"nodeName"`
	PlanConfigHash     string   `json:"planConfigHash"`
	Reason             string   `json:"reason"`
	SelectedUPSDevices []string `json:"selectedUPSDevices"`
	ShutdownFlow       string   `json:"shutdownFlow"`
	Timestamp          string   `json:"timestamp"`
}

type signalStatus struct {
	Active  bool
	Reason  string
	Key     string
	Payload shutdownSignal
}

type actuatorFunc func(*log.Logger, actuatorConfig, signalStatus) error

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

func block(logger *log.Logger, reason string) {
	logger.Printf("blocking forever: %s", reason)
	select {}
}

func watchSignals(logger *log.Logger, config actuatorConfig, actuate actuatorFunc) {
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	seen := map[string]struct{}{}

	for {
		status := inspectSignal(config.SignalPath, config.SignalTTL, time.Now().UTC(), config.NodeName)
		if status.Active {
			if _, alreadySeen := seen[status.Key]; !alreadySeen {
				seen[status.Key] = struct{}{}
				if err := actuate(logger, config, status); err != nil {
					logger.Printf("actuator rejected signal executionID=%s node=%s reason=%s error=%v", status.Payload.ExecutionID, status.Payload.NodeName, status.Reason, err)
				}
			}
		} else if status.Reason != "SignalMissing" {
			logger.Printf("ignoring shutdown signal path=%s reason=%s", config.SignalPath, status.Reason)
		}
		<-ticker.C
	}
}

func inspectSignal(path string, ttl time.Duration, now time.Time, expectedNode string) signalStatus {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return signalStatus{Reason: "SignalMissing"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return signalStatus{Reason: "SignalUnreadable"}
	}
	var payload shutdownSignal
	if err := json.Unmarshal(data, &payload); err != nil {
		return signalStatus{Reason: "SignalInvalidJSON"}
	}
	if payload.ExecutionID == "" || payload.NodeName == "" || payload.PlanConfigHash == "" || payload.ShutdownFlow == "" || payload.Timestamp == "" {
		return signalStatus{Reason: "SignalMissingRequiredFields"}
	}
	if expectedNode != "" && payload.NodeName != expectedNode {
		return signalStatus{Reason: "SignalWrongNode", Payload: payload}
	}
	timestamp, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
	if err != nil {
		return signalStatus{Reason: "SignalInvalidTimestamp", Payload: payload}
	}
	if ttl > 0 {
		if now.Sub(timestamp) > ttl {
			return signalStatus{Reason: "SignalStale", Payload: payload}
		}
		if timestamp.Sub(now) > ttl {
			return signalStatus{Reason: "SignalFromFuture", Payload: payload}
		}
	}
	return signalStatus{
		Active:  true,
		Reason:  "SignalAccepted",
		Key:     payload.ExecutionID + ":" + payload.NodeName + ":" + payload.PlanConfigHash + ":" + payload.Timestamp,
		Payload: payload,
	}
}

func stubActuator(logger *log.Logger, _ actuatorConfig, status signalStatus) error {
	logger.Printf("stub actuator accepted shutdown signal executionID=%s node=%s flow=%s dryRun=%t", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow, status.Payload.DryRun)
	return nil
}

func systemdPoweroffActuator(logger *log.Logger, config actuatorConfig, status signalStatus) error {
	if status.Payload.DryRun {
		logger.Printf("systemd actuator observed dry-run signal executionID=%s node=%s", status.Payload.ExecutionID, status.Payload.NodeName)
		return nil
	}
	logger.Printf("systemd actuator executing poweroff command executionID=%s node=%s flow=%s", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow)
	return runPoweroff(config.PoweroffCommand, config.PoweroffArgs)
}

func runPoweroff(command string, args []string) error {
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
